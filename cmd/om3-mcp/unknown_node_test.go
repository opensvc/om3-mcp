package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/om3-mcp/internal/client"
)

// unknownNodeDaemon answers every read as the daemon answers a node it has
// no status of.
type unknownNodeDaemon struct{}

func (unknownNodeDaemon) GetJSON(_ context.Context, path string, _ url.Values, _ any) error {
	return &client.APIError{Method: http.MethodGet, Path: path, StatusCode: http.StatusNotFound, Status: "404 Not Found", Title: "node status data not found", Detail: "oc3-mcp"}
}

// TestUnknownNodeRefusalNamesTheNextStep holds a node the daemon does not
// know to a refusal naming where the nodes are, as on 2026-10-30, when a
// model gave list_daemon_orchestrations a made-up node and asked the user
// for one after the bare 404.
func TestUnknownNodeRefusalNamesTheNextStep(t *testing.T) {
	handler, err := newConfiguredToolsHandler(unknownNodeDaemon{}, nil, nil, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_daemon_orchestrations", Arguments: map[string]any{"node": "oc3-mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("unknown node not refused: %+v", result)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"404 Not Found: oc3-mcp", "get_cluster_status lists the nodes of the cluster"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal %q lacks %q", text, want)
		}
	}
}
