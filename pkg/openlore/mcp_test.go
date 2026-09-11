package openlore

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPToolAnnotations(t *testing.T) {
	tests := []struct {
		name              string
		opts              []MCPOption
		wantShellReadOnly bool
		wantDestructive   bool
	}{
		{name: "read-only by default", wantShellReadOnly: true},
		{
			name:            "writable filesystem",
			opts:            []MCPOption{WithMCPReadOnly(false)},
			wantDestructive: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := NewFSAdapter(fstest.MapFS{"test.md": {Data: []byte("test")}})
			server := NewMCPServer(fs, tt.opts...)
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			serverSession, err := server.Connect(t.Context(), serverTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer serverSession.Close()

			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
			clientSession, err := client.Connect(t.Context(), clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer clientSession.Close()

			result, err := clientSession.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}

			tools := make(map[string]*mcp.Tool, len(result.Tools))
			for _, tool := range result.Tools {
				tools[tool.Name] = tool
			}

			shellTool, ok := tools["shell"]
			if !ok {
				t.Fatal("shell tool is not registered")
			}
			shellAnnotations := shellTool.Annotations
			if shellAnnotations == nil {
				t.Fatal("shell annotations are nil")
			}
			if shellAnnotations.Title != "OpenLore Shell" {
				t.Errorf("shell title = %q", shellAnnotations.Title)
			}
			if shellAnnotations.ReadOnlyHint != tt.wantShellReadOnly {
				t.Errorf("shell readOnlyHint = %t, want %t", shellAnnotations.ReadOnlyHint, tt.wantShellReadOnly)
			}
			if shellAnnotations.DestructiveHint == nil || *shellAnnotations.DestructiveHint != tt.wantDestructive {
				t.Errorf("shell destructiveHint = %v, want %t", shellAnnotations.DestructiveHint, tt.wantDestructive)
			}
			if shellAnnotations.OpenWorldHint == nil || *shellAnnotations.OpenWorldHint {
				t.Errorf("shell openWorldHint = %v, want false", shellAnnotations.OpenWorldHint)
			}

			listTool, ok := tools["list_commands"]
			if !ok {
				t.Fatal("list_commands tool is not registered")
			}
			listAnnotations := listTool.Annotations
			if listAnnotations == nil {
				t.Fatal("list_commands annotations are nil")
			}
			if listAnnotations.Title != "List OpenLore Commands" {
				t.Errorf("list_commands title = %q", listAnnotations.Title)
			}
			if !listAnnotations.ReadOnlyHint {
				t.Error("list_commands readOnlyHint = false, want true")
			}
			if !listAnnotations.IdempotentHint {
				t.Error("list_commands idempotentHint = false, want true")
			}
			if listAnnotations.OpenWorldHint == nil || *listAnnotations.OpenWorldHint {
				t.Errorf("list_commands openWorldHint = %v, want false", listAnnotations.OpenWorldHint)
			}
		})
	}
}

func TestMCPShellFailureIsError(t *testing.T) {
	fs := NewFSAdapter(fstest.MapFS{})
	server := NewMCPServer(fs)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "shell",
		Arguments: map[string]any{"command": "echo structured-output; cat /does/not/exist"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("IsError = false, want true")
	}
	output := result.Content[0].(*mcp.TextContent).Text
	if strings.HasPrefix(output, "\n") {
		t.Fatalf("output starts with a blank line: %q", output)
	}
	if !strings.Contains(output, "structured-output") {
		t.Fatalf("output %q does not contain stdout", output)
	}
	if !strings.HasSuffix(output, "exit code: 1") {
		t.Fatalf("output %q does not end with %q", output, "exit code: 1")
	}
	exitCode, ok := exitCodeFromStructured(result.StructuredContent)
	if !ok {
		t.Fatalf("StructuredContent = %#v, want a map with an integer exit_code", result.StructuredContent)
	}
	if exitCode != 1 {
		t.Fatalf("exit_code = %d, want 1", exitCode)
	}
	structured := result.StructuredContent.(map[string]any)
	if structured["output"] != output {
		t.Fatalf("structured output = %#v, want %q", structured["output"], output)
	}
}

func TestExecShellTranscriptNeverStartsWithBlankLine(t *testing.T) {
	sh := shell.NewShell(NewFSAdapter(fstest.MapFS{
		"docs/a.md": &fstest.MapFile{Data: []byte("hello\n")},
	}))

	cases := []struct {
		name       string
		command    string
		wantPrefix string
		wantSuffix string
		wantExit   int
	}{
		{name: "stdout only", command: "cat /docs/a.md", wantPrefix: "hello\n", wantSuffix: "hello\n"},
		{name: "stderr only", command: "cat /does/not/exist", wantPrefix: "cat: ", wantSuffix: "\nexit code: 1", wantExit: 1},
		{name: "exit code only", command: "false", wantPrefix: "exit code: 1", wantSuffix: "exit code: 1", wantExit: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, exitCode := execShellTranscript(sh, tc.command)
			if exitCode != tc.wantExit {
				t.Fatalf("exit code = %d, want %d", exitCode, tc.wantExit)
			}
			if strings.HasPrefix(got, "\n") {
				t.Fatalf("transcript starts with a blank line: %q", got)
			}
			if !strings.HasPrefix(got, tc.wantPrefix) || !strings.HasSuffix(got, tc.wantSuffix) {
				t.Fatalf("transcript = %q, want prefix %q and suffix %q", got, tc.wantPrefix, tc.wantSuffix)
			}
		})
	}
}

func TestExitCodeFromStructured(t *testing.T) {
	cases := []struct {
		name   string
		in     any
		want   int
		wantOK bool
	}{
		{name: "float64", in: map[string]any{"exit_code": float64(2)}, want: 2, wantOK: true},
		{name: "int", in: map[string]any{"exit_code": 3}, want: 3, wantOK: true},
		{name: "int64", in: map[string]any{"exit_code": int64(4)}, want: 4, wantOK: true},
		{name: "json.Number", in: map[string]any{"exit_code": json.Number("5")}, want: 5, wantOK: true},
		{name: "missing", in: map[string]any{}, want: 0, wantOK: false},
		{name: "not a map", in: "nope", want: 0, wantOK: false},
		{name: "wrong type", in: map[string]any{"exit_code": "1"}, want: 0, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := exitCodeFromStructured(tc.in)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("exitCodeFromStructured(%#v) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
