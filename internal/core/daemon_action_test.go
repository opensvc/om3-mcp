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

// daemonActionClient answers the node status, as a sequence, the log level,
// and records the POST.
type daemonActionClient struct {
	statuses    []string
	statusReads int
	statusErrs  int
	level       string
	posts       []string
	bodies      []string
	postErr     error
}

func (c *daemonActionClient) GetJSON(_ context.Context, path string, _ url.Values, output any) error {
	recordRoute("GET", path)
	switch {
	case path == "/api/cluster/status":
		c.statusReads++
		if c.statusReads > 1 && c.statusErrs > 0 {
			c.statusErrs--
			return fmt.Errorf("connection refused")
		}
		return json.Unmarshal([]byte(c.statuses[min(c.statusReads-1, len(c.statuses)-1)]), output)
	case strings.HasSuffix(path, "/daemon/log/control"):
		return json.Unmarshal([]byte(`{"level":"`+c.level+`"}`), output)
	}
	return fmt.Errorf("unexpected read %s", path)
}

func (c *daemonActionClient) PostJSON(_ context.Context, path string, _ url.Values, input, output any) error {
	recordRoute("POST", path)
	c.posts = append(c.posts, path)
	data, _ := json.Marshal(input)
	c.bodies = append(c.bodies, string(data))
	if c.postErr != nil {
		return c.postErr
	}
	if strings.HasSuffix(path, "/daemon/log/control") {
		var body daemonLogControl
		_ = json.Unmarshal(data, &body)
		c.level = body.Level
		return json.Unmarshal([]byte(`{"level":"`+body.Level+`"}`), output)
	}
	return json.Unmarshal([]byte(`{"title":"daemon action queued","detail":"queued","status":200}`), output)
}

// stream is a heartbeat stream of the node status, beating with n1 or not;
// created is its registration time, updated its last change.
type stream struct {
	id, state, created string
	beating            bool
	updated            string
}

// nodeDaemonStatus builds the status of n2; listener is the update time of
// the listener, or <created>|<updated>, created c by default.
func nodeDaemonStatus(pid int, started string, listener string, streams ...stream) string {
	listenerCreated, listenerUpdated, ok := strings.Cut(listener, "|")
	if !ok {
		listenerCreated, listenerUpdated = "c", listener
	}
	items := []map[string]any{}
	for _, s := range streams {
		items = append(items, map[string]any{"id": s.id, "type": "unicast", "state": s.state, "created_at": s.created, "updated_at": s.updated,
			"peers": map[string]any{"n1": map[string]any{"is_beating": s.beating}}})
	}
	data, _ := json.Marshal(map[string]any{"cluster": map[string]any{
		"config": map[string]any{"nodes": []string{"n1", "n2"}},
		"node": map[string]any{"n2": map[string]any{"daemon": map[string]any{
			"pid": pid, "started_at": started,
			"listener":  map[string]any{"state": "running", "created_at": listenerCreated, "updated_at": listenerUpdated, "addr": "::", "port": "1215"},
			"heartbeat": map[string]any{"streams": items},
		}}},
	}})
	return string(data)
}

func TestSetDaemonLogLevel(t *testing.T) {
	client := &daemonActionClient{level: "info"}
	result, err := New(client).SetDaemonLogLevel(context.Background(), "n2", "WARN")
	if err != nil || result.Level != "warn" || result.Previous != "info" || client.posts[0] != "/api/node/name/n2/daemon/log/control" || client.bodies[0] != `{"level":"warn"}` {
		t.Fatalf("%+v %v %v", result, err, client.bodies)
	}
	for _, level := range []string{"debug", "trace", "loud"} {
		client := &daemonActionClient{level: "info"}
		if _, err := New(client).SetDaemonLogLevel(context.Background(), "n2", level); err == nil || len(client.posts) != 0 {
			t.Fatalf("%s accepted", level)
		}
	}
	if _, err := New(&daemonActionClient{}).SetDaemonLogLevel(context.Background(), "_", "info"); err == nil {
		t.Fatal("the alias was accepted")
	}
}

