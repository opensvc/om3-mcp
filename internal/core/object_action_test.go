package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

type objectActionClient struct {
	t        *testing.T
	path     string
	query    url.Values
	body     string
	response string
	err      error
	posts    int
}

func (c *objectActionClient) GetJSON(_ context.Context, path string, _ url.Values, _ any) error {
	recordRoute("GET", path)
	c.t.Error("an object action must not read before posting")
	return nil
}

func (c *objectActionClient) PostJSON(_ context.Context, path string, query url.Values, input any, output any) error {
	recordRoute("POST", path)
	c.posts++
	body := ""
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			c.t.Fatal(err)
		}
		body = string(encoded)
	}
	if path != c.path || query.Encode() != c.query.Encode() || body != c.body {
		c.t.Errorf("got POST %s %v %s, want %s %v %s", path, query, body, c.path, c.query, c.body)
	}
	if c.err != nil {
		return c.err
	}
	return json.Unmarshal([]byte(c.response), output)
}

func TestSubmitObjectActionQueuesAnOrchestration(t *testing.T) {
	for _, tc := range []struct {
		path   string
		action ObjectAction
		post   string
	}{
		{"prod/svc/web", ObjectActionFreeze, "/api/object/path/prod/svc/web/action/freeze"},
		{"web", ObjectActionUnfreeze, "/api/object/path/root/svc/web/action/unfreeze"},
		{"prod/vol/data", ObjectActionAbort, "/api/object/path/prod/vol/data/action/abort"},
		{"prod/cfg/settings", ObjectActionAbort, "/api/object/path/prod/cfg/settings/action/abort"},
		{"prod/svc/web", ObjectActionStart, "/api/object/path/prod/svc/web/action/start"},
		{"prod/svc/web", ObjectActionStop, "/api/object/path/prod/svc/web/action/stop"},
		{"prod/svc/web", ObjectActionRestart, "/api/object/path/prod/svc/web/action/restart"},
		{"prod/svc/web", ObjectActionGiveback, "/api/object/path/prod/svc/web/action/giveback"},
	} {
		client := &objectActionClient{t: t, path: tc.post, response: `{"orchestration_id":"30000000-0000-0000-0000-00000000000A"}`}
		result, err := New(client).SubmitObjectAction(context.Background(), tc.path, tc.action, ObjectActionOptions{})
		if err != nil {
			t.Fatalf("%s %s: %v", tc.action, tc.path, err)
		}
		if client.posts != 1 || result.Action != string(tc.action) || result.OrchestrationID != "30000000-0000-0000-0000-00000000000a" || result.Provenance.Source == "" {
			t.Fatalf("%s %s gave %+v", tc.action, tc.path, result)
		}
	}
}

func TestSubmitObjectActionRejectsInvalidTargetsBeforePosting(t *testing.T) {
	for _, tc := range []struct {
		path   string
		action ObjectAction
	}{
		{"", ObjectActionFreeze},
		{"prod/svc/*", ObjectActionFreeze},
		{"prod/svc/a,prod/svc/b", ObjectActionFreeze},
		{"prod/cfg/settings", ObjectActionFreeze},
		{"prod/sec/secret", ObjectActionUnfreeze},
		{"prod/svc/web", ObjectAction("boot")},
		{"prod/cfg/settings", ObjectActionUnprovision},
		{"prod/nscfg/prod", ObjectActionPurge},
		{"prod/svc/web", ObjectActionResize},
		{"prod/vol/data", ObjectActionStart},
		{"prod/vol/data", ObjectActionStop},
		{"prod/vol/data", ObjectActionRestart},
		{"prod/vol/data", ObjectActionSwitch},
		{"prod/vol/data", ObjectActionGiveback},
	} {
		client := &objectActionClient{t: t}
		if _, err := New(client).SubmitObjectAction(context.Background(), tc.path, tc.action, ObjectActionOptions{}); err == nil || client.posts != 0 {
			t.Fatalf("%s %q was accepted or posted", tc.action, tc.path)
		}
	}
}

