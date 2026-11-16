package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// rootObjectDaemon answers as the daemon does for objects of the root
// namespace: whatever form a request used, it names an object in the
// canonical form, foo for root/svc/foo.
type rootObjectDaemon struct {
	t        *testing.T
	payloads map[string]string
	events   [][]byte
	paths    []string
	queries  []url.Values
}

func (d *rootObjectDaemon) GetJSON(_ context.Context, path string, query url.Values, output any) error {
	recordRoute("GET", path)
	d.paths = append(d.paths, path)
	d.queries = append(d.queries, query)
	payload, ok := d.payloads[path]
	if !ok {
		return fmt.Errorf("unexpected read %s", path)
	}
	return json.Unmarshal([]byte(payload), output)
}

func (d *rootObjectDaemon) GetSSE(_ context.Context, path string, query url.Values, consume func(string, string, []byte) error) error {
	recordRoute("GET", path)
	d.paths = append(d.paths, path)
	d.queries = append(d.queries, query)
	for _, event := range d.events {
		if err := consume("log", "0", event); err != nil {
			return err
		}
	}
	return nil
}

// queryPath is the path selector of the last read, empty when it had none.
func (d *rootObjectDaemon) queryPath() string {
	return d.queries[len(d.queries)-1].Get("path")
}

func TestParseClusterObjectReferenceGivesTheCanonicalPath(t *testing.T) {
	for _, tc := range []struct {
		forms     []string
		canonical string
		kind      string
	}{
		{[]string{"foo", "svc/foo", "root/svc/foo"}, "foo", "svc"},
		{[]string{"cfg/app", "root/cfg/app"}, "cfg/app", "cfg"},
		{[]string{"prod/svc/web"}, "prod/svc/web", "svc"},
		{[]string{"cluster", "ccfg/cluster", "root/ccfg/cluster"}, "cluster", "ccfg"},
		{[]string{"prod/"}, "prod/", "nscfg"},
		{[]string{"/", "root/"}, "/", "nscfg"},
	} {
		for _, form := range tc.forms {
			reference, err := parseClusterObjectReference(form)
			if err != nil || reference.Path != tc.canonical || reference.Kind != tc.kind {
				t.Errorf("%s: %+v %v, want path %s", form, reference, err, tc.canonical)
			}
		}
	}
}

