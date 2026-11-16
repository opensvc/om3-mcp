package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/om3-mcp/internal/client"
)

type emptyInput struct{}
type emptyOutput struct{}

// call registers one tool answering err, and calls it as a client would.
func call(t *testing.T, err error) *mcp.CallToolResult {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "om3-mcp", Version: "test"}, nil)
	registrar, regErr := NewRegistrar(server)
	if regErr != nil {
		t.Fatal(regErr)
	}
	tool := &mcp.Tool{Name: "list_cluster_objects", Title: "List cluster objects", Description: "List the objects.", Annotations: readOnlyClosedWorldAnnotations()}
	if regErr := registerTool(registrar, tool, func(context.Context, *mcp.CallToolRequest, emptyInput) (*mcp.CallToolResult, emptyOutput, error) {
		return nil, emptyOutput{}, err
	}, false); regErr != nil {
		t.Fatal(regErr)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, connErr := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0"}, nil).Connect(t.Context(), clientTransport, nil)
	if connErr != nil {
		t.Fatal(connErr)
	}
	t.Cleanup(func() { _ = session.Close() })
	result, callErr := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_cluster_objects", Arguments: map[string]any{}})
	if callErr != nil {
		t.Fatal(callErr)
	}
	return result
}

func resultText(result *mcp.CallToolResult) string {
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

func TestAToolSaysTheDaemonIsIncompatible(t *testing.T) {
	SetServerVersion("v9.9.9")
	t.Cleanup(func() { serverVersion = "unknown" })
	incompatible := &client.IncompatibleError{Method: "GET", Path: "/api/object/path", Reason: "the answer is not in the form this MCP reads"}
	result := call(t, fmt.Errorf("list cluster object paths: %w", incompatible))
	text := resultText(result)
	if !result.IsError {
		t.Fatalf("got a result, want an error: %q", text)
	}
	for _, want := range []string{
		"daemon_incompatible: the tool list_cluster_objects cannot be used on this cluster",
		"om3-mcp v9.9.9",
		"GET /api/object/path",
		"om3-mcp must be upgraded",
		"Do not read this as an empty or a missing result",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("result %q does not say %q", text, want)
		}
	}
}

func TestAnotherFailureIsSaidAsIs(t *testing.T) {
	result := call(t, errors.New("OpenSVC daemon GET /api/object/path returned HTTP 403 Forbidden: missing grant"))
	if text := resultText(result); !result.IsError || strings.Contains(text, "daemon_incompatible") || !strings.Contains(text, "403 Forbidden") {
		t.Fatalf("got %q, want the failure as it is", text)
	}
}
