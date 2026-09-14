package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// otelSpanStarter mimics what otelhttp.NewHandler does in production: it
// starts a span for the request *before* handing it to the router, and
// attaches the span to the request context via r.WithContext so downstream
// middleware (in particular RouteTag, which must run inside the router) can
// find it with trace.SpanFromContext.
func otelSpanStarter(tracer trace.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, span := tracer.Start(r.Context(), "http-server")
			defer span.End()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func TestRouteTagRenamesSpanToMethodAndMatchedRoute(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	router := chi.NewRouter()
	router.Use(otelSpanStarter(provider.Tracer("test")))
	router.Use(RouteTag)
	router.Get("/avatars/{avatarID}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/avatars/123", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	stubs := exporter.GetSpans()
	if len(stubs) != 1 {
		t.Fatalf("expected 1 exported span, got %d", len(stubs))
	}
	if got, want := stubs[0].Name, "GET /avatars/{avatarID}"; got != want {
		t.Fatalf("expected span name %q, got %q", want, got)
	}
}

func TestRouteTagLeavesSpanUntouchedWhenRouteDoesNotMatch(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	router := chi.NewRouter()
	router.Use(otelSpanStarter(provider.Tracer("test")))
	router.Use(RouteTag)
	router.Get("/known", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	stubs := exporter.GetSpans()
	if len(stubs) != 1 {
		t.Fatalf("expected 1 exported span, got %d", len(stubs))
	}
	if stubs[0].Name != "http-server" {
		t.Fatalf("expected span name to stay %q for an unmatched route, got %q",
			"http-server", stubs[0].Name)
	}
}
