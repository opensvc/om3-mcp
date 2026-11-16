package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	testSessionID = "40000000-0000-0000-0000-00000000000a"
	testExecID    = "40000000-0000-0000-0000-00000000000b"
)

// instanceActionClient answers the reads of an instance action, the
// instances, the topology and the execution, and records the POST.
type instanceActionClient struct {
	instances []string
	topology  string
	// execs are the answers to the successive reads of the execution; the
	// last one repeats.
	execs   []string
	execErr error
	postErr error
	// monitor are the monitor states the successive reads of the instance
	// give a clear; the last one repeats.
	monitor []string
	// logs are the journal entries of the execution; logErr fails their read.
	logs      []string
	logErr    error
	logQuery  url.Values
	logReads  int
	reads     []string
	execReads int
	posts     []string
	query     url.Values
	// taskTypes are the task#<name>.type records, taskKeywords the
	// confirmation and log records, and taskInfo the info of each task in
	// the instance status, for a run.
	taskTypes    string
	taskKeywords string
	taskInfo     string
	// orchestrate and frozen are those of the object, nodeFrozenAt the
	// freeze time of n1, for a shutdown.
	orchestrate, frozen, nodeFrozenAt string
}

func (c *instanceActionClient) GetJSON(_ context.Context, path string, query url.Values, output any) error {
	recordRoute("GET", path)
	c.reads = append(c.reads, path)
	switch {
	case path == "/api/instance":
		items := c.instances
		if len(c.monitor) > 0 {
			state := c.monitor[min(c.instanceReads(), len(c.monitor)-1)]
			items = []string{instanceMonitorRecord("test/svc/web", "n1", "down", state)}
		}
		return json.Unmarshal([]byte(`{"items":[`+strings.Join(items, ",")+`]}`), output)
	case path == "/api/object/path/test/svc/web/config":
		if kw := query["kw"]; len(kw) == 1 && kw[0] == "task.type" {
			return json.Unmarshal([]byte(`{"kind":"KeywordList","items":[`+c.taskTypes+`]}`), output)
		}
		return json.Unmarshal([]byte(`{"kind":"KeywordList","items":[`+c.taskKeywords+`]}`), output)
	case path == "/api/node/name/n1/instance/path/test/svc/web":
		return json.Unmarshal([]byte(`{"kind":"InstanceItem","meta":{"node":"n1","object":"test/svc/web"},"data":{"config":{"csum":"c","priority":50,"scope":["n1"],"updated_at":"2026-10-23T12:00:00Z"},"status":{"avail":"n/a","overall":"n/a","provisioned":"n/a","resources":{`+c.taskInfo+`}}}}`), output)
	case path == "/api/object":
		return json.Unmarshal([]byte(fmt.Sprintf(`{"items":[{"meta":{"object":%q},"data":{"topology":%q,"orchestrate":%q,"frozen":%q}}]}`, query.Get("path"), c.topology, c.orchestrate, c.frozen)), output)
	case path == "/api/cluster/status":
		return json.Unmarshal([]byte(fmt.Sprintf(`{"cluster":{"config":{"nodes":["n1","n2"]},"node":{"n1":{"status":{"frozen_at":%q}}}}}`, c.nodeFrozenAt)), output)
	case strings.HasPrefix(path, "/api/node/name/n1/daemon/exec/id/"):
		c.execReads++
		if c.execErr != nil {
			return c.execErr
		}
		return json.Unmarshal([]byte(c.execs[min(c.execReads-1, len(c.execs)-1)]), output)
	}
	return fmt.Errorf("unexpected read %s", path)
}

func (c *instanceActionClient) GetSSE(_ context.Context, path string, query url.Values, consume func(string, string, []byte) error) error {
	recordRoute("GET", path)
	c.logReads++
	c.logQuery = query
	if path != "/api/node/name/n1/log" {
		return fmt.Errorf("unexpected log read %s", path)
	}
	if c.logErr != nil {
		return c.logErr
	}
	for _, entry := range c.logs {
		if err := consume("log", "", []byte(entry)); err != nil {
			return err
		}
	}
	return nil
}

