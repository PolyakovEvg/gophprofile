package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/pelfox/gophprofile/internal/tracing"
	"github.com/pelfox/gophprofile/pkg"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
)

type rabbitMQQueue struct {
	channel *amqp.Channel
	queues  map[string]amqp.Queue
	mu      sync.Mutex
}

// NewRabbitMQQueue creates a RabbitMQ-backed queue provider.
func NewRabbitMQQueue(conn *amqp.Connection) (PublisherProvider, error) {
	channel, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open a new RabbitMQ channel: %w", err)
	}

	queueNames := []string{
		ResizeQueueName,
		ResizeDoneQueueName,
		DeleteQueueName,
	}
	queues := make(map[string]amqp.Queue, len(queueNames))
	for _, name := range queueNames {
		queue, err := channel.QueueDeclare(
			name,
			true,
			false,
			false,
			false,
			nil,
		)
		if err != nil {
			_ = channel.Close()
			return nil, fmt.Errorf("failed to declare %s queue: %w", name, err)
		}

		queues[name] = queue
	}

	return &rabbitMQQueue{
		channel: channel,
		queues:  queues,
	}, nil
}

func (r *rabbitMQQueue) RequestResize(
	ctx context.Context,
	message pkg.MessageResizeRequest,
) error {
	return r.publish(ctx, ResizeQueueName, message)
}

func (r *rabbitMQQueue) RequestDelete(
	ctx context.Context,
	message pkg.MessageDeleteRequest,
) error {
	return r.publish(ctx, DeleteQueueName, message)
}

func (r *rabbitMQQueue) CompleteResize(
	ctx context.Context,
	message pkg.MessageResizeDone,
) error {
	return r.publish(ctx, ResizeDoneQueueName, message)
}

func (r *rabbitMQQueue) publish(
	ctx context.Context,
	queueName string,
	message any,
) (err error) {
	ctx, span := otel.Tracer(tracerName).Start(
		ctx,
		"queue.publish "+queueName,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			semconv.MessagingSystemRabbitMQ,
			semconv.MessagingDestinationName(queueName),
		),
	)
	defer span.End()
	defer tracing.RecordSpanErr(span, &err)

	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal request message: %w", err)
	}
	queue, ok := r.queues[queueName]
	if !ok {
		return fmt.Errorf("unknown queue: %s", queueName)
	}

	publishing := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Headers:      injectHeaders(ctx, amqp.Table{}),
		Body:         body,
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.channel.PublishWithContext(
		ctx,
		"",
		queue.Name,
		false,
		false,
		publishing,
	); err != nil {
		return fmt.Errorf("failed to publish request message: %w", err)
	}

	return nil
}

func (r *rabbitMQQueue) Close() error {
	return r.channel.Close()
}
