package config

import (
	"strings"
	"testing"

	"github.com/opensvc/om3-mcp/internal/testutil"
	"github.com/opensvc/osvc-auth-mcp/mcpauthtest"
)

func clearListenerEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"OPENSVC_MCP_LISTEN_ADDR", "OPENSVC_MCP_TLS_CERT_FILE", "OPENSVC_MCP_TLS_KEY_FILE", "OPENSVC_MCP_CLUSTER_CONFIG_FILE", "OPENSVC_MCP_OAUTH_RESOURCE_URL", "OPENSVC_MCP_OAUTH_RESOURCE_NAME", "OPENSVC_MCP_OAUTH_ISSUER", "OPENSVC_MCP_OAUTH_CA_FILE", "OPENSVC_MCP_AUTH_CONFIG_FILE", "OPENSVC_MCP_DELEGATED_SOCKET", "OPENSVC_MCP_MAX_CONCURRENT_CALLS", "OPENSVC_MCP_MAX_CALLS_PER_MINUTE", "OPENSVC_MCP_DELEGATED_CA_FILE", "OPENSVC_MCP_COLLECTOR_URL", "OPENSVC_MCP_COLLECTOR_CA_FILE", "OPENSVC_MCP_COLLECTOR_AUDIENCE", "OPENSVC_MCP_COLLECTOR_AUTH_PROFILE"} {
		t.Setenv(name, "")
	}
	t.Setenv("OPENSVC_MCP_OAUTH_RESOURCE_URL", "https://mcp.example.test/mcp")
	t.Setenv("OPENSVC_MCP_OAUTH_ISSUER", "https://sso.example.test/issuer/")
}

func TestLoadOAuthConfiguration(t *testing.T) {
	clearListenerEnvironment(t)
	t.Setenv("OPENSVC_MCP_TLS_CERT_FILE", "/tmp/server.crt")
	t.Setenv("OPENSVC_MCP_TLS_KEY_FILE", "/tmp/server.key")
	t.Setenv("OPENSVC_MCP_LISTEN_ADDR", "0.0.0.0:8443")
	t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", testutil.WriteClusters(t, map[string]string{"cluster-a": "Example cluster"}))
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Clusters.Len() != 1 || got.ListenAddress != "0.0.0.0:8443" {
		t.Fatalf("catalogue must be loaded independently of bind address: %+v", got)
	}
}

func TestLoadOAuthDoesNotRequireCatalogue(t *testing.T) {
	clearListenerEnvironment(t)
	t.Setenv("OPENSVC_MCP_TLS_CERT_FILE", "/tmp/server.crt")
	t.Setenv("OPENSVC_MCP_TLS_KEY_FILE", "/tmp/server.key")
	if cfg, err := Load(); err != nil || cfg.Clusters != nil {
		t.Fatalf("OAuth bootstrap should not require a daemon catalogue: %v", err)
	}
}

