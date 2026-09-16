package breaker

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/sony/gobreaker"
)

var errUpstream = errors.New("upstream failed")

func TestNewTripsAfterConsecutiveFailures(t *testing.T) {
	cb := New(slog.New(slog.DiscardHandler), "test-dependency")

	for i := 0; i < consecutiveFailureThreshold; i++ {
		_, err := cb.Execute(func() (any, error) {
			return nil, errUpstream
		})
		if !errors.Is(err, errUpstream) {
			t.Fatalf("call %d: expected upstream error, got %v", i, err)
		}
	}

	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("expected breaker to be open after %d consecutive failures, got %s",
			consecutiveFailureThreshold, cb.State())
	}

	called := false
	_, err := cb.Execute(func() (any, error) {
		called = true
		return nil, nil
	})
	if !errors.Is(err, gobreaker.ErrOpenState) {
		t.Fatalf("expected ErrOpenState while open, got %v", err)
	}
	if called {
		t.Fatal("the wrapped function must not run while the breaker is open")
	}
}

func TestNewClosedStateAllowsCalls(t *testing.T) {
	cb := New(slog.New(slog.DiscardHandler), "test-dependency")

	result, err := cb.Execute(func() (any, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error in closed state: %v", err)
	}
	if result.(string) != "ok" {
		t.Fatalf("expected result %q, got %q", "ok", result)
	}
	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("expected breaker to stay closed after a success, got %s", cb.State())
	}
}

func TestNewWithIsSuccessfulTreatsMatchingErrorsAsSuccess(t *testing.T) {
	errNotFound := errors.New("not found")
	cb := NewWithIsSuccessful(
		slog.New(slog.DiscardHandler),
		"test-dependency",
		func(err error) bool { return errors.Is(err, errNotFound) },
	)

	for i := 0; i < consecutiveFailureThreshold*2; i++ {
		_, err := cb.Execute(func() (any, error) {
			return nil, errNotFound
		})
		if !errors.Is(err, errNotFound) {
			t.Fatalf("call %d: expected errNotFound, got %v", i, err)
		}
	}

	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("a whitelisted error must never trip the breaker, got state %s",
			cb.State())
	}
}
