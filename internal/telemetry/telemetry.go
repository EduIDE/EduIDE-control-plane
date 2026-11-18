// Package telemetry sets up logging, tracing and metrics the same way in every binary.
package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/EduIDE/EduIDE-control-plane/internal/logx"
)

// Setup installs a JSON slog logger with trace_id and, when endpoint is set, an
// OTLP trace exporter. The returned function flushes traces on shutdown.
func Setup(ctx context.Context, service, endpoint string) (func(context.Context) error, error) {
	InitLogger(service)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(service))),
		sdktrace.WithSampler(StartPathSampler{}),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// InitLogger installs the JSON logger on stdout. Binaries call it first so that even a config error
// is logged as JSON; Setup calls it again, which is harmless.
func InitLogger(service string) {
	slog.SetDefault(slog.New(traceHandler{slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logx.Level()})}).With("service", service))
}

// BridgeLogs sends controller-runtime and client-go (klog) output through the default logger.
func BridgeLogs() {
	l := logr.FromSlogHandler(slog.Default().Handler())
	ctrl.SetLogger(l)
	klog.SetLogger(l)
}

// UserID is the pseudonymous user id for spans and logs (G17): HMAC-SHA256 with a per-deployment salt.
func UserID(salt []byte, owner string) string {
	m := hmac.New(sha256.New, salt)
	m.Write([]byte(owner))
	return hex.EncodeToString(m.Sum(nil))[:16]
}

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(a)}
}
func (h traceHandler) WithGroup(n string) slog.Handler { return traceHandler{h.Handler.WithGroup(n)} }

// StartPathSpan names the root span of a launch. It and everything below it is always sampled (plan:
// start path traces at 100 %); other traces follow the parent and sample new roots at 1 %.
const StartPathSpan = "launch"

type StartPathSampler struct{}

func (StartPathSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if p.Name == StartPathSpan {
		return sdktrace.AlwaysSample().ShouldSample(p)
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.01)).ShouldSample(p)
}

func (StartPathSampler) Description() string { return "StartPathSampler" }
