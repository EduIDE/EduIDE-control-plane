package gitproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

// Results of a request, the label values of eduide_gitproxy_requests_total.
const (
	resultOK             = "ok"
	resultDenied         = "denied"
	resultUpstreamDenied = "upstream-denied"
	resultUpstreamError  = "upstream-error"
	resultUnavailable    = "unavailable"
)

// requestHeaders are the only headers that go to the provider. Credentials of the client are never among them.
var requestHeaders = []string{"Git-Protocol", "Content-Type", "Content-Encoding", "Accept-Encoding", "Accept", "User-Agent"}

// responseHeaders are the only headers that go back. No Set-Cookie, no WWW-Authenticate, no Location.
var responseHeaders = []string{"Content-Type", "Content-Encoding", "Content-Length", "Cache-Control", "Expires", "Pragma"}

// Provider is a configured Git host.
type Provider struct{ Name, DisplayName string }

// Handler serves the Git paths below /{host}/.
type Handler struct {
	providers  map[string]Provider // by lower case host
	landingURL string
	authz      Authorizer
	cache      *decisionCache
	log        *slog.Logger
	rp         *httputil.ReverseProxy
	clients    *clientLimiter
	running    slots
	idle       time.Duration
}

// New returns a handler for the provider hosts. rt reaches the provider hosts over HTTPS; nil takes a transport
// with default TLS verification. Redirects of a provider are never followed.
func New(providers []config.GitProvider, landingURL string, az Authorizer, rt http.RoundTripper, log *slog.Logger) *Handler {
	h := &Handler{providers: map[string]Provider{}, landingURL: landingURL, authz: az, cache: newDecisionCache(), log: log, clients: newClientLimiter(), idle: idleTimeout}
	for _, p := range providers {
		h.providers[strings.ToLower(p.Host)] = Provider{Name: p.Name, DisplayName: p.DisplayName}
	}
	if rt == nil {
		rt = Transport(nil)
	}
	// No timeout of the whole request: a clone or push of gigabytes must not be cut off. A request that moves
	// no byte for h.idle is cancelled (ServeHTTP).
	h.rp = &httputil.ReverseProxy{
		FlushInterval:  -1,
		BufferPool:     bufPool{},
		Transport:      rt,
		Rewrite:        h.rewrite,
		ModifyResponse: h.modifyResponse,
		ErrorHandler:   h.upstreamFailed,
	}
	return h
}

// Transport reaches the provider hosts directly over HTTPS. pool holds the roots to trust; nil means the system roots.
func Transport(pool *x509.CertPool) http.RoundTripper {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // the provider hosts are reached directly; an environment proxy would see the credential
	tr.MaxIdleConnsPerHost = 32
	if pool != nil {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return tr
}

// reqState travels with one forwarded request.
type reqState struct {
	t       Target
	prov    Provider
	user    string
	session string
	cred    *gitapi.Upstream
	// connected and sessionRepo come from the authorize answer; they only shape the message of an anonymous failure.
	connected   bool
	sessionRepo string
	approval    string
	// progress is called whenever bytes arrive (from the client or from the provider). It moves the idle timer and the
	// connection deadlines, so a write to a client that stopped reading ends after the idle time.
	progress func()
	start    time.Time
	in, out  atomic.Int64
	idleCut  atomic.Bool
	result   string
	upstream int
}

type stateKey struct{}

func stateOf(ctx context.Context) *reqState { s, _ := ctx.Value(stateKey{}).(*reqState); return s }

func (h *Handler) rewrite(pr *httputil.ProxyRequest) {
	st := stateOf(pr.In.Context())
	u := &url.URL{Scheme: "https", Host: st.t.Host, Path: "/" + st.t.Repo + st.t.upstreamPath()}
	if st.t.Op == OpInfoRefs {
		u.RawQuery = "service=" + st.t.Service
	}
	pr.Out.URL, pr.Out.Host = u, st.t.Host
	hdr := http.Header{}
	for _, k := range requestHeaders {
		if v := pr.In.Header.Values(k); len(v) > 0 {
			hdr[k] = v
		}
	}
	pr.Out.Header = hdr
	if st.cred != nil {
		pr.Out.SetBasicAuth(st.cred.Username, st.cred.Password)
	}
	if b := pr.Out.Body; b != nil && b != http.NoBody {
		pr.Out.Body = &countReader{ReadCloser: b, n: &st.in, progress: st.progress}
	}
}

func (h *Handler) modifyResponse(resp *http.Response) error {
	st := stateOf(resp.Request.Context())
	st.upstream = resp.StatusCode
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		st.result = resultUpstreamDenied
		h.replaceWithError(resp, st, h.upstreamDeniedMessage(st))
		return nil
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		st.result = resultUpstreamDenied
		h.replaceWithError(resp, st, fmt.Sprintf("%s/%s moved, and EduIDE does not follow redirects. Use the new repository URL.", st.t.Host, gitapi.NormalizeRepo(st.t.Repo)))
		return nil
	case resp.StatusCode >= 500:
		st.result = resultUpstreamError
	default:
		st.result = resultOK
	}
	for k := range resp.Header {
		if !allowed(responseHeaders, k) {
			delete(resp.Header, k)
		}
	}
	resp.Body = &countReader{ReadCloser: resp.Body, n: &st.out, progress: st.progress}
	return nil
}