func TestLoadExchangeProfilesForClustersWithAuth(t *testing.T) {
	clearListenerEnvironment(t)
	t.Setenv("OPENSVC_MCP_TLS_CERT_FILE", "/tmp/server.crt")
	t.Setenv("OPENSVC_MCP_TLS_KEY_FILE", "/tmp/server.key")
	p := mcpauthtest.NewOAuthProvider(t)
	profiles := mcpauthtest.WriteExchangeProfiles(t, p, "client_secret_basic")
	entry := map[string]any{"name": "cluster-a", "cluster_id": "cluster-a", "endpoint": "https://cluster-a.test:1215", "request_timeout": "20s", "auth": map[string]string{"profile": "test-sso", "audience": "daemon-a"}}
	catalog := testutil.WriteYAML(t, map[string]any{"clusters": map[string]any{"cluster-a": entry}})
	t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", catalog)
	if _, err := Load(); err == nil {
		t.Fatal("cluster auth allowed without exchange configuration")
	}
	t.Setenv("OPENSVC_MCP_AUTH_CONFIG_FILE", profiles)
	if cfg, err := Load(); err != nil || cfg.Exchange == nil || cfg.Clusters.WithAuth().Len() != 1 {
		t.Fatalf("valid exchange configuration rejected: %v", err)
	}
	entry["auth"] = map[string]string{"profile": "missing", "audience": "daemon-a"}
	t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", testutil.WriteYAML(t, map[string]any{"clusters": map[string]any{"cluster-a": entry}}))
	if _, err := Load(); err == nil {
		t.Fatal("unknown profile accepted")
	}
	// Clusters without auth need no exchange profile.
	t.Setenv("OPENSVC_MCP_AUTH_CONFIG_FILE", "")
	t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", testutil.WriteClusters(t, map[string]string{"cluster-a": "Delegated only"}))
	if cfg, err := Load(); err != nil || cfg.Clusters.WithAuth().Len() != 0 {
		t.Fatalf("catalogue without auth rejected: %v", err)
	}
	t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", "")
	t.Setenv("OPENSVC_MCP_AUTH_CONFIG_FILE", profiles)
	if _, err := Load(); err == nil {
		t.Fatal("exchange profiles accepted without a catalogue")
	}
	if p.MetadataCalls.Load() != 0 || p.KeyCalls.Load() != 0 {
		t.Fatal("startup contacted SSO")
	}
}

func TestLoadDelegatedSocket(t *testing.T) {
	clearListenerEnvironment(t)
	t.Setenv("OPENSVC_MCP_OAUTH_RESOURCE_URL", "")
	t.Setenv("OPENSVC_MCP_OAUTH_ISSUER", "")
	clusters := testutil.WriteClusters(t, map[string]string{"cluster-a": "Example cluster"})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OPENSVC_MCP_DELEGATED_SOCKET") {
		t.Fatalf("no listener accepted: %v", err)
	}
	t.Setenv("OPENSVC_MCP_DELEGATED_SOCKET", "/run/opensvc-mcp/delegated.sock")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OPENSVC_MCP_CLUSTER_CONFIG_FILE") {
		t.Fatalf("socket accepted without a catalogue: %v", err)
	}
	t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", clusters)
	cfg, err := Load()
	if err != nil || cfg.HTTPSEnabled() || cfg.DelegatedSocket != "/run/opensvc-mcp/delegated.sock" || cfg.Clusters.Len() != 1 {
		t.Fatalf("socket-only configuration rejected: %+v %v", cfg, err)
	}
	for _, path := range []string{"relative.sock", "/", "/" + strings.Repeat("a", maxUnixSocketPathBytes)} {
		t.Setenv("OPENSVC_MCP_DELEGATED_SOCKET", path)
		if _, err := Load(); err == nil {
			t.Fatalf("invalid socket path %q accepted", path)
		}
	}
	// OAuth, exchange and listen settings require the HTTPS listener.
	t.Setenv("OPENSVC_MCP_DELEGATED_SOCKET", "/run/opensvc-mcp/delegated.sock")
	for _, variable := range []string{"OPENSVC_MCP_OAUTH_ISSUER", "OPENSVC_MCP_LISTEN_ADDR", "OPENSVC_MCP_AUTH_CONFIG_FILE"} {
		t.Run(variable, func(t *testing.T) {
			t.Setenv(variable, "/value")
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "require the HTTPS listener") {
				t.Fatalf("%s accepted without HTTPS: %v", variable, err)
			}
		})
	}
	// Both listeners can run together.
	t.Setenv("OPENSVC_MCP_TLS_CERT_FILE", "/tmp/server.crt")
	t.Setenv("OPENSVC_MCP_TLS_KEY_FILE", "/tmp/server.key")
	t.Setenv("OPENSVC_MCP_OAUTH_RESOURCE_URL", "https://mcp.example.test/mcp")
	t.Setenv("OPENSVC_MCP_OAUTH_ISSUER", "https://sso.example.test/issuer/")
	if cfg, err := Load(); err != nil || !cfg.HTTPSEnabled() || cfg.DelegatedSocket == "" {
		t.Fatalf("both listeners rejected: %v", err)
	}
}

func TestLoadRejectsInvalidOAuthConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, variable, value string }{
		{"missing resource", "OPENSVC_MCP_OAUTH_RESOURCE_URL", ""},
		{"HTTP resource", "OPENSVC_MCP_OAUTH_RESOURCE_URL", "http://mcp.example.test/mcp"},
		{"resource credentials", "OPENSVC_MCP_OAUTH_RESOURCE_URL", "https://user:password@mcp.example.test/mcp"},
		{"wrong resource path", "OPENSVC_MCP_OAUTH_RESOURCE_URL", "https://mcp.example.test/other"},
		{"resource query", "OPENSVC_MCP_OAUTH_RESOURCE_URL", "https://mcp.example.test/mcp?x=1"},
		{"resource fragment", "OPENSVC_MCP_OAUTH_RESOURCE_URL", "https://mcp.example.test/mcp#"},
		{"invalid resource name", "OPENSVC_MCP_OAUTH_RESOURCE_NAME", "MCP\nname"},
		{"missing issuer", "OPENSVC_MCP_OAUTH_ISSUER", ""},
		{"HTTP issuer", "OPENSVC_MCP_OAUTH_ISSUER", "http://sso.example.test"},
		{"issuer credentials", "OPENSVC_MCP_OAUTH_ISSUER", "https://user:password@sso.example.test"},
		{"issuer query", "OPENSVC_MCP_OAUTH_ISSUER", "https://sso.example.test?"},
		{"relative trust file", "OPENSVC_MCP_OAUTH_CA_FILE", "ca.pem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearListenerEnvironment(t)
			t.Setenv("OPENSVC_MCP_TLS_CERT_FILE", "/tmp/server.crt")
			t.Setenv("OPENSVC_MCP_TLS_KEY_FILE", "/tmp/server.key")
			t.Setenv(tc.variable, tc.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OPENSVC_MCP_OAUTH") {
				t.Fatalf("invalid OAuth configuration accepted: %v", err)
			}
		})
	}
}

func TestLoadRejectsInvalidClusterConfigBeforeListening(t *testing.T) {
	clearListenerEnvironment(t)
	t.Setenv("OPENSVC_MCP_TLS_CERT_FILE", "/tmp/server.crt")
	t.Setenv("OPENSVC_MCP_TLS_KEY_FILE", "/tmp/server.key")
	for _, path := range []string{"relative.yaml", "/nonexistent/clusters.yaml", t.TempDir()} {
		t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", path)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OPENSVC_MCP_CLUSTER_CONFIG_FILE") {
			t.Fatalf("invalid file %q accepted: %v", path, err)
		}
	}
}

func TestLoadHTTPS(t *testing.T) {
	clearListenerEnvironment(t)
	t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", testutil.WriteClusters(t, map[string]string{"cluster-a": "Example cluster"}))
	t.Setenv("OPENSVC_MCP_TLS_CERT_FILE", "/etc/opensvc-mcp/tls/server.crt")
	t.Setenv("OPENSVC_MCP_TLS_KEY_FILE", "/etc/opensvc-mcp/tls/server.key")
	for _, address := range []string{"", "192.0.2.10:443", "[::1]:8443"} {
		t.Run(address, func(t *testing.T) {
			t.Setenv("OPENSVC_MCP_LISTEN_ADDR", address)
			got, err := Load()
			if err != nil {
				t.Fatalf("load HTTPS configuration: %v", err)
			}
			wantAddress := address
			if wantAddress == "" {
				wantAddress = defaultListenAddress
			}
			if got.ListenAddress != wantAddress || got.TLSCertFile != "/etc/opensvc-mcp/tls/server.crt" || got.TLSKeyFile != "/etc/opensvc-mcp/tls/server.key" {
				t.Fatalf("unexpected HTTPS configuration: %#v", got)
			}
		})
	}
}

func TestLoadRejectsInvalidListenerConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, variable, value, wantError string
	}{
		{"missing certificate", "OPENSVC_MCP_TLS_CERT_FILE", "", "OPENSVC_MCP_TLS_CERT_FILE"},
		{"missing key", "OPENSVC_MCP_TLS_KEY_FILE", "", "OPENSVC_MCP_TLS_KEY_FILE"},
		{"relative certificate", "OPENSVC_MCP_TLS_CERT_FILE", "server.crt", "OPENSVC_MCP_TLS_CERT_FILE"},
		{"relative key", "OPENSVC_MCP_TLS_KEY_FILE", "server.key", "OPENSVC_MCP_TLS_KEY_FILE"},
		{"URL as address", "OPENSVC_MCP_LISTEN_ADDR", "https://192.0.2.10:443", "OPENSVC_MCP_LISTEN_ADDR"},
		{"missing port", "OPENSVC_MCP_LISTEN_ADDR", "192.0.2.10", "OPENSVC_MCP_LISTEN_ADDR"},
		{"implicit host", "OPENSVC_MCP_LISTEN_ADDR", ":443", "OPENSVC_MCP_LISTEN_ADDR"},
		{"hostname", "OPENSVC_MCP_LISTEN_ADDR", "localhost:443", "OPENSVC_MCP_LISTEN_ADDR"},
		{"ephemeral port", "OPENSVC_MCP_LISTEN_ADDR", "127.0.0.1:0", "OPENSVC_MCP_LISTEN_ADDR"},
		{"invalid port", "OPENSVC_MCP_LISTEN_ADDR", "127.0.0.1:65536", "OPENSVC_MCP_LISTEN_ADDR"},
		{"named port", "OPENSVC_MCP_LISTEN_ADDR", "127.0.0.1:https", "OPENSVC_MCP_LISTEN_ADDR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearListenerEnvironment(t)
			t.Setenv("OPENSVC_MCP_TLS_CERT_FILE", "/tmp/server.crt")
			t.Setenv("OPENSVC_MCP_TLS_KEY_FILE", "/tmp/server.key")
			t.Setenv(tc.variable, tc.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("Load() error = %v, want %s", err, tc.wantError)
			}
		})
	}
}

func TestLoadActions(t *testing.T) {
	clearListenerEnvironment(t)
	t.Setenv("OPENSVC_MCP_OAUTH_RESOURCE_URL", "")
	t.Setenv("OPENSVC_MCP_OAUTH_ISSUER", "")
	clusters := testutil.WriteClusters(t, map[string]string{"cluster-a": "Example cluster"})
	t.Setenv("OPENSVC_MCP_DELEGATED_SOCKET", "/run/opensvc-mcp/delegated.sock")
	t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", clusters)
	for value, want := range map[string]bool{"": false, "disabled": false, "enabled": true, " enabled ": true} {
		t.Setenv("OPENSVC_MCP_ACTIONS", value)
		cfg, err := Load()
		if err != nil || cfg.Actions != want {
			t.Fatalf("OPENSVC_MCP_ACTIONS=%q gave %v, %v; want %v", value, cfg.Actions, err, want)
		}
	}
	for _, value := range []string{"true", "yes", "ENABLED"} {
		t.Setenv("OPENSVC_MCP_ACTIONS", value)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OPENSVC_MCP_ACTIONS") {
			t.Fatalf("OPENSVC_MCP_ACTIONS=%q accepted: %v", value, err)
		}
	}
}

