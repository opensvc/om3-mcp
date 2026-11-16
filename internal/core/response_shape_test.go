package core

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/opensvc/om3-mcp/internal/client"
)

// A list, or an item of it, of another kind than the one this MCP reads is
// an answer of another API: the tool tells the model the daemon moved on.
func TestAnAnswerOfAnotherKindIsIncompatible(t *testing.T) {
	for name, payload := range map[string]string{
		"a list of another kind":  `{"kind":"PackageInventory","items":[]}`,
		"an item of another kind": `{"kind":"PackageList","items":[{"kind":"Package","meta":{"node":"node-a"},"data":{"name":"curl","version":"8","arch":"amd64","type":"deb","installedat":"2026-09-01T10:00:00Z","sig":""}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			getter := &recordingJSONGetter{t: t, path: "/api/node/name/node-a/system/package", query: url.Values{}, payload: payload}
			_, err := New(getter).ListNodePackages(context.Background(), ListNodePackagesOptions{Node: "node-a"})
			if !errors.Is(err, client.ErrIncompatible) || !strings.Contains(err.Error(), "unexpected") || !strings.Contains(err.Error(), "kind") {
				t.Fatalf("got %v, want an answer of another API", err)
			}
		})
	}
}

// An item naming another node is no answer of another API: it stays an error
// of the request, said as it is.
func TestAnItemOfAnotherNodeStaysAnError(t *testing.T) {
	payload := `{"kind":"PackageList","items":[{"kind":"PackageItem","meta":{"node":"node-b"},"data":{"name":"curl","version":"8","arch":"amd64","type":"deb","installedat":"2026-09-01T10:00:00Z","sig":""}}]}`
	getter := &recordingJSONGetter{t: t, path: "/api/node/name/node-a/system/package", query: url.Values{}, payload: payload}
	_, err := New(getter).ListNodePackages(context.Background(), ListNodePackagesOptions{Node: "node-a"})
	if err == nil || errors.Is(err, client.ErrIncompatible) || !strings.Contains(err.Error(), `unexpected node "node-b"`) {
		t.Fatalf("got %v, want the item of another node refused as such", err)
	}
}

// The instance status tells the two apart too: another kind is an answer of
// another API, the instance of another node or object is not.
func TestAnInstanceOfAnotherKindOrIdentity(t *testing.T) {
	for name, test := range map[string]struct {
		replace, with string
		incompatible  bool
	}{
		"another kind":   {`"kind":"InstanceItem"`, `"kind":"InstanceStatus"`, true},
		"another node":   {`"node":"node-a"`, `"node":"node-b"`, false},
		"another object": {`"object":"prod/svc/app"`, `"object":"prod/svc/other"`, false},
	} {
		t.Run(name, func(t *testing.T) {
			payload := strings.Replace(instanceStatusPayload, test.replace, test.with, 1)
			getter := &recordingJSONGetter{t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app", query: url.Values{}, payload: payload}
			_, err := New(getter).GetInstanceStatus(context.Background(), GetInstanceStatusOptions{Path: "prod/svc/app", Node: "node-a"})
			if err == nil || errors.Is(err, client.ErrIncompatible) != test.incompatible {
				t.Fatalf("got %v, want incompatible %v", err, test.incompatible)
			}
		})
	}
}
