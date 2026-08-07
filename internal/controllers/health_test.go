package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestHealthController_Health_AllHealthy(t *testing.T) {
	controller := NewHealthController(
		zerolog.Nop(),
		NewFuncHealthChecker("database", func(context.Context) error { return nil }),
		NewFuncHealthChecker("storage", func(context.Context) error { return nil }),
	)

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	recorder := httptest.NewRecorder()

	controller.Health(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}

	var body healthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("expected status ok, got %q", body.Status)
	}
	if body.Components["database"].Status != "ok" {
		t.Fatalf("expected database status ok, got %+v", body.Components["database"])
	}
	if body.Components["storage"].Status != "ok" {
		t.Fatalf("expected storage status ok, got %+v", body.Components["storage"])
	}
}

func TestHealthController_Health_Degraded(t *testing.T) {
	controller := NewHealthController(
		zerolog.Nop(),
		NewFuncHealthChecker("database", func(context.Context) error { return nil }),
		NewFuncHealthChecker("broker", func(context.Context) error {
			return errors.New("connection refused")
		}),
	)

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	recorder := httptest.NewRecorder()

	controller.Health(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusServiceUnavailable,
			recorder.Code,
		)
	}

	var body healthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.Status != "degraded" {
		t.Fatalf("expected status degraded, got %q", body.Status)
	}
	if body.Components["database"].Status != "ok" {
		t.Fatalf("expected database status ok, got %+v", body.Components["database"])
	}
	broker := body.Components["broker"]
	if broker.Status != "error" {
		t.Fatalf("unexpected broker status: %+v", broker)
	}
	if strings.Contains(recorder.Body.String(), "connection refused") {
		t.Fatalf(
			"response body must not leak internal error details: %s",
			recorder.Body.String(),
		)
	}
}
