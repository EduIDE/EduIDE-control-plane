package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

const basePath = "/api/v1"

var (
	opsOnce sync.Once
	ops     map[string]string
)

// operationID maps a mux pattern such as "GET /api/v1/sessions/{name}" to its OpenAPI operationId.
func operationID(pattern string) string {
	opsOnce.Do(func() {
		ops = map[string]string{}
		spec, err := GetSpec()
		if err != nil {
			return
		}
		for path, item := range spec.Paths.Map() {
			for method, op := range item.Operations() {
				// the embedded spec capitalizes operationIds; the contract uses lowerCamel
				ops[method+" "+basePath+path] = strings.ToLower(op.OperationID[:1]) + op.OperationID[1:]
			}
		}
	})
	if id, ok := ops[pattern]; ok {
		return id
	}
	return "unknown"
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

// Unwrap lets http.ResponseController reach Flush for SSE.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		op := operationID(r.Pattern)
		telemetry.APIRequests.WithLabelValues(op, strconv.Itoa(rec.code)).Inc()
		if !strings.HasPrefix(op, "watch") {
			telemetry.APIRequestDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
		}
	})
}