// execLogEntry is a journal entry of the execution, shaped after dev5.
func execLogEntry(level, message string) string {
	payload, _ := json.Marshal(map[string]string{"level": level, "node": "n1", "exec_id": testExecID, "obj_path": "test/svc/web", "time": "2026-10-22T12:00:01Z", "message": message})
	entry, _ := json.Marshal(map[string]string{"MESSAGE": message, "NODE": "n1", "EXEC_ID": testExecID, "PRIORITY": "4", "JSON": string(payload)})
	return string(entry)
}

func (c *instanceActionClient) instanceReads() int {
	count := 0
	for _, path := range c.reads {
		if path == "/api/instance" {
			count++
		}
	}
	return count - 1
}

func (c *instanceActionClient) PostJSON(_ context.Context, path string, query url.Values, _, output any) error {
	recordRoute("POST", path)
	c.posts = append(c.posts, path)
	c.query = query
	if c.postErr != nil {
		return c.postErr
	}
	if output == nil {
		return nil
	}
	return json.Unmarshal([]byte(`{"session_id":"`+testSessionID+`","exec_id":"`+testExecID+`"}`), output)
}

func instanceMonitorRecord(object, node, avail, state string) string {
	return fmt.Sprintf(`{"meta":{"node":%q,"object":%q},"data":{"status":{"avail":%q},"monitor":{"state":%q}}}`, node, object, avail, state)
}

func execRecord(state string, exitCode *int, ended bool, failure string) string {
	record := map[string]any{
		"session_id": testSessionID, "exec_id": testExecID, "node": "n1", "origin": "api",
		"command": "om test/svc/web instance start", "state": state, "started_at": "2026-10-22T12:00:00Z",
	}
	if exitCode != nil {
		record["exit_code"] = *exitCode
	}
	if ended {
		record["ended_at"] = "2026-10-22T12:00:02Z"
	}
	if failure != "" {
		record["error"] = failure
	}
	data, _ := json.Marshal(record)
	return string(data)
}

func TestRunInstanceActionSendsItsOptionsAndReadsTheExecution(t *testing.T) {
	one := 1
	for _, tc := range []struct {
		action  InstanceAction
		options InstanceActionOptions
		query   string
	}{
		{InstanceActionStart, InstanceActionOptions{Rids: []string{"fs#1", "app#2"}, DisableRollback: true}, "disable_rollback=true&rid=fs%231%2Capp%232"},
		{InstanceActionStop, InstanceActionOptions{InterruptSyncs: true, ConfigUpdatedAt: "2026-10-22T12:00:00.5Z"}, "config_updated_at=2026-10-22T12%3A00%3A00.5Z&interrupt_syncs=true"},
		{InstanceActionRestart, InstanceActionOptions{}, ""},
		{InstanceActionFreeze, InstanceActionOptions{}, ""},
		{InstanceActionUnfreeze, InstanceActionOptions{}, ""},
		{InstanceActionUnprovision, InstanceActionOptions{Rids: []string{"fs#1"}, Leader: true}, "leader=true&rid=fs%231"},
		{InstanceActionUnprovision, InstanceActionOptions{}, ""},
		{InstanceActionUnprovision, InstanceActionOptions{Rids: []string{"fs#1"}, StateOnly: true}, "rid=fs%231&state_only=true"},
		{InstanceActionStartStandby, InstanceActionOptions{Rids: []string{"fs#1"}, DisableRollback: true}, "disable_rollback=true&rid=fs%231"},
		{InstanceActionShutdown, InstanceActionOptions{Rids: []string{"fs#2"}}, "rid=fs%232"},
		{InstanceActionPGUpdate, InstanceActionOptions{Rids: []string{"app#1"}}, "rid=app%231"},
		{InstanceActionPGReset, InstanceActionOptions{}, ""},
		{InstanceActionInfo, InstanceActionOptions{Rids: []string{"disk#2"}}, "rid=disk%232"},
	} {
		client := &instanceActionClient{
			instances: []string{instanceMonitorRecord("test/svc/web", "n1", "down", "idle")},
			topology:  "failover",
			execs:     []string{execRecord("running", nil, false, ""), execRecord("failed", &one, true, "exit status 1")},
			logs:      []string{execLogEntry("warn", `ip#1: stderr: Cannot find device "eth0"`), execLogEntry("error", "ip#1: start: ip exit code 1")},
		}
		result, err := New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", tc.action, tc.options)
		if err != nil {
			t.Fatalf("%s: %v", tc.action, err)
		}
		if len(client.posts) != 1 || client.posts[0] != "/api/node/name/n1/instance/path/test/svc/web/action/"+string(tc.action) || client.query.Encode() != tc.query {
			t.Fatalf("%s: posted %v %s", tc.action, client.posts, client.query.Encode())
		}
		if !result.Completed || result.State != "failed" || result.ExitCode == nil || *result.ExitCode != 1 || result.Error == nil || *result.Error != "exit status 1" ||
			result.ExecID != testExecID || result.SessionID != testSessionID || result.Instance.Availability != "down" || client.execReads != 2 {
			t.Fatalf("%s: %+v, %d reads", tc.action, result, client.execReads)
		}
		// A failure carries the end of the journal of its execution, in time
		// order, read once.
		if client.logReads != 1 || client.logQuery.Get("lines") != "21" || client.logQuery.Get("filter") != "EXEC_ID="+testExecID ||
			len(result.LogTail) != 2 || result.LogTail[0].Level != "warn" || result.LogTail[0].Message != `ip#1: stderr: Cannot find device "eth0"` ||
			result.LogTail[1].Level != "error" || result.LogTruncated || result.LogError != "" {
			t.Fatalf("%s: log %+v, %d reads, query %v", tc.action, result.LogTail, client.logReads, client.logQuery)
		}
	}
}

