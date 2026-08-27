// Package logging provides structured, trace-correlated slog loggers.
package logging

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// New creates a JSON slog logger tagged with the given service name.
func New(serviceName string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	return slog.New(handler).With("service", serviceName)
}

// FromContext returns base enriched with the trace_id/span_id of the span
// active in ctx, if any, so log records can be correlated with traces.
func FromContext(ctx context.Context, base *slog.Logger) *slog.Logger {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return base
	}

	return base.With(
		"trace_id", spanContext.TraceID().String(),
		"span_id", spanContext.SpanID().String(),
	)
}
