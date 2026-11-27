package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/opensvc/om3-mcp/internal/auth"
	"github.com/opensvc/om3-mcp/internal/clusterconfig"
)

// DelegatedClient forwards the user's OpenID Connect access token, verified
// by the delegation middleware, unchanged to the VIP of the cluster selected
// by the request header. It shares connection pools,
// never a current cluster or user credentials.
type DelegatedClient struct {
	clients map[string]*Client
}

func NewDelegated(catalog *clusterconfig.Catalog) (*DelegatedClient, error) {
	if catalog.Len() == 0 {
		return nil, errors.New("token delegation requires a cluster catalogue")
	}
	c := &DelegatedClient{clients: make(map[string]*Client)}
	for _, cluster := range catalog.List() {
		var roots *x509.CertPool
		if len(cluster.CAPEM) > 0 {
			roots = x509.NewCertPool()
			if !roots.AppendCertsFromPEM(cluster.CAPEM) {
				return nil, errors.New("invalid daemon TLS trust")
			}
		}
		base := &http.Transport{
			// Explicit per-cluster demo option. HTTPS and origin binding remain
			// required, but the peer is NOT authenticated when this is true.
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, InsecureSkipVerify: cluster.TLSInsecure},
			// Four idle connections, as validate_object_configs sends four
			// validations at once: fewer would open a TLS connection for most.
			TLSHandshakeTimeout: cluster.RequestTimeout, IdleConnTimeout: 90 * time.Second, MaxIdleConns: 4, MaxIdleConnsPerHost: 4,
			// Proxy is intentionally nil: delegated credentials travel directly.
		}
		httpClient := &http.Client{
			Transport:     &delegatedTransport{base: base, clusterID: cluster.ID, origin: cluster.Endpoint},
			Timeout:       cluster.RequestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		api, err := New(cluster.Endpoint, httpClient)
		if err != nil {
			return nil, err
		}
		c.clients[cluster.ID] = api
	}
	return c, nil
}

func (c *DelegatedClient) selected(ctx context.Context) (*Client, error) {
	delegation, _, ok := auth.DelegationFromContext(ctx)
	if !ok {
		return nil, errors.New("daemon request requires a checked delegated JWT")
	}
	api, ok := c.clients[delegation.ClusterID]
	if !ok {
		return nil, errors.New("daemon target is not configured")
	}
	return api, nil
}

type delegatedTransport struct {
	base              http.RoundTripper
	clusterID, origin string
}

func (t *delegatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	delegation, token, ok := auth.DelegationFromContext(request.Context())
	if !ok || delegation.ClusterID != t.clusterID {
		return nil, errors.New("daemon request does not match its checked delegation")
	}
	if request.URL.User != nil || request.URL.Scheme+"://"+request.URL.Host != t.origin || request.Host != "" && request.Host != request.URL.Host {
		return nil, errors.New("daemon request does not match its configured origin")
	}
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+token)
	clone.Header.Del(auth.ClusterIDHeader)
	return t.base.RoundTrip(clone)
}

func (c *DelegatedClient) GetJSON(ctx context.Context, path string, query url.Values, output any) error {
	api, err := c.selected(ctx)
	if err != nil {
		return err
	}
	return api.GetJSON(ctx, path, query, output)
}

func (c *DelegatedClient) PostJSON(ctx context.Context, path string, query url.Values, input, output any) error {
	api, err := c.selected(ctx)
	if err != nil {
		return err
	}
	return api.PostJSON(ctx, path, query, input, output)
}

func (c *DelegatedClient) GetSSE(ctx context.Context, path string, query url.Values, consume func(string, string, []byte) error) error {
	api, err := c.selected(ctx)
	if err != nil {
		return err
	}
	return api.GetSSE(ctx, path, query, consume)
}

func (c *DelegatedClient) GetStream(ctx context.Context, path string, query url.Values, consume func([]byte) error) error {
	api, err := c.selected(ctx)
	if err != nil {
		return err
	}
	return api.GetStream(ctx, path, query, consume)
}

func (c *DelegatedClient) GetFile(ctx context.Context, path string, query url.Values) ([]byte, error) {
	api, err := c.selected(ctx)
	if err != nil {
		return nil, err
	}
	return api.GetFile(ctx, path, query)
}

func (c *DelegatedClient) GetText(ctx context.Context, path string, query url.Values) ([]byte, error) {
	api, err := c.selected(ctx)
	if err != nil {
		return nil, err
	}
	return api.GetText(ctx, path, query)
}

func (c *DelegatedClient) GetNoContent(ctx context.Context, path string, query url.Values) error {
	api, err := c.selected(ctx)
	if err != nil {
		return err
	}
	return api.GetNoContent(ctx, path, query)
}

func (c *DelegatedClient) PatchJSON(ctx context.Context, path string, query url.Values, output any) (http.Header, error) {
	api, err := c.selected(ctx)
	if err != nil {
		return nil, err
	}
	return api.PatchJSON(ctx, path, query, output)
}

func (c *DelegatedClient) PostFile(ctx context.Context, path string, query url.Values, body []byte) (http.Header, error) {
	api, err := c.selected(ctx)
	if err != nil {
		return nil, err
	}
	return api.PostFile(ctx, path, query, body)
}

func (c *DelegatedClient) PostJSONWithHeader(ctx context.Context, path string, query url.Values, input, output any) (http.Header, error) {
	api, err := c.selected(ctx)
	if err != nil {
		return nil, err
	}
	return api.PostJSONWithHeader(ctx, path, query, input, output)
}

func (c *DelegatedClient) GetBlob(ctx context.Context, path string, query url.Values) ([]byte, string, bool, error) {
	api, err := c.selected(ctx)
	if err != nil {
		return nil, "", false, err
	}
	return api.GetBlob(ctx, path, query)
}

func (c *DelegatedClient) PatchJSONBody(ctx context.Context, path string, query url.Values, input any, output any) (http.Header, error) {
	api, err := c.selected(ctx)
	if err != nil {
		return nil, err
	}
	return api.PatchJSONBody(ctx, path, query, input, output)
}
