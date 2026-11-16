package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

type instanceLogsClient struct {
	t          *testing.T
	path       string
	query      url.Values
	eventTypes []string
	events     [][]byte
	err        error
	calls      int
}

func (f *instanceLogsClient) GetJSON(_ context.Context, path string, _ url.Values, _ any) error {
	recordRoute("GET", path)
	f.t.Helper()
	return fmt.Errorf("unexpected JSON request")
}

func (f *instanceLogsClient) GetSSE(_ context.Context, path string, query url.Values, consume func(string, string, []byte) error) error {
	recordRoute("GET", path)
	f.t.Helper()
	f.calls++
	if path != f.path {
		return fmt.Errorf("got SSE path %q, want %q", path, f.path)
	}
	if !reflect.DeepEqual(query, f.query) {
		return fmt.Errorf("got SSE query %#v, want %#v", query, f.query)
	}
	if f.err != nil {
		return f.err
	}
	for index, event := range f.events {
		eventType := "log"
		if index < len(f.eventTypes) {
			eventType = f.eventTypes[index]
		}
		if err := consume(eventType, "0", event); err != nil {
			return err
		}
	}
	return nil
}

func TestGetInstanceLogs(t *testing.T) {
	client := &instanceLogsClient{
		t:     t,
		path:  "/api/node/name/node-a/instance/path/example/svc/cache/log",
		query: url.Values{"follow": {"false"}, "lines": {"3"}},
		events: [][]byte{
			instanceLogEvent(t, "2026-07-15T10:00:00Z", "old", "daemon/imon", ""),
			instanceLogEvent(t, "2026-07-15T10:01:00Z", "status failed", "daemon/imon", "container#redis"),
			instanceLogEvent(t, "2026-07-15T10:02:00Z", "instance down", "daemon/imon", "container#redis"),
		},
	}

	result, err := New(client).GetInstanceLogs(context.Background(), GetInstanceLogsOptions{
		Path: " example/svc/cache ", Node: " node-a ", Lines: 2,
	})
	if err != nil {
		t.Fatalf("get instance logs: %v", err)
	}
	if client.calls != 1 || result.Object.Path != "example/svc/cache" || result.Node != "node-a" || result.Lines != 2 || result.Count != 2 || !result.Truncated {
		t.Fatalf("got unexpected log result %+v client=%+v", result, client)
	}
	if result.Entries[0].Message != "status failed" || result.Entries[1].Message != "instance down" {
		t.Errorf("got unexpected entries %+v", result.Entries)
	}
	entry := result.Entries[0]
	if entry.Timestamp != "2026-07-15T10:01:00Z" || entry.Level != "error" || entry.Component != "daemon/imon" || entry.ResourceID != "container#redis" || entry.SessionID != "session-1" || entry.RequestID != "request-1" {
		t.Errorf("got unexpected projected entry %+v", entry)
	}
}

