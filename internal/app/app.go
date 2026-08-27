package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/exaring/otelpgx"
	"github.com/go-chi/chi/v5"
	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pelfox/gophprofile/internal/config"
	"github.com/pelfox/gophprofile/internal/controllers"
	"github.com/pelfox/gophprofile/internal/logging"
	"github.com/pelfox/gophprofile/internal/metrics"
	"github.com/pelfox/gophprofile/internal/queue"
	"github.com/pelfox/gophprofile/internal/repositories"
	"github.com/pelfox/gophprofile/internal/services"
	"github.com/pelfox/gophprofile/internal/storage"
	"github.com/pelfox/gophprofile/internal/telemetry"
	"github.com/pelfox/gophprofile/internal/tracing"
	"github.com/pelfox/gophprofile/migrations"
	"github.com/pelfox/gophprofile/pkg"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/sync/errgroup"
)

const shutdownTimeout = 10 * time.Second

// Run starts the application with the given logger and configuration.
func Run(logger *slog.Logger, cfg *config.AppConfig) error {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("failed to parse database URL: %w", err)
	}
	poolConfig.ConnConfig.Tracer = otelpgx.NewTracer()

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("failed to create database pool: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := runMigrations(cfg.DatabaseURL); err != nil {
		return fmt.Errorf("failed to apply database migrations: %w", err)
	}

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

	avatarsRepository := repositories.NewAvatarsRepository(pool)
	avatarsService := services.NewAvatarsService(
		logger,
		avatarsRepository,
		storage.NewS3Storage(s3Client, cfg.S3Bucket, cfg.S3Endpoint),
		queueProvider,
	)
	avatarsController := controllers.NewAvatarsController(
		logger,
		avatarsService,
	)
	healthController := controllers.NewHealthController(
		logger,
		controllers.NewFuncHealthChecker("database", func(ctx context.Context) error {
			return pool.Ping(ctx)
		}),
		controllers.NewFuncHealthChecker("storage", func(ctx context.Context) error {
			_, err := s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
				Bucket: aws.String(cfg.S3Bucket),
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

	handler := otelhttp.NewHandler(
		newRouter(avatarsController, healthController),
		"http-server",
	)
	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: handler,
	}

	if err := metrics.WatchQueueDepths(ctx, logger, conn, []string{
		queue.ResizeQueueName,
		queue.ResizeDoneQueueName,
		queue.DeleteQueueName,
	}); err != nil {
		return fmt.Errorf("failed to start queue depth watcher: %w", err)
	}
	go metrics.WatchDBPool(ctx, pool)

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return consumeResizeDoneQueue(groupCtx, logger, conn, avatarsService)
	})
	group.Go(func() error {
		logging.FromContext(ctx, logger).
			Info("started HTTP server", "addr", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("failed to run HTTP server: %w", err)
		}
		return nil
	})

	<-groupCtx.Done()
	shutdownErr := shutdownServer(server)
	if err := group.Wait(); err != nil {
		return err
	}
	return shutdownErr
}

func newRouter(
	avatarsController *controllers.AvatarsController,
	healthController *controllers.HealthController,
) http.Handler {
	router := chi.NewRouter()
	router.Use(metrics.HTTPMiddleware)
	router.Use(telemetry.RouteTag)
	router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/index.html")
	})
	router.Get("/health", healthController.Health)
	router.Handle("/metrics", promhttp.Handler())
	router.Route("/api/v1", func(router chi.Router) {
		router.Post("/avatars", avatarsController.Upload)
		router.Get("/avatars/{avatarID}", avatarsController.GetByID)
		router.Get("/avatars/{avatarID}/metadata", avatarsController.GetMetadata)
		router.Delete("/avatars/{avatarID}", avatarsController.Delete)
		router.Get("/users/{userID}/avatar", avatarsController.GetUserAvatar)
		router.Delete("/users/{userID}/avatar", avatarsController.DeleteUserAvatar)
		router.Get("/users/{userID}/avatars", avatarsController.ListUserAvatars)
	})

	return router
}

func consumeResizeDoneQueue(
	ctx context.Context,
	logger *slog.Logger,
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

	logger.Info("started resize completion consumer")
	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("resize completion consumer closed")
			}

			if err := handleResizeDoneDelivery(
				ctx, logger, delivery, avatarsService,
			); err != nil {
				return err
			}
		}
	}
}

func handleResizeDoneDelivery(
	ctx context.Context,
	logger *slog.Logger,
	delivery amqp.Delivery,
	avatarsService services.AvatarsService,
) error {
	spanCtx, span := queue.StartConsumerSpan(
		ctx, delivery.Headers, queue.ResizeDoneQueueName,
	)
	defer span.End()
	requestLogger := logging.FromContext(spanCtx, logger)

	var message pkg.MessageResizeDone
	if err := json.Unmarshal(delivery.Body, &message); err != nil {
		tracing.RecordError(span, err)
		requestLogger.Error("failed to unmarshal resize done message", "error", err)
		if err := delivery.Nack(false, false); err != nil {
			return fmt.Errorf("failed to reject resize done message: %w", err)
		}
		return nil
	}

	if err := avatarsService.CompleteResize(spanCtx, message); err != nil {
		tracing.RecordError(span, err)
		requestLogger.Error("failed to complete avatar resize", "error", err)
		requeue := !errors.Is(err, services.ErrAvatarNotFound)
		if err := delivery.Nack(false, requeue); err != nil {
			return fmt.Errorf("failed to reject resize done message: %w", err)
		}
		return nil
	}

	if err := delivery.Ack(false); err != nil {
		return fmt.Errorf("failed to acknowledge resize done message: %w", err)
	}
	return nil
}

func runMigrations(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("failed to open migration database connection: %w", err)
	}
	defer db.Close()

	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		return fmt.Errorf("failed to initialize migration driver: %w", err)
	}

	source, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("failed to load embedded migrations: %w", err)
	}

	migrator, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("failed to initialize migrator: %w", err)
	}

	if err := migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	return nil
}

func shutdownServer(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shut down HTTP server: %w", err)
	}

	return nil
}
