package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/vfs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type analyticsTestFS struct{}

func (analyticsTestFS) Stat(string) (*vfs.FileInfo, error)     { return nil, fs.ErrNotExist }
func (analyticsTestFS) ReadDir(string) ([]vfs.FileInfo, error) { return nil, fs.ErrNotExist }
func (analyticsTestFS) ReadFile(string) ([]byte, error)        { return nil, fs.ErrNotExist }

func TestCommandObserverSeesPipelineCommandsAndOutput(t *testing.T) {
	sh := NewShell(analyticsTestFS{})
	var got []CommandExecution
	sh.SetCommandObserver(func(e CommandExecution) { got = append(got, e) })
	var out bytes.Buffer
	if code := sh.ExecPipeline("echo hello | wc -c", &out, &out, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(got) != 2 || got[0].Command != "echo" || got[0].PipelinePosition != 0 || got[0].BytesOut != 6 || got[1].Command != "wc" || got[1].PipelinePosition != 1 || got[0].InvocationID == "" || got[0].InvocationID != got[1].InvocationID {
		t.Fatalf("unexpected observations: %#v", got)
	}
}

func TestCommandObserverSeesXargsGeneratedCommands(t *testing.T) {
	sh := NewShell(analyticsTestFS{})
	var got []CommandExecution
	sh.SetCommandObserver(func(e CommandExecution) { got = append(got, e) })
	var out bytes.Buffer
	if code := sh.ExecPipeline("printf 'one two' | xargs -n 1 echo", &out, &out, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(got) != 4 {
		t.Fatalf("observations = %#v, want printf, two echo calls, and xargs", got)
	}
	var echoes int
	for _, observation := range got {
		if observation.Command == "echo" {
			echoes++
			if observation.InvocationID == "" || observation.BytesOut == 0 {
				t.Fatalf("generated command was not fully observed: %#v", observation)
			}
		}
	}
	if echoes != 2 {
		t.Fatalf("observations = %#v, want two generated echo commands", got)
	}
}

func TestShellTracingPreservesNestedExecutionAndRedactsContent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer provider.Shutdown(context.Background())
	ctx, parent := provider.Tracer("test").Start(t.Context(), "agent")
	defer parent.End()
	sh := NewShell(analyticsTestFS{})
	var out bytes.Buffer
	if code := sh.ExecPipelineContext(ctx, `eval 'echo private-text'; printf 'one two' | xargs -n 1 echo; private-secret-command`, &out, io.Discard, nil); code != 127 || out.String() != "private-text\none\ntwo\n" {
		t.Fatalf("exit=%d output=%q", code, out.String())
	}
	var echoes, unknown int
	for _, span := range recorder.Ended() {
		if span.SpanContext().TraceID() != parent.SpanContext().TraceID() {
			t.Fatalf("nested execution escaped the caller's trace: %s", span.Name())
		}
		data, _ := json.Marshal(span.Attributes())
		if strings.Contains(string(data), "private-") || strings.Contains(span.Status().Description, "private-") {
			t.Fatalf("span leaked content: %s", data)
		}
		for _, attr := range span.Attributes() {
			if attr.Key == "openlore.command.name" {
				switch attr.Value.AsString() {
				case "echo":
					echoes++
				case "unknown":
					unknown++
					if span.Status().Code != codes.Error {
						t.Fatal("unknown command was not marked failed")
					}
				}
			}
		}
	}
	if echoes != 3 || unknown != 1 {
		t.Fatalf("missing nested commands: echoes=%d unknown=%d", echoes, unknown)
	}
	// A later unparented invocation on the same shell must not reuse this trace.
	sh.ExecPipeline("true", io.Discard, io.Discard, nil)
	spans := recorder.Ended()
	if spans[len(spans)-1].SpanContext().TraceID() == parent.SpanContext().TraceID() {
		t.Fatal("shell retained the previous request's trace")
	}
	// Reading the command name for tracing must not execute substitutions.
	out.Reset()
	if code := sh.ExecPipelineContext(ctx, `$(export N=${N}x; echo echo) "$N"`, &out, io.Discard, nil); code != 0 || out.String() != "x\n" {
		t.Fatalf("tracing re-evaluated command substitution: exit=%d output=%q", code, out.String())
	}
}
