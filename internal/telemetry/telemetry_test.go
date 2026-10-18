package telemetry

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestSetupExportsOTLPAndCorrelatesLogsWithoutContent(t *testing.T) {
	previous, previousErrors := otel.GetTracerProvider(), otel.GetErrorHandler()
	defer otel.SetTracerProvider(previous)
	defer otel.SetErrorHandler(previousErrors)
	requests := make(chan *collector.ExportTraceServiceRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" || r.Header.Get("X-Test-Auth") != "test-only" || r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Errorf("unexpected OTLP request: path=%s type=%s", r.URL.Path, r.Header.Get("Content-Type"))
		}
		data, _ := io.ReadAll(r.Body)
		var request collector.ExportTraceServiceRequest
		if err := proto.Unmarshal(data, &request); err != nil {
			t.Error(err)
		}
		requests <- &request
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "X-Test-Auth=test-only")
	t.Setenv("OTEL_SERVICE_NAME", "test-openlore")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=test")
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_always_on")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	shutdown, err := Setup(t.Context(), "test-version")
	if err != nil || otel.GetTracerProvider() != previous {
		t.Fatal("disabled tracing replaced the host's provider")
	}
	_ = shutdown(t.Context())
	t.Setenv("OTEL_SDK_DISABLED", "false")
	shutdown, err = Setup(t.Context(), "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(LogHandler{slog.NewJSONHandler(&logs, nil)}).With("component", "http")
	handler := HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.InfoContext(r.Context(), "request completed")
		if _, ok := w.(http.Flusher); !ok {
			t.Error("tracing removed streaming support")
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("private-output"))
	}))
	req := httptest.NewRequest(http.MethodGet, "/private-path?token=private-query", nil)
	req.Header.Set("Authorization", "Bearer private-credential")
	req.Header.Set("traceparent", "00-77777777777777777777777777777777-dddddddddddddddd-01")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != "private-output" {
		t.Fatal("tracing changed the HTTP response")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case exported := <-requests:
		if strings.Contains(exported.String(), "private-") {
			t.Fatal("OTLP export contained private request/response data")
		}
		if len(exported.ResourceSpans) != 1 || len(exported.ResourceSpans[0].ScopeSpans) != 1 || len(exported.ResourceSpans[0].ScopeSpans[0].Spans) != 1 {
			t.Fatalf("unexpected export: %v", exported)
		}
		resource := exported.ResourceSpans[0]
		attributes := map[string]string{}
		for _, attr := range resource.Resource.Attributes {
			attributes[attr.Key] = attr.Value.GetStringValue()
		}
		if attributes["service.name"] != "test-openlore" || attributes["service.version"] != "test-version" || attributes["deployment.environment.name"] != "test" {
			t.Fatalf("resource attributes: %v", attributes)
		}
		span := resource.ScopeSpans[0].Spans[0]
		if span.Status.Code != tracepb.Status_STATUS_CODE_ERROR || span.Name != "openlore.http" {
			t.Fatalf("HTTP failure not exported correctly: %v", span)
		}
		var record map[string]any
		if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record["trace_id"] != "77777777777777777777777777777777" || record["span_id"] != hex.EncodeToString(span.SpanId) || record["component"] != "http" {
			t.Fatalf("log correlation: %v", record)
		}
	case <-ctx.Done():
		t.Fatal("shutdown did not flush spans to the OTLP receiver")
	}
}
