package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/opensvc/om3-mcp/internal/clusterconfig"
	"github.com/opensvc/osvc-auth-mcp/mcpauth"
)

const (
	defaultListenAddress = "127.0.0.1:8443"
	// maxUnixSocketPathBytes is the portable sun_path limit, including the NUL.
	maxUnixSocketPathBytes = 103
)

// Config contains the runtime configuration of the MCP server process. It has
// two optional listeners, at least one of which is enabled: HTTPS for OAuth
// external agents, and a local Unix socket for delegated OpenSVC tokens.
type Config struct {
	// HTTPS listener, enabled when TLSCertFile is set. It requires OAuth.
	ListenAddress string
	TLSCertFile   string
	TLSKeyFile    string
	OAuth         mcpauth.OAuthConfig
	Exchange      *mcpauth.ExchangeProfiles
	// MaxConcurrentCalls and MaxCallsPerMinute bound the tool calls of each
	// identity on the HTTPS listener; 0 disables a limit.
	MaxConcurrentCalls int
	MaxCallsPerMinute  int
	// DelegatedSocket enables the Unix socket listener. It requires a catalogue.
	DelegatedSocket string
	// DelegatedCAFile is the PEM trust bundle for the OpenID providers of the
	// tokens of the socket, replacing the system roots; empty for the OAuth
	// bundle of the HTTPS listener, if any, else the system roots.
	DelegatedCAFile   string
	Clusters          *clusterconfig.Catalog
	ClusterConfigFile string
	// Actions registers the tools that change the cluster state, on both
	// listeners. They are left out unless OPENSVC_MCP_ACTIONS is enabled.
	Actions bool
	// Collector serves the resources and the skills of the AI assistant the
	// collector stores, on both listeners; nil without OPENSVC_MCP_COLLECTOR_URL.
	Collector *Collector
}

// Collector is the collector API the resources and the skills are read
// from, with a token of the caller exchanged for Audience with the exchange
// profile AuthProfile.
type Collector struct {
	URL         string
	CAFile      string
	Audience    string
	AuthProfile string
}

func (c Config) HTTPSEnabled() bool { return c.TLSCertFile != "" }