func TestGetInstanceLogsUsesOuterFieldsWhenNestedPayloadIsAbsent(t *testing.T) {
	payload, err := json.Marshal(daemonInstanceLogEnvelope{
		Timestamp: "2026-07-15T10:00:00Z", Level: "WARN", Message: " outer\nmessage ",
		Node: "node-a", Object: "example/svc/cache", Component: "daemon/imon",
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	client := &instanceLogsClient{
		t: t, path: "/api/node/name/node-a/instance/path/example/svc/cache/log",
		query: url.Values{"follow": {"false"}, "lines": {"51"}}, events: [][]byte{payload},
	}
	result, err := New(client).GetInstanceLogs(context.Background(), GetInstanceLogsOptions{Path: "example/svc/cache", Node: "node-a"})
	if err != nil {
		t.Fatalf("get instance logs: %v", err)
	}
	if result.Lines != defaultGetInstanceLogsLines || result.Entries[0].Message != "outer message" || result.Entries[0].Level != "warn" {
		t.Errorf("got unexpected fallback entry %+v", result)
	}
}

func TestGetInstanceLogsBoundsMessage(t *testing.T) {
	client := &instanceLogsClient{
		t: t, path: "/api/node/name/node-a/instance/path/example/svc/cache/log",
		query:  url.Values{"follow": {"false"}, "lines": {"2"}},
		events: [][]byte{instanceLogEvent(t, "2026-07-15T10:00:00Z", strings.Repeat("é", maxInstanceLogMessageRunes+1), "daemon/imon", "")},
	}
	result, err := New(client).GetInstanceLogs(context.Background(), GetInstanceLogsOptions{Path: "example/svc/cache", Node: "node-a", Lines: 1})
	if err != nil {
		t.Fatalf("get instance logs: %v", err)
	}
	message := []rune(result.Entries[0].Message)
	if !result.Truncated || !result.Entries[0].MessageTruncated || len(message) != maxInstanceLogMessageRunes || message[len(message)-1] != '…' {
		t.Errorf("message was not bounded correctly: %+v", result)
	}
}

func TestGetInstanceLogsRejectsInvalidInputBeforeDaemonCall(t *testing.T) {
	for _, test := range []struct {
		name    string
		options GetInstanceLogsOptions
	}{
		{name: "node", options: GetInstanceLogsOptions{Path: "example/svc/cache"}},
		{name: "lines", options: GetInstanceLogsOptions{Path: "example/svc/cache", Node: "node-a", Lines: maxGetInstanceLogsLines + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &instanceLogsClient{t: t}
			if _, err := New(client).GetInstanceLogs(context.Background(), test.options); err == nil {
				t.Fatal("GetInstanceLogs succeeded, want an error")
			}
			if client.calls != 0 {
				t.Fatalf("got %d daemon calls, want 0", client.calls)
			}
		})
	}
}

func TestGetInstanceLogsPropagatesSSEAndPayloadErrors(t *testing.T) {
	want := errors.New("SSE failed")
	client := &instanceLogsClient{
		t: t, path: "/api/node/name/node-a/instance/path/example/svc/cache/log",
		query: url.Values{"follow": {"false"}, "lines": {"51"}}, err: want,
	}
	_, err := New(client).GetInstanceLogs(context.Background(), GetInstanceLogsOptions{Path: "example/svc/cache", Node: "node-a"})
	if !errors.Is(err, want) {
		t.Fatalf("got error %v, want SSE error", err)
	}

	client.err = nil
	client.eventTypes = []string{"unexpected"}
	client.events = [][]byte{[]byte(`{}`)}
	if _, err := New(client).GetInstanceLogs(context.Background(), GetInstanceLogsOptions{Path: "example/svc/cache", Node: "node-a"}); err == nil {
		t.Fatal("GetInstanceLogs accepted an unexpected SSE event")
	}

	client.eventTypes = nil
	client.events = [][]byte{[]byte(`{"JSON":"not-json"}`)}
	if _, err := New(client).GetInstanceLogs(context.Background(), GetInstanceLogsOptions{Path: "example/svc/cache", Node: "node-a"}); err == nil {
		t.Fatal("GetInstanceLogs accepted malformed nested JSON")
	}
}

func instanceLogEvent(t *testing.T, timestamp string, message string, component string, resourceID string) []byte {
	t.Helper()
	nested, err := json.Marshal(daemonInstanceLogPayload{
		Timestamp: timestamp, Level: "ERROR", Message: message,
		Node: "node-a", Object: "example/svc/cache", Component: component,
		ResourceID: resourceID, SessionID: "session-1", EventID: "event-1",
		RequestID: "request-1", OrchestrationID: "orchestration-1",
	})
	if err != nil {
		t.Fatalf("marshal nested log: %v", err)
	}
	envelope, err := json.Marshal(daemonInstanceLogEnvelope{
		JSON: string(nested), Message: "raw journald message", Node: "node-a", Object: "example/svc/cache",
	})
	if err != nil {
		t.Fatalf("marshal log envelope: %v", err)
	}
	return envelope
}

func TestGetInstanceLogsReportsTheSessionAndExecutionIDs(t *testing.T) {
	event := `{"__REALTIME_TIMESTAMP":"1789732800000000","MESSAGE":"stopped","NODE":"node-a","OBJ_PATH":"prod/svc/app","SESSION_ID":"ee34b12e-9f4b-4b39-9fd9-bd01bca40a99","EXEC_ID":"2fc52a5a-741b-414a-9325-a382bab43282","JSON":"{\"message\":\"stopped\",\"session_id\":\"ee34b12e-9f4b-4b39-9fd9-bd01bca40a99\",\"exec_id\":\"2fc52a5a-741b-414a-9325-a382bab43282\"}"}`
	client := &instanceLogsClient{
		t: t, path: "/api/node/name/node-a/instance/path/prod/svc/app/log",
		query: url.Values{"follow": {"false"}, "lines": {"51"}}, events: [][]byte{[]byte(event)},
	}
	result, err := New(client).GetInstanceLogs(context.Background(), GetInstanceLogsOptions{Path: "prod/svc/app", Node: "node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if entry := result.Entries[0]; entry.SessionID != "ee34b12e-9f4b-4b39-9fd9-bd01bca40a99" || entry.ExecID != "2fc52a5a-741b-414a-9325-a382bab43282" {
		t.Fatalf("ids not reported: %+v", entry)
	}
}