// A cursor of list_cluster_objects is the path of the last object of a page,
// as the daemon gives it, the canonical form: the same before and after the
// MCP made its input paths canonical, and walking the pages gives each
// object once, in order.
func TestListClusterObjectsCursorsAreTheDaemonPaths(t *testing.T) {
	daemonPaths := []string{"test/svc/web", "foo", "cluster", "cfg/app", "system/sec/ca", "bar", "test/vol/data"}
	payload, _ := json.Marshal(daemonPaths)
	client := &rootObjectDaemon{t: t, payloads: map[string]string{"/api/object/path": string(payload)}}
	var walked []string
	cursor := ""
	for page := 0; page < 10; page++ {
		result, err := New(client).ListClusterObjects(context.Background(), ListClusterObjectsOptions{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range result.Objects {
			walked = append(walked, object.Path)
		}
		if !result.Truncated {
			break
		}
		if !strings.Contains(string(payload), `"`+result.NextCursor+`"`) {
			t.Fatalf("cursor %q is not a path the daemon gave", result.NextCursor)
		}
		cursor = result.NextCursor
	}
	if got := strings.Join(walked, " "); got != "bar cfg/app cluster foo system/sec/ca test/svc/web test/vol/data" {
		t.Fatalf("walked %s", got)
	}
}

// Each tool that compares what the daemon answers to the object asked takes
// the long form of a path of the root namespace.
func TestReadsTakeTheLongFormOfARootObjectPath(t *testing.T) {
	foo := func(payload, from string) string { return strings.ReplaceAll(payload, from, "foo") }
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		payloads map[string]string
		events   [][]byte
		// call runs the tool with the long form and gives the path of the
		// object of its result.
		call func(*Service) (string, error)
		// query is the path selector the read sends, when it sends one.
		query string
	}{
		{
			name:     "get_object_config",
			payloads: map[string]string{"/api/object/path/root/svc/foo/config": `{"kind":"KeywordList","items":[{"object":"foo","node":"","keyword":"fs#1.type","value":"flag"}]}`},
			call: func(s *Service) (string, error) {
				r, err := s.GetObjectConfig(ctx, GetObjectConfigOptions{Path: "root/svc/foo"})
				return r.Object.Path, err
			},
		},
		{
			name:     "get_object_status",
			payloads: map[string]string{"/api/object": `{"kind":"ObjectList","items":[{"kind":"ObjectItem","meta":{"object":"foo"},"data":{"avail":"up","instances":{"n1":{}}}}]}`},
			call: func(s *Service) (string, error) {
				r, err := s.GetObjectStatus(ctx, "root/svc/foo")
				return r.Object.Path, err
			},
			query: "foo",
		},
		{
			name:     "list_object_instances",
			payloads: map[string]string{"/api/instance": `{"kind":"InstanceList","items":[{"meta":{"node":"n1","object":"foo"},"data":{"status":{"avail":"up"}}}]}`},
			call: func(s *Service) (string, error) {
				r, err := s.ListObjectInstances(ctx, ListObjectInstancesOptions{Path: "root/svc/foo"})
				if err == nil && r.Total != 1 {
					err = fmt.Errorf("total %d", r.Total)
				}
				return r.Object.Path, err
			},
			query: "foo",
		},
		{
			name:     "get_instance_status",
			payloads: map[string]string{"/api/node/name/node-a/instance/path/root/svc/foo": foo(instanceStatusPayload, "prod/svc/app")},
			call: func(s *Service) (string, error) {
				r, err := s.GetInstanceStatus(ctx, GetInstanceStatusOptions{Path: "root/svc/foo", Node: "node-a"})
				return r.Object.Path, err
			},
		},
		{
			name:     "list_object_resources",
			payloads: map[string]string{"/api/resource": `{"kind":"ResourceList","items":[{"meta":{"node":"n1","object":"foo","rid":"fs#1"},"data":{"status":{"type":"fs.flag","status":"up"}}}]}`},
			call: func(s *Service) (string, error) {
				r, err := s.ListObjectResources(ctx, ListObjectResourcesOptions{Path: "root/svc/foo"})
				if err == nil && r.Total != 1 {
					err = fmt.Errorf("total %d", r.Total)
				}
				return r.Object.Path, err
			},
			query: "foo",
		},
		{
			name:     "list_schedules",
			payloads: map[string]string{"/api/object/path/root/svc/foo/schedule": foo(scheduleTestPayload, "lab/svc/app")},
			call: func(s *Service) (string, error) {
				r, err := s.ListSchedules(ctx, ListSchedulesOptions{Scope: ScheduleScopeObject, Path: "root/svc/foo"})
				if err != nil || r.Object == nil {
					return "", err
				}
				if r.Total != 3 {
					return "", fmt.Errorf("total %d", r.Total)
				}
				return r.Object.Path, nil
			},
		},
		{
			name:     "list_resource_info",
			payloads: map[string]string{"/api/object/path/root/svc/foo/resource/info": foo(resourceInfoTestPayload, "lab/svc/app")},
			call: func(s *Service) (string, error) {
				r, err := s.ListResourceInfo(ctx, ListResourceInfoOptions{Scope: ResourceInfoScopeObject, Path: "root/svc/foo"})
				if err != nil {
					return "", err
				}
				if r.Total != 3 {
					return "", fmt.Errorf("total %d", r.Total)
				}
				return r.Object.Path, nil
			},
		},
		{
			name:   "get_instance_logs",
			events: [][]byte{rootInstanceLogEvent(t)},
			call: func(s *Service) (string, error) {
				r, err := s.GetInstanceLogs(ctx, GetInstanceLogsOptions{Path: "root/svc/foo", Node: "node-a", Lines: 1})
				if err == nil && r.Count != 1 {
					err = fmt.Errorf("count %d", r.Count)
				}
				return r.Object.Path, err
			},
		},
		{
			name:     "list_cluster_ip_resources",
			payloads: map[string]string{"/api/resource": foo(clusterIPResourcePayload, "prod/svc/app")},
			call: func(s *Service) (string, error) {
				r, err := s.ListClusterIPResources(ctx, ListClusterIPResourcesOptions{Path: "root/svc/foo"})
				if err != nil {
					return "", err
				}
				if r.Total != 1 {
					return "", fmt.Errorf("total %d", r.Total)
				}
				return r.Resources[0].Object.Path, nil
			},
			query: "foo",
		},
	} {
		client := &rootObjectDaemon{t: t, payloads: tc.payloads, events: tc.events}
		path, err := tc.call(New(client))
		if err != nil || path != "foo" {
			t.Errorf("%s: object %q, %v", tc.name, path, err)
			continue
		}
		if tc.query != "" && client.queryPath() != tc.query {
			t.Errorf("%s: sent path %q, want %q", tc.name, client.queryPath(), tc.query)
		}
	}
}

// rootInstanceLogEvent is a journal entry of foo, named canonically.
func rootInstanceLogEvent(t *testing.T) []byte {
	t.Helper()
	nested, _ := json.Marshal(daemonInstanceLogPayload{Timestamp: "2026-10-23T10:00:00Z", Level: "INFO", Message: "started", Node: "node-a", Object: "foo"})
	envelope, _ := json.Marshal(daemonInstanceLogEnvelope{JSON: string(nested), Message: "started", Node: "node-a", Object: "foo"})
	return envelope
}
