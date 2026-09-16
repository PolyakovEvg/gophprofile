package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	amqp "github.com/rabbitmq/amqp091-go"
)

const pollInterval = 5 * time.Second

var (
	// DBPoolConnections reports pgx pool connection counts by state.
	DBPoolConnections = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "db_pool_connections",
			Help: "Database connection pool size by state.",
		},
		[]string{"state"},
	)

	// QueueDepth reports the number of ready messages per RabbitMQ queue.
	QueueDepth = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "queue_depth",
			Help: "Number of ready messages in a RabbitMQ queue.",
		},
		[]string{"queue"},
	)

	// CircuitBreakerState reports the current state of each dependency's
	// circuit breaker: 0 = closed, 1 = half-open, 2 = open (matching
	// gobreaker.State's own ordering).
	CircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "circuit_breaker_state",
			Help: "Circuit breaker state per dependency (0=closed, 1=half-open, 2=open).",
		},
		[]string{"dependency"},
	)
)

// WatchDBPool periodically reports pool connection stats until ctx is done.
func WatchDBPool(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stat := pool.Stat()
			DBPoolConnections.WithLabelValues("acquired").Set(float64(stat.AcquiredConns()))
			DBPoolConnections.WithLabelValues("idle").Set(float64(stat.IdleConns()))
			DBPoolConnections.WithLabelValues("total").Set(float64(stat.TotalConns()))
			DBPoolConnections.WithLabelValues("max").Set(float64(stat.MaxConns()))
		}
	}
}

// WatchQueueDepths periodically reports the ready message count for each
// named queue until ctx is done. A failed inspection (e.g. a transient
// broker hiccup) is logged and the inspection channel is reopened so
// reporting keeps going instead of silently stopping forever.
func WatchQueueDepths(
	ctx context.Context,
	logger *slog.Logger,
	conn *amqp.Connection,
	queueNames []string,
) error {
	logger = logger.With("component", "queue-depth-watcher")

	channel, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open queue depth inspection channel: %w", err)
	}

	ticker := time.NewTicker(pollInterval)
	go func() {
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				channel.Close()
				return
			case <-ticker.C:
				for _, name := range queueNames {
					info, err := channel.QueueInspect(name)
					if err != nil {
						logger.Error("failed to inspect queue depth, reopening channel",
							"error", err, "queue", name)
						channel.Close()

						newChannel, reopenErr := conn.Channel()
						if reopenErr != nil {
							logger.Error("failed to reopen queue depth inspection channel, "+
								"giving up on queue depth reporting", "error", reopenErr)
							return
						}
						channel = newChannel
						continue
					}
					QueueDepth.WithLabelValues(name).Set(float64(info.Messages))
				}
			}
		}
	}()

	return nil
}
