package openlore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPHTTPAPI is a plain JSON HTTP API in front of an MCP server. Unlike the
// Streamable HTTP transport (which speaks the MCP wire protocol), this exposes
// simple REST-style endpoints that any HTTP client can call, while still
// routing every request through the MCP server's tools.
//
// Stateless requests run on a short-lived in-process MCP session. Clients that
// need shell state across commands can create a logical session. Each logical
// session owns one Shell while all sessions share the same HTTP listener.
//
// It mirrors the MCP server's two tools:
//
//	POST {prefix}/shell     -> body {"command": "..."} runs the `shell` tool
//	GET  {prefix}/commands  -> runs the `list_commands` tool
//	POST {prefix}/sessions  -> creates a persistent logical shell session
//	POST {prefix}/sessions/{id}/shell -> runs a command in that session
//	POST {prefix}/sessions/{id}/touch -> extends that session's idle lifetime
//	DELETE {prefix}/sessions/{id} -> closes that session
type MCPHTTPAPI struct {
	server   *mcp.Server
	sessions *httpShellSessions
}

// NewMCPHTTPAPI returns an API backed by the given MCP server. shellFactory
// builds identity-scoped shells for persistent logical sessions.
func NewMCPHTTPAPI(server *mcp.Server, shellFactory func(context.Context) *shell.Shell) *MCPHTTPAPI {
	return &MCPHTTPAPI{server: server, sessions: newHTTPShellSessions(shellFactory)}
}

// Handler returns an http.Handler serving the API under the given path prefix
// (e.g. "/api"). Register it on a mux at prefix+"/".
func (a *MCPHTTPAPI) Handler(prefix string) http.Handler {
	prefix = "/" + strings.Trim(prefix, "/")

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+prefix+"/shell", a.handleShell)
	mux.HandleFunc("GET "+prefix+"/commands", a.handleListCommands)
	mux.HandleFunc("POST "+prefix+"/sessions", a.sessions.handleCreate)
	mux.HandleFunc("POST "+prefix+"/sessions/{id}/shell", a.sessions.handleShell)
	mux.HandleFunc("POST "+prefix+"/sessions/{id}/touch", a.sessions.handleTouch)
	mux.HandleFunc("DELETE "+prefix+"/sessions/{id}", a.sessions.handleDelete)
	return mux
}

type shellRequest struct {
	Command string `json:"command"`
}

type toolResponse struct {
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}

func (a *MCPHTTPAPI) handleShell(w http.ResponseWriter, r *http.Request) {
	var req shellRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		writeAPIError(w, http.StatusBadRequest, "command is required")
		return
	}

	a.callTool(w, r.Context(), "shell", map[string]any{"command": req.Command})
}

func (a *MCPHTTPAPI) handleListCommands(w http.ResponseWriter, r *http.Request) {
	a.callTool(w, r.Context(), "list_commands", map[string]any{})
}

// callTool spins up a per-request in-memory MCP session, invokes an MCP tool,
// and writes the textual result as JSON. The session is connected with ctx so
// the tool handler resolves the caller's identity from the same context.
func (a *MCPHTTPAPI) callTool(w http.ResponseWriter, ctx context.Context, name string, args map[string]any) {
	session, err := a.connect(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, toolResponse{
		Output:  contentText(result),
		IsError: result.IsError,
	})
}

// connect establishes a fresh in-process client<->server MCP session bound to
// ctx. The server must be connected before the client, since the client
// initializes the session during connection.
func (a *MCPHTTPAPI) connect(ctx context.Context) (*mcp.ClientSession, error) {
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	if _, err := a.server.Connect(ctx, serverTransport, nil); err != nil {
		return nil, fmt.Errorf("connecting MCP server transport: %w", err)
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "openlore-http-api",
		Version: "1.0.0",
	}, nil)

	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting MCP client session: %w", err)
	}
	return session, nil
}

// contentText concatenates the text of all TextContent blocks in a tool result.
func contentText(result *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
