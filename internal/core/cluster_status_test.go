package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGetClusterStatusPreservesFactsAndExactValues(t *testing.T) {
	service := New(&fakeJSONGetter{t: t, payload: `{
		"cluster": {
			"config": {"id":"cluster-123","name":"prod","nodes":["node-a","node-b"],"quorum":true,"issues":["raw cluster issue"]},
			"status": {"is_compat":false,"is_frozen":true},
			"node": {
				"node-a": {
					"config":{"min_avail_mem_pct":2,"min_avail_swap_pct":10,"issues":["raw node issue"]},
					"stats":{"load_15m":0.4,"mem_avail":74,"mem_total":3902,"score":67,"swap_avail":0,"swap_total":0},
					"status":{"agent":"v3-dev","api":1,"compat":2,"is_leader":true,"is_overloaded":true,
						"booted_at":"2026-09-10T08:49:40Z","frozen_at":"0001-01-01T00:00:00Z",
						"left_at":"2026-09-23T09:12:42Z","rejoined_at":"2026-09-23T09:12:44Z",
						"gen":{"node-z":9,"node-a":7},"arbitrators":{"arb-b":{"url":"https://arb-b","status":"mystery","weight":2}}},
					"monitor":{"state":"custom-state","state_updated_at":"2026-09-23T09:12:44Z","global_expect":"none",
						"global_expect_updated_at":"0001-01-01T00:00:00Z","local_expect":"custom-expect",
						"local_expect_updated_at":"2026-09-23T09:12:43Z","orchestration_id":"orch-1",
						"orchestration_is_done":false,"session_id":"session-1","updated_at":"2026-09-23T09:12:44Z"},
					"instance":{"prod/svc/api":{"monitor":{"state":"idle"}},"prod/svc/db":{"monitor":{"state":"starting","global_expect":"started"}}},
					"daemon":{"pid":42,"started_at":"2026-09-23T09:12:40Z","heartbeat":{
						"updated_at":"2026-09-23T09:15:00Z","last_message":{"from":"node-a","patch_length":3,"type":"patch"},
						"last_messages":[{"from":"node-b","patch_length":4,"type":"full"}],"secret_version":{"main":5,"alt":4},
						"streams":[{"id":"hb#1.rx","type":"unicast","state":"unexpected-state","configured_at":"c","created_at":"r","updated_at":"u",
							"alerts":[{"severity":"notice","message":"raw alert"}],"peers":{"node-b":{"desc":"from node-b","is_beating":false,"changed_at":"x","last_beating_at":"y"}}}]
					}}
				},
				"node-c": {"status":{"agent":"v3-other"},"monitor":{"state":"idle"},"daemon":{"pid":43},"instance":{
					"prod/svc/api":{"monitor":{"state":"provision failed","state_updated_at":"2026-09-23T09:13:00Z","global_expect":"none","state_reason":"raw reason"}},
					"prod/svc/db":{"monitor":{"state":"idle"}},
					"prod/cfg/app":{"monitor":{"state":"custom-cfg-state"}},
					"prod/svc/gone":{"config":{}}
				}}
			},
			"object": {
				"cluster":{"scope":["node-a","node-b"]},
				"prod/svc/api":{"avail":"UP-CUSTOM","overall":"odd","provisioned":"maybe","frozen":"cold","placement_state":"elsewhere",
					"placement_policy":"nodes order","orchestrate":"ha","topology":"failover","priority":50,"up_instances_count":1,
					"scope":["node-b","node-a"],"updated_at":"2026-09-23T09:14:00Z"},
				"prod/svc/db":{"avail":"down","overall":"down","provisioned":"false","frozen":"unfrozen","placement_state":"non-optimal"}
			}
		},
		"daemon": {"nodename": "node-b"}
	}`})
	service.now = func() time.Time { return time.Date(2026, 9, 23, 9, 16, 0, 0, time.UTC) }

	result, err := service.GetClusterStatus(context.Background(), GetClusterStatusOptions{NodeLimit: 2, ObjectLimit: 1})
	if err != nil {
		t.Fatalf("get cluster status: %v", err)
	}
	if result.Cluster.ID != "cluster-123" || !result.Cluster.QuorumEnabled || result.Cluster.IsCompatible || !result.Cluster.IsFrozen {
		t.Errorf("cluster facts = %+v", result.Cluster)
	}
	if result.Nodes.ConfiguredTotal != 2 || result.Nodes.ReportedTotal != 2 || result.Nodes.Total != 3 || result.Nodes.Count != 2 || !result.Nodes.Truncated || result.Nodes.NextCursor != "node-b" {
		t.Fatalf("node page = %+v", result.Nodes)
	}
	nodeA := result.Nodes.Items[0]
	if nodeA.Name != "node-a" || !nodeA.Configured || !nodeA.Reported || nodeA.Status == nil || !nodeA.Status.IsOverloaded {
		t.Fatalf("node-a facts = %+v", nodeA)
	}
	if nodeA.Status.Generations.Items[0].Node != "node-a" || nodeA.Status.Generations.Items[1].Value != 9 {
		t.Errorf("generations = %+v", nodeA.Status.Generations)
	}
	if nodeA.Monitor.State != "custom-state" || nodeA.Monitor.LocalExpect != "custom-expect" || nodeA.Policy.MinAvailSwapPct != 10 || nodeA.Stats.SwapTotalMB != 0 {
		t.Errorf("node facts were changed: %+v", nodeA)
	}
	if nodeA.Heartbeat.Streams.Items[0].State != "unexpected-state" || nodeA.Heartbeat.Streams.Items[0].Peers.Items[0].IsBeating {
		t.Errorf("heartbeat facts were changed: %+v", nodeA.Heartbeat)
	}
	if result.Nodes.Items[1].Name != "node-b" || !result.Nodes.Items[1].Configured || result.Nodes.Items[1].Reported || result.Nodes.Items[1].Status != nil {
		t.Errorf("configured node without status = %+v", result.Nodes.Items[1])
	}
	if result.Objects.ReportedTotal != 3 || result.Objects.ActorTotal != 2 || result.Objects.Count != 1 || !result.Objects.Truncated || result.Objects.NextCursor != "prod/svc/api" {
		t.Fatalf("object page = %+v", result.Objects)
	}
	object := result.Objects.Items[0]
	if object.Availability != "UP-CUSTOM" || object.Overall != "odd" || object.Scope[0] != "node-a" {
		t.Errorf("object facts were changed: %+v", object)
	}
	if !hasExactValueCount(result.Objects.StateCounts.Availability, "UP-CUSTOM", 1) || !hasExactValueCount(result.Objects.StateCounts.PlacementState, "elsewhere", 1) {
		t.Errorf("exact state counts = %+v", result.Objects.StateCounts)
	}
	if len(object.InstanceMonitors) != 1 || object.InstanceMonitors[0] != (ClusterStatusInstanceMonitor{Node: "node-c", State: "provision failed", StateUpdatedAt: "2026-09-23T09:13:00Z", StateReason: "raw reason", GlobalExpect: "none"}) || object.InstanceMonitorsTotal != 1 || object.InstanceMonitorsTruncated || result.Objects.InstanceMonitorsTruncated {
		t.Errorf("instance monitors not idle = %+v (total %d, truncated %v, page truncated %v)", object.InstanceMonitors, object.InstanceMonitorsTotal, object.InstanceMonitorsTruncated, result.Objects.InstanceMonitorsTruncated)
	}
	// Instances of actor objects only, idle included; a non-actor or an
	// instance without monitor is not counted.
	counts := result.Objects.StateCounts.InstanceMonitorState
	if len(counts) != 3 || !hasExactValueCount(counts, "idle", 2) || !hasExactValueCount(counts, "provision failed", 1) || !hasExactValueCount(counts, "starting", 1) {
		t.Errorf("instance monitor state counts = %+v", counts)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal cluster status: %v", err)
	}
	for _, forbidden := range []string{`"healthy"`, `"remediation"`, `"swap_below_threshold"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("factual result contains deterministic diagnosis %q: %s", forbidden, encoded)
		}
	}
	if result.Provenance.Source != provenanceSourceOpenSVCDaemon || result.Provenance.ObservedAt != "2026-09-23T09:16:00Z" || result.Provenance.ServedBy != "node-b" {
		t.Errorf("provenance = %+v", result.Provenance)
	}

	next, err := service.GetClusterStatus(context.Background(), GetClusterStatusOptions{NodeLimit: 2, NodeCursor: result.Nodes.NextCursor, ObjectLimit: 1, ObjectCursor: result.Objects.NextCursor})
	if err != nil {
		t.Fatalf("get next cluster status page: %v", err)
	}
	if next.Nodes.Count != 1 || next.Nodes.Items[0].Name != "node-c" || next.Nodes.Items[0].Configured || !next.Nodes.Items[0].Reported || next.Nodes.Truncated {
		t.Errorf("next node page = %+v", next.Nodes)
	}
	if next.Objects.Count != 1 || next.Objects.Items[0].Path != "prod/svc/db" || next.Objects.Truncated {
		t.Errorf("next object page = %+v", next.Objects)
	}
	if monitors := next.Objects.Items[0].InstanceMonitors; len(monitors) != 1 || monitors[0].Node != "node-a" || monitors[0].State != "starting" || monitors[0].GlobalExpect != "started" {
		t.Errorf("next object instance monitors = %+v", monitors)
	} else if encoded, err := json.Marshal(monitors[0]); err != nil || strings.Contains(string(encoded), "state_reason") {
		t.Errorf("monitor without a reason carries state_reason: %s", encoded)
	}
}

func TestGetClusterStatusRejectsInvalidBoundsBeforeDaemonCall(t *testing.T) {
	service := New(&fakeJSONGetter{t: t, payload: `{}`})
	tests := []GetClusterStatusOptions{
		{NodeLimit: -1},
		{NodeLimit: maxClusterStatusPageLimit + 1},
		{ObjectLimit: -1},
		{ObjectLimit: maxClusterStatusPageLimit + 1},
		{NodeCursor: strings.Repeat("n", maxClusterStatusCursorLength+1)},
		{ObjectCursor: strings.Repeat("o", maxClusterStatusCursorLength+1)},
	}
	for _, options := range tests {
		if _, err := service.GetClusterStatus(context.Background(), options); err == nil {
			t.Errorf("options %+v: expected validation error", options)
		}
	}
}

func TestClusterStatusNestedCollectionsAreBounded(t *testing.T) {
	heartbeat := &clusterHeartbeat{
		LastMessages: make([]clusterHeartbeatLastMessage, maxHeartbeatMessages+1),
		Streams:      make([]clusterHeartbeatStream, maxHeartbeatStreams+1),
	}
	for i := range heartbeat.Streams {
		heartbeat.Streams[i].ID = fmt.Sprintf("hb#%03d.rx", i)
	}
	heartbeat.Streams[0].Alerts = make([]clusterHeartbeatAlert, maxHeartbeatAlerts+1)
	heartbeat.Streams[0].Alerts[0].Message = strings.Repeat("é", maxHeartbeatTextRunes+1)
	heartbeat.Streams[0].Peers = make(map[string]clusterHeartbeatPeer, maxHeartbeatPeers+1)
	for i := 0; i <= maxHeartbeatPeers; i++ {
		heartbeat.Streams[0].Peers[fmt.Sprintf("node-%03d", i)] = clusterHeartbeatPeer{}
	}

	result := heartbeatFacts(heartbeat)
	if !result.LastMessages.Truncated || result.LastMessages.Count != maxHeartbeatMessages {
		t.Errorf("last messages bounds = %+v", result.LastMessages)
	}
	if !result.Streams.Truncated || result.Streams.Count != maxHeartbeatStreams {
		t.Errorf("stream bounds = %+v", result.Streams)
	}
	stream := result.Streams.Items[0]
	if !stream.Alerts.Truncated || stream.Alerts.Count != maxHeartbeatAlerts || !stream.Alerts.Items[0].MessageTruncated || len([]rune(stream.Alerts.Items[0].Message)) != maxHeartbeatTextRunes {
		t.Errorf("alert bounds = %+v", stream.Alerts)
	}
	if !stream.Peers.Truncated || stream.Peers.Count != maxHeartbeatPeers {
		t.Errorf("peer bounds = %+v", stream.Peers)
	}
}

func hasExactValueCount(values []ClusterStatusValueCount, value string, count int) bool {
	for _, item := range values {
		if item.Value == value && item.Count == count {
			return true
		}
	}
	return false
}

func TestClusterStatusOmitsIdleInstanceMonitors(t *testing.T) {
	service := New(&fakeJSONGetter{t: t, payload: `{"cluster": {
		"node": {"node-a": {"instance": {"prod/svc/idle": {"monitor": {"state": "idle"}}, "prod/svc/none": {"config": {}}}}},
		"object": {"prod/svc/idle": {"avail": "up"}, "prod/svc/none": {"avail": "up"}}
	}}`})
	result, err := service.GetClusterStatus(context.Background(), GetClusterStatusOptions{})
	if err != nil {
		t.Fatalf("get cluster status: %v", err)
	}
	encoded, err := json.Marshal(result.Objects.Items)
	if err != nil {
		t.Fatalf("marshal objects: %v", err)
	}
	for _, key := range []string{`"instance_monitors"`, `"instance_monitors_total"`, `"instance_monitors_truncated"`} {
		if strings.Contains(string(encoded), key) {
			t.Errorf("objects without a monitor other than idle carry %s: %s", key, encoded)
		}
	}
	if counts := result.Objects.StateCounts.InstanceMonitorState; len(counts) != 1 || !hasExactValueCount(counts, "idle", 1) {
		t.Errorf("instance monitor state counts = %+v", counts)
	}
}

func TestClusterStatusInstanceMonitorsAreBounded(t *testing.T) {
	nodes := make(map[string]any, maxClusterStatusInstanceMonitors+1)
	for i := 0; i <= maxClusterStatusInstanceMonitors; i++ {
		nodes[fmt.Sprintf("node-%03d", i)] = map[string]any{"instance": map[string]any{
			"prod/svc/api": map[string]any{"monitor": map[string]any{"state": "start failed", "state_reason": strings.Repeat("é", maxClusterStatusStateReasonRunes+1)}},
		}}
	}
	payload, err := json.Marshal(map[string]any{"cluster": map[string]any{
		"node":   nodes,
		"object": map[string]any{"prod/svc/api": map[string]any{"avail": "down"}},
	}})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	service := New(&fakeJSONGetter{t: t, payload: string(payload)})
	result, err := service.GetClusterStatus(context.Background(), GetClusterStatusOptions{})
	if err != nil {
		t.Fatalf("get cluster status: %v", err)
	}
	object := result.Objects.Items[0]
	if len(object.InstanceMonitors) != maxClusterStatusInstanceMonitors || object.InstanceMonitorsTotal != maxClusterStatusInstanceMonitors+1 || !object.InstanceMonitorsTruncated || object.InstanceMonitors[0].Node != "node-000" || result.Objects.InstanceMonitorsTruncated {
		t.Errorf("instance monitors bounds: count %d, total %d, truncated %v, first %q, page truncated %v", len(object.InstanceMonitors), object.InstanceMonitorsTotal, object.InstanceMonitorsTruncated, object.InstanceMonitors[0].Node, result.Objects.InstanceMonitorsTruncated)
	}
	if first := object.InstanceMonitors[0]; !first.StateReasonTruncated || len([]rune(first.StateReason)) != maxClusterStatusStateReasonRunes {
		t.Errorf("state reason bound: %d runes, truncated %v", len([]rune(first.StateReason)), first.StateReasonTruncated)
	}
	if !hasExactValueCount(result.Objects.StateCounts.InstanceMonitorState, "start failed", maxClusterStatusInstanceMonitors+1) {
		t.Errorf("instance monitor state counts = %+v", result.Objects.StateCounts.InstanceMonitorState)
	}
}

func TestClusterStatusInstanceMonitorsAreBoundedPerPage(t *testing.T) {
	// Five objects of 50 instances in failure: 250 entries for a page of 200.
	const objects = 5
	nodes := make(map[string]any, maxClusterStatusInstanceMonitors)
	actors := make(map[string]any, objects)
	for o := 0; o < objects; o++ {
		actors[fmt.Sprintf("prod/svc/s%d", o)] = map[string]any{"avail": "down"}
	}
	for i := 0; i < maxClusterStatusInstanceMonitors; i++ {
		instances := make(map[string]any, objects)
		for path := range actors {
			instances[path] = map[string]any{"monitor": map[string]any{"state": "start failed"}}
		}
		nodes[fmt.Sprintf("node-%03d", i)] = map[string]any{"instance": instances}
	}
	payload, err := json.Marshal(map[string]any{"cluster": map[string]any{"node": nodes, "object": actors}})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	service := New(&fakeJSONGetter{t: t, payload: string(payload)})
	result, err := service.GetClusterStatus(context.Background(), GetClusterStatusOptions{})
	if err != nil {
		t.Fatalf("get cluster status: %v", err)
	}
	if !result.Objects.InstanceMonitorsTruncated {
		t.Errorf("page bound not reported")
	}
	listed := 0
	for i, object := range result.Objects.Items {
		listed += len(object.InstanceMonitors)
		full := i < maxClusterStatusPageInstanceMonitors/maxClusterStatusInstanceMonitors
		if object.InstanceMonitorsTotal != maxClusterStatusInstanceMonitors || object.InstanceMonitorsTruncated == full || (len(object.InstanceMonitors) == maxClusterStatusInstanceMonitors) != full {
			t.Errorf("object %s: count %d, total %d, truncated %v", object.Path, len(object.InstanceMonitors), object.InstanceMonitorsTotal, object.InstanceMonitorsTruncated)
		}
	}
	if listed != maxClusterStatusPageInstanceMonitors {
		t.Errorf("page lists %d instance monitors, want %d", listed, maxClusterStatusPageInstanceMonitors)
	}
	encoded, err := json.Marshal(result.Objects.Items[objects-1])
	if err != nil {
		t.Fatalf("marshal object: %v", err)
	}
	if strings.Contains(string(encoded), `"instance_monitors":`) || !strings.Contains(string(encoded), `"instance_monitors_total":50`) {
		t.Errorf("object cut by the page bound = %s", encoded)
	}
}
