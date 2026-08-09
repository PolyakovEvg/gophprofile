package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"path"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/pelfox/gophprofile/internal/config"
	"github.com/pelfox/gophprofile/internal/models"
	"github.com/pelfox/gophprofile/internal/queue"
	"github.com/pelfox/gophprofile/internal/storage"
	"github.com/pelfox/gophprofile/pkg"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	consumerPrefetch     = 1
	thumbnailContentType = "image/jpeg"
	thumbnailQuality     = 90

	// retryCountHeader stores the number of prior processing attempts.
	retryCountHeader = "x-retry-count"
	// maxRetryAttempts is how many times a failed job is retried before
	// being dropped.
	maxRetryAttempts = 5
	// baseRetryDelay is the backoff delay after the first failure; it
	// doubles with every subsequent attempt.
	baseRetryDelay = 500 * time.Millisecond
	// maxRetryDelay caps the exponential backoff delay.
	maxRetryDelay = 30 * time.Second
)

type thumbnailSize struct {
	label string
	size  int
}

var thumbnailSizes = []thumbnailSize{
	{label: "100x100", size: 100},
	{label: "300x300", size: 300},
}

type processor struct {
	logger  zerolog.Logger
	queue   queue.PublisherProvider
	storage storage.Provider
}

// Run starts the avatar resize worker.
func Run(
	ctx context.Context,
	logger zerolog.Logger,
	cfg *config.WorkerConfig,
) error {
	conn, err := amqp.Dial(cfg.RabbitMQURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer conn.Close()

	queueProvider, err := queue.NewRabbitMQQueue(logger, conn)
	if err != nil {
		return err
	}
	defer queueProvider.Close()

	processor := &processor{
		logger: logger.With().Str("worker", "avatar").Logger(),
		queue:  queueProvider,
		storage: storage.NewS3StorageFromConfig(storage.S3StorageConfig{
			Region:    cfg.S3Region,
			Endpoint:  cfg.S3Endpoint,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
			Bucket:    cfg.S3Bucket,
		}),
	}

	return consumeQueues(ctx, processor.logger, conn, processor)
}

func consumeQueues(
	ctx context.Context,
	logger zerolog.Logger,
	conn *amqp.Connection,
	processor *processor,
) error {
	channel, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open RabbitMQ channel: %w", err)
	}
	defer channel.Close()

	resizeQueue, err := declareQueue(channel, queue.ResizeQueueName)
	if err != nil {
		return err
	}

	deleteQueue, err := declareQueue(channel, queue.DeleteQueueName)
	if err != nil {
		return err
	}

	resizeRetryQueue, err := declareRetryQueue(channel, queue.ResizeQueueName)
	if err != nil {
		return err
	}

	deleteRetryQueue, err := declareRetryQueue(channel, queue.DeleteQueueName)
	if err != nil {
		return err
	}

	if err := channel.Qos(consumerPrefetch, 0, false); err != nil {
		return fmt.Errorf("failed to configure worker prefetch: %w", err)
	}

	resizeDeliveries, err := channel.Consume(
		resizeQueue.Name,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to consume resize queue: %w", err)
	}

	deleteDeliveries, err := channel.Consume(
		deleteQueue.Name,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to consume delete queue: %w", err)
	}

	logger.Info().Msg("started avatar worker")
	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-resizeDeliveries:
			if !ok {
				return errors.New("resize queue consumer closed")
			}

			if err := processor.processResize(ctx, delivery.Body); err != nil {
				logger.Error().Err(err).Msg("failed to process resize job")
				if err := handleFailedDelivery(
					channel, logger, delivery, resizeRetryQueue.Name, err,
				); err != nil {
					return err
				}
				continue
			}

			if err := delivery.Ack(false); err != nil {
				return fmt.Errorf("failed to acknowledge resize job: %w", err)
			}
		case delivery, ok := <-deleteDeliveries:
			if !ok {
				return errors.New("delete queue consumer closed")
			}

			if err := processor.processDelete(ctx, delivery.Body); err != nil {
				logger.Error().Err(err).Msg("failed to process delete job")
				if err := handleFailedDelivery(
					channel, logger, delivery, deleteRetryQueue.Name, err,
				); err != nil {
					return err
				}
				continue
			}

			if err := delivery.Ack(false); err != nil {
				return fmt.Errorf("failed to acknowledge delete job: %w", err)
			}
		}
	}
}

