// Package telemetry sets up logging, tracing and metrics the same way in every binary.
package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Setup installs a JSON slog logger with trace_id and, when endpoint is set, an
// OTLP trace exporter. The returned function flushes traces on shutdown.
func Setup(ctx context.Context, service, endpoint string) (func(context.Context) error, error) {
	slog.SetDefault(slog.New(traceHandler{slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level()})}).With("service", service))
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
		// Start path traces come with a sampled parent; reconcile traces sample at 1 %.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.01))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

func level() slog.Level {
	if os.Getenv("LOG_LEVEL") == "debug" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
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
