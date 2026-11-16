package core

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

type nodeReachabilityClient struct {
	t     *testing.T
	path  string
	query url.Values
	err   error
	calls int
}

func (f *nodeReachabilityClient) GetJSON(_ context.Context, path string, _ url.Values, _ any) error {
	recordRoute("GET", path)
	f.t.Helper()
	f.t.Fatal("unexpected JSON request")
	return nil
}

func (f *nodeReachabilityClient) GetNoContent(_ context.Context, path string, query url.Values) error {
	recordRoute("GET", path)
	f.t.Helper()
	f.calls++
	if path != f.path {
		f.t.Errorf("got path %q, want %q", path, f.path)
	}
	if !reflect.DeepEqual(query, f.query) {
		f.t.Errorf("got query %#v, want %#v", query, f.query)
	}
	return f.err
}

func TestProbeNodeReachability(t *testing.T) {
	client := &nodeReachabilityClient{
		t: t, path: "/api/node/name/node-b/ping", query: url.Values{},
	}
	result, err := New(client).ProbeNodeReachability(context.Background(), "node-b")
	if err != nil {
		t.Fatalf("probe node reachability: %v", err)
	}
	if result.Node != "node-b" || !result.Reachable || result.StatusCode != 204 || result.RoundTripMS < 0 {
		t.Fatalf("unexpected probe result: %#v", result)
	}
	if result.Provenance.Source != provenanceSourceOpenSVCDaemon || result.Provenance.ObservedAt == "" {
		t.Errorf("unexpected provenance: %#v", result.Provenance)
	}
	if client.calls != 1 {
		t.Errorf("got %d daemon calls, want 1", client.calls)
	}
}

func TestProbeNodeReachabilityRejectsInvalidNodeBeforeCallingDaemon(t *testing.T) {
	for _, node := range []string{"", "_", " node-b", "node*", "node/b", strings.Repeat("x", 256)} {
		t.Run(fmt.Sprintf("node_%q", node), func(t *testing.T) {
			client := &nodeReachabilityClient{t: t}
			if _, err := New(client).ProbeNodeReachability(context.Background(), node); err == nil {
				t.Fatal("expected validation error")
			}
			if client.calls != 0 {
				t.Errorf("daemon was called %d times", client.calls)
			}
		})
	}
}

func TestProbeNodeReachabilityPropagatesDaemonFailure(t *testing.T) {
	client := &nodeReachabilityClient{
		t: t, path: "/api/node/name/node-b/ping", query: url.Values{}, err: fmt.Errorf("request peer: connection refused"),
	}
	_, err := New(client).ProbeNodeReachability(context.Background(), "node-b")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("got error %v, want daemon failure", err)
	}
}

func TestProbeNodeReachabilityRequiresNoContentClient(t *testing.T) {
	client := &recordingJSONGetter{t: t}
	_, err := New(client).ProbeNodeReachability(context.Background(), "node-b")
	if err == nil || !strings.Contains(err.Error(), "does not support no-content") {
		t.Fatalf("got error %v", err)
	}
}

var _ JSONGetter = (*nodeReachabilityClient)(nil)
var _ NoContentGetter = (*nodeReachabilityClient)(nil)
