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

const testOrchestrationID = "40000000-0000-0000-0000-00000000000c"

// nodeActionClient answers the reads of a node action, the node status, the
// execution, the orchestration and the journal, and records the POST.
type nodeActionClient struct {
	// monitors are the successive node monitors the cluster status gives,
	// each "state|local_expect|frozen_at"; the last one repeats.
	monitors []string
	// execs and orchestrations are the successive answers to their reads;
	// "410" answers Gone. The last one repeats.
	execs          []string
	orchestrations []string
	logs           []string
	// instances are the instance records the node holds.
	instances     []string
	instanceQuery url.Values
	postErr       error
	statusReads   int
	execReads     int
	orchReads     int
	logReads      int
	posts         []string
	postQuery     url.Values
	// caches are the successive answers of the cache reads, by path; the
	// last one repeats.
	caches     map[string][]string
	cacheReads map[string]int
}

func (c *nodeActionClient) GetJSON(_ context.Context, path string, query url.Values, output any) error {
	recordRoute("GET", path)
	switch {
	case path == "/api/instance":
		c.instanceQuery = query
		return json.Unmarshal([]byte(`{"items":[`+strings.Join(c.instances, ",")+`]}`), output)
	case path == "/api/cluster/status":
		monitor := strings.Split(c.monitors[min(c.statusReads, len(c.monitors)-1)], "|")
		c.statusReads++
		return json.Unmarshal([]byte(fmt.Sprintf(`{"cluster":{"config":{"nodes":["n1","n2"]},"node":{"n1":{"status":{"frozen_at":%q},"monitor":{"state":%q,"local_expect":%q}},"n2":{}}}}`, monitor[2], monitor[0], monitor[1])), output)
	case strings.HasPrefix(path, "/api/node/name/n1/daemon/exec/id/"):
		c.execReads++
		return json.Unmarshal([]byte(c.execs[min(c.execReads-1, len(c.execs)-1)]), output)
	case c.caches[path] != nil:
		if c.cacheReads == nil {
			c.cacheReads = map[string]int{}
		}
		answers := c.caches[path]
		answer := answers[min(c.cacheReads[path], len(answers)-1)]
		c.cacheReads[path]++
		if answer == "404" {
			return daemonProblemError{status: http.StatusNotFound, detail: "waiting for cached value"}
		}
		return json.Unmarshal([]byte(answer), output)
	case path == "/api/node/name/n1/daemon/orchestration/id/"+testOrchestrationID:
		c.orchReads++
		answer := c.orchestrations[min(c.orchReads-1, len(c.orchestrations)-1)]
		if answer == "410" {
			return daemonProblemError{status: http.StatusGone, detail: "orchestration no longer known"}
		}
		return json.Unmarshal([]byte(answer), output)
	}
	return fmt.Errorf("unexpected read %s", path)
}

func (c *nodeActionClient) GetSSE(_ context.Context, path string, _ url.Values, consume func(string, string, []byte) error) error {
	recordRoute("GET", path)
	c.logReads++
	for _, entry := range c.logs {
		if err := consume("log", "", []byte(entry)); err != nil {
			return err
		}
	}
	return nil
}

func (c *nodeActionClient) PostJSON(_ context.Context, path string, query url.Values, _, output any) error {
	recordRoute("POST", path)
	c.posts = append(c.posts, path)
	c.postQuery = query
	if c.postErr != nil {
		return c.postErr
	}
	switch {
	case output == nil:
		return nil
	case strings.HasSuffix(path, "/freeze"), strings.HasSuffix(path, "/unfreeze"), strings.Contains(path, "/action/scan/"), strings.Contains(path, "/action/push/"),
		strings.HasSuffix(path, "/scsi/scan"), strings.HasSuffix(path, "/dequeue"):
		return json.Unmarshal([]byte(`{"session_id":"`+testSessionID+`","exec_id":"`+testExecID+`"}`), output)
	}
	return json.Unmarshal([]byte(`{"orchestration_id":"`+testOrchestrationID+`","node":"n1"}`), output)
}

func orchestrationRecord(state string, ended bool, failure string) string {
	record := map[string]any{"orchestration_id": testOrchestrationID, "node": "n1", "expect": "drained", "state": state, "started_at": "2026-10-22T12:00:00Z"}
	if ended {
		record["ended_at"] = "2026-10-22T12:00:05Z"
	}
	if failure != "" {
		record["error"] = failure
	}
	data, _ := json.Marshal(record)
	return string(data)
}

