package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"
)

func TestListObjectInstances(t *testing.T) {
	client := &recordingJSONGetter{
		t:     t,
		path:  "/api/instance",
		query: url.Values{"path": {"prod/svc/mysql"}},
		payload: `{
			"kind": "InstanceList",
			"items": [
				{"meta": {"node": "node-b", "object": "prod/svc/mysql"}, "data": {
					"monitor": {"state": "idle", "global_expect": "started", "is_ha_leader": false},
					"status": {"avail": "down", "overall": "warn", "provisioned": "true", "resources": {"ip#1": {"status": "down"}}}
				}},
				{"meta": {"node": "node-a", "object": "prod/svc/mysql"}, "data": {
					"monitor": {"state": "idle", "global_expect": "started", "is_ha_leader": true},
					"status": {"avail": "up", "overall": "up", "provisioned": "true", "resources": {"ip#1": {"status": "up"}, "app#1": {"status": "n/a"}}}
				}}
			]
		}`,
	}

	result, err := New(client).ListObjectInstances(context.Background(), ListObjectInstancesOptions{Path: "prod/svc/mysql", Limit: 1})
	if err != nil {
		t.Fatalf("list object instances: %v", err)
	}
	if result.Total != 2 || result.Count != 1 || !result.Truncated || result.NextCursor != "node-a" {
		t.Fatalf("got unexpected page metadata %+v", result)
	}
	instance := result.Instances[0]
	if instance.Node != "node-a" || instance.Availability != "up" || !instance.IsHALeader {
		t.Errorf("got unexpected instance %+v", instance)
	}
	if instance.ResourceSummary.Total != 2 || instance.ResourceSummary.DistinctTotal != 2 || !hasResourceStatusCount(instance.ResourceSummary, "up", 1) || !hasResourceStatusCount(instance.ResourceSummary, "n/a", 1) {
		t.Errorf("got unexpected resource summary %+v", instance.ResourceSummary)
	}
}

func TestSummarizeResourceStatusesPreservesExactValues(t *testing.T) {
	resources := map[string]daemonResourceStatusData{
		"r1": {Status: "up"},
		"r2": {Status: "up"},
		"r3": {Status: "stdby up"},
		"r4": {Status: "UP"},
		"r5": {Status: " up "},
		"r6": {Status: "unexpected-state"},
		"r7": {Status: ""},
	}
	summary := summarizeResourceStatuses(resources)
	if summary.Total != 7 || summary.DistinctTotal != 6 || summary.Count != 6 || summary.Truncated {
		t.Fatalf("summary metadata = %+v", summary)
	}
	for status, count := range map[string]int{"up": 2, "stdby up": 1, "UP": 1, " up ": 1, "unexpected-state": 1, "": 1} {
		if !hasResourceStatusCount(summary, status, count) {
			t.Errorf("summary %+v does not preserve %q=%d", summary, status, count)
		}
	}
	if summary.StatusCounts[0].Status != "" || summary.StatusCounts[1].Status != " up " {
		t.Errorf("status counters are not sorted by exact value: %+v", summary.StatusCounts)
	}
}

func TestSummarizeResourceStatusesBoundsDistinctValues(t *testing.T) {
	resources := make(map[string]daemonResourceStatusData, maxResourceStatusCounts+1)
	for i := 0; i <= maxResourceStatusCounts; i++ {
		resources[fmt.Sprintf("resource-%03d", i)] = daemonResourceStatusData{Status: fmt.Sprintf("status-%03d", i)}
	}
	summary := summarizeResourceStatuses(resources)
	if summary.Total != maxResourceStatusCounts+1 || summary.DistinctTotal != maxResourceStatusCounts+1 || summary.Count != maxResourceStatusCounts || !summary.Truncated {
		t.Fatalf("bounded summary = %+v", summary)
	}
	if summary.StatusCounts[0].Status != "status-000" || summary.StatusCounts[len(summary.StatusCounts)-1].Status != "status-099" {
		t.Errorf("bounded statuses are not stable: first=%+v last=%+v", summary.StatusCounts[0], summary.StatusCounts[len(summary.StatusCounts)-1])
	}
}

func TestListObjectInstancesRejectsInvalidLimitBeforeDaemonCall(t *testing.T) {
	client := &recordingJSONGetter{t: t}
	_, err := New(client).ListObjectInstances(context.Background(), ListObjectInstancesOptions{Path: "prod/svc/mysql", Limit: 101})
	if err == nil {
		t.Fatal("expected invalid limit error")
	}
	if client.calls != 0 {
		t.Fatalf("got %d daemon calls, want 0", client.calls)
	}
}