func declareQueue(channel *amqp.Channel, name string) (amqp.Queue, error) {
	queue, err := channel.QueueDeclare(
		name,
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return amqp.Queue{}, fmt.Errorf("failed to declare %s queue: %w", name, err)
	}

	return queue, nil
}

// declareRetryQueue declares a holding queue that dead-letters expired
// messages back into targetQueue, used to delay retries without requiring
// the RabbitMQ delayed-message plugin.
func declareRetryQueue(
	channel *amqp.Channel,
	targetQueue string,
) (amqp.Queue, error) {
	name := targetQueue + ".retry"
	queue, err := channel.QueueDeclare(
		name,
		true,
		false,
		false,
		false,
		amqp.Table{
			"x-dead-letter-exchange":    "",
			"x-dead-letter-routing-key": targetQueue,
		},
	)
	if err != nil {
		return amqp.Queue{}, fmt.Errorf("failed to declare %s queue: %w", name, err)
	}

	return queue, nil
}

// handleFailedDelivery decides whether to retry a failed job with
// exponential backoff or give up after too many attempts. Retries are
// implemented by republishing to a per-queue retry queue with a
// per-message TTL; once the TTL expires, RabbitMQ dead-letters the
// message back into the original queue for reprocessing.
func handleFailedDelivery(
	channel *amqp.Channel,
	logger zerolog.Logger,
	delivery amqp.Delivery,
	retryQueueName string,
	processingErr error,
) error {
	if errors.Is(processingErr, context.Canceled) ||
		errors.Is(processingErr, context.DeadlineExceeded) {
		if err := delivery.Nack(false, true); err != nil {
			return fmt.Errorf("failed to requeue job: %w", err)
		}
		return nil
	}

	attempt := retryAttempt(delivery.Headers) + 1
	if attempt > maxRetryAttempts {
		logger.Error().
			Err(processingErr).
			Int("attempts", attempt-1).
			Msg("giving up after exhausting retry attempts")
		if err := delivery.Ack(false); err != nil {
			return fmt.Errorf("failed to drop poison job: %w", err)
		}
		return nil
	}

	delay := backoffDelay(attempt)
	headers := amqp.Table{}
	for key, value := range delivery.Headers {
		headers[key] = value
	}
	headers[retryCountHeader] = int32(attempt)

	err := channel.PublishWithContext(
		context.Background(),
		"",
		retryQueueName,
		false,
		false,
		amqp.Publishing{
			ContentType:  delivery.ContentType,
			DeliveryMode: amqp.Persistent,
			Headers:      headers,
			Body:         delivery.Body,
			Expiration:   strconv.FormatInt(delay.Milliseconds(), 10),
		},
	)
	if err != nil {
		logger.Error().
			Err(err).
			Msg("failed to schedule retry, requeueing immediately instead")
		if nackErr := delivery.Nack(false, true); nackErr != nil {
			return fmt.Errorf("failed to requeue job: %w", nackErr)
		}
		return nil
	}

	logger.Warn().
		Err(processingErr).
		Int("attempt", attempt).
		Dur("delay", delay).
		Msg("scheduled job retry with backoff")

	if err := delivery.Ack(false); err != nil {
		return fmt.Errorf("failed to acknowledge job pending retry: %w", err)
	}
	return nil
}

