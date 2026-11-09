package client

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

const (
	// CollectorTarget is the target of the tokens exchanged for the
	// collector, as mcpauth.ExchangedFromContext returns it.
	CollectorTarget = "collector"
	// collectorTimeout bounds a request of the collector.
	collectorTimeout = 30 * time.Second
	// maxCollectorCABytes bounds the trust bundle of the collector.
	maxCollectorCABytes = 1 << 20
	// maxCollectorConns bounds the connections of the whole process to the
	// collector, whatever the number of callers: the Unix socket has no call
	// limiter. A request beyond it waits for a connection, within its own
	// deadline. It bounds the concurrent requests only because the transport,
	// with its own TLSClientConfig, speaks HTTP/1.1, one request per
	// connection: enabling HTTP/2 would multiplex hundreds of requests on one
	// connection, and this bound would then need a semaphore on the requests.
	maxCollectorConns = 64
)

// NewCollector reads the collector API at baseURL, an HTTPS URL such as
// https://collector.example.com/api, with the token exchanged for
// CollectorTarget only, trusting the PEM bundle caFile, or the system roots
// when it is empty. It follows no redirect and uses no proxy.
func NewCollector(baseURL, caFile string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, errors.New("the collector URL must be an HTTPS URL without credentials")
	}
	var roots *x509.CertPool
	if caFile != "" {
		info, err := os.Stat(caFile)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxCollectorCABytes {
			return nil, fmt.Errorf("read the collector CA file: not a regular file of at most %d bytes", maxCollectorCABytes)
		}
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read the collector CA file: %w", err)
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("the collector CA file holds no PEM certificate")
		}
	}
	base := &http.Transport{
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConns: 2, MaxIdleConnsPerHost: 2,
		MaxConnsPerHost: maxCollectorConns,
	}
	httpClient := &http.Client{
		Transport: &exchangedTransport{base: base, target: CollectorTarget, origin: u.Scheme + "://" + u.Host},
		Timeout:   collectorTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return New(baseURL, httpClient)
}
