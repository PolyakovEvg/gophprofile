package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pelfox/gophprofile/internal/config"
	"github.com/pelfox/gophprofile/internal/controllers"
	"github.com/pelfox/gophprofile/internal/queue"
	"github.com/pelfox/gophprofile/internal/repositories"
	"github.com/pelfox/gophprofile/internal/services"
	"github.com/pelfox/gophprofile/internal/storage"
	"github.com/pelfox/gophprofile/pkg"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"
)

const shutdownTimeout = 10 * time.Second

// Run starts the application with the given logger and configuration.
func Run(logger zerolog.Logger, cfg *config.AppConfig) error {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("failed to create database pool: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

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

	avatarsRepository := repositories.NewAvatarsRepository(pool)
	avatarsService := services.NewAvatarsService(
		logger,
		avatarsRepository,
		newS3Storage(cfg),
		queueProvider,
	)
	avatarsController := controllers.NewAvatarsController(
		logger,
		avatarsService,
	)

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: newRouter(avatarsController),
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- consumeResizeDoneQueue(ctx, logger, conn, avatarsService)
	}()
	go func() {
		logger.Info().Str("addr", cfg.ListenAddr).Msg("started HTTP server")
		if err := server.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("failed to run HTTP server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		return shutdownServer(server)
	case err := <-errCh:
		stop()
		if shutdownErr := shutdownServer(server); shutdownErr != nil {
			return shutdownErr
		}
		return err
	}
}

func newRouter(avatarsController *controllers.AvatarsController) http.Handler {
	router := chi.NewRouter()
	router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/index.html")
	})
	router.Route("/api/v1", func(router chi.Router) {
		router.Post("/avatars", avatarsController.Upload)
		router.Get("/avatars/{avatarID}", avatarsController.GetByID)
		router.Get("/avatars/{avatarID}/metadata", avatarsController.GetMetadata)
		router.Delete("/avatars/{avatarID}", avatarsController.Delete)
		router.Get("/users/me/avatar", avatarsController.GetUserAvatar)
	})

	return router
}

func newS3Storage(cfg *config.AppConfig) storage.Provider {
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

func consumeResizeDoneQueue(
	ctx context.Context,
	logger zerolog.Logger,
	conn *amqp.Connection,
	avatarsService services.AvatarsService,
) error {
	channel, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open RabbitMQ channel: %w", err)
	}
	defer channel.Close()

	resizeDoneQueue, err := channel.QueueDeclare(
		queue.ResizeDoneQueueName,
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to declare resize done queue: %w", err)
	}

	if err := channel.Qos(1, 0, false); err != nil {
		return fmt.Errorf("failed to configure resize done prefetch: %w", err)
	}

	deliveries, err := channel.Consume(
		resizeDoneQueue.Name,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to consume resize done queue: %w", err)
	}

	logger.Info().Msg("started resize completion consumer")
	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("resize completion consumer closed")
			}

			var message pkg.MessageResizeDone
			if err := json.Unmarshal(delivery.Body, &message); err != nil {
				logger.Error().Err(err).Msg("failed to unmarshal resize done message")
				if err := delivery.Nack(false, false); err != nil {
					return fmt.Errorf("failed to reject resize done message: %w", err)
				}
				continue
			}

			if err := avatarsService.CompleteResize(ctx, message); err != nil {
				logger.Error().Err(err).Msg("failed to complete avatar resize")
				requeue := !errors.Is(err, services.ErrAvatarNotFound)
				if err := delivery.Nack(false, requeue); err != nil {
					return fmt.Errorf("failed to reject resize done message: %w", err)
				}
				continue
			}

			if err := delivery.Ack(false); err != nil {
				return fmt.Errorf("failed to acknowledge resize done message: %w", err)
			}
		}
	}
}

func shutdownServer(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shut down HTTP server: %w", err)
	}

	return nil
}