func TestRunInstanceActionReturnsAnExecutionStillRunning(t *testing.T) {
	client := &instanceActionClient{
		instances: []string{instanceMonitorRecord("test/svc/web", "n1", "down", "idle")},
		execs:     []string{execRecord("running", nil, false, "")},
	}
	result, err := New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionFreeze, InstanceActionOptions{Wait: time.Second})
	if err != nil || result.Completed || result.State != "running" || result.ExitCode != nil || result.ExecutionError != "" || client.logReads != 0 {
		t.Fatalf("%+v %v", result, err)
	}

	// A success reads no journal; a journal that cannot be read leaves the
	// result of the execution as it is.
	zero := 0
	client = &instanceActionClient{
		instances: []string{instanceMonitorRecord("test/svc/web", "n1", "down", "idle")},
		execs:     []string{execRecord("succeeded", &zero, true, "")},
	}
	result, err = New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionFreeze, InstanceActionOptions{})
	if err != nil || !result.Completed || client.logReads != 0 || result.LogTail != nil {
		t.Fatalf("success: %+v %v", result, err)
	}
	two := 2
	client = &instanceActionClient{
		instances: []string{instanceMonitorRecord("test/svc/web", "n1", "down", "idle")},
		execs:     []string{execRecord("failed", &two, true, "exit status 2")},
		logErr:    daemonProblemError{status: http.StatusForbidden, detail: "need root"},
	}
	result, err = New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionStop, InstanceActionOptions{})
	if err != nil || !result.Completed || *result.ExitCode != 2 || result.LogTail != nil || !strings.Contains(result.LogError, "need root") {
		t.Fatalf("log refused: %+v %v", result, err)
	}

	// An operator may run the action, not read its execution.
	client = &instanceActionClient{
		instances: []string{instanceMonitorRecord("test/svc/web", "n1", "down", "idle")},
		execErr:   daemonProblemError{status: http.StatusForbidden, detail: "need root"},
	}
	result, err = New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionFreeze, InstanceActionOptions{})
	if err != nil || result.Completed || !strings.HasPrefix(result.ExecutionError, "reading the execution requires root access") || client.execReads != 1 {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRunInstanceActionRefusesBeforeTheCall(t *testing.T) {
	up := instanceMonitorRecord("test/svc/web", "n2", "up", "idle")
	down := instanceMonitorRecord("test/svc/web", "n1", "down", "idle")
	for _, tc := range []struct {
		name      string
		path      string
		node      string
		action    InstanceAction
		options   InstanceActionOptions
		instances []string
		topology  string
		want      string
	}{
		{"selector", "test/svc/*", "n1", InstanceActionStart, InstanceActionOptions{}, nil, "", "without wildcard or selector"},
		{"kind", "test/cfg/web", "n1", InstanceActionStart, InstanceActionOptions{}, nil, "", "svc, vol objects only"},
		{"node alias", "test/svc/web", "_", InstanceActionStart, InstanceActionOptions{}, nil, "", "the _ alias"},
		{"rid selector", "test/svc/web", "n1", InstanceActionStart, InstanceActionOptions{Rids: []string{"fs"}}, nil, "", "one exact resource id"},
		{"rids on freeze", "test/svc/web", "n1", InstanceActionFreeze, InstanceActionOptions{Rids: []string{"fs#1"}}, nil, "", "do not apply to the freeze and unfreeze actions"},
		{"leader on stop", "test/svc/web", "n1", InstanceActionStop, InstanceActionOptions{Leader: true}, nil, "", "unprovision action only"},
		{"state only on stop", "test/svc/web", "n1", InstanceActionStop, InstanceActionOptions{StateOnly: true}, nil, "", "unprovision action only"},
		{"state only and leader", "test/svc/web", "n1", InstanceActionUnprovision, InstanceActionOptions{StateOnly: true, Leader: true}, nil, "", "exclusive"},
		{"rollback on stop", "test/svc/web", "n1", InstanceActionStop, InstanceActionOptions{DisableRollback: true}, nil, "", "start, restart and startstandby actions only"},
		{"rollback on shutdown", "test/svc/web", "n1", InstanceActionShutdown, InstanceActionOptions{DisableRollback: true}, nil, "", "start, restart and startstandby actions only"},
		{"syncs on shutdown", "test/svc/web", "n1", InstanceActionShutdown, InstanceActionOptions{InterruptSyncs: true}, nil, "", "stop action only"},
		{"syncs on start", "test/svc/web", "n1", InstanceActionStart, InstanceActionOptions{InterruptSyncs: true}, nil, "", "stop action only"},
		{"config time", "test/svc/web", "n1", InstanceActionStart, InstanceActionOptions{ConfigUpdatedAt: "yesterday"}, nil, "", "RFC 3339"},
		{"wait", "test/svc/web", "n1", InstanceActionStart, InstanceActionOptions{Wait: 61 * time.Second}, nil, "", "between 1 and 60 seconds"},
		{"no instance", "test/svc/web", "n3", InstanceActionStop, InstanceActionOptions{}, []string{down, up}, "", "no instance on that node"},
		{"failover start", "test/svc/web", "n1", InstanceActionStart, InstanceActionOptions{}, []string{down, up}, "failover", "its instance on n2 is up"},
		{"failover restart", "test/svc/web", "n1", InstanceActionRestart, InstanceActionOptions{Rids: []string{"fs#1"}}, []string{down, instanceMonitorRecord("test/svc/web", "n2", "warn", "idle")}, "failover", "its instance on n2 is warn"},
		{"unprovision up", "test/svc/web", "n2", InstanceActionUnprovision, InstanceActionOptions{}, []string{down, up}, "", "stop it first, stop_instance"},
		{"unprovision warn", "test/svc/web", "n2", InstanceActionUnprovision, InstanceActionOptions{Leader: true}, []string{down, instanceMonitorRecord("test/svc/web", "n2", "warn", "idle")}, "", "the instance is warn"},
	} {
		client := &instanceActionClient{instances: tc.instances, topology: tc.topology}
		_, err := New(client).RunInstanceAction(context.Background(), tc.path, tc.node, tc.action, tc.options)
		if err == nil || !strings.Contains(err.Error(), tc.want) || len(client.posts) != 0 {
			t.Fatalf("%s: %v, posts %v", tc.name, err, client.posts)
		}
	}

	// A flex service, or a stop, runs beside an instance up elsewhere.
	for _, tc := range []struct {
		action   InstanceAction
		topology string
	}{{InstanceActionStart, "flex"}, {InstanceActionStop, "failover"}, {InstanceActionFreeze, "failover"}, {InstanceActionUnprovision, "failover"}} {
		client := &instanceActionClient{instances: []string{down, up}, topology: tc.topology, execs: []string{execRecord("succeeded", new(int), true, "")}}
		if _, err := New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", tc.action, InstanceActionOptions{}); err != nil || len(client.posts) != 1 {
			t.Fatalf("%s %s: %v", tc.action, tc.topology, err)
		}
	}

	// A record of the state touches nothing: an instance up is not refused.
	client := &instanceActionClient{instances: []string{down, up}, execs: []string{execRecord("succeeded", new(int), true, "")}}
	if result, err := New(client).RunInstanceAction(context.Background(), "test/svc/web", "n2", InstanceActionUnprovision, InstanceActionOptions{StateOnly: true}); err != nil || len(client.posts) != 1 || client.query.Get("state_only") != "true" || !result.StateOnly {
		t.Fatalf("state only on an instance up: %+v %v", result, err)
	}

	// The refusal of the daemon is given as it says it.
	client = &instanceActionClient{instances: []string{down}, postErr: daemonProblemError{status: http.StatusConflict, detail: "config is older"}}
	if _, err := New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionStop, InstanceActionOptions{}); err == nil || !strings.Contains(err.Error(), "config is older") {
		t.Fatalf("daemon refusal: %v", err)
	}

	// A 409 to a config_updated_at names it as the likely cause, and the
	// next step: the call again without it.
	client = &instanceActionClient{instances: []string{down}, postErr: daemonProblemError{status: http.StatusConflict, detail: "config is older"}}
	if _, err := New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionStop, InstanceActionOptions{ConfigUpdatedAt: "2026-10-28T11:16:40Z"}); err == nil ||
		!strings.Contains(err.Error(), "leave config_updated_at out and call again") || !strings.Contains(err.Error(), "config is older") {
		t.Fatalf("config_updated_at refusal: %v", err)
	}
}