// Load reads and validates process configuration from environment variables.
func Load() (Config, error) {
	var cfg Config
	cfg.TLSCertFile = strings.TrimSpace(os.Getenv("OPENSVC_MCP_TLS_CERT_FILE"))
	cfg.TLSKeyFile = strings.TrimSpace(os.Getenv("OPENSVC_MCP_TLS_KEY_FILE"))
	listenAddress := strings.TrimSpace(os.Getenv("OPENSVC_MCP_LISTEN_ADDR"))
	oauth := mcpauth.OAuthConfig{
		ResourceURL:  strings.TrimSpace(os.Getenv("OPENSVC_MCP_OAUTH_RESOURCE_URL")),
		ResourceName: strings.TrimSpace(os.Getenv("OPENSVC_MCP_OAUTH_RESOURCE_NAME")),
		Issuer:       strings.TrimSpace(os.Getenv("OPENSVC_MCP_OAUTH_ISSUER")),
		CAFile:       strings.TrimSpace(os.Getenv("OPENSVC_MCP_OAUTH_CA_FILE")),
	}
	exchangeFile := strings.TrimSpace(os.Getenv("OPENSVC_MCP_AUTH_CONFIG_FILE"))
	maxConcurrent := strings.TrimSpace(os.Getenv("OPENSVC_MCP_MAX_CONCURRENT_CALLS"))
	maxPerMinute := strings.TrimSpace(os.Getenv("OPENSVC_MCP_MAX_CALLS_PER_MINUTE"))
	if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" {
		if !filepath.IsAbs(cfg.TLSCertFile) || !filepath.IsAbs(cfg.TLSKeyFile) {
			return Config{}, fmt.Errorf("OPENSVC_MCP_TLS_CERT_FILE and OPENSVC_MCP_TLS_KEY_FILE must be absolute file paths")
		}
		if listenAddress == "" {
			listenAddress = defaultListenAddress
		}
		if err := validateListenAddress(listenAddress); err != nil {
			return Config{}, fmt.Errorf("parse OPENSVC_MCP_LISTEN_ADDR: %w", err)
		}
		if err := oauth.Validate(); err != nil {
			return Config{}, fmt.Errorf("OPENSVC_MCP_OAUTH configuration: %w", err)
		}
		cfg.ListenAddress, cfg.OAuth = listenAddress, oauth
		var err error
		if cfg.MaxConcurrentCalls, err = callLimit("OPENSVC_MCP_MAX_CONCURRENT_CALLS", maxConcurrent, mcpauth.DefaultMaxConcurrentCalls, 1000); err != nil {
			return Config{}, err
		}
		if cfg.MaxCallsPerMinute, err = callLimit("OPENSVC_MCP_MAX_CALLS_PER_MINUTE", maxPerMinute, mcpauth.DefaultMaxCallsPerMinute, 100000); err != nil {
			return Config{}, err
		}
	} else if listenAddress != "" || oauth != (mcpauth.OAuthConfig{}) || maxConcurrent != "" || maxPerMinute != "" {
		return Config{}, fmt.Errorf("OPENSVC_MCP_LISTEN_ADDR, OPENSVC_MCP_OAUTH_* and OPENSVC_MCP_MAX_* require the HTTPS listener (OPENSVC_MCP_TLS_CERT_FILE and OPENSVC_MCP_TLS_KEY_FILE)")
	}
	collector, err := loadCollector()
	if err != nil {
		return Config{}, err
	}
	if exchangeFile != "" && !cfg.HTTPSEnabled() && collector == nil {
		return Config{}, fmt.Errorf("OPENSVC_MCP_AUTH_CONFIG_FILE: the exchange profiles require the HTTPS listener (OPENSVC_MCP_TLS_CERT_FILE and OPENSVC_MCP_TLS_KEY_FILE), or OPENSVC_MCP_COLLECTOR_URL")
	}
	cfg.Collector = collector
	if socket := strings.TrimSpace(os.Getenv("OPENSVC_MCP_DELEGATED_SOCKET")); socket != "" {
		path := filepath.Clean(socket)
		if !filepath.IsAbs(path) || path == string(filepath.Separator) || len(path) > maxUnixSocketPathBytes {
			return Config{}, fmt.Errorf("OPENSVC_MCP_DELEGATED_SOCKET must be an absolute socket path of at most %d bytes", maxUnixSocketPathBytes)
		}
		cfg.DelegatedSocket = path
	}
	if caFile := strings.TrimSpace(os.Getenv("OPENSVC_MCP_DELEGATED_CA_FILE")); caFile != "" {
		if cfg.DelegatedSocket == "" {
			return Config{}, fmt.Errorf("OPENSVC_MCP_DELEGATED_CA_FILE requires OPENSVC_MCP_DELEGATED_SOCKET")
		}
		if !filepath.IsAbs(caFile) {
			return Config{}, fmt.Errorf("OPENSVC_MCP_DELEGATED_CA_FILE must be an absolute file path")
		}
		cfg.DelegatedCAFile = caFile
	}
	if !cfg.HTTPSEnabled() && cfg.DelegatedSocket == "" {
		return Config{}, fmt.Errorf("configure the HTTPS listener (OPENSVC_MCP_TLS_CERT_FILE and OPENSVC_MCP_TLS_KEY_FILE), OPENSVC_MCP_DELEGATED_SOCKET, or both")
	}
	cfg.ClusterConfigFile = strings.TrimSpace(os.Getenv("OPENSVC_MCP_CLUSTER_CONFIG_FILE"))
	if cfg.ClusterConfigFile != "" {
		var err error
		cfg.Clusters, err = clusterconfig.Load(cfg.ClusterConfigFile)
		if err != nil {
			return Config{}, fmt.Errorf("OPENSVC_MCP_CLUSTER_CONFIG_FILE: %w", err)
		}
	}
	if cfg.DelegatedSocket != "" && cfg.Clusters == nil {
		return Config{}, fmt.Errorf("OPENSVC_MCP_DELEGATED_SOCKET requires OPENSVC_MCP_CLUSTER_CONFIG_FILE")
	}
	if cfg.DelegatedSocket != "" {
		// No token of the socket reaches a daemon unverified.
		for _, c := range cfg.Clusters.List() {
			if c.DelegatedIssuer == "" {
				return Config{}, fmt.Errorf("cluster %s: OPENSVC_MCP_DELEGATED_SOCKET requires delegation.issuer, the issuer of the OpenID provider of the daemon", c.Ref)
			}
		}
	}
	if exchangeFile != "" {
		if cfg.Clusters == nil {
			return Config{}, fmt.Errorf("OPENSVC_MCP_AUTH_CONFIG_FILE requires OPENSVC_MCP_CLUSTER_CONFIG_FILE")
		}
		var err error
		cfg.Exchange, err = mcpauth.LoadExchangeProfiles(exchangeFile)
		if err != nil {
			return Config{}, fmt.Errorf("OPENSVC_MCP_AUTH_CONFIG_FILE: %w", err)
		}
	}
	if cfg.Collector != nil && !cfg.Exchange.Has(cfg.Collector.AuthProfile) {
		return Config{}, fmt.Errorf("OPENSVC_MCP_COLLECTOR_AUTH_PROFILE names a profile missing from OPENSVC_MCP_AUTH_CONFIG_FILE")
	}
	switch actions := strings.TrimSpace(os.Getenv("OPENSVC_MCP_ACTIONS")); actions {
	case "", "disabled":
	case "enabled":
		cfg.Actions = true
	default:
		return Config{}, fmt.Errorf("OPENSVC_MCP_ACTIONS must be enabled or disabled")
	}
	for _, c := range cfg.Clusters.List() {
		if c.AuthProfile == "" {
			continue
		}
		if !cfg.HTTPSEnabled() {
			return Config{}, fmt.Errorf("cluster %s: auth requires the HTTPS listener", c.Ref)
		}
		if !cfg.Exchange.Has(c.AuthProfile) {
			return Config{}, fmt.Errorf("cluster %s references an auth profile missing from OPENSVC_MCP_AUTH_CONFIG_FILE", c.Ref)
		}
	}
	return cfg, nil
}

