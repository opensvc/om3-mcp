package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

const instanceStatusPayload = `{
	"kind":"InstanceItem",
	"meta":{"node":"node-a","object":"prod/svc/app"},
	"data":{
		"config":{
			"csum":"test-checksum","priority":50,"scope":["node-b","node-a"],
			"updated_at":"2026-09-28T11:40:57.064043402+02:00",
			"claims":{"cpu":-1,"memory":9007199254740993},"subsets":{},
			"resources":{"app#1":{"is_disabled":false,"is_monitored":false,"is_standby":false,"restart_delay":500000000}},
			"unknown_private_field":"must-not-survive"
		},
		"monitor":{
			"global_expect":"none","global_expect_options":null,"local_expect":"none",
			"is_leader":true,"is_ha_leader":false,"orchestration_is_done":false,
			"orchestration_id":"00000000-0000-0000-0000-000000000000",
			"session_id":"00000000-0000-0000-0000-000000000000","state":"future-state","preserved":false
		},
		"status":{
			"avail":" stdby UP ","overall":"up","provisioned":"n/a","frozen_at":null,"last_started_at":null,
			"resources":{"app#1":{
				"type":"app.forking","label":"worker","status":"n/a","disable":true,"monitor":false,
				"provisioned":{"state":"n/a","mtime":null},
				"info":{"large":9007199254740993,"zero":0,"empty":"","false":false,"null":null,"nested":{"values":[false,1.25,"x"]}}
			}}
		}
	}
}`

func TestGetInstanceStatus(t *testing.T) {
	client := &recordingJSONGetter{
		t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app", query: url.Values{}, payload: instanceStatusPayload,
	}
	service := New(client)
	service.now = func() time.Time { return time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC) }

	result, err := service.GetInstanceStatus(context.Background(), GetInstanceStatusOptions{Path: " prod/svc/app ", Node: "node-a"})
	if err != nil {
		t.Fatalf("get instance status: %v", err)
	}
	if client.calls != 1 {
		t.Errorf("got %d daemon calls, want one passive read", client.calls)
	}
	if result.Node != "node-a" || result.Object.Path != "prod/svc/app" || result.Object.Namespace != "prod" || result.Object.Kind != "svc" || result.Object.Name != "app" {
		t.Fatalf("unexpected instance identity: node=%q object=%+v", result.Node, result.Object)
	}
	if result.Config == nil || result.Monitor == nil || result.Status == nil {
		t.Fatalf("missing published blocks: %+v", result)
	}
	if result.Config.Checksum != "test-checksum" || result.Config.Claims["memory"] != 9007199254740993 || result.Config.Claims["cpu"] != -1 {
		t.Errorf("unexpected checksum or compute claims: %+v", result.Config)
	}
	if !reflect.DeepEqual(result.Config.Scope, []string{"node-b", "node-a"}) || result.Status.Availability != " stdby UP " || result.Monitor.State != "future-state" {
		t.Errorf("changed placement order or exact state values: %+v", result)
	}
	configuration := result.Config.Resources["app#1"]
	resource := result.Status.Resources["app#1"]
	if configuration.RestartDelay == nil || *configuration.RestartDelay != 500000000 || configuration.IsDisabled || resource.Disable == nil || !*resource.Disable || resource.Monitor == nil || *resource.Monitor {
		t.Errorf("unexpected resource configuration or status flags: config=%+v status=%+v", configuration, resource)
	}
	if result.Status.FrozenAt != nil || result.Status.LastStartedAt != nil || resource.Provisioned.Mtime != nil || result.Monitor.OrchestrationID != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("changed nullable dates or zero UUID: %+v", result)
	}
	if result.Truncated || result.Truncations == nil || len(result.Truncations) != 0 {
		t.Errorf("unexpected truncation metadata: %+v", result)
	}
	if result.Provenance.Source != provenanceSourceOpenSVCDaemon || result.Provenance.ObservedAt != "2026-09-29T10:00:00Z" {
		t.Errorf("unexpected provenance: %+v", result.Provenance)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal instance status: %v", err)
	}
	for _, want := range []string{`"csum":"test-checksum"`, `"preserved":false`, `"large":9007199254740993`, `"zero":0`, `"empty":""`, `"false":false`, `"null":null`, `"subsets":{}`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("output missing %s: %s", want, encoded)
		}
	}
	for _, notWant := range []string{"must-not-survive", `"checksum":`, `"is_preserved":`} {
		if strings.Contains(string(encoded), notWant) {
			t.Errorf("output contains unexpected field or value %q", notWant)
		}
	}
}