func TestClearInstanceLiftsAFailureOnly(t *testing.T) {
	client := &instanceActionClient{monitor: []string{"start failed", "start failed", "idle"}}
	result, err := New(client).ClearInstance(context.Background(), "test/svc/web", "n1")
	if err != nil || !result.Cleared || result.PreviousState != "start failed" || result.CurrentState != "idle" {
		t.Fatalf("%+v %v", result, err)
	}
	if len(client.posts) != 1 || client.posts[0] != "/api/node/name/n1/instance/path/test/svc/web/clear" {
		t.Fatalf("posted %v", client.posts)
	}

	for state, want := range map[string]string{
		"idle":     "nothing to clear",
		"starting": `its monitor state is "starting", not a failure`,
		"stopped":  `its monitor state is "stopped", not a failure`,
	} {
		client := &instanceActionClient{monitor: []string{state}}
		if _, err := New(client).ClearInstance(context.Background(), "test/svc/web", "n1"); err == nil || !strings.Contains(err.Error(), want) || len(client.posts) != 0 {
			t.Fatalf("%s: %v, posts %v", state, err, client.posts)
		}
	}
	client = &instanceActionClient{}
	if _, err := New(client).ClearInstance(context.Background(), "test/sec/web", "dev5n1,dev5n2"); err == nil || len(client.reads) != 0 {
		t.Fatalf("node selector: %v", err)
	}
}