func TestRunNodeExecActionWaitsForTheExecution(t *testing.T) {
	zero, one := 0, 1
	client := &nodeActionClient{monitors: []string{"idle|none|"}, execs: []string{execRecord("running", nil, false, ""), execRecord("succeeded", &zero, true, "")}}
	result, err := New(client).RunNodeExecAction(context.Background(), "n1", NodeActionFreeze, 0)
	if err != nil || !result.Completed || *result.ExitCode != 0 || result.ExecID != testExecID || result.Action != "freeze" || client.logReads != 0 ||
		len(client.posts) != 1 || client.posts[0] != "/api/node/name/n1/action/freeze" {
		t.Fatalf("%+v %v, posts %v", result, err, client.posts)
	}
	// A failure carries the end of the journal of its execution.
	client = &nodeActionClient{monitors: []string{"idle|none|"}, execs: []string{execRecord("failed", &one, true, "exit status 1")}, logs: []string{execLogEntry("error", "node: unfreeze: refused")}}
	result, err = New(client).RunNodeExecAction(context.Background(), "n1", NodeActionUnfreeze, 0)
	if err != nil || !result.Completed || *result.ExitCode != 1 || len(result.LogTail) != 1 || result.LogTail[0].Message != "node: unfreeze: refused" ||
		client.posts[0] != "/api/node/name/n1/action/unfreeze" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestNodeActionsRefuseBeforeTheCall(t *testing.T) {
	service := func() (*Service, *nodeActionClient) {
		client := &nodeActionClient{monitors: []string{"idle|none|"}}
		return New(client), client
	}
	for name, call := range map[string]func(*Service) error{
		"alias": func(s *Service) error {
			_, err := s.RunNodeExecAction(context.Background(), "_", NodeActionFreeze, 0)
			return err
		},
		"selector": func(s *Service) error { _, err := s.DrainNode(context.Background(), "n1,n2", 0); return err },
		"wait":     func(s *Service) error { _, err := s.DrainNode(context.Background(), "n1", 61*time.Second); return err },
		"unknown":  func(s *Service) error { _, err := s.DrainNode(context.Background(), "n9", 0); return err },
		"action": func(s *Service) error {
			_, err := s.RunNodeExecAction(context.Background(), "n1", NodeActionDrain, 0)
			return err
		},
		"abort unknown": func(s *Service) error { _, err := s.AbortNode(context.Background(), "n9"); return err },
		"clear idle":    func(s *Service) error { _, err := s.ClearNode(context.Background(), "n1"); return err },
	} {
		s, client := service()
		if err := call(s); err == nil || len(client.posts) != 0 {
			t.Fatalf("%s: %v, posts %v", name, err, client.posts)
		}
	}
	client := &nodeActionClient{monitors: []string{"draining|drained|2026-10-22T12:00:00Z"}}
	if _, err := New(client).ClearNode(context.Background(), "n1"); err == nil || !strings.Contains(err.Error(), `its monitor state is "draining", not a failure`) || len(client.posts) != 0 {
		t.Fatalf("clear draining: %v", err)
	}
	// The refusal of the daemon is given as it says it.
	client = &nodeActionClient{monitors: []string{"idle|none|"}, postErr: daemonProblemError{status: http.StatusConflict, detail: "same local expect none"}}
	if _, err := New(client).AbortNode(context.Background(), "n1"); err == nil || !strings.Contains(err.Error(), "same local expect none") {
		t.Fatalf("abort refused: %v", err)
	}
}

func TestDrainNodeFollowsTheOrchestration(t *testing.T) {
	// The node does not know the orchestration at once, then runs it.
	client := &nodeActionClient{
		monitors:       []string{"idle|none|", "idle|none|2026-10-22T12:00:01Z"},
		orchestrations: []string{"410", orchestrationRecord("running", false, ""), orchestrationRecord("succeeded", true, "")},
		// The shutdown stops the svc instances up, warn or in standby.
		instances: []string{
			instanceMonitorRecord("test/svc/web", "n1", "up", "idle"), instanceMonitorRecord("system/svc/vip", "n1", "stdby up", "idle"),
			instanceMonitorRecord("foo", "n1", "warn", "idle"), instanceMonitorRecord("test/svc/db", "n1", "down", "idle"),
			instanceMonitorRecord("test/vol/data", "n1", "up", "idle"), instanceMonitorRecord("test/svc/app", "n2", "up", "idle"),
		},
	}
	result, err := New(client).DrainNode(context.Background(), "n1", 0)
	want := []NodeInstance{{"foo", "warn"}, {"system/svc/vip", "stdby up"}, {"test/svc/web", "up"}}
	if fmt.Sprint(result.InstancesUp) != fmt.Sprint(want) || client.instanceQuery.Encode() != "node=n1" || result.InstancesError != "" {
		t.Fatalf("instances up %+v, query %v", result.InstancesUp, client.instanceQuery)
	}
	if err != nil || !result.Completed || result.State != "succeeded" || result.OrchestrationError != "" || result.OrchestrationID != testOrchestrationID ||
		result.MonitorState != "idle" || result.LocalExpect != "none" || result.FrozenAt != "2026-10-22T12:00:01Z" || client.orchReads != 3 ||
		len(client.posts) != 1 || client.posts[0] != "/api/node/name/n1/action/drain" {
		t.Fatalf("%+v %v, reads %d", result, err, client.orchReads)
	}
	client = &nodeActionClient{
		monitors:       []string{"idle|none|", "drain failed|none|2026-10-22T12:00:01Z"},
		orchestrations: []string{orchestrationRecord("failed", true, "drain failed")},
	}
	result, err = New(client).DrainNode(context.Background(), "n1", 0)
	if err != nil || !result.Completed || result.State != "failed" || result.Error == nil || *result.Error != "drain failed" || result.MonitorState != "drain failed" {
		t.Fatalf("failure: %+v %v", result, err)
	}
	// A drain that outlasts the wait returns what was read.
	client = &nodeActionClient{
		monitors:       []string{"idle|none|", "draining|drained|2026-10-22T12:00:01Z"},
		orchestrations: []string{orchestrationRecord("running", false, "")},
	}
	result, err = New(client).DrainNode(context.Background(), "n1", time.Second)
	if err != nil || result.Completed || result.State != "running" || result.LocalExpect != "drained" {
		t.Fatalf("running: %+v %v", result, err)
	}
}

func TestAbortNodeAndClearNode(t *testing.T) {
	client := &nodeActionClient{monitors: []string{"draining|drained|"}, orchestrations: []string{orchestrationRecord("succeeded", true, "")}}
	result, err := New(client).AbortNode(context.Background(), "n1")
	if err != nil || result.OrchestrationID != testOrchestrationID || !result.Completed || result.State != "succeeded" || client.posts[0] != "/api/node/name/n1/action/abort" {
		t.Fatalf("%+v %v", result, err)
	}
	// The daemon accepts an abort of a node running nothing, and its monitor
	// refuses it then.
	client = &nodeActionClient{monitors: []string{"idle|none|"}, orchestrations: []string{orchestrationRecord("refused", true, "node monitor local expect is already set to the same value none")}}
	result, err = New(client).AbortNode(context.Background(), "n1")
	if err != nil || result.State != "refused" || result.Error == nil || !strings.Contains(*result.Error, "same value none") {
		t.Fatalf("refused: %+v %v", result, err)
	}
	client = &nodeActionClient{monitors: []string{"drain failed|none|", "drain failed|none|", "idle|none|"}}
	cleared, err := New(client).ClearNode(context.Background(), "n1")
	if err != nil || !cleared.Cleared || cleared.PreviousState != "drain failed" || cleared.CurrentState != "idle" || client.posts[0] != "/api/node/name/n1/action/clear" {
		t.Fatalf("%+v %v", cleared, err)
	}
}

func hardwareCache(entries int) string {
	items := make([]string, entries)
	for i := range items {
		items[i] = fmt.Sprintf(`{"kind":"HardwareItem","meta":{"node":"n1"},"data":{"type":"pci","class":"Bridge","driver":"pcieport","path":"00:%02d.0","description":"bridge"}}`, i)
	}
	return `{"kind":"HardwareList","items":[` + strings.Join(items, ",") + `]}`
}

// A refresh of a cache counts its entries before and after; a push failing
// on the collector it sends to has refreshed the cache all the same.
func TestRunNodeRefreshCountsTheCacheBeforeAndAfter(t *testing.T) {
	one := 1
	hardware := "/api/node/name/n1/system/hardware"
	client := &nodeActionClient{
		monitors: []string{"idle|none|"},
		execs:    []string{execRecord("failed", &one, true, "exit status 1")},
		logs:     []string{execLogEntry("error", "node: push asset: collector not configured")},
		caches:   map[string][]string{hardware: {hardwareCache(2), hardwareCache(3)}},
	}
	result, err := New(client).RunNodeRefresh(context.Background(), "n1", NodeRefreshHardware, NodeRefreshOptions{})
	if err != nil || client.posts[0] != "/api/node/name/n1/action/push/asset" || *result.CacheEntriesBefore != 2 || *result.CacheEntriesAfter != 3 ||
		*result.ExitCode != 1 || !strings.Contains(result.LogTail[0].Message, "collector") {
		t.Fatalf("%+v %v", result, err)
	}
	// A cache missing before is no error.
	zero := 0
	client = &nodeActionClient{monitors: []string{"idle|none|"}, execs: []string{execRecord("succeeded", &zero, true, "")}, caches: map[string][]string{hardware: {"404", hardwareCache(1)}}}
	result, err = New(client).RunNodeRefresh(context.Background(), "n1", NodeRefreshHardware, NodeRefreshOptions{})
	if err != nil || result.CacheEntriesBefore != nil || *result.CacheEntriesAfter != 1 {
		t.Fatalf("missing before: %+v %v", result, err)
	}
	// A scan or a dequeue reads no cache.
	for _, refresh := range []NodeRefresh{NodeScanSCSI, NodeDequeue} {
		client := &nodeActionClient{monitors: []string{"idle|none|"}, execs: []string{execRecord("succeeded", &zero, true, "")}}
		result, err := New(client).RunNodeRefresh(context.Background(), "n1", refresh, NodeRefreshOptions{})
		if err != nil || result.CacheEntriesBefore != nil || result.CacheEntriesAfter != nil || client.posts[0] != "/api/node/name/n1/action/"+string(refresh) {
			t.Fatalf("%s: %+v %v", refresh, result, err)
		}
	}
}

func TestScanNodeSCSINarrowsAsOm3Does(t *testing.T) {
	zero := 0
	wwn, iqn := "0x500143802426baf4", "iqn.2026-10.com.opensvc:lab"
	client := &nodeActionClient{monitors: []string{"idle|none|"}, execs: []string{execRecord("succeeded", &zero, true, "")}}
	if _, err := New(client).RunNodeRefresh(context.Background(), "n1", NodeScanSCSI, NodeRefreshOptions{HBA: wwn, Target: iqn, LUN: "3"}); err != nil ||
		client.postQuery.Get("hba") != wwn || client.postQuery.Get("target") != iqn || client.postQuery.Get("lun") != "3" {
		t.Fatalf("%v %v", err, client.postQuery)
	}
	for name, tc := range map[string]struct {
		refresh NodeRefresh
		options NodeRefreshOptions
		want    string
	}{
		"target alone":   {NodeScanSCSI, NodeRefreshOptions{Target: wwn}, "target needs hba"},
		"lun alone":      {NodeScanSCSI, NodeRefreshOptions{HBA: wwn, LUN: "1"}, "lun needs target"},
		"hba not a name": {NodeScanSCSI, NodeRefreshOptions{HBA: "2"}, "port name"},
		"lun not a lun":  {NodeScanSCSI, NodeRefreshOptions{HBA: wwn, Target: wwn, LUN: "x"}, "logical unit number"},
		"hba elsewhere":  {NodeRefreshDisks, NodeRefreshOptions{HBA: wwn}, "SCSI scan only"},
		"unknown":        {NodeRefresh("register"), NodeRefreshOptions{}, "unsupported"},
	} {
		client := &nodeActionClient{monitors: []string{"idle|none|"}}
		if _, err := New(client).RunNodeRefresh(context.Background(), "n1", tc.refresh, tc.options); err == nil || !strings.Contains(err.Error(), tc.want) || len(client.posts) != 0 {
			t.Errorf("%s: %v, posts %v", name, err, client.posts)
		}
	}
}
