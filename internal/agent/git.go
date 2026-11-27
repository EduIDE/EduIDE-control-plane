package agent

import (
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/agentapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

// gitRoute is the state of the Git path (ADR 0023). A nil route means the feature is off.
type gitRoute struct {
	hosts   []string
	landing string
	proxy   *httputil.ReverseProxy
}

// EnableGit turns the Git path on: Git requests of the IDE go to proxyURL (the git-proxy Service), with the
// instance name and the instance token. hosts and landingURL are only reported to the extension.
// The instance name is the first segment of PathPrefix, so set that first.
func (a *Agent) EnableGit(proxyURL string, hosts []string, landingURL string) error {
	target, err := url.Parse(proxyURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return errors.New("invalid git proxy URL")
	}
	instance := strings.Trim(a.PathPrefix, "/")
	if instance == "" {
		return errors.New("git proxy needs the instance name (path prefix)")
	}
	// No overall timeout: a clone or push of gigabytes must not be cut off. Only connecting is bounded.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	rp := &httputil.ReverseProxy{
		FlushInterval: -1,
		BufferPool:    bufPool{},
		Transport:     tr,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// /git/<host>/<rest> becomes /<host>/<rest>
			pr.Out.URL.Path = "/" + strings.TrimPrefix(pr.In.URL.Path, "/git/")
			pr.Out.URL.RawPath = ""
			// Whatever the client sent as credentials stays here; the proxy sees only the instance's own.
			for _, h := range []string{"Authorization", "Cookie", "Proxy-Authorization", gitapi.InstanceHeader, TokenHeader} {
				pr.Out.Header.Del(h)
			}
			pr.Out.Header.Set("Authorization", "Bearer "+string(a.token))
			pr.Out.Header.Set(gitapi.InstanceHeader, instance)
		},
		ModifyResponse: func(resp *http.Response) error {
			// The Git client in the pod must never be asked for credentials.
			resp.Header.Del("WWW-Authenticate")
			resp.Header.Del("Set-Cookie")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if ok, n := a.limit.Allow("git-upstream"); ok {
				a.log.Warn("git proxy unreachable", "error", errString(err), "suppressed", n)
			}
			http.Error(w, "git proxy unavailable", http.StatusBadGateway)
		},
	}
	a.git = &gitRoute{hosts: append([]string{}, hosts...), landing: landingURL, proxy: rp}
	return nil
}

// gitHandler serves /git/{host}/{rest...} while the instance is claimed.
func (a *Agent) gitHandler(w http.ResponseWriter, r *http.Request) {
	// net/http reads and discards an unread request body with no deadline: before the first flush of the answer,
	// when the reverse proxy closes the body, and after the handler returns. The listener has no ReadTimeout (a
	// push of gigabytes must not be cut), so a client that stalls its body would hold the connection forever.
	// An answer that starts before the body ended means git is done sending, so the read deadline expires with
	// it, and the discard fails at once. A body at its end is left alone: a long clone must keep its connection.
	if r.Body != nil && r.Body != http.NoBody {
		b := &eofBody{Reader: r.Body}
		r.Body = b
		w = &answerGuard{ResponseWriter: w, rc: http.NewResponseController(w), body: b}
	}
	a.mu.Lock()
	claimed := a.session != ""
	a.mu.Unlock()
	if !claimed {
		http.Error(w, "this instance is not in use", http.StatusForbidden)
		return
	}
	a.git.proxy.ServeHTTP(w, r)
}

// eofBody tells whether the request body was read to its end. Its Close does nothing, so the reverse proxy never
// closes (and so never discards) the body of the server.
type eofBody struct {
	io.Reader
	eof atomic.Bool
}

func (b *eofBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		b.eof.Store(true)
	}
	return n, err
}

func (b *eofBody) Close() error { return nil }

// answerGuard expires the read deadline when the answer starts while the request body is not at its end.
type answerGuard struct {
	http.ResponseWriter
	rc   *http.ResponseController
	body *eofBody
}

func (g *answerGuard) WriteHeader(code int) {
	if !g.body.eof.Load() {
		_ = g.rc.SetReadDeadline(time.Now())
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *answerGuard) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (a *Agent) getGitHosts(w http.ResponseWriter, _ *http.Request) {
	out := agentapi.GitHosts{Hosts: []string{}}
	if a.git != nil {
		out.Hosts = a.git.hosts
		if a.git.landing != "" {
			out.LandingUrl = &a.git.landing
		}
	}
	writeJSON(w, out)
}