func TestSubmitObjectActionReportsDaemonRefusalsAndBadAnswers(t *testing.T) {
	client := &objectActionClient{t: t, path: "/api/object/path/prod/svc/web/action/freeze", err: fmt.Errorf("OpenSVC daemon POST returned HTTP 403 Forbidden")}
	if _, err := New(client).SubmitObjectAction(context.Background(), "prod/svc/web", ObjectActionFreeze, ObjectActionOptions{}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("got %v, want the daemon refusal", err)
	}
	client = &objectActionClient{t: t, path: "/api/object/path/prod/svc/web/action/freeze", response: `{"orchestration_id":""}`}
	if _, err := New(client).SubmitObjectAction(context.Background(), "prod/svc/web", ObjectActionFreeze, ObjectActionOptions{}); err == nil {
		t.Fatal("an answer without orchestration id was accepted")
	}
}

func TestSubmitObjectActionStopCanInterruptSyncs(t *testing.T) {
	client := &objectActionClient{t: t, path: "/api/object/path/prod/svc/web/action/stop", query: url.Values{"interrupt_syncs": {"true"}}, response: `{"orchestration_id":"30000000-0000-0000-0000-00000000000a"}`}
	if _, err := New(client).SubmitObjectAction(context.Background(), "prod/svc/web", ObjectActionStop, ObjectActionOptions{InterruptSyncs: true}); err != nil || client.posts != 1 {
		t.Fatalf("stop interrupting syncs: %v, %d posts", err, client.posts)
	}
	client = &objectActionClient{t: t}
	if _, err := New(client).SubmitObjectAction(context.Background(), "prod/svc/web", ObjectActionStart, ObjectActionOptions{InterruptSyncs: true}); err == nil || client.posts != 0 {
		t.Fatal("interrupt_syncs was accepted for a start")
	}
}

func TestSubmitObjectActionSwitchSendsOneDestination(t *testing.T) {
	for _, tc := range []struct {
		options ObjectActionOptions
		body    string
	}{
		{ObjectActionOptions{}, `{"live":false}`},
		{ObjectActionOptions{To: "n2"}, `{"destination":["n2"],"live":false}`},
		{ObjectActionOptions{To: "n2", Live: true, InterruptSyncs: true}, `{"destination":["n2"],"live":true,"interrupt_syncs":true}`},
	} {
		client := &objectActionClient{t: t, path: "/api/object/path/prod/svc/web/action/switch", body: tc.body, response: `{"orchestration_id":"30000000-0000-0000-0000-00000000000a"}`}
		if _, err := New(client).SubmitObjectAction(context.Background(), "prod/svc/web", ObjectActionSwitch, tc.options); err != nil || client.posts != 1 {
			t.Fatalf("switch %+v: %v, %d posts", tc.options, err, client.posts)
		}
	}
}

func TestSubmitObjectActionRejectsMisplacedOptions(t *testing.T) {
	for _, tc := range []struct {
		action  ObjectAction
		options ObjectActionOptions
	}{
		{ObjectActionSwitch, ObjectActionOptions{To: "n1,n2"}},
		{ObjectActionSwitch, ObjectActionOptions{To: "n*"}},
		{ObjectActionSwitch, ObjectActionOptions{To: "_"}},
		{ObjectActionStop, ObjectActionOptions{To: "n2"}},
		{ObjectActionGiveback, ObjectActionOptions{Live: true}},
		{ObjectActionRestart, ObjectActionOptions{InterruptSyncs: true}},
	} {
		client := &objectActionClient{t: t}
		if _, err := New(client).SubmitObjectAction(context.Background(), "prod/svc/web", tc.action, tc.options); err == nil || client.posts != 0 {
			t.Fatalf("%s %+v was accepted or posted", tc.action, tc.options)
		}
	}
}
