package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/pelfox/gophprofile/pkg"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"
)

type rabbitMQQueue struct {
	logger  zerolog.Logger
	channel *amqp.Channel
	queue   amqp.Queue
	mu      sync.Mutex
}

// NewRabbitMQQueue creates a RabbitMQ-backed queue provider.
func NewRabbitMQQueue(
	logger zerolog.Logger,
	conn *amqp.Connection,
) (Provider, error) {
	channel, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open a new RabbitMQ channel: %w", err)
	}

	queue, err := channel.QueueDeclare(
		ResizeQueueName,
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		_ = channel.Close()
		return nil, fmt.Errorf("failed to declare a new queue: %w", err)
	}

	return &rabbitMQQueue{
		logger:  logger.With().Str("queue", "rabbitmq").Logger(),
		channel: channel,
		queue:   queue,
	}, nil
}

func (r *rabbitMQQueue) RequestResize(
	ctx context.Context,
	message pkg.MessageResizeRequest,
) error {

	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal request message: %w", err)
	}

	publishing := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	err = r.channel.PublishWithContext(
		ctx,
		"",
		r.queue.Name,
		false,
		false,
		publishing,
	)
	if err != nil {
		return fmt.Errorf("failed to publish request message: %w", err)
	}

	return nil
}

func (r *rabbitMQQueue) Close() error {
	return r.channel.Close()
}
