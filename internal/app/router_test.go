package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pelfox/gophprofile/internal/config"
	"github.com/pelfox/gophprofile/internal/controllers"
	"github.com/pelfox/gophprofile/internal/services"
	"github.com/pelfox/gophprofile/pkg"
)

// noopAvatarsService is a minimal services.AvatarsService that always
// returns an error, since these tests only care about which requests reach
// the rate limiter and never actually exercise avatar business logic.
type noopAvatarsService struct{}

func (noopAvatarsService) Create(
	context.Context, uuid.UUID, services.CreateAvatarInput,
) (*services.CreateAvatarResult, error) {
	return nil, errNoop
}

func (noopAvatarsService) GetByID(
	context.Context, uuid.UUID,
) (string, []byte, error) {
	return "", nil, errNoop
}

func (noopAvatarsService) GetMetadataByID(
	context.Context, uuid.UUID,
) (*services.GetMetadataResult, error) {
	return nil, errNoop
}

func (noopAvatarsService) GetByUserID(
	context.Context, uuid.UUID,
) (string, []byte, error) {
	return "", nil, errNoop
}

func (noopAvatarsService) ListForUser(
	context.Context, uuid.UUID,
) ([]services.GetMetadataResult, error) {
	return nil, errNoop
}

func (noopAvatarsService) DeleteByID(context.Context, uuid.UUID, uuid.UUID) error {
	return errNoop
}

func (noopAvatarsService) DeleteLatestForUser(
	context.Context, uuid.UUID, uuid.UUID,
) error {
	return errNoop
}

func (noopAvatarsService) CompleteResize(
	context.Context, pkg.MessageResizeDone,
) error {
	return errNoop
}

var errNoop = errNoopError{}

type errNoopError struct{}

func (errNoopError) Error() string { return "noop" }

func testRouter(t *testing.T, requestLimit int) http.Handler {
	t.Helper()

	logger := slog.New(slog.DiscardHandler)
	avatarsController := controllers.NewAvatarsController(logger, noopAvatarsService{})
	healthController := controllers.NewHealthController(logger)
	cfg := &config.AppConfig{
		RateLimitRequests: requestLimit,
		RateLimitWindow:   time.Minute,
	}

	return newRouter(avatarsController, healthController, cfg)
}

func TestRouterRateLimitsAPIRoutes(t *testing.T) {
	router := testRouter(t, 3)
	userID := uuid.New()

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(
			http.MethodGet, "/api/v1/users/"+userID.String()+"/avatars", nil,
		)
		req.RemoteAddr = "203.0.113.1:1234"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d: got 429 before exhausting the limit", i)
		}
	}

	req := httptest.NewRequest(
		http.MethodGet, "/api/v1/users/"+userID.String()+"/avatars", nil,
	)
	req.RemoteAddr = "203.0.113.1:1234"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exhausting the limit, got %d", rec.Code)
	}
}

func TestRouterRateLimitDoesNotBlockProbesOrMetrics(t *testing.T) {
	router := testRouter(t, 1)

	// Exhaust the /api/v1 limit for this IP first.
	apiReq := httptest.NewRequest(
		http.MethodGet, "/api/v1/users/"+uuid.New().String()+"/avatars", nil,
	)
	apiReq.RemoteAddr = "203.0.113.2:1234"
	router.ServeHTTP(httptest.NewRecorder(), apiReq)

	for _, path := range []string{"/health", "/livez", "/metrics"} {
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "203.0.113.2:1234"
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code == http.StatusTooManyRequests {
				t.Fatalf("%s: got 429, probes/metrics must never be rate limited", path)
			}
		}
	}
}

func TestRouterRateLimitIsPerClientIP(t *testing.T) {
	router := testRouter(t, 1)

	req1 := httptest.NewRequest(
		http.MethodGet, "/api/v1/users/"+uuid.New().String()+"/avatars", nil,
	)
	req1.RemoteAddr = "203.0.113.3:1234"
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	if rec1.Code == http.StatusTooManyRequests {
		t.Fatalf("first client's first request should not be rate limited")
	}

	req2 := httptest.NewRequest(
		http.MethodGet, "/api/v1/users/"+uuid.New().String()+"/avatars", nil,
	)
	req2.RemoteAddr = "203.0.113.4:1234"
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusTooManyRequests {
		t.Fatalf("a different client IP should get its own rate limit budget")
	}
}
