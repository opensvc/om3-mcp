package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// lifecycleClient answers the reads the lifecycle actions make, the instances
// before a delete and the keywords after an enable, and records the POST.
type lifecycleClient struct {
	instances string
	keywords  string
	// types are the <group>.type records the read of the resources answers.
	types string
	reads []url.Values
	path  string
	query url.Values
	body  string
	posts int
	err   error
}

func (c *lifecycleClient) GetJSON(_ context.Context, path string, query url.Values, output any) error {
	recordRoute("GET", path)
	c.reads = append(c.reads, query)
	if path == "/api/instance" {
		return json.Unmarshal([]byte(`{"items":[`+c.instances+`]}`), output)
	}
	if kw := query["kw"]; len(kw) > 0 && strings.HasSuffix(kw[0], ".type") && !strings.Contains(kw[0], "#") {
		return json.Unmarshal([]byte(`{"kind":"KeywordList","items":[`+c.types+`]}`), output)
	}
	return json.Unmarshal([]byte(`{"kind":"KeywordList","items":[`+c.keywords+`]}`), output)
}

func (c *lifecycleClient) record(path string, query url.Values, input any) {
	c.posts++
	c.path, c.query = path, query
	c.body = ""
	if input != nil {
		data, _ := json.Marshal(input)
		c.body = string(data)
	}
}

func (c *lifecycleClient) PostJSON(_ context.Context, path string, query url.Values, input, output any) error {
	recordRoute("POST", path)
	c.record(path, query, input)
	if c.err != nil {
		return c.err
	}
	if output == nil {
		return nil
	}
	return json.Unmarshal([]byte(`{"orchestration_id":"30000000-0000-0000-0000-00000000000b"}`), output)
}

func (c *lifecycleClient) PostJSONWithHeader(ctx context.Context, path string, query url.Values, input, output any) (http.Header, error) {
	recordRoute("POST", path)
	return http.Header{"Om-Last-Modified": {"2026-10-22T12:00:00Z"}}, c.PostJSON(ctx, path, query, input, output)
}

func instanceRecord(object, node, avail, provisioned string) string {
	return fmt.Sprintf(`{"meta":{"node":%q,"object":%q},"data":{"status":{"avail":%q,"provisioned":%q}}}`, node, object, avail, provisioned)
}

func TestLifecycleActionsSendTheirOptions(t *testing.T) {
	for _, tc := range []struct {
		path    string
		action  ObjectAction
		options ObjectActionOptions
		post    string
		query   string
		body    string
	}{
		{"test/svc/web", ObjectActionProvision, ObjectActionOptions{}, "/api/object/path/test/svc/web/action/provision", "", ""},
		{"test/vol/data", ObjectActionProvision, ObjectActionOptions{Adopt: true}, "/api/object/path/test/vol/data/action/provision", "adopt=true", ""},
		{"test/vol/data", ObjectActionResize, ObjectActionOptions{Size: "+1g"}, "/api/object/path/test/vol/data/action/resize", "", `{"size":"+1g"}`},
		{"test/vol/data", ObjectActionResize, ObjectActionOptions{ConfigUpdatedAt: "2026-10-22T12:00:00.123+02:00"}, "/api/object/path/test/vol/data/action/resize", "config_updated_at=2026-10-22T12%3A00%3A00.123%2B02%3A00", `{}`},
		{"test/svc/web", ObjectActionCap, ObjectActionOptions{Sets: []string{"pg_cpus=0-1", "app#1.pg_mem_limit@n1=512m"}}, "/api/object/path/test/svc/web/action/cap", "set=pg_cpus%3D0-1&set=app%231.pg_mem_limit%40n1%3D512m", ""},
		{"test/svc/web", ObjectActionCap, ObjectActionOptions{}, "/api/object/path/test/svc/web/action/cap", "", ""},
	} {
		client := &lifecycleClient{}
		result, err := New(client).SubmitObjectAction(context.Background(), tc.path, tc.action, tc.options)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.action, tc.path, err)
		}
		if client.path != tc.post || client.query.Encode() != tc.query || client.body != tc.body || result.OrchestrationID == "" {
			t.Errorf("%s: POST %s %q %s, result %+v", tc.action, client.path, client.query.Encode(), client.body, result)
		}
		if (tc.action == ObjectActionCap) != (result.LastModified == "2026-10-22T12:00:00Z") {
			t.Errorf("%s: last_modified %q", tc.action, result.LastModified)
		}
	}
}

