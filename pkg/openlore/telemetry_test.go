package openlore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/aakarim/go-openlore/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func recordTraces(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})
	return recorder
}

func TestMCPTracingUsesEachRequestParent(t *testing.T) {
	recorder := recordTraces(t)
	fs := NewFSAdapter(fstest.MapFS{"doc.md": {Data: []byte("private-document-body")}})
	mcpServer := NewMCPServer(fs)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	server := httptest.NewServer(telemetry.HTTPHandler(handler))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var sessionID string
	post := func(body, parent string) string {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
			req.Header.Set("Mcp-Protocol-Version", "2025-03-26")
		}
		if parent != "" {
			req.Header.Set("traceparent", parent)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode >= 400 {
			t.Fatalf("MCP response: status=%d body=%s err=%v", resp.StatusCode, data, err)
		}
		if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
			sessionID = id
		}
		return string(data)
	}
	post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"tracing-test","version":"1"}}}`, "00-11111111111111111111111111111111-1111111111111111-01")
	if sessionID == "" {
		t.Fatal("expected a persistent MCP session")
	}
	post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, "")
	for i, traceID := range []string{"22222222222222222222222222222222", "33333333333333333333333333333333", ""} {
		before := len(recorder.Ended())
		parent := ""
		if traceID != "" {
			parent = "00-" + traceID + "-aaaaaaaaaaaaaaaa-01"
		}
		body := post(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"shell","arguments":{"command":"cat /doc.md; false"}}}`, i+2), parent)
		if !strings.Contains(body, "private-document-body") || !strings.Contains(body, `"exit_code":1`) {
			t.Fatalf("shell result changed: %s", body)
		}
		spans := recorder.Ended()[before:]
		var httpSpan, mcpSpan, shellSpan sdktrace.ReadOnlySpan
		for _, span := range spans {
			switch span.Name() {
			case "openlore.http":
				httpSpan = span
			case "tools/call":
				mcpSpan = span
			case "openlore.shell":
				shellSpan = span
			}
			data, _ := json.Marshal(span.Attributes())
			if strings.Contains(string(data), "private-document-body") || strings.Contains(string(data), "/doc.md") {
				t.Fatalf("trace leaked content: %s", data)
			}
		}
		if httpSpan == nil || mcpSpan == nil || shellSpan == nil {
			t.Fatalf("missing execution spans: %v", spans)
		}
		if traceID == "" {
			if httpSpan.Parent().IsValid() || httpSpan.SpanContext().TraceID().String() == "11111111111111111111111111111111" {
				t.Fatal("unparented call inherited the initialization trace")
			}
		} else if httpSpan.SpanContext().TraceID().String() != traceID || httpSpan.Parent().SpanID().String() != "aaaaaaaaaaaaaaaa" {
			t.Fatalf("HTTP span has wrong parent: %v", httpSpan.Parent())
		}
		if mcpSpan.Parent().SpanID() != httpSpan.SpanContext().SpanID() || shellSpan.Parent().SpanID() != mcpSpan.SpanContext().SpanID() {
			t.Fatal("expected HTTP → MCP → shell parentage")
		}
		if mcpSpan.Status().Code != codes.Error || shellSpan.Status().Code != codes.Error {
			t.Fatal("nonzero command exit was not traced as a failure")
		}
	}
	// Close the persistent session, including the SDK's associated goroutines.
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, server.URL, nil)
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestMCPTracingAcceptsMetadataWithoutChangingArguments(t *testing.T) {
	recorder := recordTraces(t)
	server := NewMCPServer(NewFSAdapter(fstest.MapFS{}))
	a, b := mcp.NewInMemoryTransports()
	session, err := server.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "trace-test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "shell", Arguments: map[string]any{"command": "echo private-argument"},
		Meta: mcp.Meta{"traceparent": "00-44444444444444444444444444444444-bbbbbbbbbbbbbbbb-01"},
	})
	if err != nil || contentText(result) != "private-argument\n" {
		t.Fatalf("result=%v err=%v", result, err)
	}
	for _, span := range recorder.Ended() {
		if span.SpanContext().TraceID().String() != "44444444444444444444444444444444" {
			t.Fatalf("wrong trace for %s", span.Name())
		}
		if span.Name() == "tools/call" && span.Parent().SpanID().String() != "bbbbbbbbbbbbbbbb" {
			t.Fatal("metadata parent was not used")
		}
	}
	if len(recorder.Ended()) != 3 {
		t.Fatalf("expected MCP, shell and command spans, got %d", len(recorder.Ended()))
	}
}

func TestHTTPAPITracingUsesCurrentRequestForPersistentShell(t *testing.T) {
	recorder := recordTraces(t)
	_, api := newSessionTestAPI(t)
	handler := telemetry.HTTPHandler(api)
	created := createSession(t, handler, Identity{})
	for _, path := range []string{"/api/shell", "/api/sessions/" + created.ID + "/shell"} {
		for _, traceID := range []string{"55555555555555555555555555555555", "66666666666666666666666666666666"} {
			before := len(recorder.Ended())
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"command":"echo private-query | wc -c"}`))
			req = req.WithContext(contextWithIdentity(req.Context(), Identity{}))
			req.Header.Set("traceparent", "00-"+traceID+"-cccccccccccccccc-01")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			var result toolResponse
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || strings.TrimSpace(result.Stdout) != "14" || result.ExitCode != 0 {
				t.Fatalf("unexpected API result: %s (%v)", w.Body.String(), err)
			}
			spans := recorder.Ended()[before:]
			if len(spans) < 4 {
				t.Fatalf("missing spans: %d", len(spans))
			}
			for _, span := range spans {
				if span.SpanContext().TraceID().String() != traceID {
					t.Fatalf("%s used a stale session trace", span.Name())
				}
			}
		}
	}
}