func TestLoadCallLimits(t *testing.T) {
	https := func(t *testing.T) {
		clearListenerEnvironment(t)
		t.Setenv("OPENSVC_MCP_TLS_CERT_FILE", "/tmp/server.crt")
		t.Setenv("OPENSVC_MCP_TLS_KEY_FILE", "/tmp/server.key")
	}
	https(t)
	cfg, err := Load()
	if err != nil || cfg.MaxConcurrentCalls != 10 || cfg.MaxCallsPerMinute != 120 {
		t.Fatalf("default limits: %d %d %v", cfg.MaxConcurrentCalls, cfg.MaxCallsPerMinute, err)
	}
	t.Setenv("OPENSVC_MCP_MAX_CONCURRENT_CALLS", "0")
	t.Setenv("OPENSVC_MCP_MAX_CALLS_PER_MINUTE", "600")
	if cfg, err = Load(); err != nil || cfg.MaxConcurrentCalls != 0 || cfg.MaxCallsPerMinute != 600 {
		t.Fatalf("configured limits: %d %d %v", cfg.MaxConcurrentCalls, cfg.MaxCallsPerMinute, err)
	}
	for _, tc := range []struct{ name, value string }{
		{"OPENSVC_MCP_MAX_CONCURRENT_CALLS", "-1"},
		{"OPENSVC_MCP_MAX_CONCURRENT_CALLS", "1001"},
		{"OPENSVC_MCP_MAX_CALLS_PER_MINUTE", "many"},
		{"OPENSVC_MCP_MAX_CALLS_PER_MINUTE", "100001"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			https(t)
			t.Setenv(tc.name, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid limit accepted")
			}
		})
	}
	t.Run("without HTTPS", func(t *testing.T) {
		clearListenerEnvironment(t)
		t.Setenv("OPENSVC_MCP_OAUTH_RESOURCE_URL", "")
		t.Setenv("OPENSVC_MCP_OAUTH_ISSUER", "")
		t.Setenv("OPENSVC_MCP_DELEGATED_SOCKET", "/run/opensvc-mcp/delegated.sock")
		t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", testutil.WriteClusters(t, map[string]string{"cluster-a": "Example cluster"}))
		if _, err := Load(); err != nil {
			t.Fatalf("socket configuration refused: %v", err)
		}
		t.Setenv("OPENSVC_MCP_MAX_CALLS_PER_MINUTE", "60")
		if _, err := Load(); err == nil {
			t.Fatal("limit accepted without the HTTPS listener")
		}
	})
}

func TestLoadDelegatedSocketRequiresTheProviderOfEveryCluster(t *testing.T) {
	clearListenerEnvironment(t)
	t.Setenv("OPENSVC_MCP_OAUTH_RESOURCE_URL", "")
	t.Setenv("OPENSVC_MCP_OAUTH_ISSUER", "")
	t.Setenv("OPENSVC_MCP_DELEGATED_SOCKET", "/run/opensvc-mcp/delegated.sock")
	t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", testutil.WriteCatalog(t, map[string]any{
		"a": map[string]any{"name": "a", "cluster_id": "cluster-a", "endpoint": "https://192.0.2.20:1215", "request_timeout": "2s",
			"delegation": map[string]string{"issuer": "https://idp.example.test/", "audience": "om3-a"}},
		"b": map[string]any{"name": "b", "cluster_id": "cluster-b", "endpoint": "https://192.0.2.21:1215", "request_timeout": "2s"},
	}))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "cluster b") || !strings.Contains(err.Error(), "delegation.issuer") {
		t.Fatalf("cluster without provider accepted on the socket: %v", err)
	}
}

func TestLoadDelegatedCAFile(t *testing.T) {
	socket := func(t *testing.T) {
		clearListenerEnvironment(t)
		t.Setenv("OPENSVC_MCP_OAUTH_RESOURCE_URL", "")
		t.Setenv("OPENSVC_MCP_OAUTH_ISSUER", "")
		t.Setenv("OPENSVC_MCP_DELEGATED_SOCKET", "/run/opensvc-mcp/delegated.sock")
		t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", testutil.WriteClusters(t, map[string]string{"cluster-a": "Example cluster"}))
	}
	// A socket-only configuration trusts a private CA for the OpenID providers.
	socket(t)
	t.Setenv("OPENSVC_MCP_DELEGATED_CA_FILE", "/etc/opensvc-mcp/sso-ca.pem")
	if cfg, err := Load(); err != nil || cfg.HTTPSEnabled() || cfg.DelegatedCAFile != "/etc/opensvc-mcp/sso-ca.pem" {
		t.Fatalf("socket-only CA refused: %+v %v", cfg, err)
	}
	t.Run("relative", func(t *testing.T) {
		socket(t)
		t.Setenv("OPENSVC_MCP_DELEGATED_CA_FILE", "sso-ca.pem")
		if _, err := Load(); err == nil {
			t.Fatal("relative CA path accepted")
		}
	})
	t.Run("without the socket", func(t *testing.T) {
		clearListenerEnvironment(t)
		t.Setenv("OPENSVC_MCP_TLS_CERT_FILE", "/tmp/server.crt")
		t.Setenv("OPENSVC_MCP_TLS_KEY_FILE", "/tmp/server.key")
		t.Setenv("OPENSVC_MCP_DELEGATED_CA_FILE", "/etc/opensvc-mcp/sso-ca.pem")
		if _, err := Load(); err == nil {
			t.Fatal("CA accepted without the socket")
		}
	})
}

