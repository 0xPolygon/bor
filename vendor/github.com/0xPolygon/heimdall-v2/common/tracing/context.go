package tracing

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type tracerKey struct{}

type Option func(context.Context, trace.Span)

func WithTracer(ctx context.Context, tr trace.Tracer) context.Context {
	return context.WithValue(ctx, tracerKey{}, tr)
}

func FromContext(ctx context.Context) trace.Tracer {
	tr, _ := ctx.Value(tracerKey{}).(trace.Tracer)

	return tr
}

func StartSpan(ctx context.Context, spanName string) (context.Context, trace.Span) {
	tr := FromContext(ctx)

	if tr == nil {
		return ctx, nil
	}

	ctx, span := tr.Start(ctx, spanName)
	ctx = WithTracer(ctx, tr)

	return ctx, span
}

func EndSpan(span trace.Span) {
	if span != nil {
		span.End()
	}
}

func SetAttributes(span trace.Span, kvs ...attribute.KeyValue) {
	if span != nil {
		span.SetAttributes(kvs...)
	}
}
