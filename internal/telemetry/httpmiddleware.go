package telemetry

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/trace"
)

// RouteTag renames the span active in the request context (started by
// otelhttp, which only knows a fixed operation name) to "<METHOD>
// <route pattern>" once chi has matched the route. Without this, every HTTP
// span exported to Jaeger shares the same operation name, making it
// impossible to tell requests to different endpoints apart in the trace
// list. Must run inside the chi router (e.g. via router.Use), after routing
// has populated the chi.RouteContext.
func RouteTag(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			return
		}

		trace.SpanFromContext(r.Context()).SetName(r.Method + " " + route)
	})
}
