package agent

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProxyHandler forwards HTTP and WebSocket traffic to the IDE after checking the instance token.
func (a *Agent) ProxyHandler() http.Handler {
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", a.idePort)}
	rp := &httputil.ReverseProxy{
		BufferPool: bufPool{},
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header.Del(TokenHeader)
			stripAuthCookies(pr.Out.Header)
			pr.Out.Host = pr.In.Host
			// Rewrite drops X-Forwarded-*; keep what the gateway set so the IDE sees the public scheme and host
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				if v, ok := pr.In.Header[h]; ok {
					pr.Out.Header[h] = v
				}
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if ok, n := a.limit.Allow("upstream"); ok {
				a.log.Warn("proxy upstream error", "method", r.Method, "prefix", pathPrefix(r.URL.Path), "error", errString(err), "suppressed", n)
			}
			http.Error(w, "ide unavailable", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.headerTokenOK(r.Header) {
			a.tokenRejected("proxy", r)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		a.markOnce(&a.firstRequest, msFirstRequest)
		if !a.log.Enabled(r.Context(), slog.LevelDebug) {
			rp.ServeHTTP(&countingWriter{ResponseWriter: w, a: a}, r)
			return
		}
		start := time.Now()
		rec := &statusWriter{ResponseWriter: w}
		rp.ServeHTTP(&countingWriter{ResponseWriter: rec, a: a}, r)
		status := rec.code
		if status == 0 && r.Header.Get("Upgrade") != "" {
			status = http.StatusSwitchingProtocols // hijacked: the proxy wrote the response itself
		}
		a.log.Debug("proxy request", "method", r.Method, "prefix", pathPrefix(r.URL.Path), "status", status, "duration_ms", time.Since(start).Milliseconds())
	})
}

// statusWriter remembers the status code for the debug log.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) {
	if w.code == 0 {
		w.code = c
	}
	w.ResponseWriter.WriteHeader(c)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// pathPrefix keeps the first path segment: the rest may carry file names of the user.
func pathPrefix(p string) string {
	p = strings.TrimPrefix(p, "/")
	seg, _, _ := strings.Cut(p, "/")
	return "/" + seg
}

// errString is the error text without the request URL, which may carry a query.
func errString(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// countingWriter counts hijacked (upgraded) connections until they close.
type countingWriter struct {
	http.ResponseWriter
	a *Agent
}

func (w *countingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *countingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.a.conns.Add(1)
	w.a.markOnce(&w.a.firstUpgrade, msFirstUpgrade)
	return &countedConn{Conn: c, a: w.a}, rw, nil
}

type countedConn struct {
	net.Conn
	a    *Agent
	once sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() { c.a.conns.Add(-1) })
	return c.Conn.Close()
}

// bufPool shares copy buffers between requests so the proxy does not allocate 32 KiB per response.
type bufPool struct{}

var bufs = sync.Pool{New: func() any { b := make([]byte, 16<<10); return &b }}

func (bufPool) Get() []byte  { return *bufs.Get().(*[]byte) }
func (bufPool) Put(b []byte) { bufs.Put(&b) }

// authCookiePrefixes are the gateway's login cookies (ADR 0005). The IDE never needs them, so code
// running in the IDE cannot read the user's tokens from requests.
var authCookiePrefixes = []string{"eduide-id", "IdToken", "AccessToken", "BearerToken", "RefreshToken", "OauthHMAC", "OauthExpires", "OauthNonce", "CodeVerifier"}

func stripAuthCookies(h http.Header) {
	var keep []string
	for _, line := range h.Values("Cookie") {
		for _, c := range strings.Split(line, ";") {
			c = strings.TrimSpace(c)
			name, _, _ := strings.Cut(c, "=")
			if c != "" && !hasAuthPrefix(name) {
				keep = append(keep, c)
			}
		}
	}
	h.Del("Cookie")
	if len(keep) > 0 {
		h.Set("Cookie", strings.Join(keep, "; "))
	}
}

func hasAuthPrefix(name string) bool {
	for _, p := range authCookiePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
