package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// clusterActionClient answers the cluster status, before then after the
// action, the orchestration and the locks, and records the POST.
type clusterActionClient struct {
	statuses       []string
	statusReads    int
	orchestrations []string
	orchReads      int
	orchPath       string
	locks          string
	posts          []string
	postErr        error
	queuedNode     string
}

func (c *clusterActionClient) GetJSON(_ context.Context, path string, _ url.Values, output any) error {
	recordRoute("GET", path)
	switch {
	case path == "/api/cluster/status":
		body := c.statuses[min(c.statusReads, len(c.statuses)-1)]
		c.statusReads++
		return json.Unmarshal([]byte(body), output)
	case strings.Contains(path, "/daemon/orchestration/id/"):
		c.orchPath = path
		c.orchReads++
		return json.Unmarshal([]byte(c.orchestrations[min(c.orchReads-1, len(c.orchestrations)-1)]), output)
	case path == "/api/cluster/lock":
		return json.Unmarshal([]byte(c.locks), output)
	}
	return fmt.Errorf("unexpected read %s", path)
}

func (c *clusterActionClient) PostJSON(_ context.Context, path string, _ url.Values, _, output any) error {
	recordRoute("POST", path)
	c.posts = append(c.posts, path)
	if c.postErr != nil {
		return c.postErr
	}
	return json.Unmarshal([]byte(`{"orchestration_id":"`+testOrchestrationID+`","node":"`+c.queuedNode+`"}`), output)
}

func clusterStatusBody(frozen bool, frozenAt map[string]string) string {
	nodes := map[string]any{}
	for name, at := range frozenAt {
		nodes[name] = map[string]any{"status": map[string]any{"frozen_at": at}}
	}
	data, _ := json.Marshal(map[string]any{
		"cluster": map[string]any{"config": map[string]any{"nodes": []string{"n1", "n2"}}, "status": map[string]any{"is_frozen": frozen}, "node": nodes},
		"daemon":  map[string]any{"nodename": "n1"},
	})
	return string(data)
}

func TestFreezeClusterFollowsTheOrchestrationAndTellsThePendingNodes(t *testing.T) {
	zero := "0001-01-01T00:00:00Z"
	client := &clusterActionClient{
		queuedNode:     "n1",
		statuses:       []string{clusterStatusBody(false, map[string]string{"n1": zero, "n2": zero}), clusterStatusBody(true, map[string]string{"n1": "2026-10-23T10:00:00Z", "n2": zero})},
		orchestrations: []string{orchestrationRecord("running", false, ""), orchestrationRecord("succeeded", true, "")},
	}
	result, err := New(client).RunClusterAction(context.Background(), ClusterActionFreeze, 0)
	if err != nil {
		t.Fatal(err)
	}
	if client.posts[0] != "/api/cluster/action/freeze" || client.orchPath != "/api/node/name/n1/daemon/orchestration/id/"+testOrchestrationID {
		t.Fatalf("posts %v, orchestration read %s", client.posts, client.orchPath)
	}
	if !result.Completed || result.State != "succeeded" || result.Node != "n1" || result.IsFrozen == nil || !*result.IsFrozen ||
		len(result.Nodes) != 2 || result.Nodes[0].FrozenAt == "" || result.Nodes[1].FrozenAt != "" || strings.Join(result.PendingNodes, ",") != "n2" {
		t.Fatalf("result = %+v", result)
	}
	// An unfreeze tells the nodes still frozen; without a node in the
	// answer, the node answering the status is followed.
	client = &clusterActionClient{
		statuses:       []string{clusterStatusBody(true, nil), clusterStatusBody(false, map[string]string{"n1": zero, "n2": "2026-10-23T10:00:00Z"})},
		orchestrations: []string{orchestrationRecord("succeeded", true, "")},
	}
	result, err = New(client).RunClusterAction(context.Background(), ClusterActionUnfreeze, time.Second)
	if err != nil || result.Node != "n1" || strings.Join(result.PendingNodes, ",") != "n2" || client.posts[0] != "/api/cluster/action/unfreeze" {
		t.Fatalf("unfreeze: %+v %v", result, err)
	}
	// The refusal of the daemon is given as it says it.
	client = &clusterActionClient{statuses: []string{clusterStatusBody(false, nil)}, postErr: daemonProblemError{http.StatusConflict, "node n2 state is draining"}}
	if _, err := New(client).RunClusterAction(context.Background(), ClusterActionFreeze, 0); err == nil || !strings.Contains(err.Error(), "draining") {
		t.Fatalf("refused: %v", err)
	}
	for _, wait := range []time.Duration{-time.Second, 61 * time.Second} {
		client := &clusterActionClient{statuses: []string{clusterStatusBody(false, nil)}}
		if _, err := New(client).RunClusterAction(context.Background(), ClusterActionFreeze, wait); err == nil || len(client.posts) != 0 {
			t.Fatalf("wait %s accepted", wait)
		}
	}
	client = &clusterActionClient{queuedNode: "n1", statuses: []string{clusterStatusBody(false, nil)}, orchestrations: []string{orchestrationRecord("succeeded", true, "")}}
	if result, err := New(client).RunClusterAction(context.Background(), ClusterActionAbort, 0); err != nil || client.posts[0] != "/api/cluster/action/abort" || result.State != "succeeded" {
		t.Fatalf("abort: %+v %v", result, err)
	}
}

func TestListClusterLocksSortsAndBounds(t *testing.T) {
	client := &clusterActionClient{locks: `{"kind":"ClusterLockList","items":[
		{"name":"network/b","id":"2","node":"n2","holder":"ipam","acquired_at":"2026-10-23T10:00:00Z","expires_at":"2026-10-23T10:01:00Z","expires_in":"55s"},
		{"name":"network/a","id":"1","node":"n1","holder":"ipam","acquired_at":"2026-10-23T10:00:05Z","expires_at":"2026-10-23T10:01:05Z","expires_in":"60s"}]}`}
	result, err := New(client).ListClusterLocks(context.Background())
	if err != nil || result.Total != 2 || result.Locks[0].Name != "network/a" || result.Truncated {
		t.Fatalf("%+v %v", result, err)
	}
	client = &clusterActionClient{locks: `{"kind":"ClusterLockList","items":[]}`}
	if result, err := New(client).ListClusterLocks(context.Background()); err != nil || result.Total != 0 || result.Locks == nil {
		t.Fatalf("empty: %+v %v", result, err)
	}
}