func TestGetInstanceStatusAllowsUnpublishedBlocksAndNonActorConfig(t *testing.T) {
	tests := map[string]string{
		"absent": `{"config":{"csum":"test-checksum","priority":0,"scope":[],"updated_at":null}}`,
		"null":   `{"config":{"csum":"test-checksum","priority":0,"scope":[],"updated_at":null},"monitor":null,"status":null}`,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			client := &recordingJSONGetter{
				t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app", query: url.Values{},
				payload: `{"kind":"InstanceItem","meta":{"node":"node-a","object":"prod/svc/app"},"data":` + data + `}`,
			}
			result, err := New(client).GetInstanceStatus(context.Background(), GetInstanceStatusOptions{Path: "prod/svc/app", Node: "node-a"})
			if err != nil {
				t.Fatalf("get instance with unpublished blocks: %v", err)
			}
			if result.Monitor != nil || result.Status != nil || result.Config.Resources != nil || result.Config.Scope == nil {
				t.Errorf("unexpected unpublished blocks or empty scope: %+v", result)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`"monitor":null`, `"status":null`, `"scope":[]`} {
				if !strings.Contains(string(encoded), want) {
					t.Errorf("output missing %s: %s", want, encoded)
				}
			}
		})
	}
}

func TestGetInstanceStatusNormalizesNestedDates(t *testing.T) {
	client := &recordingJSONGetter{
		t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app", query: url.Values{},
		payload: `{"kind":"InstanceItem","meta":{"node":"node-a","object":"prod/svc/app"},"data":{
			"config":{"csum":"test-checksum","priority":0,"scope":[],"updated_at":"2026-09-28T11:40:57.064043402+02:00"},
			"monitor":{
				"global_expect":"none","local_expect":"none","is_leader":false,"is_ha_leader":false,
				"orchestration_id":"zero","orchestration_is_done":false,"session_id":"zero","state":"idle","preserved":false,
				"global_expect_options":{"config_updated_at":"0001-01-01T00:00:00Z"},
				"resources":{"app#1":{"restart":{"remaining":0,"last_at":"0001-01-01T00:00:00Z"}}}
			},
			"status":{
				"avail":"down","overall":"down","provisioned":"n/a","stopped_at":"0001-01-01T00:00:00Z",
				"running":[{"rid":"app#1","session_id":"zero","pid":0,"at":null}],
				"resources":{"app#1":{"type":"app.forking","label":"worker","status":"n/a",
					"provisioned":{"state":"n/a","mtime":"0001-01-01T00:00:00Z"},
					"files":[{"name":"/test","csum":"file-checksum","mtime":"0001-01-01T00:00:00Z","ingest":false}]
				}}
			}
		}}`,
	}
	result, err := New(client).GetInstanceStatus(context.Background(), GetInstanceStatusOptions{Path: "prod/svc/app", Node: "node-a"})
	if err != nil {
		t.Fatalf("get instance with nested dates: %v", err)
	}
	restart := result.Monitor.Resources["app#1"].Restart
	if restart == nil || restart.LastAt != nil || restart.Remaining == nil || *restart.Remaining != 0 {
		t.Errorf("unexpected restart facts: %+v", restart)
	}
	if len(result.Status.Running) != 1 || result.Status.Running[0].At != nil {
		t.Errorf("unexpected running action: %+v", result.Status.Running)
	}
	resource := result.Status.Resources["app#1"]
	if result.Status.StoppedAt != nil || resource.Provisioned.Mtime != nil || len(resource.Files) != 1 || resource.Files[0].Mtime != nil {
		t.Errorf("nested zero dates not normalized: %+v", result.Status)
	}
	options, err := json.Marshal(result.Monitor.GlobalExpectOptions)
	if err != nil || string(options) != `{"config_updated_at":null}` {
		t.Errorf("monitor options = %s, err=%v", options, err)
	}
	if result.Config.UpdatedAt == nil || *result.Config.UpdatedAt != "2026-09-28T11:40:57.064043402+02:00" {
		t.Errorf("changed timestamp precision or offset: %+v", result.Config)
	}
}

