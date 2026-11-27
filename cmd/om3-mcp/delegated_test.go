package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/om3-mcp/internal/auth"
	"github.com/opensvc/om3-mcp/internal/clusterconfig"
	"github.com/opensvc/om3-mcp/internal/config"
	"github.com/opensvc/om3-mcp/internal/testutil"
	"github.com/opensvc/osvc-auth-mcp/mcpauthtest"
)

type mcpBearerTransport struct {
	base      http.RoundTripper
	token     string
	clusterID string
	node      string
	requestID string
}

func (t mcpBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	if t.requestID != "" {
		clone.Header.Set(requestIDHeader, t.requestID)
	}
	clone.Header.Del(auth.ClusterIDHeader)
	if t.clusterID != "" {
		clone.Header.Set(auth.ClusterIDHeader, t.clusterID)
	}
	clone.Header.Del("X-OpenSVC-Node")
	if t.node != "" {
		clone.Header.Set("X-OpenSVC-Node", t.node)
	}
	return t.base.RoundTrip(clone)
}

// shortSocketPath keeps the path within the Unix socket length limit.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "delegated.sock")
}

func unixTransport(path string) *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
}

func TestDelegatedSocketVerifiesLocallyThenAtTheSelectedClusterDaemon(t *testing.T) {
	// One OpenID provider signs for both clusters, distinguished by audience.
	provider := mcpauthtest.NewOAuthProvider(t)
	testIssuer := provider.Issuer
	calls := map[string]*atomic.Int32{}
	clusters := map[string]any{}
	var strategy atomic.Value
	strategy.Store("jwt-openid")
	for _, id := range []string{"cluster-a", "cluster-b"} {
		calls[id] = &atomic.Int32{}
		count := calls[id]
		audience := "client-" + strings.TrimPrefix(id, "cluster-")
		d := testutil.NewDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			var claims jwt.RegisteredClaims
			// The daemon verifies again the provider signature and its audience.
			if _, err := jwt.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) { return &provider.Key.PublicKey, nil }, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(testIssuer), jwt.WithAudience(audience)); err != nil {
				w.WriteHeader(401)
				return
			}
			if r.Header.Get(auth.ClusterIDHeader) != "" || r.Header.Get("X-OpenSVC-Node") != "" {
				t.Error("routing headers reached the daemon")
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/auth/whoami" {
				_ = json.NewEncoder(w).Encode(map[string]string{"name": claims.Subject, "auth": strategy.Load().(string)})
				return
			}
			fmt.Fprintf(w, `{"cluster":{"config":{"id":%q,"name":%q,"nodes":["shared-node"]},"node":{"shared-node":{"status":{"agent":%q}}}}}`, id, id, claims.Subject+"-"+id)
		}))
		// No auth block: the cluster is reachable through the socket only.
		clusters[id] = map[string]any{"name": id, "cluster_id": id, "endpoint": d.Server.URL, "request_timeout": "2s", "tls": map[string]string{"ca_file": d.CAFile},
			"delegation": map[string]string{"issuer": testIssuer, "audience": audience}}
	}
	catalog, err := clusterconfig.Load(testutil.WriteCatalog(t, clusters))
	if err != nil {
		t.Fatal(err)
	}
	var audit syncBuffer
	defer func(logger *slog.Logger) { auditLogger = logger }(auditLogger)
	auditLogger = slog.New(slog.NewTextHandler(&audit, nil))
	path := shortSocketPath(t)
	// A socket-only configuration: the provider is trusted with the delegated
	// CA bundle, the OAuth one of the HTTPS listener being unavailable.
	handler, err := newDelegatedHandler(config.Config{Clusters: catalog, DelegatedSocket: path, DelegatedCAFile: provider.CAFile})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := listenDelegated(path)
	if err != nil {
		t.Fatal(err)
	}
	server := newHTTPServer(handler)
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o660 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}

	// Without preferred_username nor email, the daemon names the user by sub.
	alice := provider.Token(t, "client-a", "alice", nil)
	transport := unixTransport(path)
	t.Cleanup(transport.CloseIdleConnections)
	whoami := func(token, clusterID string) (int, string) {
		t.Helper()
		client := &http.Client{Transport: mcpBearerTransport{base: transport, token: token, clusterID: clusterID, node: "ignored-node"}}
		response, err := client.Get("http://opensvc-mcp/mcp/auth/whoami")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if strings.Contains(string(body), token) {
			t.Error("whoami leaked the token")
		}
		return response.StatusCode, string(body)
	}
	status, body := whoami(alice, "cluster-a")
	if status != 200 || !strings.Contains(body, `"cluster_id":"cluster-a"`) || !strings.Contains(body, `"subject":"alice"`) || !strings.Contains(body, `"issuer":"`+testIssuer+`"`) {
		t.Fatalf("whoami: %d %s", status, body)
	}
	// The header selects the cluster whose provider the token is verified
	// with: a token intended for another cluster is refused by the MCP, and
	// never reaches that cluster's daemon.
	if status, _ := whoami(alice, "cluster-b"); status != 401 || calls["cluster-b"].Load() != 0 {
		t.Fatalf("token of cluster-a accepted for cluster-b: %d", status)
	}
	forged := mcpauthtest.NewOAuthProvider(t).Token(t, "client-a", "alice", func(c jwt.MapClaims) { c["iss"] = testIssuer })
	before := calls["cluster-a"].Load()
	if status, _ := whoami(forged, "cluster-a"); status != 401 || calls["cluster-a"].Load() != before {
		t.Fatalf("forged token reached the daemon: %d", status)
	}
	before = calls["cluster-a"].Load() + calls["cluster-b"].Load()
	for _, clusterID := range []string{"", "unknown"} {
		if status, _ := whoami(alice, clusterID); status != 401 {
			t.Fatalf("cluster %q accepted: %d", clusterID, status)
		}
	}
	if calls["cluster-a"].Load()+calls["cluster-b"].Load() != before {
		t.Fatal("missing or unknown cluster reached a daemon")
	}
	strategy.Store("public")
	if status, _ := whoami(alice, "cluster-a"); status != 401 {
		t.Fatal("unexpected daemon authentication strategy accepted")
	}
	strategy.Store("jwt-openid")
	whoamiLogs := audit.String()
	for _, want := range []string{
		`msg="mcp whoami" cluster_id=cluster-a issuer=` + testIssuer + ` subject=alice outcome=ok status=200`,
		`outcome=identity_mismatch status=401`,
		`auth=public`,
	} {
		if !strings.Contains(whoamiLogs, want) {
			t.Fatalf("whoami audit misses %q:\n%s", want, whoamiLogs)
		}
	}
	// A missing or unknown cluster, or a token the MCP refuses, is refused
	// before the bridge, without a line.
	if strings.Count(whoamiLogs, `msg="mcp whoami"`) != 2 || strings.Contains(whoamiLogs, alice) {
		t.Fatalf("unexpected whoami audit lines:\n%s", whoamiLogs)
	}

	// Tools are bound to the header cluster and take no cluster_id argument.
	session, err := mcp.NewClient(&mcp.Implementation{Name: "ai-agent", Version: "test"}, nil).Connect(t.Context(),
		&mcp.StreamableClientTransport{Endpoint: "http://opensvc-mcp/mcp", HTTPClient: &http.Client{Transport: mcpBearerTransport{base: transport, token: alice, clusterID: "cluster-a", requestID: testRequestID}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	list, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
	if err != nil || len(list.Tools) == 0 {
		t.Fatalf("list tools: %v", err)
	}
	var core []string
	for _, tool := range list.Tools {
		data, _ := json.Marshal(tool.InputSchema)
		if tool.Name == "list_clusters" || strings.Contains(string(data), "cluster_id") {
			t.Fatalf("delegated tool %s exposes cluster selection", tool.Name)
		}
		// The tool search of the AI agent always shows the core tools, and
		// ranks the others with their keywords; the hidden ones, for the
		// client only, it never shows.
		if tool.Meta["opensvc/hidden"] == true {
			continue
		}
		if keywords, ok := tool.Meta["opensvc/keywords"].([]any); !ok || len(keywords) == 0 {
			t.Fatalf("delegated tool %s has no search keywords: %v", tool.Name, tool.Meta)
		}
		if tool.Meta["opensvc/core"] == true {
			core = append(core, tool.Name)
		}
	}
	slices.Sort(core)
	if strings.Join(core, ",") != "get_caller_identity,get_cluster_status,get_instance_status,get_node_status,get_object_status,list_cluster_objects,validate_object_configs" {
		t.Fatalf("core tools = %v", core)
	}
	beforeB := calls["cluster-b"].Load()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_node_status", Arguments: map[string]any{"node": "shared-node"}})
	if err != nil || result.IsError {
		t.Fatalf("tool call failed: %v %+v", err, result)
	}
	data, _ := json.Marshal(result)
	if !strings.Contains(string(data), "alice-cluster-a") || strings.Contains(string(data), alice) || calls["cluster-b"].Load() != beforeB {
		t.Fatalf("tool call escaped its cluster binding: %s", data)
	}
	logs := audit.String()
	if !strings.Contains(logs, "tool=get_node_status cluster_id=cluster-a issuer="+testIssuer+" subject=alice") || !strings.Contains(logs, "exchange=delegated daemon_subject=alice outcome=ok") ||
		!strings.Contains(logs, "request_id="+testRequestID) || !strings.Contains(whoamiLogs, `request_id=""`) || strings.Contains(logs, alice) {
		t.Fatalf("unexpected delegated audit line:\n%s", logs)
	}
}

func TestDelegatedSocketFile(t *testing.T) {
	path := shortSocketPath(t)
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenDelegated(path); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("regular file replaced: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	listener, err := listenDelegated(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listenDelegated(path); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("live socket replaced: %v", err)
	}
	// Simulate a crash: the socket file remains without a listener.
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	listener.Close()
	listener, err = listenDelegated(path)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	listener.Close()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("socket file left after close")
	}
}
