package queue

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// withTestTelemetry installs a real tracer provider and W3C propagator for
// the duration of a test, restoring the previous globals afterwards, so
// injected/extracted trace IDs are non-zero and comparable.
func withTestTelemetry(t *testing.T) {
	t.Helper()

	prevProvider := otel.GetTracerProvider()
	prevPropagator := otel.GetTextMapPropagator()

	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
	))

	t.Cleanup(func() {
		otel.SetTracerProvider(prevProvider)
		otel.SetTextMapPropagator(prevPropagator)
	})
}

func TestInjectHeadersAndStartConsumerSpanPropagateTraceContext(t *testing.T) {
	withTestTelemetry(t)

	ctx, producerSpan := otel.Tracer("test").Start(context.Background(), "publish")
	defer producerSpan.End()

	headers := injectHeaders(ctx, amqp.Table{})
	if _, ok := headers["traceparent"].(string); !ok {
		t.Fatalf("expected injectHeaders to set a traceparent header, got %#v", headers)
	}

	consumerCtx, consumerSpan := StartConsumerSpan(context.Background(), headers, "resize")
	defer consumerSpan.End()

	producerTraceID := producerSpan.SpanContext().TraceID()
	consumerTraceID := trace.SpanContextFromContext(consumerCtx).TraceID()

	if consumerTraceID != producerTraceID {
		t.Fatalf(
			"expected consumer span to join the producer's trace: producer=%s consumer=%s",
			producerTraceID, consumerTraceID,
		)
	}
}

func TestStartConsumerSpanHandlesNilHeaders(t *testing.T) {
	withTestTelemetry(t)

	ctx, span := StartConsumerSpan(context.Background(), nil, "resize")
	defer span.End()

	if ctx == nil {
		t.Fatal("expected a non-nil context")
	}
}

func TestAmqpHeaderCarrierRoundTrip(t *testing.T) {
	carrier := amqpHeaderCarrier(amqp.Table{})
	carrier.Set("traceparent", "00-1234-5678-01")

	if got := carrier.Get("traceparent"); got != "00-1234-5678-01" {
		t.Fatalf("expected round-tripped header value, got %q", got)
	}
	if got := carrier.Get("missing"); got != "" {
		t.Fatalf("expected empty string for missing header, got %q", got)
	}

	keys := carrier.Keys()
	if len(keys) != 1 || keys[0] != "traceparent" {
		t.Fatalf("expected keys to contain only traceparent, got %#v", keys)
	}
}

func TestAmqpHeaderCarrierGetIgnoresNonStringValues(t *testing.T) {
	carrier := amqpHeaderCarrier(amqp.Table{"count": 42})

	if got := carrier.Get("count"); got != "" {
		t.Fatalf("expected non-string header value to be ignored, got %q", got)
	}
}
