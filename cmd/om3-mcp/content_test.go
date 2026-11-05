package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/om3-mcp/internal/clusterconfig"
	"github.com/opensvc/om3-mcp/internal/config"
	"github.com/opensvc/om3-mcp/internal/content"
	"github.com/opensvc/om3-mcp/internal/testutil"
	"github.com/opensvc/osvc-auth-mcp/mcpauth"
	"github.com/opensvc/osvc-auth-mcp/mcpauthtest"
)

const testCollectorAudience = "collector-client"

// contentTest is a fake SSO exchanging any token of its own for a collector
// token, and a fake collector accepting collector tokens only.
type contentTest struct {
	provider  *mcpauthtest.OAuthProvider
	cfg       config.Config
	exchanges atomic.Int32
	mu        sync.Mutex
	subjects  []string // the audiences of the subject tokens exchanged
	asked     []string // the paths and queries the collector answered
}

func newContentTest(t *testing.T) *contentTest {
	t.Helper()
	h := &contentTest{provider: mcpauthtest.NewOAuthProvider(t)}
	p := h.provider
	p.Metadata["token_endpoint"] = p.Server.URL + "/token"
	p.TokenHandler = func(w http.ResponseWriter, r *http.Request) {
		h.exchanges.Add(1)
		_ = r.ParseForm()
		var claims jwt.RegisteredClaims
		if _, err := jwt.ParseWithClaims(r.Form.Get("subject_token"), &claims, func(*jwt.Token) (any, error) { return &p.Key.PublicKey, nil }, jwt.WithIssuer(p.Issuer)); err != nil {
			t.Errorf("the subject token of the exchange: %v", err)
			w.WriteHeader(400)
			return
		}
		if r.Form.Get("audience") != testCollectorAudience {
			t.Errorf("exchange for %q", r.Form.Get("audience"))
		}
		h.mu.Lock()
		h.subjects = append(h.subjects, strings.Join(claims.Audience, ","))
		h.mu.Unlock()
		raw := p.Token(t, testCollectorAudience, claims.Subject+"-collector", nil)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": raw, "token_type": "Bearer", "issued_token_type": "urn:ietf:params:oauth:token-type:access_token", "expires_in": 120})
	}
	collector := testutil.NewDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var claims jwt.RegisteredClaims
		if _, err := jwt.ParseWithClaims(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &claims, func(*jwt.Token) (any, error) { return &p.Key.PublicKey, nil },
			jwt.WithIssuer(p.Issuer), jwt.WithAudience(testCollectorAudience)); err != nil {
			t.Errorf("a token other than a collector token reached the collector: %v", err)
			w.WriteHeader(401)
			return
		}
		h.mu.Lock()
		h.asked = append(h.asked, r.URL.Path+"?"+r.URL.RawQuery)
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/ai/resources":
			_, _ = w.Write([]byte(`{"data":[{"name":"object-config","title":"Object configuration","description":"The configuration file of an object.","mime_type":"text/markdown","size":9}]}`))
		case "/api/ai/resources/object-config":
			_, _ = w.Write([]byte(`{"name":"object-config","mime_type":"text/markdown","body":"# Object\n"}`))
		case "/api/ai/skills":
			_, _ = w.Write([]byte(`{"data":[{"name":"node-maintenance","frontmatter":{"name":"node-maintenance","description":"Maintain a node."},"size":30,"digest":"sha256:bb"}]}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"title":"Not Found","status":404,"detail":"no such object"}`))
		}
	}))
	daemon := testutil.NewDaemon(t, http.NotFoundHandler())
	catalog, err := clusterconfig.Load(testutil.WriteCatalog(t, map[string]any{"a": map[string]any{
		"name": "cluster-a", "cluster_id": "cluster-a", "endpoint": daemon.Server.URL, "request_timeout": "2s", "tls": map[string]string{"ca_file": daemon.CAFile},
		"delegation": map[string]string{"issuer": p.Issuer, "audience": "client-a"},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := mcpauth.LoadExchangeProfiles(mcpauthtest.WriteExchangeProfiles(t, p, "client_secret_basic"))
	if err != nil {
		t.Fatal(err)
	}
	h.cfg = config.Config{
		Clusters: catalog, Exchange: profiles, DelegatedCAFile: p.CAFile,
		OAuth:     mcpauth.OAuthConfig{ResourceURL: "https://mcp.example.test/mcp", Issuer: p.Issuer, CAFile: p.CAFile},
		Collector: &config.Collector{URL: collector.Server.URL + "/api", CAFile: collector.CAFile, Audience: testCollectorAudience, AuthProfile: "test-sso"},
	}
	return h
}

// check lists and reads the content through session, and checks the
// collector was read with collector tokens only, for the om3 backend, the
// subject tokens exchanged once, of audience subject.
func (h *contentTest) check(t *testing.T, session *mcp.ClientSession, subject string) {
	t.Helper()
	if extensions := session.InitializeResult().Capabilities.Extensions; extensions[content.Extension] == nil {
		t.Errorf("extensions = %v", extensions)
	}
	list, err := session.ListResources(t.Context(), nil)
	if err != nil || len(list.Resources) != 1 || list.Resources[0].URI != "om3://resources/object-config" {
		t.Fatalf("resources %+v, %v", list, err)
	}
	read, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "om3://resources/object-config"})
	if err != nil || read.Contents[0].Text != "# Object\n" {
		t.Fatalf("read %+v, %v", read, err)
	}
	if _, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "om3://resources/missing"}); err == nil {
		t.Error("a missing resource was read")
	}
	skills, err := mcp.CallCustomMethod[*content.ListParams, *content.ListResult](t.Context(), session, "skills/list", &content.ListParams{})
	if err != nil || len(skills.Skills) != 1 || skills.Skills[0].URI != "skill://node-maintenance/SKILL.md" {
		t.Fatalf("skills %+v, %v", skills, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.exchanges.Load() != 1 || len(h.subjects) != 1 || h.subjects[0] != subject {
		t.Errorf("%d exchanges of subjects %v, want one of %s", h.exchanges.Load(), h.subjects, subject)
	}
	if len(h.asked) != 4 {
		t.Errorf("the collector answered %v", h.asked)
	}
	for _, asked := range h.asked {
		if !strings.HasSuffix(asked, "?backend=om3") {
			t.Errorf("the collector was asked %s", asked)
		}
	}
}

// contentClient is an MCP client sending skills/list.
func contentClient(t *testing.T, name string) *mcp.Client {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: name, Version: "1"}, nil)
	if err := mcp.AddSendingCustomMethod[*content.ListParams, *content.ListResult](client, "skills/list"); err != nil {
		t.Fatal(err)
	}
	return client
}

