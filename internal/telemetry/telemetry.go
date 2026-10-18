// Package telemetry provides opt-in OTLP tracing for the OpenLore executable.
// Libraries only use the OTel API and leave provider ownership to their host.
package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Setup initializes tracing only when an OTLP endpoint is explicitly set.
// The SDK reads standard OTEL_* resource, exporter, and sampler variables.
// The caller owns shutdown and must give it a bounded, uncancelled context.
func Setup(ctx context.Context, version string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") ||
		(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "") {
		return noop, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return noop, err
	}
	res, err := resource.New(ctx, resource.WithTelemetrySDK(), resource.WithAttributes(
		attribute.String("service.name", "openlore"),
		attribute.String("service.version", version),
	), resource.WithFromEnv())
	if err != nil {
		_ = exporter.Shutdown(ctx)
		return noop, err
	}
	// Exporter errors may contain collector URLs. Keep diagnostics content-free.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) { slog.Warn("telemetry export failed") }))
	provider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exporter))
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

// HTTPHandler intentionally omits URLs, queries, headers and peer identities.
// httpsnoop preserves optional writer interfaces required by streaming MCP.
func HTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("github.com/aakarim/go-openlore/http").Start(ctx, "openlore.http", trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		method := r.Method
		switch method {
		case "GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH":
		default:
			method = "_OTHER"
		}
		span.SetAttributes(attribute.String("http.request.method", method))
		r = r.Clone(ctx)
		// MCP SDK sessions retain their initial context. Bridge this POST's
		// span through its private header copy, which reaches RequestExtra.
		propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(r.Header))
		metrics := httpsnoop.CaptureMetrics(next, w, r)
		span.SetAttributes(attribute.Int("http.response.status_code", metrics.Code), attribute.Int64("http.response.body.size", metrics.Written))
		if metrics.Code >= 500 {
			span.SetStatus(codes.Error, "HTTP request failed")
		}
	})
}

// LogHandler adds trace correlation to context-aware structured log records.
type LogHandler struct{ slog.Handler }

func (h LogHandler) Handle(ctx context.Context, record slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		record.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, record)
}

func (h LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return LogHandler{h.Handler.WithAttrs(attrs)}
}

func (h LogHandler) WithGroup(name string) slog.Handler {
	return LogHandler{h.Handler.WithGroup(name)}
}
