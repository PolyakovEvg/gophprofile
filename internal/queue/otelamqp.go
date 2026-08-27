package queue

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "gophprofile/queue"

// amqpHeaderCarrier adapts amqp.Table to propagation.TextMapCarrier so trace
// context can be injected into / extracted from AMQP message headers.
type amqpHeaderCarrier amqp.Table

func (c amqpHeaderCarrier) Get(key string) string {
	value, ok := c[key]
	if !ok {
		return ""
	}
	str, _ := value.(string)
	return str
}

func (c amqpHeaderCarrier) Set(key, value string) {
	c[key] = value
}

func (c amqpHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}

// injectHeaders returns headers (creating a table if needed) carrying the
// trace context active in ctx, for propagation across the message broker.
func injectHeaders(ctx context.Context, headers amqp.Table) amqp.Table {
	if headers == nil {
		headers = amqp.Table{}
	}
	otel.GetTextMapPropagator().Inject(ctx, amqpHeaderCarrier(headers))
	return headers
}

// StartConsumerSpan extracts the trace context propagated in AMQP message
// headers and starts a child consumer span, linking the consuming service's
// trace back to the span that published the message.
func StartConsumerSpan(
	ctx context.Context,
	headers amqp.Table,
	queueName string,
) (context.Context, trace.Span) {
	if headers == nil {
		headers = amqp.Table{}
	}
	ctx = otel.GetTextMapPropagator().Extract(ctx, amqpHeaderCarrier(headers))
	return otel.Tracer(tracerName).Start(
		ctx,
		"queue.consume "+queueName,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemRabbitMQ,
			semconv.MessagingDestinationName(queueName),
		),
	)
}