func taskKeyword(keyword, value string) string {
	return fmt.Sprintf(`{"object":"test/svc/web","node":"","keyword":%q,"value":%q}`, keyword, value)
}

func taskInfo(rid, session string, exitCode int) string {
	return fmt.Sprintf(`%q:{"type":"task.host","label":"host","status":"n/a","disable":false,"monitor":false,"provisioned":{"state":"n/a","mtime":null},"info":{"last_run_at":"2026-10-23T12:51:01Z","last_run_exitcode":%d,"last_run_session_id":%q}}`, rid, exitCode, session)
}

func TestRunTaskReadsItsKeywordsThenItsLastRun(t *testing.T) {
	zero := 0
	types := taskKeyword("task#echo.type", "host") + "," + taskKeyword("task#touch.type", "host")
	client := &instanceActionClient{
		instances:    []string{instanceMonitorRecord("test/svc/web", "n1", "n/a", "idle")},
		execs:        []string{execRecord("succeeded", &zero, true, "")},
		taskTypes:    types,
		taskKeywords: taskKeyword("task#echo.log", "true") + "," + taskKeyword("task#echo.confirmation", "") + "," + taskKeyword("task#touch.log@n2", "true"),
		taskInfo:     taskInfo("task#echo", testSessionID, 0) + "," + taskInfo("task#touch", testSessionID, 0),
	}
	result, err := New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionRun, InstanceActionOptions{Rids: []string{"task#echo", "task#touch"}, Env: []string{"MCP_VAR=hi", "OTHER=a=b"}})
	if err != nil {
		t.Fatal(err)
	}
	if client.posts[0] != "/api/node/name/n1/instance/path/test/svc/web/action/run" || client.query.Get("rid") != "task#echo,task#touch" || strings.Join(client.query["env"], "|") != "MCP_VAR=hi|OTHER=a=b" {
		t.Fatalf("posted %v %v", client.posts, client.query)
	}
	// log scoped to another node does not count.
	if len(result.Tasks) != 2 || !result.Tasks[0].OutputLogged || result.Tasks[1].OutputLogged || !result.Tasks[0].ThisRun || *result.Tasks[0].LastRunExitCode != 0 || result.Tasks[0].LastRunAt == "" {
		t.Fatalf("tasks %+v %s", result.Tasks, result.TasksError)
	}
	if out, _ := json.Marshal(result); strings.Contains(string(out), "MCP_VAR") {
		t.Fatalf("an env variable was returned: %s", out)
	}

	// A run the task did not record, refused by max_parallel, leaves the
	// last run of another session: this_run is false.
	defer func(previous time.Duration) { taskRunReadAgainFor = previous }(taskRunReadAgainFor)
	taskRunReadAgainFor = 300 * time.Millisecond
	one := 1
	client = &instanceActionClient{
		instances: []string{instanceMonitorRecord("test/svc/web", "n1", "n/a", "idle")},
		execs:     []string{execRecord("failed", &one, true, "exit status 1")},
		logs:      []string{execLogEntry("error", "task#touch: run: task is already running 1 times")},
		taskTypes: types, taskInfo: taskInfo("task#touch", "40000000-0000-0000-0000-0000000000ff", 0),
	}
	result, err = New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionRun, InstanceActionOptions{Rids: []string{"task#touch"}})
	if err != nil || len(result.Tasks) != 1 || result.Tasks[0].ThisRun || len(result.LogTail) != 1 || !strings.Contains(result.LogTail[0].Message, "already running") {
		t.Fatalf("not this run: %+v %v", result, err)
	}
}

