package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A tool the daemon of the cluster can no longer serve is audited as such, an
// outcome of its own, which the audit is searched by; another refusal stays a
// tool error.
func TestAuditNamesAnIncompatibleDaemon(t *testing.T) {
	defer func(logger *slog.Logger) { auditLogger = logger }(auditLogger)
	for text, outcome := range map[string]string{
		"daemon_incompatible: the tool list_cluster_objects cannot be used on this cluster": "daemon_incompatible",
		"OpenSVC daemon GET /api/object/path returned HTTP 403 Forbidden":                   "tool_error",
	} {
		var audit syncBuffer
		auditLogger = slog.New(slog.NewTextHandler(&audit, nil))
		handler := auditToolCalls(func(context.Context, string, mcp.Request) (mcp.Result, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
		})
		request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "list_cluster_objects", Arguments: json.RawMessage(`{}`)}}
		if _, err := handler(context.Background(), "tools/call", request); err != nil {
			t.Fatal(err)
		}
		if line := audit.String(); !strings.Contains(line, " outcome="+outcome+" ") {
			t.Errorf("got %q, want outcome %s", line, outcome)
		}
	}
}