func allowed(list []string, k string) bool {
	for _, a := range list {
		if strings.EqualFold(a, k) {
			return true
		}
	}
	return false
}

func (h *Handler) upstreamDeniedMessage(st *reqState) string {
	if st.cred == nil && !st.connected {
		return gitapi.ConnectMessage(st.prov.DisplayName, h.landingURL)
	}
	if st.cred == nil && st.approval != "" { // an account exists: the user can approve the repository for this session
		return gitapi.ApproveMessage(h.landingURL, st.session, st.t.Host, st.t.Repo, gitapi.KindRead)
	}
	if st.cred == nil { // an account exists, but this session uses it for its own repository only
		return gitapi.OtherRepositoryMessage(st.prov.DisplayName, st.sessionRepo, st.t.Host, st.t.Repo)
	}
	return fmt.Sprintf("%s/%s: not found, or your %s account has no access", st.t.Host, gitapi.NormalizeRepo(st.t.Repo), st.prov.DisplayName)
}

// upstreamFailed answers when the provider host cannot be reached. No header went out yet.
func (h *Handler) upstreamFailed(w http.ResponseWriter, r *http.Request, err error) {
	st := stateOf(r.Context())
	st.result = resultUpstreamError
	if st.idleCut.Load() {
		writeGitError(w, st.t, "The transfer stalled and EduIDE cancelled it. Try again.")
		return
	}
	if r.Context().Err() != nil { // the client went away
		return
	}
	h.log.Warn("git upstream unreachable", "provider", st.prov.Name, "error", errText(err))
	writeGitError(w, st.t, st.prov.DisplayName+" is not reachable right now. Try again in a moment.")
}

func errText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// replaceWithError turns the provider's answer into a valid Git answer that carries an ERR line, so git
// prints the message and never sees a 401 with WWW-Authenticate, which would make it ask for a password.
func (h *Handler) replaceWithError(resp *http.Response, st *reqState, msg string) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	body := errorBody(st.t, msg)
	resp.StatusCode, resp.Status = http.StatusOK, "200 OK"
	resp.Header = http.Header{"Content-Type": {contentType(st.t)}, "Cache-Control": {"no-cache"}}
	resp.Body, resp.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
	resp.TransferEncoding = nil
	st.out.Add(int64(len(body)))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.clients.allow(clientIP(r)) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	t, ok := ParsePath(r.Method, r.URL.Path, r.URL.RawQuery)
	prov, known := h.providers[t.Host]
	if !ok || !known {
		http.NotFound(w, r)
		return
	}
	if t.Op == OpLFS {
		writeLFSError(w)
		return
	}
	instance := r.Header.Get(gitapi.InstanceHeader)
	token, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if instance == "" || !hasBearer || token == "" {
		http.Error(w, "instance name and token required", http.StatusUnauthorized)
		return
	}
	st := &reqState{t: t, prov: prov, start: time.Now()}
	resp, err := h.authorize(r.Context(), instance, token, t)
	if err != nil {
		st.result = resultUnavailable
		h.log.Warn("git authorize unavailable", "provider", prov.Name, "kind", t.Kind(), "error", err.Error())
		writeGitError(w, t, "EduIDE cannot check your access right now. Try again in a moment.")
		h.finish(st, "")
		return
	}
	st.user, st.session, st.cred, st.connected, st.sessionRepo, st.approval = resp.User, resp.Session, resp.Upstream, resp.Connected, resp.SessionRepository, resp.ApprovalMode
	if !resp.Allow {
		st.result = resultDenied
		msg := resp.Message
		if msg == "" {
			msg = "EduIDE does not allow this Git request."
		}
		writeGitError(w, t, msg)
		h.finish(st, resp.Reason)
		return
	}
	if !h.running.acquire(instance) {
		st.result = resultDenied
		writeGitError(w, t, "Too many Git requests of this session run at the same time. Try again in a moment.")
		h.finish(st, "busy")
		return
	}
	defer h.running.release(instance)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	timer := time.AfterFunc(h.idle, func() { st.idleCut.Store(true); cancel() })
	defer timer.Stop()
	rc := http.NewResponseController(w)
	st.progress = func() {
		timer.Reset(h.idle)
		_ = rc.SetReadDeadline(time.Now().Add(h.idle))
		_ = rc.SetWriteDeadline(time.Now().Add(h.idle))
	}
	st.progress()
	defer func() { _ = rc.SetReadDeadline(time.Time{}); _ = rc.SetWriteDeadline(time.Time{}) }()
	h.rp.ServeHTTP(w, r.WithContext(context.WithValue(ctx, stateKey{}, st)))
	if st.result == "" { // the client went away before an answer
		st.result = resultUpstreamError
	}
	h.finish(st, resp.Reason)
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func (h *Handler) authorize(ctx context.Context, instance, token string, t Target) (gitapi.AuthorizeResponse, error) {
	req := gitapi.AuthorizeRequest{Instance: instance, Host: t.Host, Repository: gitapi.NormalizeRepo(t.Repo), Kind: t.Kind()}
	key := cacheKey{instance: instance, host: req.Host, repo: req.Repository, kind: req.Kind, token: sha256.Sum256([]byte(token))}
	if r, ok := h.cache.get(key); ok {
		return r, nil
	}
	r, err := h.authz.Authorize(ctx, token, req)
	if err == nil {
		h.cache.put(key, r)
	}
	return r, err
}

