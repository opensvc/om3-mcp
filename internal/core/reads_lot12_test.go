package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// readsClient answers the reads of lot 12 by path, records them, and serves
// the raw files.
type readsClient struct {
	payloads map[string]string
	errs     map[string]error
	blob     []byte
	paths    []string
	queries  []url.Values
}

func (c *readsClient) GetJSON(_ context.Context, path string, query url.Values, output any) error {
	recordRoute("GET", path)
	c.paths = append(c.paths, path)
	c.queries = append(c.queries, query)
	if err := c.errs[path]; err != nil {
		return err
	}
	payload, ok := c.payloads[path]
	if !ok {
		return fmt.Errorf("unexpected read %s", path)
	}
	return json.Unmarshal([]byte(payload), output)
}

func (c *readsClient) GetBlob(_ context.Context, path string, query url.Values) ([]byte, string, bool, error) {
	recordRoute("GET", path)
	c.paths = append(c.paths, path)
	c.queries = append(c.queries, query)
	return c.blob, "application/octet-stream", true, nil
}

func (c *readsClient) last() (string, url.Values) {
	return c.paths[len(c.paths)-1], c.queries[len(c.queries)-1]
}

func TestListInstanceSyncSnapshots(t *testing.T) {
	endpoint := "/api/node/name/n1/instance/path/test/svc/store/sync/snapshots"
	client := &readsClient{payloads: map[string]string{endpoint: `{"kind":"SyncSnapshotList","items":[
		{"meta":{"node":"n1","object":"test/svc/store"},"data":{"rid":"sync#1","id":"old","created_at":"2026-10-22T03:00:00Z","node":"n1","members":["fs#1"],"location":"mcp"}},
		{"meta":{"node":"n1","object":"test/svc/store"},"data":{"rid":"sync#1","id":"7c8acf03","created_at":"2026-10-23T14:46:48+02:00","node":"n1","members":["fs#1","config"],"location":"mcp"}}]}`}}
	result, err := New(client).ListInstanceSyncSnapshots(context.Background(), ListInstanceSyncSnapshotsOptions{Path: "test/svc/store", Node: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, query := client.last(); query.Get("rid") != "sync" {
		t.Fatalf("rid %v", query)
	}
	if result.Total != 2 || result.Snapshots[0].ID != "7c8acf03" || strings.Join(result.Snapshots[0].Members, ",") != "fs#1,config" || result.RIDFilter != "sync" {
		t.Fatalf("result %+v", result)
	}
	// A driver in error is relayed.
	client = &readsClient{errs: map[string]error{endpoint: daemonProblemError{http.StatusInternalServerError, "sync#1: stat /srv/store: no such file or directory"}}}
	if _, err := New(client).ListInstanceSyncSnapshots(context.Background(), ListInstanceSyncSnapshotsOptions{Path: "test/svc/store", Node: "n1", RID: "sync#1"}); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("driver error: %v", err)
	}
	for _, rid := range []string{"fs#1", "sync*", "sync#a/b"} {
		client := &readsClient{}
		if _, err := New(client).ListInstanceSyncSnapshots(context.Background(), ListInstanceSyncSnapshotsOptions{Path: "test/svc/store", Node: "n1", RID: rid}); err == nil || len(client.paths) != 0 {
			t.Errorf("rid %q accepted", rid)
		}
	}
	client = &readsClient{payloads: map[string]string{endpoint: `{"items":[{"meta":{"node":"n1","object":"test/svc/other"},"data":{}}]}`}}
	if _, err := New(client).ListInstanceSyncSnapshots(context.Background(), ListInstanceSyncSnapshotsOptions{Path: "test/svc/store", Node: "n1"}); err == nil {
		t.Fatal("a snapshot of another object was taken")
	}
}

func TestGetRelayStatus(t *testing.T) {
	client := &readsClient{payloads: map[string]string{"/api/relay/status": `{"kind":"RelayStatusList","items":[
		{"relay":"https://hb:s3cret@relay2:1215","cluster_id":"b","cluster_name":"c2","nodename":"n2","node_addr":"10.0.0.2","msg_len":512,"updated_at":"2026-10-23T12:00:00Z","username":"hb","status":""},
		{"relay":"https://relay1:1215","cluster_id":"a","cluster_name":"c1","nodename":"n1","node_addr":"10.0.0.1","msg_len":0,"updated_at":"2026-10-23T11:00:00Z","username":"hb","status":"dial tcp: connection refused"}]}`}}
	result, err := New(client).GetRelayStatus(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, query := client.last(); query.Get("remote") != "true" {
		t.Fatalf("query %v", query)
	}
	if result.Total != 2 || result.Slots[0].Relay != "https://relay1:1215" || result.Slots[0].Status == "" || result.Slots[1].Relay != "https://relay2:1215" {
		t.Fatalf("result %+v", result)
	}
	if out, _ := json.Marshal(result); strings.Contains(string(out), "s3cret") {
		t.Fatalf("relay credentials returned: %s", out)
	}
	if got := relayURLWithoutCredentials("https://u:p@[bad"); strings.Contains(got, "u:p") {
		t.Fatalf("unparsable URL kept its credentials: %s", got)
	}
	client = &readsClient{payloads: map[string]string{"/api/relay/status": `{"items":[]}`}}
	if result, err := New(client).GetRelayStatus(context.Background(), false); err != nil || result.Remote || len(client.queries[0]) != 0 || result.Slots == nil {
		t.Fatalf("local: %+v %v", result, err)
	}
}

func TestListNodeNetworkReservations(t *testing.T) {
	endpoint := "/api/node/name/n1/network/reservation"
	client := &readsClient{payloads: map[string]string{endpoint: `{"kind":"NetworkReservationList","items":[
		{"network":"default","ip":"10.22.238.228","path":"test/svc/netns","rid":"ip#1"},
		{"network":"backend","ip":"10.1.0.5","path":"test/svc/web","rid":"ip#2"}]}`}}
	result, err := New(client).ListNodeNetworkReservations(context.Background(), "n1", "")
	if err != nil || result.Total != 2 || result.Reservations[0].Network != "backend" || result.Reservations[1].IP != "10.22.238.228" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := New(client).ListNodeNetworkReservations(context.Background(), "n1", "default"); err != nil {
		t.Fatal(err)
	}
	if _, query := client.last(); query.Get("name") != "default" {
		t.Fatalf("query %v", query)
	}
	client = &readsClient{errs: map[string]error{endpoint: daemonProblemError{http.StatusServiceUnavailable, "the daemon of n1 is starting"}}}
	if _, err := New(client).ListNodeNetworkReservations(context.Background(), "n1", ""); err == nil || !strings.Contains(err.Error(), "starting") {
		t.Fatalf("starting: %v", err)
	}
	for _, tc := range [][2]string{{"_", ""}, {"n1", "def*"}} {
		client := &readsClient{}
		if _, err := New(client).ListNodeNetworkReservations(context.Background(), tc[0], tc[1]); err == nil || len(client.paths) != 0 {
			t.Errorf("%v accepted", tc)
		}
	}
}

func TestListNodeLocks(t *testing.T) {
	client := &readsClient{payloads: map[string]string{"/api/node/name/n2/lock": `{"kind":"ClusterLockList","items":[
		{"name":"network/default","id":"i2","node":"n2","holder":"ip#1","acquired_at":"2026-10-23T12:00:01Z","expires_at":"2026-10-23T12:01:01Z","expires_in":"1m"},
		{"name":"network/default","id":"i1","node":"n2","holder":"ip#2","acquired_at":"2026-10-23T12:00:00Z","expires_at":"2026-10-23T12:01:00Z","expires_in":"1m"}]}`}}
	result, err := New(client).ListNodeLocks(context.Background(), "n2")
	if err != nil || result.Node != "n2" || result.Total != 2 || result.Locks[0].ID != "i1" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := New(&readsClient{}).ListNodeLocks(context.Background(), "_"); err == nil {
		t.Fatal("the alias was accepted")
	}
}

func TestGetNodeDRBDConfigMasksTheSharedSecret(t *testing.T) {
	file := "resource mcpdrbd {\n  net {\n    cram-hmac-alg sha1;\n    shared-secret \"t0p-s3cret\";\n  }\n  on dev5n1 {\n    disk /dev/loop1;\n  }\n}\n"
	data, _ := json.Marshal(map[string][]byte{"data": []byte(file)})
	endpoint := "/api/node/name/n1/drbd/config"
	client := &readsClient{payloads: map[string]string{endpoint: string(data)}}
	result, err := New(client).GetNodeDRBDConfig(context.Background(), "n1", "mcpdrbd")
	if err != nil {
		t.Fatal(err)
	}
	if _, query := client.last(); query.Get("name") != "mcpdrbd" {
		t.Fatalf("query %v", query)
	}
	if result.SecretsMasked != 1 || !strings.Contains(result.Content, `shared-secret "<masked>";`) || !strings.Contains(result.Content, "/dev/loop1") || result.SizeBytes != len(file) {
		t.Fatalf("result %+v", result)
	}
	if out, _ := json.Marshal(result); strings.Contains(string(out), "t0p-s3cret") {
		t.Fatalf("the shared secret was returned: %s", out)
	}
	client = &readsClient{errs: map[string]error{endpoint: daemonProblemError{http.StatusNotFound, "ReadFile /etc/drbd.d/r0.res (may be deleted)"}}}
	if _, err := New(client).GetNodeDRBDConfig(context.Background(), "n1", "r0"); err == nil || !strings.Contains(err.Error(), "may be deleted") {
		t.Fatalf("missing: %v", err)
	}
	for _, name := range []string{"../x", "a/b", `a\b`, "..", ""} {
		client := &readsClient{}
		if _, err := New(client).GetNodeDRBDConfig(context.Background(), "n1", name); err == nil || len(client.paths) != 0 {
			t.Errorf("name %q accepted", name)
		}
	}
}

// resourceFileStatus is the status of test/svc/kvm on n1: disk#2 declares
// sub_devs, container#1 a domain definition, fs#1 nothing.
const resourceFileStatus = `{"kind":"InstanceItem","meta":{"node":"n1","object":"test/svc/kvm"},"data":{
	"config":{"csum":"c","priority":50,"scope":["n1"],"updated_at":"2026-10-23T12:00:00Z"},
	"status":{"avail":"up","overall":"up","provisioned":"true","resources":{
		"disk#2":{"type":"disk.zpool","label":"zpool","status":"up","disable":false,"monitor":false,"provisioned":{"state":"true","mtime":null},"files":[{"name":"/var/lib/opensvc/namespaces/test/svc/kvm/disk#2/sub_devs","csum":"abc","mtime":"2026-10-23T12:00:00Z","ingest":false}]},
		"container#1":{"type":"container.kvm","label":"kvm","status":"up","disable":false,"monitor":false,"provisioned":{"state":"true","mtime":null},"files":[{"name":"/etc/libvirt/qemu/vm1.xml","csum":"def","mtime":null,"ingest":false}]},
		"container#2":{"type":"container.lxc","label":"lxc","status":"up","disable":false,"monitor":false,"provisioned":{"state":"true","mtime":null},"files":[{"name":"/var/lib/lxc/a/config","csum":"1","mtime":null,"ingest":false},{"name":"/var/lib/lxc/b/config","csum":"2","mtime":null,"ingest":false}]},
		"fs#1":{"type":"fs.flag","label":"flag","status":"up","disable":false,"monitor":false,"provisioned":{"state":"true","mtime":null}}}}}}`

func TestGetInstanceResourceFileReadsADeclaredFileOnly(t *testing.T) {
	statusPath := "/api/node/name/n1/instance/path/test/svc/kvm"
	subDevs := "/var/lib/opensvc/namespaces/test/svc/kvm/disk#2/sub_devs"
	client := &readsClient{payloads: map[string]string{statusPath: resourceFileStatus}, blob: []byte(`["/dev/loop0"]`)}
	result, err := New(client).GetInstanceResourceFile(context.Background(), "test/svc/kvm", "n1", "disk#2", subDevs)
	if err != nil {
		t.Fatal(err)
	}
	if path, query := client.last(); path != statusPath+"/resource/file" || query.Get("rid") != "disk#2" || query.Get("name") != subDevs {
		t.Fatalf("read %s %v", path, query)
	}
	if result.Content != `["/dev/loop0"]` || result.Csum != "abc" || result.Binary || result.Truncated || !result.ReadWhole {
		t.Fatalf("result %+v", result)
	}

	// A base name naming one declared file only resolves to it.
	client = &readsClient{payloads: map[string]string{statusPath: resourceFileStatus}, blob: []byte(`["/dev/loop0"]`)}
	result, err = New(client).GetInstanceResourceFile(context.Background(), "test/svc/kvm", "n1", "disk#2", "sub_devs")
	if _, query := client.last(); err != nil || query.Get("name") != subDevs || result.Name != subDevs {
		t.Fatalf("base name: %+v %v %v", result, err, query)
	}

	// The graphics password of a domain definition is masked.
	client = &readsClient{payloads: map[string]string{statusPath: resourceFileStatus}, blob: []byte(`<domain><devices><graphics type='vnc' port='5900' passwd='vncs3cret'/></devices></domain>`)}
	result, err = New(client).GetInstanceResourceFile(context.Background(), "test/svc/kvm", "n1", "container#1", "/etc/libvirt/qemu/vm1.xml")
	if err != nil || result.SecretsMasked != 1 || strings.Contains(result.Content, "vncs3cret") || !strings.Contains(result.Content, `passwd="<masked>"`) {
		t.Fatalf("kvm: %+v %v", result, err)
	}

	// A binary file returns its size and csum only.
	client = &readsClient{payloads: map[string]string{statusPath: resourceFileStatus}, blob: []byte{0x00, 0xff, 0x10}}
	result, err = New(client).GetInstanceResourceFile(context.Background(), "test/svc/kvm", "n1", "disk#2", subDevs)
	if err != nil || !result.Binary || result.Content != "" || result.SizeBytes != 3 {
		t.Fatalf("binary: %+v %v", result, err)
	}

	// A file the resource does not declare is refused before the read,
	// naming those it declares; a resource declaring none says so.
	for _, tc := range []struct{ rid, name, want string }{
		{"disk#2", "/etc/passwd", "it declares " + subDevs + "; call again with one of these names"},
		{"disk#2", "passwd", "it declares " + subDevs},
		// A base name two declared files have stays a refusal listing the
		// full names.
		{"container#2", "config", "it declares /var/lib/lxc/a/config, /var/lib/lxc/b/config; call again with one of these names"},
		{"fs#1", "/etc/passwd", "it declares no file"},
		{"disk#9", "/etc/passwd", "the resources declaring files are container#1, container#2, disk#2"},
	} {
		client := &readsClient{payloads: map[string]string{statusPath: resourceFileStatus}}
		if _, err := New(client).GetInstanceResourceFile(context.Background(), "test/svc/kvm", "n1", tc.rid, tc.name); err == nil || !strings.Contains(err.Error(), tc.want) || len(client.paths) != 1 {
			t.Errorf("%s %s: %v, reads %v", tc.rid, tc.name, err, client.paths)
		}
	}
	for _, tc := range [][3]string{{"test/svc/*", "disk#2", subDevs}, {"test/svc/kvm", "disk", subDevs}, {"test/svc/kvm", "disk#2", ""}} {
		client := &readsClient{}
		if _, err := New(client).GetInstanceResourceFile(context.Background(), tc[0], "n1", tc[1], tc[2]); err == nil || len(client.paths) != 0 {
			t.Errorf("%v accepted", tc)
		}
	}
}
