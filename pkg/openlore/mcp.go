package openlore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/assets"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// MCPOption configures the MCP server constructed by NewMCPServer.
type MCPOption func(*mcpConfig)

type mcpConfig struct {
	serverName       string
	instructions     string
	shellDescription string
	envVars          map[string]string
	readOnly         bool
	// shellFactory, when set, builds the shell for each `shell` tool call from
	// the request context (used to scope the shell per authenticated identity).
	// When nil, a shell is built from the fixed filesystem plus envVars.
	shellFactory func(ctx context.Context) *shell.Shell
}

// WithMCPServerName overrides the MCP server name reported in the initialize
// response. Clients see this in their connector list.
func WithMCPServerName(name string) MCPOption {
	return func(c *mcpConfig) { c.serverName = name }
}

// WithMCPInstructions sets server instructions that are automatically injected
// into the client's context when it connects.
func WithMCPInstructions(instructions string) MCPOption {
	return func(c *mcpConfig) { c.instructions = instructions }
}

// WithMCPShellDescription overrides the shell tool's description.
func WithMCPShellDescription(desc string) MCPOption {
	return func(c *mcpConfig) { c.shellDescription = desc }
}

// WithMCPEnvVars sets environment variables on the shell for every command
// execution.
func WithMCPEnvVars(vars map[string]string) MCPOption {
	return func(c *mcpConfig) { c.envVars = vars }
}

// WithMCPReadOnly describes whether the shell's filesystem is globally
// read-only. The hint is reported to MCP clients and does not replace runtime
// filesystem or identity-level authorization.
func WithMCPReadOnly(readOnly bool) MCPOption {
	return func(c *mcpConfig) { c.readOnly = readOnly }
}

// withMCPShellFactory sets a per-request shell factory. Used internally by the
// server to scope the `shell` tool to the authenticated identity resolved from
// the request context. Unexported: external callers scope by passing their own
// filesystem to NewMCPServer instead.
func withMCPShellFactory(fn func(ctx context.Context) *shell.Shell) MCPOption {
	return func(c *mcpConfig) { c.shellFactory = fn }
}

// NewMCPServer creates an MCP server backed by the given filesystem. The
// returned server exposes two tools — `shell` and `list_commands` — that let
// agents browse and operate on the filesystem via a restricted shell.
func NewMCPServer(fs vfs.FileSystem, opts ...MCPOption) *mcp.Server {
	cfg := mcpConfig{readOnly: true}
	for _, opt := range opts {
		opt(&cfg)
	}

	var serverOpts *mcp.ServerOptions
	if cfg.instructions != "" {
		serverOpts = &mcp.ServerOptions{Instructions: cfg.instructions}
	}

	serverName := "openlore"
	if cfg.serverName != "" {
		serverName = cfg.serverName
	}
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    serverName,
			Version: assets.Version(),
			Icons: []mcp.Icon{{
				Source:   "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(assets.OiyaIcon()),
				MIMEType: "image/svg+xml",
				Sizes:    []string{"any"},
			}},
		},
		serverOpts,
	)
	server.AddReceivingMiddleware(mcpTracingMiddleware)

	shellDesc := "Execute a command in OpenLore's restricted knowledge-repository shell. This is not Bash and cannot run arbitrary executables. Supports ls, cat, grep, find, tree, head, tail, wc, stat, sort, uniq, cut, sed, awk, jq, xargs, pipes, loops, and more; use list_commands for the available surface."
	if cfg.shellDescription != "" {
		shellDesc = cfg.shellDescription
	}
	destructive := !cfg.readOnly
	openWorld := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "shell",
		Description: shellDesc,
		Annotations: &mcp.ToolAnnotations{
			Title:           "OpenLore Shell",
			ReadOnlyHint:    cfg.readOnly,
			DestructiveHint: &destructive,
			OpenWorldHint:   &openWorld,
		},
	}, newMCPShellHandler(fs, cfg.envVars, cfg.shellFactory))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_commands",
		Description: "List all available shell commands.",
		Annotations: &mcp.ToolAnnotations{
			Title:          "List OpenLore Commands",
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  &openWorld,
		},
	}, newMCPListCommandsHandler())

	return server
}

func mcpTracingMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" && method != "tools/list" {
			return next(ctx, method, req)
		}
		// HTTP sessions inherit their initialization context in the SDK, but
		// Extra contains this request's headers. Never reuse an old parent.
		hasHTTPHeaders := false
		if extra := req.GetExtra(); extra != nil && extra.Header != nil {
			hasHTTPHeaders = true
			ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
			ctx = propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(extra.Header))
		}
		if !hasHTTPHeaders || !trace.SpanContextFromContext(ctx).IsValid() {
			// tools/list permits omitted params, represented by a typed nil.
			var meta map[string]any
			switch params := req.GetParams().(type) {
			case *mcp.CallToolParamsRaw:
				if params != nil {
					meta = params.GetMeta()
				}
			case *mcp.ListToolsParams:
				if params != nil {
					meta = params.GetMeta()
				}
			}
			if parent, ok := meta["traceparent"].(string); ok {
				state, _ := meta["tracestate"].(string)
				ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": parent, "tracestate": state})
			}
		}
		ctx, span := otel.Tracer("github.com/aakarim/go-openlore/mcp").Start(ctx, method, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && params != nil {
			name := params.Name
			if name != "shell" && name != "list_commands" {
				name = "unknown"
			}
			span.SetAttributes(attribute.String("gen_ai.operation.name", "execute_tool"), attribute.String("gen_ai.tool.name", name))
		}
		result, err := next(ctx, method, req)
		if err != nil {
			// Protocol errors can echo arguments. Record classification only.
			span.SetStatus(codes.Error, "MCP request failed")
		} else if toolResult, ok := result.(*mcp.CallToolResult); ok {
			if code, ok := exitCodeFromStructured(toolResult.StructuredContent); ok {
				span.SetAttributes(attribute.Int("openlore.exit_code", code))
				if code != 0 {
					span.SetStatus(codes.Error, "shell command failed")
				}
			}
			if toolResult.IsError {
				span.SetStatus(codes.Error, "tool execution failed")
			}
		}
		return result, err
	}
}

type mcpShellInput struct {
	Command string `json:"command" jsonschema:"The OpenLore shell command to execute (e.g. grep -r auth /docs). This is a restricted interpreter, not Bash."`
}

func newMCPShellHandler(fs vfs.FileSystem, envVars map[string]string, factory func(ctx context.Context) *shell.Shell) mcp.ToolHandlerFor[mcpShellInput, any] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input mcpShellInput) (*mcp.CallToolResult, any, error) {
		if input.Command == "" {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "command is required"}},
				IsError: true,
			}, nil, nil
		}

		var sh *shell.Shell
		if factory != nil {
			// Per-identity scoped shell (built from the request context).
			sh = factory(ctx)
		} else {
			// Fixed-filesystem shell (caller scopes by choosing fs).
			sh = shell.NewShell(fs)
			for k, v := range envVars {
				sh.SetEnv(k, v)
			}
		}

		output, stdout, stderr, exitCode := execShellTranscript(ctx, sh, input.Command)

		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: output}},
			StructuredContent: map[string]any{"output": output, "stdout": stdout, "stderr": stderr, "exit_code": exitCode},
		}, nil, nil
	}
}

// execShellTranscript executes command in sh and returns its output streams
// separately, along with a merged output value for backwards compatibility.
// The exit code remains result data and is never added to either stream.
func execShellTranscript(ctx context.Context, sh *shell.Shell, command string) (output, stdout, stderr string, exitCode int) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	exitCode = sh.ExecPipelineContext(ctx, command, &stdoutBuffer, &stderrBuffer, nil)

	sections := make([]string, 0, 2)
	if stdoutBuffer.Len() > 0 {
		sections = append(sections, stdoutBuffer.String())
	}
	if stderrBuffer.Len() > 0 {
		sections = append(sections, stderrBuffer.String())
	}
	return strings.Join(sections, "\n"), stdoutBuffer.String(), stderrBuffer.String(), exitCode
}

// exitCodeFromStructured extracts the exit_code field the shell tool places in
// StructuredContent. The value has been through JSON on the way back to the
// client, so its concrete Go type depends on the decoder: float64 today,
// json.Number or an integer type if the SDK changes how it decodes.
func exitCodeFromStructured(structured any) (int, bool) {
	m, ok := structured.(map[string]any)
	if !ok {
		return 0, false
	}
	switch v := m["exit_code"].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	default:
		return 0, false
	}
}

type mcpListCommandsInput struct{}

func newMCPListCommandsHandler() mcp.ToolHandlerFor[mcpListCommandsInput, any] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input mcpListCommandsInput) (*mcp.CallToolResult, any, error) {
		names := make([]string, 0, len(cmds.Registry))
		for name := range cmds.Registry {
			names = append(names, name)
		}
		sort.Strings(names)

		var sb strings.Builder
		sb.WriteString("Available commands:\n")
		for _, name := range names {
			sb.WriteString("  ")
			sb.WriteString(name)
			sb.WriteString("\n")
		}
		sb.WriteString("\nOpenLore restricted-shell syntax: pipes (|), && / ||, for/while/if, variables, subshells. This is not Bash and cannot run arbitrary executables.")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}},
		}, nil, nil
	}
}
