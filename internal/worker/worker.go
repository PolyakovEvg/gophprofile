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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/pelfox/gophprofile/internal/config"
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
		logger:  logger.With().Str("worker", "avatar").Logger(),
		queue:   queueProvider,
		storage: newS3Storage(cfg),
	}

	return consumeQueues(ctx, processor.logger, conn, processor)
}

func newS3Storage(cfg *config.WorkerConfig) storage.Provider {
	awsCfg := aws.Config{
		Region: cfg.S3Region,
	}
	if cfg.S3AccessKey != "" || cfg.S3SecretKey != "" {
		awsCfg.Credentials = aws.CredentialsProviderFunc(
			func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     cfg.S3AccessKey,
					SecretAccessKey: cfg.S3SecretKey,
				}, nil
			},
		)
	}

	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if cfg.S3Endpoint == "" {
			return
		}

		options.BaseEndpoint = aws.String(cfg.S3Endpoint)
		options.UsePathStyle = true
	})

	return storage.NewS3Storage(client, cfg.S3Bucket, cfg.S3Endpoint)
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
				if err := rejectDelivery(delivery, err); err != nil {
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
				if err := rejectDelivery(delivery, err); err != nil {
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

func rejectDelivery(delivery amqp.Delivery, err error) error {
	requeue := errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
	if nackErr := delivery.Nack(false, requeue); nackErr != nil {
		return fmt.Errorf("failed to reject job: %w", nackErr)
	}

	return nil
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
) (pkg.ThumbnailS3Keys, error) {
	original, err := p.storage.Retrieve(ctx, message.FileName)
	if err != nil {
		return pkg.ThumbnailS3Keys{}, fmt.Errorf(
			"failed to retrieve original avatar: %w",
			err,
		)
	}

	source, _, err := image.Decode(bytes.NewReader(original))
	if err != nil {
		return pkg.ThumbnailS3Keys{}, fmt.Errorf(
			"failed to decode original avatar: %w",
			err,
		)
	}

	var keys pkg.ThumbnailS3Keys
	for _, thumbnail := range thumbnailSizes {
		key := path.Join(message.Key, thumbnail.label+".jpg")
		payload, err := resizeToJPEG(source, thumbnail.size)
		if err != nil {
			return pkg.ThumbnailS3Keys{}, err
		}

		_, err = p.storage.Store(ctx, storage.StoreInput{
			Key:         key,
			Body:        payload,
			ContentType: thumbnailContentType,
			UserID:      message.UserID,
			AvatarID:    message.ID,
		})
		if err != nil {
			return pkg.ThumbnailS3Keys{}, fmt.Errorf(
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
