package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestFromContextWithoutSpanReturnsBaseLogger(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))

	logger := FromContext(context.Background(), base)
	logger.Info("no span in context")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("failed to decode log record: %v", err)
	}
	if _, ok := record["trace_id"]; ok {
		t.Fatalf("expected no trace_id field without an active span, got %#v", record)
	}
}

func TestFromContextWithSpanAddsTraceAndSpanID(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	ctx, span := provider.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))

	logger := FromContext(ctx, base)
	logger.Info("has a span")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("failed to decode log record: %v", err)
	}

	spanCtx := span.SpanContext()
	if got := record["trace_id"]; got != spanCtx.TraceID().String() {
		t.Fatalf("expected trace_id %q, got %v", spanCtx.TraceID().String(), got)
	}
	if got := record["span_id"]; got != spanCtx.SpanID().String() {
		t.Fatalf("expected span_id %q, got %v", spanCtx.SpanID().String(), got)
	}
}

func TestNewReturnsUsableLogger(t *testing.T) {
	if l := New("gophprofile-test"); l == nil {
		t.Fatal("expected New to return a non-nil logger")
	}
}
