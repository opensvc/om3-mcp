package core

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

type recordingJSONGetter struct {
	t       *testing.T
	path    string
	query   url.Values
	payload string
	calls   int
}

func (f *recordingJSONGetter) GetJSON(_ context.Context, path string, query url.Values, output any) error {
	recordRoute("GET", path)
	f.t.Helper()
	f.calls++
	if path != f.path {
		f.t.Errorf("got path %q, want %q", path, f.path)
	}
	if !reflect.DeepEqual(query, f.query) {
		f.t.Errorf("got query %#v, want %#v", query, f.query)
	}
	return json.Unmarshal([]byte(f.payload), output)
}

func TestGetObjectStatus(t *testing.T) {
	client := &recordingJSONGetter{
		t:     t,
		path:  "/api/object",
		query: url.Values{"path": {"prod/svc/mysql"}},
		payload: `{
			"kind": "ObjectList",
			"items": [{
				"kind": "ObjectItem",
				"meta": {"object": "prod/svc/mysql"},
				"data": {
					"avail": "down", "overall": "warn", "provisioned": "true",
					"frozen": "unfrozen", "placement_state": "non-optimal",
					"placement_policy": "nodes order", "orchestrate": "ha", "topology": "failover",
					"priority": 50, "scope": ["node-b", "node-a"],
					"updated_at": "2026-07-15T10:00:00Z", "up_instances_count": 0,
					"instances": {"node-b": {}, "node-a": {}}
				}
			}]
		}`,
	}

	result, err := New(client).GetObjectStatus(context.Background(), " prod/svc/mysql ")
	if err != nil {
		t.Fatalf("get object status: %v", err)
	}
	if result.Availability != "down" || result.InstanceCount != 2 {
		t.Fatalf("got unexpected object status %+v", result)
	}
	if !reflect.DeepEqual(result.Scope, []string{"node-a", "node-b"}) || !reflect.DeepEqual(result.InstanceNodes, []string{"node-a", "node-b"}) {
		t.Errorf("got unsorted scope or instances %+v", result)
	}
}

func TestGetObjectStatusRejectsMissingObject(t *testing.T) {
	client := &recordingJSONGetter{t: t, path: "/api/object", query: url.Values{"path": {"prod/svc/missing"}}, payload: `{"items": []}`}
	if _, err := New(client).GetObjectStatus(context.Background(), "prod/svc/missing"); err == nil {
		t.Fatal("expected missing object error")
	}
}

// An exact path holding characters of a selector is refused before the
// call: the daemon would expand it, configuration terms included.
func TestGetObjectStatusRefusesASelector(t *testing.T) {
	for _, path := range []string{"cluster+cluster.secret>=m", "prod/sec/s+data.password=x", "prod/svc/*", "prod/svc/a,prod/svc/b", "!prod/svc/a", "prod/svc/a b"} {
		client := &recordingJSONGetter{t: t, path: "/api/object"}
		_, err := New(client).GetObjectStatus(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), "characters of a selector") {
			t.Errorf("path %q: got error %v, want a refusal of a selector", path, err)
		}
		if client.calls != 0 {
			t.Errorf("path %q: got %d daemon calls, want none", path, client.calls)
		}
	}
}