func TestRunTaskRefusesBeforeTheCall(t *testing.T) {
	types := taskKeyword("task#echo.type", "host") + "," + taskKeyword("task#guarded.type", "host")
	for name, tc := range map[string]struct {
		options  InstanceActionOptions
		keywords string
		want     string
	}{
		"no rid":       {InstanceActionOptions{}, "", "one task#<name> at least"},
		"not a task":   {InstanceActionOptions{Rids: []string{"fs#1"}}, "", "not a task"},
		"unknown task": {InstanceActionOptions{Rids: []string{"task#zz"}}, "", "not a resource of the service"},
		"confirmation": {InstanceActionOptions{Rids: []string{"task#guarded"}}, taskKeyword("task#guarded.confirmation", "yes"), "--confirm on n1"},
		"scoped":       {InstanceActionOptions{Rids: []string{"task#echo"}}, taskKeyword("task#echo.confirmation", "false") + "," + taskKeyword("task#echo.confirmation@n1", "true"), "confirmation=true"},
		"unreadable":   {InstanceActionOptions{Rids: []string{"task#echo"}}, taskKeyword("task#echo.confirmation", "{env.confirm}"), "confirmation=true"},
		"ld preload":   {InstanceActionOptions{Rids: []string{"task#echo"}, Env: []string{"LD_PRELOAD=x"}}, "", "change what the command runs"},
		"path":         {InstanceActionOptions{Rids: []string{"task#echo"}, Env: []string{"PATH=/tmp"}}, "", "change what the command runs"},
		"opensvc":      {InstanceActionOptions{Rids: []string{"task#echo"}, Env: []string{"OPENSVC_RID=x"}}, "", "change what the command runs"},
		"lower case":   {InstanceActionOptions{Rids: []string{"task#echo"}, Env: []string{"var=x"}}, "", "KEY=VALUE"},
		"no value":     {InstanceActionOptions{Rids: []string{"task#echo"}, Env: []string{"VAR"}}, "", "KEY=VALUE"},
		"new line":     {InstanceActionOptions{Rids: []string{"task#echo"}, Env: []string{"VAR=a\nb"}}, "", "one line"},
		"twice":        {InstanceActionOptions{Rids: []string{"task#echo"}, Env: []string{"VAR=a", "VAR=b"}}, "", "twice"},
		"too many":     {InstanceActionOptions{Rids: []string{"task#echo"}, Env: slices.Repeat([]string{"VAR=a"}, 33)}, "", "at most 32"},
		"too long":     {InstanceActionOptions{Rids: []string{"task#echo"}, Env: []string{"VAR=" + strings.Repeat("x", 4096)}}, "", "4096 bytes"},
	} {
		client := &instanceActionClient{instances: []string{instanceMonitorRecord("test/svc/web", "n1", "n/a", "idle")}, taskTypes: types, taskKeywords: tc.keywords}
		if _, err := New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionRun, tc.options); err == nil || !strings.Contains(err.Error(), tc.want) || len(client.posts) != 0 {
			t.Errorf("%s: %v, posts %v", name, err, client.posts)
		}
	}
	client := &instanceActionClient{instances: []string{instanceMonitorRecord("test/svc/web", "n1", "down", "idle")}}
	if _, err := New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionStart, InstanceActionOptions{Env: []string{"VAR=a"}}); err == nil || !strings.Contains(err.Error(), "run action only") {
		t.Errorf("env on a start: %v", err)
	}
}