func TestGetInstanceStatusRejectsInvalidInputsBeforeDaemonCall(t *testing.T) {
	tests := map[string]GetInstanceStatusOptions{
		"missing path":        {Node: "node-a"},
		"blank path":          {Path: " ", Node: "node-a"},
		"long path":           {Path: strings.Repeat("x", maxObjectSelectorLength+1), Node: "node-a"},
		"empty component":     {Path: "prod//app", Node: "node-a"},
		"too many components": {Path: "prod/svc/app/extra", Node: "node-a"},
		"missing node":        {Path: "prod/svc/app"},
		"local alias":         {Path: "prod/svc/app", Node: "_"},
		"node selector":       {Path: "prod/svc/app", Node: "node*"},
		"multiple nodes":      {Path: "prod/svc/app", Node: "node-a,node-b"},
		"node path":           {Path: "prod/svc/app", Node: "../node-a"},
		"spaced node":         {Path: "prod/svc/app", Node: " node-a"},
		"long node":           {Path: "prod/svc/app", Node: strings.Repeat("n", 256)},
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			client := &recordingJSONGetter{t: t}
			if _, err := New(client).GetInstanceStatus(context.Background(), options); err == nil {
				t.Fatal("expected validation error")
			}
			if client.calls != 0 {
				t.Errorf("got %d daemon calls, want 0", client.calls)
			}
		})
	}
}

func TestGetInstanceStatusRejectsMalformedDaemonData(t *testing.T) {
	tests := map[string]string{
		"invalid JSON":          `{`,
		"null data":             `{"kind":"InstanceItem","meta":{"node":"node-a","object":"prod/svc/app"},"data":null}`,
		"missing config":        `{"kind":"InstanceItem","meta":{"node":"node-a","object":"prod/svc/app"},"data":{}}`,
		"null config":           `{"kind":"InstanceItem","meta":{"node":"node-a","object":"prod/svc/app"},"data":{"config":null}}`,
		"unexpected kind":       strings.Replace(instanceStatusPayload, `"kind":"InstanceItem"`, `"kind":"InstanceList"`, 1),
		"unexpected node":       strings.Replace(instanceStatusPayload, `"node":"node-a"`, `"node":"node-b"`, 1),
		"unexpected object":     strings.Replace(instanceStatusPayload, `"object":"prod/svc/app"`, `"object":"prod/svc/other"`, 1),
		"missing checksum":      strings.Replace(instanceStatusPayload, `"csum":"test-checksum",`, "", 1),
		"wrong scope":           strings.Replace(instanceStatusPayload, `"scope":["node-b","node-a"]`, `"scope":"node-a"`, 1),
		"null scope":            strings.Replace(instanceStatusPayload, `"scope":["node-b","node-a"]`, `"scope":null`, 1),
		"fractional integer":    strings.Replace(instanceStatusPayload, `"priority":50`, `"priority":1.5`, 1),
		"overflow integer":      strings.Replace(instanceStatusPayload, `"memory":9007199254740993`, `"memory":9223372036854775808`, 1),
		"missing monitor state": strings.Replace(instanceStatusPayload, `"state":"future-state",`, "", 1),
		"invalid date":          strings.Replace(instanceStatusPayload, `"updated_at":"2026-09-28T11:40:57.064043402+02:00"`, `"updated_at":"not-a-date"`, 1),
		"invalid options":       strings.Replace(instanceStatusPayload, `"global_expect_options":null`, `"global_expect_options":[]`, 1),
		"wrong resource flag":   strings.Replace(instanceStatusPayload, `"is_disabled":false`, `"is_disabled":"false"`, 1),
		"missing resource type": strings.Replace(instanceStatusPayload, `"type":"app.forking",`, "", 1),
		"invalid resources":     `{"kind":"InstanceItem","meta":{"node":"node-a","object":"prod/svc/app"},"data":{"config":{"csum":"test","priority":0,"scope":[],"resources":[]}}}`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			client := &recordingJSONGetter{
				t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app", query: url.Values{}, payload: payload,
			}
			if _, err := New(client).GetInstanceStatus(context.Background(), GetInstanceStatusOptions{Path: "prod/svc/app", Node: "node-a"}); err == nil {
				t.Fatal("expected malformed daemon response error")
			}
			if client.calls != 1 {
				t.Errorf("got %d daemon calls, want 1", client.calls)
			}
		})
	}
}

