package controllers

import (
	"context"
	"net/http"

	"github.com/rs/zerolog"
)

// HealthChecker reports the availability of a single dependency.
type HealthChecker interface {
	// Name identifies the dependency being checked.
	Name() string
	// Check returns an error if the dependency is unavailable.
	Check(ctx context.Context) error
}

// FuncHealthChecker adapts a function into a HealthChecker.
type FuncHealthChecker struct {
	name string
	fn   func(ctx context.Context) error
}

// NewFuncHealthChecker creates a HealthChecker backed by fn.
func NewFuncHealthChecker(
	name string,
	fn func(ctx context.Context) error,
) *FuncHealthChecker {
	return &FuncHealthChecker{name: name, fn: fn}
}

// Name identifies the dependency being checked.
func (c *FuncHealthChecker) Name() string {
	return c.name
}

// Check returns an error if the dependency is unavailable.
func (c *FuncHealthChecker) Check(ctx context.Context) error {
	return c.fn(ctx)
}

// componentStatus is the reported health of a single dependency.
type componentStatus struct {
	// Status is either "ok" or "error".
	Status string `json:"status"`
	// Error describes the failure, if any.
	Error string `json:"error,omitempty"`
}

// healthResponse is the JSON body returned by the health endpoint.
type healthResponse struct {
	// Status is "ok" if every component is healthy, otherwise "degraded".
	Status string `json:"status"`
	// Components maps dependency name to its status.
	Components map[string]componentStatus `json:"components"`
}

// HealthController reports the health of the service and its dependencies.
type HealthController struct {
	logger   zerolog.Logger
	checkers []HealthChecker
}

// NewHealthController creates a health HTTP controller.
func NewHealthController(
	logger zerolog.Logger,
	checkers ...HealthChecker,
) *HealthController {
	return &HealthController{
		logger:   logger.With().Str("controller", "health").Logger(),
		checkers: checkers,
	}
}

// Health handles healthcheck requests, probing every registered dependency.
func (c *HealthController) Health(w http.ResponseWriter, r *http.Request) {
	components := make(map[string]componentStatus, len(c.checkers))
	healthy := true

	for _, checker := range c.checkers {
		if err := checker.Check(r.Context()); err != nil {
			healthy = false
			components[checker.Name()] = componentStatus{
				Status: "error",
				Error:  err.Error(),
			}
			c.logger.Error().
				Err(err).
				Str("component", checker.Name()).
				Msg("health check failed")
			continue
		}

		components[checker.Name()] = componentStatus{Status: "ok"}
	}

	status := "ok"
	code := http.StatusOK
	if !healthy {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	WriteJSON(w, code, healthResponse{
		Status:     status,
		Components: components,
	})
}
