package agent

import (
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
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
	// net/http reads and discards an unread request body with no deadline of its own: when the answer starts, when
	// the reverse proxy closes the body, and after the handler returns. The listener has no ReadTimeout (a push of
	// gigabytes must not be cut), so a client that stalls its body would hold the connection forever. A request with
	// a body therefore never reuses its connection: Connection: close, and an expired read deadline when the handler
	// returns (only reads are touched, a streamed answer is never cut). The body hides its Close, so the reverse
	// proxy does not discard the body of the server either.
	if r.Body != nil && r.Body != http.NoBody {
		w.Header().Set("Connection", "close")
		b := &guardedBody{r: r.Body}
		r.Body = b
		defer b.end(http.NewResponseController(w))
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

// guardedBody is the request body for the reverse proxy: no Close, and no read once the handler ended.
type guardedBody struct {
	mu   sync.Mutex // held during a read
	r    io.Reader
	done bool
}

func (b *guardedBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return 0, http.ErrBodyReadAfterClose
	}
	return b.r.Read(p)
}

func (b *guardedBody) Close() error { return nil }

// end expires the read deadline, which fails a read of the transport that still waits for a stalled client, and
// waits until that read returned. net/http clears the deadline again when it aborts a read that is in flight, and the
// discard of the rest of the body after the handler would then wait for the client with no deadline.
func (b *guardedBody) end(rc *http.ResponseController) {
	_ = rc.SetReadDeadline(time.Now())
	b.mu.Lock()
	b.done = true
	b.mu.Unlock()
}

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