func TestGetInstanceStatusSortsInventoriesAndPreservesSequences(t *testing.T) {
	const firstSchedule = `{"key":"a_schedule","action":"info","schedule":"@60m","max_parallel":1,"require_collector":false,"require_provisioned":false}`
	const lastSchedule = `{"key":"z_schedule","action":"status","schedule":"@10m","max_parallel":1,"require_collector":false,"require_provisioned":false}`
	tests := []struct {
		name      string
		parents   string
		tags      string
		schedules string
	}{
		{name: "reverse", parents: `"prod/svc/z","prod/svc/a"`, tags: `"z","a","a"`, schedules: lastSchedule + "," + firstSchedule},
		{name: "sorted", parents: `"prod/svc/a","prod/svc/z"`, tags: `"a","a","z"`, schedules: firstSchedule + "," + lastSchedule},
	}
	var previous string
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &recordingJSONGetter{
				t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app", query: url.Values{},
				payload: fmt.Sprintf(`{"kind":"InstanceItem","meta":{"node":"node-a","object":"prod/svc/app"},"data":{
					"config":{"csum":"test","priority":0,"scope":["node-b","node-a"],"parents":[%s],"monitor_action":["restart","none"],"schedules":[%s]},
					"status":{"avail":"up","overall":"up","provisioned":"true","resources":{"app#1":{
						"type":"app.forking","label":"worker","status":"up","provisioned":{"state":"true"},"tags":[%s],
						"log":[{"level":"warn","message":"first"},{"level":"info","message":"second"}]
					}}}
				}}`, test.parents, test.schedules, test.tags),
			}
			service := New(client)
			service.now = func() time.Time { return time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC) }
			result, err := service.GetInstanceStatus(context.Background(), GetInstanceStatusOptions{Path: "prod/svc/app", Node: "node-a"})
			if err != nil {
				t.Fatalf("get sorted instance status: %v", err)
			}
			if !reflect.DeepEqual(result.Config.Parents, []string{"prod/svc/a", "prod/svc/z"}) || !reflect.DeepEqual(result.Status.Resources["app#1"].Tags, []string{"a", "a", "z"}) {
				t.Errorf("unexpected inventory order: %+v", result)
			}
			if len(result.Config.Schedules) != 2 || result.Config.Schedules[0].Key != "a_schedule" || result.Config.Schedules[1].Key != "z_schedule" {
				t.Errorf("unexpected schedule order: %+v", result.Config.Schedules)
			}
			if !reflect.DeepEqual(result.Config.Scope, []string{"node-b", "node-a"}) || !reflect.DeepEqual(result.Config.MonitorAction, []string{"restart", "none"}) || result.Status.Resources["app#1"].Log[0].Message != "first" {
				t.Errorf("changed placement, action or log sequence: %+v", result)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if previous != "" && previous != string(encoded) {
				t.Errorf("inventory permutation changed output")
			}
			previous = string(encoded)
		})
	}
}

