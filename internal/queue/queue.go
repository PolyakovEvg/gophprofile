package queue

import (
	"context"

	"github.com/pelfox/gophprofile/pkg"
)

const (
	// ResizeQueueName is the RabbitMQ queue name for avatar resize jobs.
	ResizeQueueName = "resize"
	// ResizeDoneQueueName is the RabbitMQ queue name for completed resize jobs.
	ResizeDoneQueueName = "resize_done"
	// DeleteQueueName is the RabbitMQ queue name for avatar deletion jobs.
	DeleteQueueName = "delete"
)

// PublisherProvider publishes avatar processing jobs.
type PublisherProvider interface {
	// RequestResize queues a thumbnail generation job.
	RequestResize(ctx context.Context, message pkg.MessageResizeRequest) error
	// RequestDelete queues an avatar storage deletion job.
	RequestDelete(ctx context.Context, message pkg.MessageDeleteRequest) error
	// CompleteResize queues a completed thumbnail generation result.
	CompleteResize(ctx context.Context, message pkg.MessageResizeDone) error
	// Close releases queue resources.
	Close() error
}