func TestHeartbeatActions(t *testing.T) {
	both := []stream{{"hb#1.rx", "running", "t1", true, "u1"}, {"hb#1.tx", "running", "t1", true, "u1"}, {"hb#2.rx", "running", "t1", true, "u1"}, {"hb#2.tx", "running", "t1", true, "u1"}}
	// A restart of a heartbeat addresses both its streams, and ends when
	// they run again, updated.
	restarted := []stream{{"hb#1.rx", "running", "t2", true, "u2"}, {"hb#1.tx", "running", "t2", true, "u2"}, both[2], both[3]}
	client := &daemonActionClient{statuses: []string{nodeDaemonStatus(1, "s1", "l1", both...), nodeDaemonStatus(1, "s1", "l1", both...), nodeDaemonStatus(1, "s1", "l1", restarted...)}}
	result, err := New(client).RunHeartbeatAction(context.Background(), "n2", "hb#1", HeartbeatRestart, 5*time.Second)
	if err != nil || !result.Completed || len(result.States) != 2 || result.States[0].UpdatedAt != "u2" || !result.States[0].Peers[0].IsBeating || client.posts[0] != "/api/node/name/n2/daemon/hb/name/hb#1/action/restart" {
		t.Fatalf("restart: %+v %v", result, err)
	}
	// A stream updated by a peer, not registered again, is not restarted.
	peerChanged := []stream{{"hb#1.rx", "running", "t1", false, "u2"}, {"hb#1.tx", "running", "t1", false, "u2"}, both[2], both[3]}
	client = &daemonActionClient{statuses: []string{nodeDaemonStatus(1, "s1", "l1", both...), nodeDaemonStatus(1, "s1", "l1", peerChanged...)}}
	if result, err := New(client).RunHeartbeatAction(context.Background(), "n2", "hb#1", HeartbeatRestart, time.Second); err != nil || result.Completed {
		t.Fatalf("peer change taken for a restart: %+v %v", result, err)
	}
	// A last read failing is told.
	client = &daemonActionClient{statuses: []string{nodeDaemonStatus(1, "s1", "l1", both...)}, statusErrs: 100}
	if result, err := New(client).RunHeartbeatAction(context.Background(), "n2", "hb#1", HeartbeatRestart, time.Second); err != nil || result.Completed || result.StatusError == "" {
		t.Fatalf("read failing: %+v %v", result, err)
	}
	// The refusal of the daemon is given as it says it.
	client = &daemonActionClient{statuses: []string{nodeDaemonStatus(1, "s1", "l1", both...)}, postErr: daemonProblemError{http.StatusBadRequest, "unknown heartbeat stream: hb#9, expected hb#1 or hb#2"}}
	if _, err := New(client).RunHeartbeatAction(context.Background(), "n2", "hb#9", HeartbeatStart, 0); err == nil || !strings.Contains(err.Error(), "expected hb#1 or hb#2") {
		t.Fatalf("unknown: %v", err)
	}
	for _, name := range []string{"hb#1.zz", "a/b", ""} {
		client := &daemonActionClient{statuses: []string{nodeDaemonStatus(1, "s1", "l1", both...)}}
		if _, err := New(client).RunHeartbeatAction(context.Background(), "n2", name, HeartbeatStart, 0); err == nil || len(client.posts) != 0 {
			t.Fatalf("%q accepted", name)
		}
	}
}

func TestRestartDaemonListenerAndDaemon(t *testing.T) {
	// The API is cut a moment: a read failing does not end the wait. The
	// listener keeps its creation time; its update time moves.
	client := &daemonActionClient{statuses: []string{nodeDaemonStatus(1, "s1", "l1"), nodeDaemonStatus(1, "s1", "l2")}, statusErrs: 1}
	listener, err := New(client).RestartDaemonListener(context.Background(), "n2", 5*time.Second)
	if err != nil || client.posts[0] != "/api/node/name/n2/daemon/listener/name/api.inet/action/restart" || !listener.Completed || listener.Port != "1215" || listener.StatusError != "" {
		t.Fatalf("listener: %+v %v", listener, err)
	}
	// The listener closes the connection while it restarts: the answer is
	// lost, the restart seen all the same.
	client = &daemonActionClient{statuses: []string{nodeDaemonStatus(1, "s1", "l1"), nodeDaemonStatus(1, "s1", "l2")}, postErr: daemonProblemError{http.StatusInternalServerError, "dev5n2: Post \"https://dev5n2:1215/...\": EOF"}}
	listener, err = New(client).RestartDaemonListener(context.Background(), "n2", 5*time.Second)
	if err != nil || !listener.Completed || !strings.Contains(listener.AnswerError, "EOF") {
		t.Fatalf("lost answer: %+v %v", listener, err)
	}
	// A creation time moving alone is a restart too.
	client = &daemonActionClient{statuses: []string{nodeDaemonStatus(1, "s1", "c1|u"), nodeDaemonStatus(1, "s1", "c2|u")}}
	if listener, err := New(client).RestartDaemonListener(context.Background(), "n2", 5*time.Second); err != nil || !listener.Completed {
		t.Fatalf("created: %+v %v", listener, err)
	}
	// A failure without a restart seen is an error.
	client = &daemonActionClient{statuses: []string{nodeDaemonStatus(1, "s1", "l1")}, postErr: daemonProblemError{http.StatusForbidden, "root required"}}
	if _, err := New(client).RestartDaemonListener(context.Background(), "n2", time.Second); err == nil || !strings.Contains(err.Error(), "root required") {
		t.Fatalf("refused: %v", err)
	}
	client = &daemonActionClient{statuses: []string{nodeDaemonStatus(100, "s1", "l1"), nodeDaemonStatus(200, "s2", "l1")}, statusErrs: 2}
	restart, err := New(client).RestartDaemon(context.Background(), "n2", 10*time.Second)
	if err != nil || client.posts[0] != "/api/node/name/n2/daemon/action/restart" || !restart.Restarted || restart.PreviousPID != 100 || restart.PID != 200 || restart.StartedAt != "s2" {
		t.Fatalf("daemon: %+v %v", restart, err)
	}
	// A daemon not seen again within the wait is told, the restart launched.
	client = &daemonActionClient{statuses: []string{nodeDaemonStatus(100, "s1", "l1")}, statusErrs: 100}
	restart, err = New(client).RestartDaemon(context.Background(), "n2", time.Second)
	if err != nil || restart.Restarted || restart.StatusError == "" || len(client.posts) != 1 {
		t.Fatalf("not seen: %+v %v", restart, err)
	}
}