func TestGetInstanceStatusBoundsMapsAndUnicodeText(t *testing.T) {
	resources := make([]string, 201)
	for n := range resources {
		resources[n] = fmt.Sprintf(`"app#%03d":{"is_disabled":false,"is_monitored":false,"is_standby":false}`, 200-n)
	}
	client := &recordingJSONGetter{
		t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app", query: url.Values{},
		payload: fmt.Sprintf(`{"kind":"InstanceItem","meta":{"node":"node-a","object":"prod/svc/app"},"data":{
			"config":{"csum":"test","priority":0,"scope":[],"resources":{%s},"labels":{"a/~":"%s"}}
		}}`, strings.Join(resources, ","), strings.Repeat("é", 4100)),
	}
	result, err := New(client).GetInstanceStatus(context.Background(), GetInstanceStatusOptions{Path: "prod/svc/app", Node: "node-a"})
	if err != nil {
		t.Fatalf("get bounded instance status: %v", err)
	}
	if len(result.Config.Resources) != 200 || !result.Truncated || len([]rune(result.Config.Labels["a/~"])) != 4096 || !strings.HasSuffix(result.Config.Labels["a/~"], "…") {
		t.Errorf("incorrect collection or Unicode text bounds: %+v", result)
	}
	if _, exists := result.Config.Resources["app#200"]; exists {
		t.Error("map truncation retained the last lexical key")
	}
	if _, exists := result.Config.Resources["app#000"]; !exists {
		t.Error("map truncation omitted the first lexical key")
	}
	want := []InstanceTruncation{
		{Path: "/config/labels/a~1~0", Kind: "text", OriginalCount: 4100, ReturnedCount: 4096},
		{Path: "/config/resources", Kind: "collection", OriginalCount: 201, ReturnedCount: 200},
	}
	if !reflect.DeepEqual(result.Truncations, want) {
		t.Errorf("truncations = %+v, want %+v", result.Truncations, want)
	}
}

func TestGetInstanceStatusBoundsStatusLogs(t *testing.T) {
	logs := make([]string, 21)
	for n := range logs {
		logs[n] = fmt.Sprintf(`{"level":"info","message":%q}`, fmt.Sprintf("message-%02d", n))
	}
	client := &recordingJSONGetter{
		t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app", query: url.Values{},
		payload: fmt.Sprintf(`{"kind":"InstanceItem","meta":{"node":"node-a","object":"prod/svc/app"},"data":{
			"config":{"csum":"test","priority":0,"scope":[]},
			"status":{"avail":"up","overall":"up","provisioned":"n/a","resources":{"app#1":{
				"type":"app.forking","label":"worker","status":"up","provisioned":{"state":"n/a"},"log":[%s]
			}}}
		}}`, strings.Join(logs, ",")),
	}
	result, err := New(client).GetInstanceStatus(context.Background(), GetInstanceStatusOptions{Path: "prod/svc/app", Node: "node-a"})
	if err != nil {
		t.Fatalf("get bounded status logs: %v", err)
	}
	got := result.Status.Resources["app#1"].Log
	if len(got) != 20 || got[0].Message != "message-00" || got[19].Message != "message-19" {
		t.Fatalf("unexpected bounded status log sequence: %+v", got)
	}
	want := []InstanceTruncation{{Path: "/status/resources/app#1/log", Kind: "collection", OriginalCount: 21, ReturnedCount: 20}}
	if !result.Truncated || !reflect.DeepEqual(result.Truncations, want) {
		t.Errorf("unexpected log truncation metadata: %+v", result.Truncations)
	}
}

func TestGetInstanceStatusBoundsEncapsulation(t *testing.T) {
	status := `{"avail":"future","overall":"future","provisioned":"n/a"}`
	for n := 0; n < 3; n++ {
		status = `{"avail":"future","overall":"future","provisioned":"n/a","encap":{"guest":` + status + `}}`
	}
	client := &recordingJSONGetter{
		t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app", query: url.Values{},
		payload: `{"kind":"InstanceItem","meta":{"node":"node-a","object":"prod/svc/app"},"data":{"config":{"csum":"test","priority":0,"scope":[]},"status":` + status + `}}`,
	}
	result, err := New(client).GetInstanceStatus(context.Background(), GetInstanceStatusOptions{Path: "prod/svc/app", Node: "node-a"})
	if err != nil {
		t.Fatalf("get bounded encapsulation: %v", err)
	}
	if !result.Truncated || len(result.Status.Encap) != 1 || len(result.Status.Encap["guest"].Encap) != 1 || len(result.Status.Encap["guest"].Encap["guest"].Encap) != 0 {
		t.Errorf("encapsulation not bounded at two levels: %+v", result.Status)
	}
	want := []InstanceTruncation{{Path: "/status/encap/guest/encap/guest/encap", Kind: "collection", OriginalCount: 1, ReturnedCount: 0}}
	if !reflect.DeepEqual(result.Truncations, want) {
		t.Errorf("truncations = %+v, want %+v", result.Truncations, want)
	}
}