func retryAttempt(headers amqp.Table) int {
	switch value := headers[retryCountHeader].(type) {
	case int32:
		return int(value)
	case int64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

func backoffDelay(attempt int) time.Duration {
	delay := baseRetryDelay * time.Duration(int64(1)<<uint(attempt-1))
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

func (p *processor) processResize(ctx context.Context, body []byte) error {
	var message pkg.MessageResizeRequest
	if err := json.Unmarshal(body, &message); err != nil {
		return fmt.Errorf("failed to unmarshal resize request: %w", err)
	}
	if message.ID == uuid.Nil {
		return errors.New("resize request does not contain avatar ID")
	}
	if message.UserID == uuid.Nil {
		return errors.New("resize request does not contain user ID")
	}
	if message.FileName == "" {
		return errors.New("resize request does not contain avatar file name")
	}
	if message.Key == "" {
		return errors.New("resize request does not contain avatar storage key")
	}

	logger := p.logger.With().
		Str("avatar_id", message.ID.String()).
		Logger()

	thumbnailKeys, err := p.createThumbnails(ctx, message)
	if err != nil {
		logger.Error().Err(err).Msg("failed to create thumbnails")
		return fmt.Errorf("failed to create thumbnails: %w", err)
	}

	if err := p.queue.CompleteResize(
		ctx,
		pkg.MessageResizeDone{
			ID:              message.ID,
			ThumbnailS3Keys: thumbnailKeys,
		},
	); err != nil {
		return fmt.Errorf("failed to publish resize completion: %w", err)
	}

	logger.Info().Msg("processed resize job")
	return nil
}

func (p *processor) processDelete(ctx context.Context, body []byte) error {
	var message pkg.MessageDeleteRequest
	if err := json.Unmarshal(body, &message); err != nil {
		return fmt.Errorf("failed to unmarshal delete request: %w", err)
	}
	if message.ID == uuid.Nil {
		return errors.New("delete request does not contain avatar ID")
	}

	if err := p.storage.Delete(ctx, message.Keys); err != nil {
		return err
	}

	p.logger.Info().
		Str("avatar_id", message.ID.String()).
		Msg("processed delete job")
	return nil
}

func (p *processor) createThumbnails(
	ctx context.Context,
	message pkg.MessageResizeRequest,
) (models.ThumbnailS3Keys, error) {
	original, err := p.storage.Retrieve(ctx, message.FileName)
	if err != nil {
		return models.ThumbnailS3Keys{}, fmt.Errorf(
			"failed to retrieve original avatar: %w",
			err,
		)
	}

	source, _, err := image.Decode(bytes.NewReader(original))
	if err != nil {
		return models.ThumbnailS3Keys{}, fmt.Errorf(
			"failed to decode original avatar: %w",
			err,
		)
	}

	var keys models.ThumbnailS3Keys
	for _, thumbnail := range thumbnailSizes {
		key := path.Join(message.Key, thumbnail.label+".jpg")
		payload, err := resizeToJPEG(source, thumbnail.size)
		if err != nil {
			return models.ThumbnailS3Keys{}, err
		}

		_, err = p.storage.Store(ctx, storage.StoreInput{
			Key:         key,
			Body:        payload,
			ContentType: thumbnailContentType,
			UserID:      message.UserID,
			AvatarID:    message.ID,
		})
		if err != nil {
			return models.ThumbnailS3Keys{}, fmt.Errorf(
				"failed to store %s thumbnail: %w",
				thumbnail.label,
				err,
			)
		}

		switch thumbnail.label {
		case "100x100":
			keys.Size100x100 = key
		case "300x300":
			keys.Size300x300 = key
		}
	}

	return keys, nil
}

func resizeToJPEG(source image.Image, size int) ([]byte, error) {
	destination := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(
		destination,
		destination.Bounds(),
		source,
		centerSquare(source.Bounds()),
		draw.Over,
		nil,
	)

	var output bytes.Buffer
	if err := jpeg.Encode(
		&output,
		destination,
		&jpeg.Options{Quality: thumbnailQuality},
	); err != nil {
		return nil, fmt.Errorf("failed to encode thumbnail: %w", err)
	}

	return output.Bytes(), nil
}

func centerSquare(bounds image.Rectangle) image.Rectangle {
	width := bounds.Dx()
	height := bounds.Dy()
	if width == height {
		return bounds
	}

	if width > height {
		offset := (width - height) / 2
		return image.Rect(
			bounds.Min.X+offset,
			bounds.Min.Y,
			bounds.Min.X+offset+height,
			bounds.Max.Y,
		)
	}

	offset := (height - width) / 2
	return image.Rect(
		bounds.Min.X,
		bounds.Min.Y+offset,
		bounds.Max.X,
		bounds.Min.Y+offset+width,
	)
}