// finish records metrics and the log line. Reads that went fine are only counted; pushes and every
// other outcome are logged. Never a token, a credential or a body.
func (h *Handler) finish(st *reqState, reason string) {
	kind := st.t.Kind()
	telemetry.GitProxyRequests.WithLabelValues(st.prov.Name, kind, st.result).Inc()
	if st.upstream != 0 {
		telemetry.GitProxyUpstreamDuration.WithLabelValues(st.prov.Name, kind).Observe(time.Since(st.start).Seconds())
		telemetry.GitProxyBytes.WithLabelValues(st.prov.Name, "in").Add(float64(st.in.Load()))
		telemetry.GitProxyBytes.WithLabelValues(st.prov.Name, "out").Add(float64(st.out.Load()))
	}
	if kind == gitapi.KindRead && st.result == resultOK {
		return
	}
	h.log.Info("git request", "user", st.user, "session", st.session, "provider", st.prov.Name, "repository", gitapi.NormalizeRepo(st.t.Repo),
		"kind", kind, "result", st.result, "reason", reason, "status", st.upstream)
}

// writeGitError answers with a valid smart HTTP body that carries an ERR line. git prints it as "remote error: ...".
func writeGitError(w http.ResponseWriter, t Target, msg string) {
	body := errorBody(t, msg)
	w.Header().Set("Content-Type", contentType(t))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	_, _ = w.Write(body)
}

func contentType(t Target) string {
	if t.Op == OpInfoRefs {
		return "application/x-" + t.Service + "-advertisement"
	}
	return "application/x-" + t.Service + "-result"
}

func pktLine(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func errorBody(t Target, msg string) []byte {
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > maxErrMessage {
		msg = strings.ToValidUTF8(msg[:maxErrMessage], "")
	}
	var b strings.Builder
	if t.Op == OpInfoRefs {
		b.WriteString(pktLine("# service=" + t.Service + "\n"))
		b.WriteString("0000")
	}
	b.WriteString(pktLine("ERR " + msg + "\n"))
	return []byte(b.String())
}

// writeLFSError is the answer a Git LFS client shows to the user: its API error with a message.
func writeLFSError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "Git LFS is not supported through EduIDE."})
}

type countReader struct {
	io.ReadCloser
	n        *atomic.Int64
	progress func() // may be nil
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	if n > 0 && c.progress != nil {
		c.progress()
	}
	return n, err
}

// bufPool shares copy buffers between requests, so a clone does not allocate 32 KiB per copy.
type bufPool struct{}

var bufs = sync.Pool{New: func() any { b := make([]byte, 32<<10); return &b }}

func (bufPool) Get() []byte  { return *bufs.Get().(*[]byte) }
func (bufPool) Put(b []byte) { bufs.Put(&b) }