func TestGetInstanceStatusRejectsJSONComplexityAndOutputBudget(t *testing.T) {
	for _, name := range []string{"depth", "nodes", "output", "truncations"} {
		t.Run(name, func(t *testing.T) {
			config := `{"csum":"test","priority":0,"scope":[]}`
			status := "null"
			wantError := "instance JSON depth or node limit exceeded"
			switch name {
			case "depth", "nodes":
				value := "false"
				if name == "depth" {
					for n := 0; n < maxInstanceStatusJSONDepth+2; n++ {
						value = `{"child":` + value + `}`
					}
				} else {
					value = `{"values":[` + strings.TrimSuffix(strings.Repeat(`[false,false],`, 100), ",") + `]}`
				}
				status = `{"avail":"up","overall":"up","provisioned":"n/a","resources":{"app#1":{"type":"app.forking","label":"worker","status":"up","provisioned":{"state":"n/a"},"info":{"value":` + value + `}}}}`
			case "output":
				labels := make([]string, 100)
				for n := range labels {
					labels[n] = fmt.Sprintf(`"label-%03d":"%s"`, n, strings.Repeat("x", 4096))
				}
				config = `{"csum":"test","priority":0,"scope":[],"labels":{` + strings.Join(labels, ",") + `}}`
				wantError = "result exceeds"
			case "truncations":
				resources := make([]string, 200)
				for n := range resources {
					resources[n] = fmt.Sprintf(`"app#%03d":{"type":"app.forking","label":"%s","status":"up","provisioned":{"state":"n/a"},"log":[{"level":"info","message":"%s"}]}`, n, strings.Repeat("x", 5000), strings.Repeat("x", 3000))
				}
				status = `{"avail":"up","overall":"up","provisioned":"n/a","resources":{` + strings.Join(resources, ",") + `}}`
				wantError = "too many instance truncations"
			}
			client := &recordingJSONGetter{
				t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app", query: url.Values{},
				payload: `{"kind":"InstanceItem","meta":{"node":"node-a","object":"prod/svc/app"},"data":{"config":` + config + `,"status":` + status + `}}`,
			}
			_, err := New(client).GetInstanceStatus(context.Background(), GetInstanceStatusOptions{Path: "prod/svc/app", Node: "node-a"})
			if err == nil || !strings.Contains(err.Error(), wantError) {
				t.Errorf("got error %v, want %q", err, wantError)
			}
		})
	}
}

// A monitor state of failure points to the logs of the instance, where its
// cause is; another state carries no note.
func TestGetInstanceStatusPointsAFailedStateToTheLogs(t *testing.T) {
	for _, test := range []struct {
		state string
		note  bool
	}{{"start failed", true}, {"provision failed", true}, {"future-state", false}, {"idle", false}} {
		payload := strings.Replace(instanceStatusPayload, `"future-state"`, `"`+test.state+`"`, 1)
		client := &recordingJSONGetter{t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app", query: url.Values{}, payload: payload}
		result, err := New(client).GetInstanceStatus(context.Background(), GetInstanceStatusOptions{Path: "prod/svc/app", Node: "node-a"})
		if err != nil {
			t.Fatalf("%s: %v", test.state, err)
		}
		if (result.FailureNote != "") != test.note || (test.note && !strings.Contains(result.FailureNote, "get_instance_logs")) {
			t.Errorf("state %q: got note %q, want one %v", test.state, result.FailureNote, test.note)
		}
	}
}
