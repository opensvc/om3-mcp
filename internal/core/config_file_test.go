package core

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

type configFileClient struct {
	t       *testing.T
	path    string
	query   url.Values
	payload []byte
	err     error
	calls   int
}

func (f *configFileClient) GetJSON(_ context.Context, path string, _ url.Values, _ any) error {
	recordRoute("GET", path)
	f.t.Helper()
	f.t.Fatal("unexpected JSON request")
	return nil
}

func (f *configFileClient) GetFile(_ context.Context, path string, query url.Values) ([]byte, error) {
	recordRoute("GET", path)
	f.t.Helper()
	f.calls++
	if path != f.path {
		f.t.Errorf("got path %q, want %q", path, f.path)
	}
	if query.Encode() != f.query.Encode() {
		f.t.Errorf("got query %q, want %q", query.Encode(), f.query.Encode())
	}
	return f.payload, f.err
}

func TestGetClusterConfigRequestsRedaction(t *testing.T) {
	payload := []byte("[cluster]\nname = prod\nsecret = ********\n")
	client := &configFileClient{
		t: t, path: "/api/cluster/config/file",
		query: url.Values{"redact-secrets": {"true"}}, payload: payload,
	}
	result, err := New(client).GetClusterConfig(context.Background())
	if err != nil {
		t.Fatalf("get cluster config: %v", err)
	}
	if result.Content != string(payload) || result.SizeBytes != len(payload) || result.ReturnedBytes != len(payload) || result.Truncated || !result.RedactionRequested {
		t.Fatalf("unexpected cluster config: %+v", result)
	}
}

func TestGetNodeConfigRequestsRedaction(t *testing.T) {
	payload := []byte("[node]\nsshkey = ********\n")
	client := &configFileClient{
		t: t, path: "/api/node/name/node-a/config/file",
		query: url.Values{"redact-secrets": {"true"}}, payload: payload,
	}
	result, err := New(client).GetNodeConfig(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get node config: %v", err)
	}
	if result.Node != "node-a" || result.Content != string(payload) || result.SizeBytes != len(payload) || result.ReturnedBytes != len(payload) || result.Truncated || !result.RedactionRequested {
		t.Fatalf("unexpected node config: %+v", result)
	}
}

func TestGetConfigBoundsUTF8Content(t *testing.T) {
	payload := []byte(strings.Repeat("x", maxConfigFileOutputBytes-1) + "é" + "tail")
	client := &configFileClient{
		t: t, path: "/api/cluster/config/file",
		query: url.Values{"redact-secrets": {"true"}}, payload: payload,
	}
	result, err := New(client).GetClusterConfig(context.Background())
	if err != nil {
		t.Fatalf("get cluster config: %v", err)
	}
	if !result.Truncated || result.SizeBytes != len(payload) || result.ReturnedBytes != maxConfigFileOutputBytes-1 {
		t.Fatalf("unexpected bounds: %+v", result)
	}
	if result.Content != strings.Repeat("x", maxConfigFileOutputBytes-1) {
		t.Fatal("truncated content does not end at the UTF-8 boundary")
	}
}

func TestGetConfigRejectsInvalidUTF8(t *testing.T) {
	client := &configFileClient{
		t: t, path: "/api/cluster/config/file",
		query: url.Values{"redact-secrets": {"true"}}, payload: []byte{0xff},
	}
	if _, err := New(client).GetClusterConfig(context.Background()); err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("got error %v, want invalid UTF-8 error", err)
	}
}

func TestGetNodeConfigRejectsInvalidNodeBeforeDaemonCall(t *testing.T) {
	for _, node := range []string{"", "_", " node-a", "node/a", strings.Repeat("x", 256)} {
		client := &configFileClient{t: t}
		if _, err := New(client).GetNodeConfig(context.Background(), node); err == nil {
			t.Errorf("node %q succeeded", node)
		}
		if client.calls != 0 {
			t.Errorf("node %q made %d daemon calls", node, client.calls)
		}
	}
}

func TestGetConfigPropagatesDaemonError(t *testing.T) {
	client := &configFileClient{
		t: t, path: "/api/cluster/config/file",
		query: url.Values{"redact-secrets": {"true"}}, err: fmt.Errorf("forbidden"),
	}
	if _, err := New(client).GetClusterConfig(context.Background()); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("got error %v, want daemon error", err)
	}
}
