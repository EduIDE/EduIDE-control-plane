package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/propagation"

	"github.com/EduIDE/EduIDE-control-plane/internal/auth"
)

type accessKey struct{}

// accessInfo is filled while the request runs; the access log reads it afterwards.
type accessInfo struct{ owner string }

// userRecorder remembers the owner of a verified token for the access log, which wraps the auth
// middleware and so cannot see the user that middleware puts into the request context.
type userRecorder struct{ auth.Verifier }

func (v userRecorder) Verify(ctx context.Context, raw string) (auth.User, error) {
	u, err := v.Verifier.Verify(ctx, raw)
	if err == nil {
		if a, ok := ctx.Value(accessKey{}).(*accessInfo); ok {
			a.owner = u.Owner
		}
	}
	return u, err
}

// logRecorder tracks status, bytes and the start of an event stream.
type logRecorder struct {
	http.ResponseWriter
	code, bytes int
	wrote       bool
	stream      bool
	errBody     []byte
	onStream    func()
}

func (l *logRecorder) WriteHeader(c int) {
	if !l.wrote {
		l.wrote, l.code = true, c
		if strings.HasPrefix(l.Header().Get("Content-Type"), "text/event-stream") {
			l.stream = true
			l.onStream()
		}
	}
	l.ResponseWriter.WriteHeader(c)
}

func (l *logRecorder) Write(p []byte) (int, error) {
	if !l.wrote {
		l.WriteHeader(http.StatusOK)
	}
	if l.code >= 500 && len(l.errBody) < 512 {
		l.errBody = append(l.errBody, p[:min(len(p), 512-len(l.errBody))]...)
	}
	n, err := l.ResponseWriter.Write(p)
	l.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach Flush for SSE.
func (l *logRecorder) Unwrap() http.ResponseWriter { return l.ResponseWriter }

// accessLog writes one line per API request: info for 1xx to 4xx, error for 5xx. An event stream
// logs once when it opens and once when it closes. The line holds the pseudonymous user id only.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &accessInfo{}
		r = r.WithContext(context.WithValue(r.Context(), accessKey{}, info))
		// the trace of the caller: the launch span lives below this handler, so only the header is known here
		ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		attrs := func(rec *logRecorder) []any {
			a := []any{"method", r.Method, "route", routeLabel(r), "op", operationID(r.Pattern), "status", rec.code}
			if info.owner != "" {
				a = append(a, "user", s.userID(info.owner))
			}
			return a
		}
		rec := &logRecorder{ResponseWriter: w}
		rec.onStream = func() { slog.InfoContext(ctx, "stream open", attrs(rec)...) }
		next.ServeHTTP(rec, r)
		if !rec.wrote {
			rec.code = http.StatusOK
		}
		a := append(attrs(rec), "duration_ms", time.Since(start).Milliseconds(), "bytes", rec.bytes)
		switch {
		case rec.stream:
			slog.InfoContext(ctx, "stream close", a...)
		case rec.code >= 500:
			slog.ErrorContext(ctx, "request", append(a, "error", string(rec.errBody))...)
		default:
			slog.InfoContext(ctx, "request", a...)
		}
	})
}

// routeLabel is the mux pattern without the method, e.g. /api/v1/sessions/{name}; "unmatched" for unknown paths.
func routeLabel(r *http.Request) string {
	if _, p, ok := strings.Cut(r.Pattern, " "); ok {
		return p
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}