type refreshInstanceClient struct {
	t        *testing.T
	posted   bool
	advance  bool
	getCalls int
}

func (f *refreshInstanceClient) GetJSON(_ context.Context, path string, query url.Values, output any) error {
	recordRoute("GET", path)
	f.t.Helper()
	f.getCalls++
	if path != "/api/instance" {
		return fmt.Errorf("got GET path %q, want /api/instance", path)
	}
	if query.Get("path") != "example/svc/cache" || query.Get("node") != "node-a" {
		return fmt.Errorf("got unexpected query %#v", query)
	}
	updatedAt := "2026-07-15T10:00:00Z"
	availability := "up"
	if f.posted && f.advance {
		updatedAt = "2026-07-15T10:00:01Z"
		availability = "down"
	}
	payload := fmt.Sprintf(`{"items":[{"meta":{"node":"node-a","object":"example/svc/cache"},"data":{"monitor":{"state":"idle","local_expect":"started"},"status":{"avail":%q,"overall":%q,"updated_at":%q,"resources":{"container#redis":{"status":%q}}}}}]}`, availability, availability, updatedAt, availability)
	return json.Unmarshal([]byte(payload), output)
}

func (f *refreshInstanceClient) PostJSON(_ context.Context, path string, query url.Values, input any, output any) error {
	recordRoute("POST", path)
	f.t.Helper()
	if path != "/api/node/name/node-a/instance/path/example/svc/cache/action/status" {
		return fmt.Errorf("got POST path %q", path)
	}
	if len(query) != 0 || input != nil {
		return fmt.Errorf("got unexpected POST query or input")
	}
	f.posted = true
	return json.Unmarshal([]byte(`{"session_id":"session-1"}`), output)
}

func TestRefreshInstanceStatus(t *testing.T) {
	client := &refreshInstanceClient{t: t, advance: true}
	result, err := New(client).RefreshInstanceStatus(context.Background(), RefreshInstanceStatusOptions{
		Path: "example/svc/cache", Node: "node-a", Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("refresh instance status: %v", err)
	}
	if !client.posted || client.getCalls != 2 {
		t.Fatalf("got posted=%v getCalls=%d, want true and 2", client.posted, client.getCalls)
	}
	if !result.RefreshObserved || result.TimedOut {
		t.Fatalf("got unexpected refresh flags %+v", result)
	}
	if result.SessionID != "session-1" || result.PreviousUpdatedAt != "2026-07-15T10:00:00Z" || result.CurrentUpdatedAt != "2026-07-15T10:00:01Z" {
		t.Errorf("got unexpected refresh metadata %+v", result)
	}
	if result.Instance.Availability != "down" || !hasResourceStatusCount(result.Instance.ResourceSummary, "down", 1) {
		t.Errorf("got unexpected refreshed instance %+v", result.Instance)
	}
}

func hasResourceStatusCount(summary ResourceStatusSummary, status string, count int) bool {
	for _, item := range summary.StatusCounts {
		if item.Status == status && item.Count == count {
			return true
		}
	}
	return false
}

func TestRefreshInstanceStatusReturnsStructuredTimeout(t *testing.T) {
	client := &refreshInstanceClient{t: t}
	service := New(client)
	service.now = func() time.Time { return time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC) }
	result, err := service.RefreshInstanceStatus(context.Background(), RefreshInstanceStatusOptions{
		Path: "example/svc/cache", Node: "node-a", Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("refresh instance status: %v", err)
	}
	if result.RefreshObserved || !result.TimedOut {
		t.Fatalf("got unexpected refresh flags %+v", result)
	}
	if !client.posted || result.SessionID != "session-1" {
		t.Errorf("timeout lost accepted action metadata: client=%+v result=%+v", client, result)
	}
	if result.CurrentUpdatedAt != result.PreviousUpdatedAt || result.Instance.UpdatedAt != result.PreviousUpdatedAt {
		t.Errorf("timeout did not return the last observed instance: %+v", result)
	}
	if result.Provenance.Source != provenanceSourceOpenSVCDaemon || result.Provenance.ObservedAt != "2026-09-16T10:00:00Z" {
		t.Errorf("timeout result has unexpected provenance: %+v", result.Provenance)
	}
}

func TestRefreshInstanceStatusRejectsInvalidTimeoutBeforeDaemonCall(t *testing.T) {
	client := &refreshInstanceClient{t: t}
	_, err := New(client).RefreshInstanceStatus(context.Background(), RefreshInstanceStatusOptions{
		Path: "example/svc/cache", Node: "node-a", Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("expected invalid timeout error")
	}
	if client.getCalls != 0 || client.posted {
		t.Fatalf("daemon was called before validation: %+v", client)
	}
}