func TestLifecycleActionsRefuseTheirOptionsElsewhere(t *testing.T) {
	for name, tc := range map[string]struct {
		path    string
		action  ObjectAction
		options ObjectActionOptions
	}{
		"resize of a svc":          {"test/svc/web", ObjectActionResize, ObjectActionOptions{Size: "1g"}},
		"provision of a cfg":       {"test/cfg/web", ObjectActionProvision, ObjectActionOptions{}},
		"delete of a nscfg":        {"test/nscfg/test", ObjectActionDelete, ObjectActionOptions{}},
		"adopt on a start":         {"test/svc/web", ObjectActionStart, ObjectActionOptions{Adopt: true}},
		"size on a cap":            {"test/svc/web", ObjectActionCap, ObjectActionOptions{Size: "1g"}},
		"sets on a provision":      {"test/svc/web", ObjectActionProvision, ObjectActionOptions{Sets: []string{"pg_cpus=1"}}},
		"config time on provision": {"test/svc/web", ObjectActionProvision, ObjectActionOptions{ConfigUpdatedAt: "2026-10-22T12:00:00Z"}},
		"config time on start":     {"test/svc/web", ObjectActionStart, ObjectActionOptions{ConfigUpdatedAt: "2026-10-22T12:00:00Z"}},
		"config time not RFC3339":  {"test/vol/data", ObjectActionResize, ObjectActionOptions{ConfigUpdatedAt: "yesterday"}},
		"size not a size":          {"test/vol/data", ObjectActionResize, ObjectActionOptions{Size: "big"}},
		"size negative":            {"test/vol/data", ObjectActionResize, ObjectActionOptions{Size: "-1g"}},
		"cap without pg_":          {"test/svc/web", ObjectActionCap, ObjectActionOptions{Sets: []string{"nodes=n1"}}},
		"cap with append":          {"test/svc/web", ObjectActionCap, ObjectActionOptions{Sets: []string{"pg_cpus+=1"}}},
		"cap without value":        {"test/svc/web", ObjectActionCap, ObjectActionOptions{Sets: []string{"pg_cpus"}}},
	} {
		client := &lifecycleClient{}
		if _, err := New(client).SubmitObjectAction(context.Background(), tc.path, tc.action, tc.options); err == nil || client.posts != 0 {
			t.Errorf("%s: %v, posts %d", name, err, client.posts)
		}
	}
}

// The daemon deletes an object whatever it runs or holds: the MCP refuses
// one an instance of which is not down, or, for a svc or a vol, provisioned.
func TestDeleteObjectRefusesWhatWouldLeak(t *testing.T) {
	for name, tc := range map[string]struct {
		path      string
		instances []string
		want      string
	}{
		"up":          {"test/svc/web", []string{instanceRecord("test/svc/web", "n1", "down", "false"), instanceRecord("test/svc/web", "n2", "up", "true")}, "on n2 is up"},
		"warn":        {"test/svc/web", []string{instanceRecord("test/svc/web", "n1", "warn", "false")}, "on n1 is warn"},
		"provisioned": {"test/svc/web", []string{instanceRecord("test/svc/web", "n1", "down", "true")}, "on n1 is provisioned"},
		"mixed":       {"test/vol/data", []string{instanceRecord("test/vol/data", "n1", "down", "mixed")}, "provisioned"},
	} {
		client := &lifecycleClient{instances: strings.Join(tc.instances, ",")}
		if _, err := New(client).SubmitObjectAction(context.Background(), tc.path, ObjectActionDelete, ObjectActionOptions{}); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "then purge it, purge_object") || client.posts != 0 {
			t.Errorf("%s: %v, posts %d", name, err, client.posts)
		}
	}
	for name, tc := range map[string]struct {
		path      string
		instances []string
	}{
		"stopped, unprovisioned": {"test/svc/web", []string{instanceRecord("test/svc/web", "n1", "down", "false"), instanceRecord("test/svc/web", "n2", "n/a", "false")}},
		"a cfg":                  {"test/cfg/web", []string{instanceRecord("test/cfg/web", "n1", "n/a", "true")}},
		"no instance":            {"test/svc/web", nil},
	} {
		client := &lifecycleClient{instances: strings.Join(tc.instances, ",")}
		if _, err := New(client).SubmitObjectAction(context.Background(), tc.path, ObjectActionDelete, ObjectActionOptions{}); err != nil || client.posts != 1 || !strings.HasSuffix(client.path, "/action/delete") {
			t.Errorf("%s: %v, posts %d %s", name, err, client.posts, client.path)
		}
	}
}

