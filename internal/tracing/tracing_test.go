package tracing

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRecordErrorMarksSpanFailedWhenErrIsNotNil(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	_, span := provider.Tracer("test").Start(context.Background(), "op")
	RecordError(span, errors.New("boom"))
	span.End()

	stubs := exporter.GetSpans()
	if len(stubs) != 1 {
		t.Fatalf("expected 1 exported span, got %d", len(stubs))
	}
	stub := stubs[0]

	if stub.Status.Code != codes.Error {
		t.Fatalf("expected span status Error, got %v", stub.Status.Code)
	}
	if stub.Status.Description != "boom" {
		t.Fatalf("expected status description %q, got %q", "boom", stub.Status.Description)
	}
	if len(stub.Events) != 1 || stub.Events[0].Name != "exception" {
		t.Fatalf("expected an exception event recording the error, got %#v", stub.Events)
	}
}

func TestRecordErrorLeavesSpanUnsetWhenErrIsNil(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	_, span := provider.Tracer("test").Start(context.Background(), "op")
	RecordError(span, nil)
	span.End()

	stubs := exporter.GetSpans()
	if len(stubs) != 1 {
		t.Fatalf("expected 1 exported span, got %d", len(stubs))
	}
	stub := stubs[0]

	if stub.Status.Code != codes.Unset {
		t.Fatalf("expected span status Unset, got %v", stub.Status.Code)
	}
	if len(stub.Events) != 0 {
		t.Fatalf("expected no events, got %#v", stub.Events)
	}
}

// TestRecordSpanErrReadsTheValuePointedToAtDeferTime verifies the exact
// pattern RecordSpanErr is meant for: `defer RecordSpanErr(span, &err)`
// bound immediately after starting a span in a function with a named error
// return, so it must observe the return value as it is when the deferred
// call actually runs, not as it was when defer was scheduled.
func TestRecordSpanErrReadsTheValuePointedToAtDeferTime(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	run := func() (err error) {
		_, span := provider.Tracer("test").Start(context.Background(), "op")
		defer span.End()
		defer RecordSpanErr(span, &err)

		return errors.New("late failure")
	}

	if err := run(); err == nil {
		t.Fatal("expected run to return an error")
	}

	stubs := exporter.GetSpans()
	if len(stubs) != 1 {
		t.Fatalf("expected 1 exported span, got %d", len(stubs))
	}
	if stubs[0].Status.Code != codes.Error {
		t.Fatalf("expected span status Error, got %v", stubs[0].Status.Code)
	}
}
