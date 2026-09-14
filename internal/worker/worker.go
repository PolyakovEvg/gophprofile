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
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/pelfox/gophprofile/internal/config"
	"github.com/pelfox/gophprofile/internal/controllers"
	"github.com/pelfox/gophprofile/internal/logging"
	"github.com/pelfox/gophprofile/internal/metrics"
	"github.com/pelfox/gophprofile/internal/models"
	"github.com/pelfox/gophprofile/internal/queue"
	"github.com/pelfox/gophprofile/internal/storage"
	"github.com/pelfox/gophprofile/internal/tracing"
	"github.com/pelfox/gophprofile/pkg"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	amqp "github.com/rabbitmq/amqp091-go"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"golang.org/x/sync/errgroup"
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
	logger  *slog.Logger
	queue   queue.PublisherProvider
	storage storage.Provider
}

// Run starts the avatar resize worker.
func Run(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.WorkerConfig,
) error {
	conn, err := amqp.Dial(cfg.RabbitMQURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer conn.Close()

	queueProvider, err := queue.NewRabbitMQQueue(conn)
	if err != nil {
		return err
	}
	defer queueProvider.Close()

	s3Client := storage.NewS3Client(storage.S3StorageConfig{
		Region:    cfg.S3Region,
		Endpoint:  cfg.S3Endpoint,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
		Bucket:    cfg.S3Bucket,
	})

	processor := &processor{
		logger:  logger.With("worker", "avatar"),
		queue:   queueProvider,
		storage: storage.NewS3Storage(s3Client, cfg.S3Bucket, cfg.S3Endpoint),
	}

	if err := metrics.WatchQueueDepths(ctx, logger, conn, []string{
		queue.ResizeQueueName,
		queue.DeleteQueueName,
	}); err != nil {
		return fmt.Errorf("failed to start queue depth watcher: %w", err)
	}

	healthController := controllers.NewHealthController(
		logger,
		controllers.NewFuncHealthChecker("storage", func(ctx context.Context) error {
			_, err := s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
				Bucket: &cfg.S3Bucket,
			})
			return err
		}),
		controllers.NewFuncHealthChecker("broker", func(_ context.Context) error {
			if conn.IsClosed() {
				return errors.New("rabbitmq connection is closed")
			}
			return nil
		}),
	)

	metricsServer := &http.Server{
		Addr:    cfg.MetricsAddr,
		Handler: metricsRouter(healthController),
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return consumeQueues(groupCtx, processor.logger, conn, processor)
	})
	group.Go(func() error {
		logger.Info("started metrics server", "addr", cfg.MetricsAddr)
		if err := metricsServer.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("failed to run metrics server: %w", err)
		}
		return nil
	})

	<-groupCtx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := metricsServer.Shutdown(shutdownCtx)
	if err := group.Wait(); err != nil {
		return err
	}
	return shutdownErr
}

func metricsRouter(healthController *controllers.HealthController) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthController.Health)
	mux.Handle("/metrics", promhttp.Handler())
	return mux
}

func consumeQueues(
	ctx context.Context,
	logger *slog.Logger,
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

	logger.Info("started avatar worker")
	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-resizeDeliveries:
			if !ok {
				return errors.New("resize queue consumer closed")
			}

			if err := processor.processResize(ctx, delivery); err != nil {
				logger.Error("failed to process resize job", "error", err)
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

			if err := processor.processDelete(ctx, delivery); err != nil {
				logger.Error("failed to process delete job", "error", err)
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
	logger *slog.Logger,
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
		logger.Error("giving up after exhausting retry attempts",
			"error", processingErr, "attempts", attempt-1)
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
		logger.Error("failed to schedule retry, requeueing immediately instead",
			"error", err)
		if nackErr := delivery.Nack(false, true); nackErr != nil {
			return fmt.Errorf("failed to requeue job: %w", nackErr)
		}
		return nil
	}

	metrics.AvatarsJobRetriesTotal.WithLabelValues(retryQueueName).Inc()
	logger.Warn("scheduled job retry with backoff",
		"error", processingErr, "attempt", attempt, "delay", delay)

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

func (p *processor) processResize(
	ctx context.Context,
	delivery amqp.Delivery,
) (err error) {
	ctx, span := queue.StartConsumerSpan(ctx, delivery.Headers, queue.ResizeQueueName)
	defer span.End()

	defer tracing.RecordSpanErr(span, &err)

	start := time.Now()
	defer func() {
		status := "success"
		if err != nil {
			status = "error"
		}
		metrics.AvatarsResizeDuration.WithLabelValues(status).
			Observe(time.Since(start).Seconds())
	}()

	var message pkg.MessageResizeRequest
	if err := json.Unmarshal(delivery.Body, &message); err != nil {
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

	logger := logging.FromContext(ctx, p.logger).With("avatar_id", message.ID.String())

	thumbnailKeys, err := p.createThumbnails(ctx, message)
	if err != nil {
		logger.Error("failed to create thumbnails", "error", err)
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

	logger.Info("processed resize job")
	return nil
}

func (p *processor) processDelete(
	ctx context.Context,
	delivery amqp.Delivery,
) (err error) {
	ctx, span := queue.StartConsumerSpan(ctx, delivery.Headers, queue.DeleteQueueName)
	defer span.End()
	defer tracing.RecordSpanErr(span, &err)

	var message pkg.MessageDeleteRequest
	if err := json.Unmarshal(delivery.Body, &message); err != nil {
		return fmt.Errorf("failed to unmarshal delete request: %w", err)
	}
	if message.ID == uuid.Nil {
		return errors.New("delete request does not contain avatar ID")
	}

	if err := p.storage.Delete(ctx, message.Keys); err != nil {
		return err
	}

	logging.FromContext(ctx, p.logger).Info("processed delete job",
		"avatar_id", message.ID.String())
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
