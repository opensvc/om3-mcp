package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/opensvc/om3-mcp/internal/clusterconfig"
	"github.com/opensvc/om3-mcp/internal/config"
	"github.com/opensvc/om3-mcp/internal/testutil"
	"github.com/opensvc/osvc-auth-mcp/mcpauthtest"
)

// slowSocket serves the Unix socket over one cluster whose daemon answers
// each request after delay, or when the request ends.
type slowSocket struct {
	provider *mcpauthtest.OAuthProvider
	client   func(token string) *http.Client
	// reached counts the daemon requests, ended those that ended before
	// their answer.
	reached, ended atomic.Int32
}

func newSlowSocket(t *testing.T, delay time.Duration) *slowSocket {
	t.Helper()
	s := &slowSocket{provider: mcpauthtest.NewOAuthProvider(t)}
	daemon := testutil.NewDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.reached.Add(1)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			s.ended.Add(1)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"cluster":{"config":{"id":"cluster-a","name":"cluster-a","nodes":["n1"]},"node":{"n1":{"status":{"agent":"v3"}}}}}`)
	}))
	catalog, err := clusterconfig.Load(testutil.WriteCatalog(t, map[string]any{"cluster-a": map[string]any{
		"name": "cluster-a", "cluster_id": "cluster-a", "endpoint": daemon.Server.URL, "request_timeout": "30s", "tls": map[string]string{"ca_file": daemon.CAFile},
		"delegation": map[string]string{"issuer": s.provider.Issuer, "audience": "client-a"},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	path := shortSocketPath(t)
	handler, err := newDelegatedHandler(config.Config{Clusters: catalog, DelegatedSocket: path, DelegatedCAFile: s.provider.CAFile})
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
	transport := unixTransport(path)
	t.Cleanup(transport.CloseIdleConnections)
	s.client = func(token string) *http.Client {
		return &http.Client{Transport: mcpBearerTransport{base: transport, token: token, clusterID: "cluster-a"}}
	}
	return s
}

// call sends one raw tools/call of get_node_status within ctx.
func (s *slowSocket) call(ctx context.Context, token string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://opensvc-mcp/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_node_status","arguments":{"node":"n1"}}}`))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := s.client(token).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return nil
}

func awaitAudit(audit *syncBuffer, needle string, limit time.Duration) string {
	for end := time.Now().Add(limit); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if text := audit.String(); strings.Contains(text, needle) {
			return text
		}
	}
	return audit.String()
}

// TestToolCallsEndWithTheirRequest checks that the daemon request of a call
// ends when its client goes away, and that the audit line still names the
// caller of the cancelled call.
func TestToolCallsEndWithTheirRequest(t *testing.T) {
	audit := captureAudit(t)
	s := newSlowSocket(t, 20*time.Second)
	token := s.provider.Token(t, "client-a", "alice", nil)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	err := s.call(ctx, token)
	cancel()
	if err == nil {
		t.Fatal("the call answered before the client deadline")
	}
	line := awaitAudit(audit, "tool=get_node_status", 5*time.Second)
	if !strings.Contains(line, "tool=get_node_status") || s.ended.Load() != s.reached.Load() || s.reached.Load() == 0 {
		t.Fatalf("the abandoned call kept running: reached=%d ended=%d\n%s", s.reached.Load(), s.ended.Load(), line)
	}
	if !strings.Contains(line, "cluster_id=cluster-a issuer="+s.provider.Issuer+" subject=alice") || strings.Contains(line, "outcome=ok") {
		t.Fatalf("unexpected audit line for a cancelled call:\n%s", line)
	}
}

// TestAuditNamesTheCallerWhenTheTokenExpiresDuringTheCall checks that the
// audit line of a call that outlives its delegated token still names the
// caller the socket admitted.
func TestAuditNamesTheCallerWhenTheTokenExpiresDuringTheCall(t *testing.T) {
	audit := captureAudit(t)
	s := newSlowSocket(t, 4*time.Second)
	token := s.provider.Token(t, "client-a", "expiring-user", func(claims jwt.MapClaims) {
		claims["exp"] = time.Now().Add(2 * time.Second).Unix()
	})
	_ = s.call(t.Context(), token)
	line := awaitAudit(audit, "tool=get_node_status", 10*time.Second)
	if s.reached.Load() == 0 {
		t.Fatal("the call did not reach the daemon")
	}
	if !strings.Contains(line, "subject=expiring-user") || !strings.Contains(line, "exchange=delegated daemon_subject=expiring-user") {
		t.Fatalf("the audit line lost the caller of a call that outlived its token:\n%s", line)
	}
}
