package agent

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
)

// ProxyHandler forwards HTTP and WebSocket traffic to the IDE after checking the instance token.
func (a *Agent) ProxyHandler() http.Handler {
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", a.idePort)}
	rp := &httputil.ReverseProxy{
		BufferPool: bufPool{},
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header.Del(TokenHeader)
			pr.Out.Host = pr.In.Host
			// Rewrite drops X-Forwarded-*; keep what the gateway set so the IDE sees the public scheme and host
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				if v, ok := pr.In.Header[h]; ok {
					pr.Out.Header[h] = v
				}
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "ide unavailable", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.headerTokenOK(r.Header) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		rp.ServeHTTP(&countingWriter{ResponseWriter: w, a: a}, r)
	})
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
