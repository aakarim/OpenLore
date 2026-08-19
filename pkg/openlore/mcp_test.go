package openlore

import (
	"testing"
	"testing/fstest"

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