// The daemon unprovisions an instance up, which it stops, then destroys its
// data: the MCP refuses an unprovision or a purge while one is up or warn.
func TestUnprovisionAndPurgeRefuseAnInstanceRunning(t *testing.T) {
	for _, action := range []ObjectAction{ObjectActionUnprovision, ObjectActionPurge} {
		for name, instances := range map[string][]string{
			"up":   {instanceRecord("test/svc/web", "n1", "down", "true"), instanceRecord("test/svc/web", "n2", "up", "true")},
			"warn": {instanceRecord("test/svc/web", "n1", "warn", "true")},
		} {
			client := &lifecycleClient{instances: strings.Join(instances, ",")}
			if _, err := New(client).SubmitObjectAction(context.Background(), "test/svc/web", action, ObjectActionOptions{}); err == nil || !strings.Contains(err.Error(), "stop it first, stop_object") || client.posts != 0 {
				t.Errorf("%s %s: %v, posts %d", action, name, err, client.posts)
			}
		}
		// A provisioned object down, or one in n/a, is submitted, without a
		// guard on the children: the daemon waits for them, visibly.
		for name, tc := range map[string]struct {
			path, post string
			instances  []string
		}{
			"down, provisioned": {"test/svc/web", "/api/object/path/test/svc/web/action/", []string{instanceRecord("test/svc/web", "n1", "down", "true"), instanceRecord("test/svc/web", "n2", "down", "true")}},
			"a vol in n/a":      {"test/vol/data", "/api/object/path/test/vol/data/action/", []string{instanceRecord("test/vol/data", "n1", "n/a", "true")}},
		} {
			client := &lifecycleClient{instances: strings.Join(tc.instances, ",")}
			result, err := New(client).SubmitObjectAction(context.Background(), tc.path, action, ObjectActionOptions{})
			if err != nil || client.posts != 1 || client.path != tc.post+string(action) || client.body != "" || len(client.query) != 0 || result.OrchestrationID == "" {
				t.Errorf("%s %s: %v, POST %s %v %q", action, name, err, client.path, client.query, client.body)
			}
		}
	}
	// The purge of a cfg, a sec or a usr is a delete; an unprovision of one
	// is refused for its kind.
	for _, path := range []string{"test/cfg/web", "test/sec/web", "test/usr/web"} {
		client := &lifecycleClient{}
		if _, err := New(client).SubmitObjectAction(context.Background(), path, ObjectActionPurge, ObjectActionOptions{}); err == nil || !strings.Contains(err.Error(), "delete_object") || client.posts != 0 {
			t.Errorf("purge %s: %v", path, err)
		}
		if _, err := New(client).SubmitObjectAction(context.Background(), path, ObjectActionUnprovision, ObjectActionOptions{}); err == nil || !strings.Contains(err.Error(), "svc, vol objects only") || client.posts != 0 {
			t.Errorf("unprovision %s: %v", path, err)
		}
	}
	for _, options := range []ObjectActionOptions{{Adopt: true}, {Size: "1g"}, {ConfigUpdatedAt: "2026-10-22T12:00:00Z"}, {InterruptSyncs: true}} {
		client := &lifecycleClient{}
		if _, err := New(client).SubmitObjectAction(context.Background(), "test/svc/web", ObjectActionUnprovision, options); err == nil || client.posts != 0 {
			t.Errorf("unprovision with %+v accepted", options)
		}
	}
}