// TestLoadCollector reads the collector of the resources and the skills: an
// HTTPS URL, its audience and an exchange profile, on a socket-only server
// too, whose exchange profiles then serve the collector alone.
func TestLoadCollector(t *testing.T) {
	clearListenerEnvironment(t)
	t.Setenv("OPENSVC_MCP_OAUTH_RESOURCE_URL", "")
	t.Setenv("OPENSVC_MCP_OAUTH_ISSUER", "")
	p := mcpauthtest.NewOAuthProvider(t)
	t.Setenv("OPENSVC_MCP_DELEGATED_SOCKET", "/run/opensvc-mcp/delegated.sock")
	t.Setenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE", testutil.WriteClusters(t, map[string]string{"cluster-a": "Example cluster"}))
	if cfg, err := Load(); err != nil || cfg.Collector != nil {
		t.Fatalf("no collector: %+v, %v", cfg.Collector, err)
	}
	t.Setenv("OPENSVC_MCP_COLLECTOR_AUDIENCE", "collector-client")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "require OPENSVC_MCP_COLLECTOR_URL") {
		t.Fatalf("a collector audience without URL: %v", err)
	}
	t.Setenv("OPENSVC_MCP_COLLECTOR_URL", "https://collector.example.test/api")
	t.Setenv("OPENSVC_MCP_COLLECTOR_AUTH_PROFILE", "test-sso")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "missing from OPENSVC_MCP_AUTH_CONFIG_FILE") {
		t.Fatalf("a collector without exchange profiles: %v", err)
	}
	t.Setenv("OPENSVC_MCP_AUTH_CONFIG_FILE", mcpauthtest.WriteExchangeProfiles(t, p, "client_secret_basic"))
	cfg, err := Load()
	if err != nil || cfg.Collector == nil || cfg.Collector.URL != "https://collector.example.test/api" || cfg.Collector.Audience != "collector-client" || cfg.Collector.AuthProfile != "test-sso" || cfg.Exchange == nil {
		t.Fatalf("a socket-only server with a collector: %+v, %v", cfg.Collector, err)
	}
	for name, change := range map[string][2]string{
		"http URL":    {"OPENSVC_MCP_COLLECTOR_URL", "http://collector.example.test/api"},
		"credentials": {"OPENSVC_MCP_COLLECTOR_URL", "https://user:pass@collector.example.test/api"},
		"query":       {"OPENSVC_MCP_COLLECTOR_URL", "https://collector.example.test/api?x=1"},
		"no audience": {"OPENSVC_MCP_COLLECTOR_AUDIENCE", ""},
		"no profile":  {"OPENSVC_MCP_COLLECTOR_AUTH_PROFILE", ""},
		"unknown":     {"OPENSVC_MCP_COLLECTOR_AUTH_PROFILE", "other"},
		"relative CA": {"OPENSVC_MCP_COLLECTOR_CA_FILE", "ca.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(change[0], change[1])
			if _, err := Load(); err == nil {
				t.Errorf("%s=%q accepted", change[0], change[1])
			}
		})
	}
	if p.MetadataCalls.Load() != 0 {
		t.Fatal("startup contacted the SSO")
	}
}