// A shutdown does not flag the instance stopped: on an object orchestrated
// ha, the daemon would start it again, or fail over, unless the object, the
// instance or the node is frozen.
func TestShutdownInstanceRefusesWhatTheDaemonWouldUndo(t *testing.T) {
	frozenInstance := `{"meta":{"node":"n1","object":"test/svc/web"},"data":{"status":{"avail":"up","frozen_at":"2026-10-23T12:00:00Z"},"monitor":{"state":"idle"}}}`
	up := instanceMonitorRecord("test/svc/web", "n1", "up", "idle")
	zero := "0001-01-01T00:00:00Z"
	client := &instanceActionClient{instances: []string{up}, orchestrate: "ha", frozen: "unfrozen", nodeFrozenAt: zero}
	if _, err := New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionShutdown, InstanceActionOptions{}); err == nil || !strings.Contains(err.Error(), "freeze_instance on n1 first") || len(client.posts) != 0 ||
		// The next step only: a tool named beside it, which does something
		// else, is taken for an instruction.
		strings.Contains(err.Error(), "stop_instance") {
		t.Fatalf("unfrozen ha: %v %v", err, client.posts)
	}
	for name, client := range map[string]*instanceActionClient{
		"object frozen":   {instances: []string{up}, orchestrate: "ha", frozen: "frozen", nodeFrozenAt: zero},
		"instance frozen": {instances: []string{frozenInstance}, orchestrate: "ha", frozen: "unfrozen", nodeFrozenAt: zero},
		"node frozen":     {instances: []string{up}, orchestrate: "ha", frozen: "unfrozen", nodeFrozenAt: "2026-10-23T12:00:00Z"},
		"not ha":          {instances: []string{up}, orchestrate: "no", frozen: "unfrozen", nodeFrozenAt: zero},
	} {
		client.execs = []string{execRecord("succeeded", new(int), true, "")}
		if _, err := New(client).RunInstanceAction(context.Background(), "test/svc/web", "n1", InstanceActionShutdown, InstanceActionOptions{}); err != nil || len(client.posts) != 1 || client.posts[0] != "/api/node/name/n1/instance/path/test/svc/web/action/shutdown" {
			t.Errorf("%s: %v %v", name, err, client.posts)
		}
	}
}