func TestSetObjectEnabledWritesAndReadsTheDisableKeywords(t *testing.T) {
	types := `{"object":"test/svc/web","keyword":"app#1.type","value":"forking","node":""},{"object":"test/svc/web","keyword":"app#2.type","value":"forking","node":""},{"object":"test/svc/web","keyword":"fs#1.type","value":"flag","node":""}`
	client := &lifecycleClient{types: types, keywords: `{"object":"test/svc/web","keyword":"fs#1.disable","value":"true","node":""}`}
	result, err := New(client).SetObjectEnabled(context.Background(), "test/svc/web", false, []string{"fs#1", "app#2"})
	if err != nil {
		t.Fatal(err)
	}
	if client.path != "/api/object/path/test/svc/web/disable" || client.query.Get("rid") != "fs#1,app#2" || client.body != "" ||
		result.Action != "disable" || len(result.Keywords) != 1 || result.Keywords[0].Value != "true" {
		t.Fatalf("POST %s %v, result %+v", client.path, client.query, result)
	}
	if got := strings.Join(client.reads[len(client.reads)-1]["kw"], ","); got != "app#2.disable,fs#1.disable" {
		t.Fatalf("read again %s", got)
	}
	client = &lifecycleClient{}
	result, err = New(client).SetObjectEnabled(context.Background(), "test/svc/web", true, nil)
	if err != nil || client.path != "/api/object/path/test/svc/web/enable" || client.query.Has("rid") || result.Action != "enable" ||
		strings.Join(client.reads[0]["kw"], ",") != "disable" {
		t.Fatalf("enable: %v %s %v", err, client.path, client.query)
	}
	for name, tc := range map[string]struct {
		path      string
		resources []string
	}{
		"a vol":        {"test/vol/data", nil},
		"a selector":   {"test/svc/web", []string{"fs"}},
		"a list":       {"test/svc/web", []string{"fs#1,app#1"}},
		"a wildcard":   {"test/svc/*", nil},
		"a dependency": {"test/svc/web", []string{"fs#1+"}},
		"too many":     {"test/svc/web", strings.Fields(strings.Repeat("fs#1 ", 51))},
	} {
		client := &lifecycleClient{}
		if _, err := New(client).SetObjectEnabled(context.Background(), tc.path, false, tc.resources); err == nil || client.posts != 0 {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The daemon answers 204 for an id it has not, writing nothing: an id
	// that is not a resource of the service is refused before the call.
	client = &lifecycleClient{types: types}
	if _, err := New(client).SetObjectEnabled(context.Background(), "test/svc/web", false, []string{"fs#1", "fs#9"}); err == nil ||
		!strings.Contains(err.Error(), "fs#9 is not a resource of the service, which has app#1, app#2, fs#1") || client.posts != 0 {
		t.Fatalf("unknown resource: %v, posts %d", err, client.posts)
	}
	if got := strings.Join(client.reads[0]["kw"], ","); got != "fs.type" {
		t.Fatalf("resources read with %s", got)
	}
	// The refusal of the daemon is given as it says it.
	client = &lifecycleClient{types: types, err: daemonProblemError{http.StatusForbidden, "denied: the resource is up: disable fs#1: up on n2: stop it before"}}
	if _, err := New(client).SetObjectEnabled(context.Background(), "test/svc/web", false, []string{"fs#1"}); err == nil || !strings.Contains(err.Error(), "stop it before") {
		t.Fatalf("refusal: %v", err)
	}
}