// TestContentOverTheSocket reads the resources and the skills of the
// collector with the delegated token of the user, verified for its cluster,
// exchanged for a collector token: the delegated token never reaches the
// collector.
func TestContentOverTheSocket(t *testing.T) {
	h := newContentTest(t)
	path := shortSocketPath(t)
	h.cfg.DelegatedSocket = path
	handler, err := newDelegatedHandler(h.cfg)
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
	token := h.provider.Token(t, "client-a", "alice", nil)
	client := &http.Client{Transport: mcpBearerTransport{base: transport, token: token, clusterID: "cluster-a"}}
	session, err := contentClient(t, "agent").Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: "http://opensvc-mcp/mcp", HTTPClient: client, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	h.check(t, session, "client-a")
}

// TestContentOverHTTPS reads them with the MCP token of an external client
// exchanged for a collector token.
func TestContentOverHTTPS(t *testing.T) {
	h := newContentTest(t)
	handler, err := newMCPHandler(h.cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := testutil.NewDaemon(t, handler)
	token := h.provider.Token(t, h.cfg.OAuth.ResourceURL, "alice", nil)
	client := &http.Client{Transport: mcpBearerTransport{base: server.Server.Client().Transport, token: token}}
	session, err := contentClient(t, "external").Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: server.Server.URL + "/mcp", HTTPClient: client, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	h.check(t, session, h.cfg.OAuth.ResourceURL)
}

// TestNoCollectorNoContent declares no resources without a collector.
func TestNoCollectorNoContent(t *testing.T) {
	handler, err := newConfiguredToolsHandler(nil, nil, nil, false, nil)
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
	if capabilities := session.InitializeResult().Capabilities; capabilities.Resources != nil || capabilities.Extensions[content.Extension] != nil {
		t.Errorf("capabilities %+v", capabilities)
	}
}
