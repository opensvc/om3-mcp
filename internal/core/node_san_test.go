package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
)

type sanJSONGetter struct {
	payloads map[string]string
	calls    []string
}

func (f *sanJSONGetter) GetJSON(_ context.Context, path string, _ url.Values, output any) error {
	recordRoute("GET", path)
	f.calls = append(f.calls, path)
	payload, ok := f.payloads[path]
	if !ok {
		return errors.New("OpenSVC daemon returned HTTP 404 Not Found: waiting for cached value")
	}
	return json.Unmarshal([]byte(payload), output)
}

// Recorded on a node with one iSCSI initiator reaching two targets.
const (
	sanInitiatorPayload = `{"kind":"SANPathInitiatorList","items":[
		{"kind":"SANPathInitiatorItem","meta":{"node":"node-a"},"data":{"name":"iqn.2009-11.com.opensvc.srv:node-a.storage.initiator","type":"iscsi"}},
		{"kind":"SANPathInitiatorItem","meta":{"node":"node-a"},"data":{"name":"0x500143802426baf4","type":"fc"}}
	]}`
	sanPathPayload = `{"kind":"SANPathList","items":[
		{"initiator":{"name":"iqn.2009-11.com.opensvc.srv:node-a.storage.initiator","type":"iscsi"},"target":{"name":"iqn.2009-11.com.opensvc.srv:node-a.storage.target.2","type":"iscsi"}},
		{"initiator":{"name":"iqn.2009-11.com.opensvc.srv:node-a.storage.initiator","type":"iscsi"},"target":{"name":"iqn.2009-11.com.opensvc.srv:node-a.storage.target.1","type":"iscsi"}},
		{"initiator":{"name":"iqn.2009-11.com.opensvc.srv:node-a.storage.initiator","type":"iscsi"},"target":{"name":"iqn.2009-11.com.opensvc.srv:node-a.storage.target.1","type":"iscsi"}}
	]}`
)

func TestGetNodeSANTopologyReportsInitiatorsPathsAndTargetCounts(t *testing.T) {
	client := &sanJSONGetter{payloads: map[string]string{
		"/api/node/name/node-a/system/san/initiator": sanInitiatorPayload,
		"/api/node/name/node-a/system/san/path":      sanPathPayload,
	}}
	topology, err := New(client).GetNodeSANTopology(context.Background(), GetNodeSANTopologyOptions{Node: "node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if topology.Node != "node-a" || len(topology.Initiators) != 2 || len(topology.Paths) != 3 || topology.InitiatorsTruncated || topology.PathsTruncated {
		t.Fatalf("unexpected topology %+v", topology)
	}
	fc, iscsi := topology.Initiators[0], topology.Initiators[1]
	if fc.Type != "fc" || fc.TargetCount != 0 || iscsi.Type != "iscsi" || iscsi.TargetCount != 2 {
		t.Fatalf("initiators not sorted or target counts wrong: %+v", topology.Initiators)
	}
	if topology.Paths[0].Target.Name != "iqn.2009-11.com.opensvc.srv:node-a.storage.target.1" || topology.Paths[2].Target.Name != "iqn.2009-11.com.opensvc.srv:node-a.storage.target.2" {
		t.Fatalf("paths not sorted by initiator then target: %+v", topology.Paths)
	}
}

func TestGetNodeSANTopologyWithoutSAN(t *testing.T) {
	client := &sanJSONGetter{payloads: map[string]string{
		"/api/node/name/node-b/system/san/initiator": `{"kind":"SANPathInitiatorList","items":[]}`,
		"/api/node/name/node-b/system/san/path":      `{"kind":"SANPathList","items":[]}`,
	}}
	topology, err := New(client).GetNodeSANTopology(context.Background(), GetNodeSANTopologyOptions{Node: "node-b"})
	if err != nil {
		t.Fatal(err)
	}
	if topology.Node != "node-b" || topology.Initiators == nil || topology.Paths == nil || len(topology.Paths) != 0 {
		t.Fatalf("unexpected empty topology %+v", topology)
	}
}

func TestGetNodeSANTopologyKeepsTheDaemonError(t *testing.T) {
	client := &sanJSONGetter{payloads: map[string]string{}}
	_, err := New(client).GetNodeSANTopology(context.Background(), GetNodeSANTopologyOptions{Node: "node-a"})
	if err == nil || len(client.calls) != 1 || !strings.Contains(err.Error(), "SAN initiators") || !strings.Contains(err.Error(), "waiting for cached value") {
		t.Fatalf("missing cache not reported as is: %v", err)
	}
	for _, node := range []string{"n*", "..", "a,b"} {
		client := &sanJSONGetter{}
		if _, err := New(client).GetNodeSANTopology(context.Background(), GetNodeSANTopologyOptions{Node: node}); err == nil || len(client.calls) != 0 {
			t.Fatalf("node %q accepted or reached the daemon", node)
		}
	}
}

func TestGetNodeSANTopologyRejectsInvalidNodeBeforeTheDaemonCall(t *testing.T) {
	for _, node := range []string{"", "_", "n*", ".."} {
		client := &recordingJSONGetter{t: t}
		if _, err := New(client).GetNodeSANTopology(context.Background(), GetNodeSANTopologyOptions{Node: node}); err == nil || client.calls != 0 {
			t.Fatalf("node %q was accepted or reached the daemon", node)
		}
	}
}
