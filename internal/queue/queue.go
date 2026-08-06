package queue

import (
	"context"

	"github.com/pelfox/gophprofile/pkg"
)

// ResizeQueueName is the RabbitMQ queue name for avatar resize jobs.
const ResizeQueueName = "resize"

// Provider publishes avatar processing jobs.
type Provider interface {
	// RequestResize queues a thumbnail generation job.
	RequestResize(ctx context.Context, message pkg.MessageResizeRequest) error
	// Close releases queue resources.
	Close() error
}
