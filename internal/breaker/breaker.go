// Package breaker wraps calls to external dependencies (object storage, the
// message broker, the database) in a circuit breaker, so a sustained outage
// fails fast instead of piling up requests against a dependency that is
// already down.
package breaker

import (
	"log/slog"
	"time"

	"github.com/pelfox/gophprofile/internal/metrics"
	"github.com/sony/gobreaker"
)

const (
	// openTimeout is how long the breaker stays open before allowing a
	// trial request through in the half-open state.
	openTimeout = 30 * time.Second
	// countsWindow is how often the closed-state failure counters reset,
	// so a dependency that failed a while ago but has since recovered
	// doesn't stay one flaky request away from tripping forever.
	countsWindow = 60 * time.Second
	// consecutiveFailureThreshold is how many failed requests in a row
	// trip the breaker to open.
	consecutiveFailureThreshold = 5
)

// New creates a circuit breaker for the named dependency (e.g. "s3",
// "rabbitmq", "postgres"). It trips to the open state after 5 consecutive
// failures, stays open for 30s, then lets a single trial request through in
// the half-open state to decide whether to close again or re-open. Every
// non-nil error counts as a failure; use NewWithIsSuccessful when some
// errors (e.g. "not found") are a normal outcome rather than a sign the
// dependency itself is unhealthy.
func New(logger *slog.Logger, dependency string) *gobreaker.CircuitBreaker {
	return NewWithIsSuccessful(logger, dependency, nil)
}

// NewWithIsSuccessful is New, but errors for which isSuccessful returns true
// are counted as successes instead of failures. isSuccessful may be nil,
// which is equivalent to calling New.
func NewWithIsSuccessful(
	logger *slog.Logger,
	dependency string,
	isSuccessful func(error) bool,
) *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        dependency,
		MaxRequests: 1,
		Interval:    countsWindow,
		Timeout:     openTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= consecutiveFailureThreshold
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			logger.Warn("circuit breaker state change",
				"dependency", name, "from", from.String(), "to", to.String())
			metrics.CircuitBreakerState.WithLabelValues(name).Set(float64(to))
		},
		IsSuccessful: isSuccessful,
	})
}