// loadCollector reads the collector settings: none without
// OPENSVC_MCP_COLLECTOR_URL, else an HTTPS URL, its audience and the exchange
// profile that exchanges the token of the caller for it.
func loadCollector() (*Collector, error) {
	c := &Collector{
		URL:         strings.TrimSpace(os.Getenv("OPENSVC_MCP_COLLECTOR_URL")),
		CAFile:      strings.TrimSpace(os.Getenv("OPENSVC_MCP_COLLECTOR_CA_FILE")),
		Audience:    strings.TrimSpace(os.Getenv("OPENSVC_MCP_COLLECTOR_AUDIENCE")),
		AuthProfile: strings.TrimSpace(os.Getenv("OPENSVC_MCP_COLLECTOR_AUTH_PROFILE")),
	}
	if c.URL == "" {
		if *c != (Collector{}) {
			return nil, fmt.Errorf("OPENSVC_MCP_COLLECTOR_* require OPENSVC_MCP_COLLECTOR_URL")
		}
		return nil, nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("OPENSVC_MCP_COLLECTOR_URL must be an HTTPS URL without credentials, query or fragment, for example https://collector.example.com/api")
	}
	if c.CAFile != "" && !filepath.IsAbs(c.CAFile) {
		return nil, fmt.Errorf("OPENSVC_MCP_COLLECTOR_CA_FILE must be an absolute file path")
	}
	if !mcpauth.ValidClaimText(c.Audience) || !mcpauth.ValidClaimText(c.AuthProfile) {
		return nil, fmt.Errorf("OPENSVC_MCP_COLLECTOR_URL requires OPENSVC_MCP_COLLECTOR_AUDIENCE, the client ID of the collector, and OPENSVC_MCP_COLLECTOR_AUTH_PROFILE, the profile of OPENSVC_MCP_AUTH_CONFIG_FILE exchanging tokens for it")
	}
	return c, nil
}

func validateListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("expected IP:port: %w", err)
	}
	if net.ParseIP(host) == nil {
		return fmt.Errorf("host must be an explicit IPv4 or IPv6 address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("port must be a number between 1 and 65535")
	}
	return nil
}

// callLimit reads a limit of the tool calls of an identity: def when value is
// empty, 0 to disable the limit, at most max.
func callLimit(name, value string, def, max int) (int, error) {
	if value == "" {
		return def, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > max {
		return 0, fmt.Errorf("%s must be an integer from 0 (no limit) to %d", name, max)
	}
	return n, nil
}
