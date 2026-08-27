// Package tracing provides small OpenTelemetry helpers shared across
// packages that create their own spans (services, queue, worker, app).
package tracing

import (
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// RecordError marks span as failed if err is non-nil.
func RecordError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// RecordSpanErr marks span as failed once *err is set. It is meant to be
// deferred right after starting a span in a function with a named error
// return, e.g. `defer tracing.RecordSpanErr(span, &err)`: the pointer is
// bound immediately, but it is only dereferenced once the deferred call
// runs after the return value has been assigned.
func RecordSpanErr(span trace.Span, err *error) {
	RecordError(span, *err)
}
