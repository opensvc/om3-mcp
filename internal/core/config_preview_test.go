package core

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// previewClient answers the keyword reads of a preview from one
// configuration: the keywords asked, or all of them, and counts the reads.
type previewClient struct {
	object string
	items  []daemonObjectConfigItem
	reads  []url.Values
	paths  []string
}

func (c *previewClient) GetJSON(_ context.Context, path string, query url.Values, output any) error {
	recordRoute("GET", path)
	c.reads, c.paths = append(c.reads, query), append(c.paths, path)
	items := []daemonObjectConfigItem{}
	for _, item := range c.items {
		item.Object = c.object
		if len(query["kw"]) == 0 || slices.Contains(query["kw"], item.Keyword) {
			items = append(items, item)
		}
	}
	data, _ := json.Marshal(map[string]any{"kind": "KeywordList", "items": items})
	return json.Unmarshal(data, output)
}

func changeOf(t *testing.T, preview Preview, key string) PreviewChange {
	t.Helper()
	for _, change := range preview.Changes {
		if change.Key == key {
			return change
		}
	}
	t.Fatalf("no change of %s in %+v", key, preview.Changes)
	return PreviewChange{}
}

func value(text *string) string {
	if text == nil {
		return "<absent>"
	}
	return *text
}

func TestApplyConfigOperationAsOm3Does(t *testing.T) {
	for _, test := range []struct {
		before, op, value string
		index             int
		after             string
		computed          bool
		note              string
	}{
		{before: "a b", op: "=", value: "c", after: "c", computed: true},
		{before: "a b", op: "+=", value: "b", after: "a b b", computed: true},
		{before: "", op: "+=", value: "a", after: "a", computed: true},
		{before: "a b a", op: "-=", value: "a", after: "b", computed: true},
		{before: "a b", op: "-=", value: "c", after: "a b", computed: true, note: "not in the list"},
		{before: "a b", op: "|=", value: "b", after: "a b", computed: true, note: "already in the list"},
		{before: "a b", op: "|=", value: "c", after: "a b c", computed: true},
		{before: "a b", op: "^=", value: "a", after: "b", computed: true},
		{before: "a b", op: "^=", value: "c", after: "a b c", computed: true},
		{before: "a c", op: "insert", value: "b", index: 1, after: "a b c", computed: true},
		{before: "a b", op: "insert", value: "c", index: 2, after: "a b c", computed: true},
		{before: "a b", op: "insert", value: "", index: 0, after: "a b", computed: true, note: "removes nothing"},
		{before: "a b", op: "insert", value: "c", index: 3, computed: false, note: "past the 2 words"},
	} {
		after, computed, note := applyConfigOperation(test.before, true, configOperation{op: test.op, value: test.value, index: test.index})
		if after != test.after || computed != test.computed || !strings.Contains(note.text, test.note) || (test.note != "" && note.key == "") {
			t.Errorf("%q %s %q [%d] = %q %v %q, want %q %v %q", test.before, test.op, test.value, test.index, after, computed, note, test.after, test.computed, test.note)
		}
	}
	// A value not known in full gives no result, but for an =.
	if _, computed, _ := applyConfigOperation("a…", false, configOperation{op: "+=", value: "b"}); computed {
		t.Fatal("+= computed from a truncated value")
	}
	if after, computed, _ := applyConfigOperation("a…", false, configOperation{op: "=", value: "b"}); !computed || after != "b" {
		t.Fatal("= not computed from a truncated value")
	}
}

func TestParseConfigUpdateSplitsAsTheDaemon(t *testing.T) {
	update, err := parseConfigUpdate([]string{"env.list+=b", "fs#1.mnt@n1=/srv", "env.x='a b'", "env.list[2]=c", "env.y=a=b", "env.z|=c", `env.q="a"b"`}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []configOperation{
		{key: "env.list", base: "env.list", op: "+=", value: "b", index: -1},
		{key: "fs#1.mnt@n1", base: "fs#1.mnt", scope: "n1", op: "=", value: "/srv", index: -1},
		{key: "env.x", base: "env.x", op: "=", value: "a b", index: -1},
		{key: "env.list", base: "env.list", op: "insert", value: "c", index: 2},
		{key: "env.y", base: "env.y", op: "=", value: "a=b", index: -1},
		{key: "env.z", base: "env.z", op: "|=", value: "c", index: -1},
		// A quote inside the value: om3 strips none, nor reads an escape.
		{key: "env.q", base: "env.q", op: "=", value: `"a"b"`, index: -1},
	}
	if !slices.Equal(update.sets, want) {
		t.Fatalf("sets = %+v", update.sets)
	}
	for _, bad := range [][]string{{"env.x"}, {"env.x!=a"}, {"env.list[1]+=a"}} {
		if _, err := parseConfigUpdate(bad, nil, nil); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if _, err := parseConfigUpdate(nil, nil, nil); err == nil {
		t.Error("an empty update accepted")
	}
}

func TestPreviewObjectConfigUpdate(t *testing.T) {
	client := &previewClient{object: "test/svc/web", items: []daemonObjectConfigItem{
		{Keyword: "env.list", Value: "a"},
		{Keyword: "fs#1.mnt@n1", Value: "/old"},
		{Keyword: "fs#2.type", Value: "flag"},
		{Keyword: "fs#2.dev", Value: "/dev/x"},
		{Keyword: "fs#3.type", Value: "flag"},
		{Keyword: "env.empty", Value: ""},
	}}
	service := New(client)
	preview, err := service.PreviewObjectConfigUpdate(t.Context(), UpdateObjectConfigOptions{
		Path:   "test/svc/web",
		Set:    []string{"env.list+=b", "env.list|=c", "fs#1.mnt@n1=/srv", "env.absent=x", "fs#2.type=zfs", "env.empty+=z"},
		Unset:  []string{"env.gone"},
		Delete: []string{"fs#2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The keys named are read once, the whole configuration once for the delete.
	if len(client.reads) != 2 || len(client.reads[0]["kw"]) != 6 || client.reads[0].Get("evaluate") != "false" || client.reads[1].Has("kw") {
		t.Fatalf("reads = %v", client.reads)
	}
	if preview.Summary.Key != "config_update" || preview.Summary.Text != "Update the configuration of test/svc/web: 6 set, 1 unset, 1 sections deleted" ||
		preview.Summary.Params["path"] != "test/svc/web" || preview.Summary.Params["sets"] != "6" {
		t.Fatalf("summary = %+v", preview.Summary)
	}
	if len(preview.Changes) != 7 || preview.Changes[0].Op != "unset" {
		t.Fatalf("changes = %+v", preview.Changes)
	}
	// Two operations on one key: the second starts from the first.
	appended := preview.Changes[1]
	if appended.Key != "env.list" || value(appended.Before) != "a" || value(appended.After) != "a b" || !appended.AfterComputedByMCP {
		t.Fatalf("+= = %+v", appended)
	}
	if merged := preview.Changes[2]; value(merged.Before) != "a b" || value(merged.After) != "a b c" {
		t.Fatalf("|= = %+v", merged)
	}
	if scoped := changeOf(t, preview, "fs#1.mnt@n1"); scoped.Scope != "n1" || value(scoped.Before) != "/old" || value(scoped.After) != "/srv" {
		t.Fatalf("scoped = %+v", scoped)
	}
	if absent := changeOf(t, preview, "env.absent"); value(absent.Before) != "" || absent.Note != notSetNote.text || !slices.Equal(absent.NoteKeys, []string{"not_set"}) || value(absent.After) != "x" {
		t.Fatalf("absent = %+v", absent)
	}
	if unset := changeOf(t, preview, "env.gone"); value(unset.After) != "" || unset.Note != notSetNote.text {
		t.Fatalf("unset = %+v", unset)
	}
	if empty := changeOf(t, preview, "env.empty"); empty.Note != emptyOrNotSetNote.text || value(empty.After) != "z" {
		t.Fatalf("empty = %+v", empty)
	}
	// The daemon deletes before it sets: the key of a deleted section starts empty.
	if inDeleted := changeOf(t, preview, "fs#2.type"); value(inDeleted.Before) != "" || !slices.Equal(inDeleted.NoteKeys, []string{"section_deleted_first"}) {
		t.Fatalf("set in a deleted section = %+v", inDeleted)
	}
	if len(preview.Deleted) != 1 || preview.Deleted[0].Section != "fs#2" || strings.Join(preview.Deleted[0].Keys, ",") != "fs#2.dev,fs#2.type" {
		t.Fatalf("deleted = %+v", preview.Deleted)
	}
	if preview.ReadAt == "" || preview.Provenance.Source != provenanceSourceOpenSVCDaemon {
		t.Fatalf("read at %q, provenance %+v", preview.ReadAt, preview.Provenance)
	}
}

func TestPreviewObjectConfigUpdateHidesSecrets(t *testing.T) {
	client := &previewClient{object: "cluster", items: []daemonObjectConfigItem{
		{Keyword: "cluster.secret", Value: "s3cret"},
		{Keyword: "cluster.name", Value: "dev5"},
	}}
	preview, err := New(client).PreviewObjectConfigUpdate(t.Context(), UpdateObjectConfigOptions{Path: "cluster", Set: []string{"cluster.secret=new", "cluster.name=lab"}})
	if err != nil {
		t.Fatal(err)
	}
	secret := changeOf(t, preview, "cluster.secret")
	if secret.Before != nil || secret.After != nil || secret.Value != "" || secret.Note != secretNote.text || !slices.Equal(secret.NoteKeys, []string{"secret"}) || preview.SecretsOmitted != 1 {
		t.Fatalf("secret = %+v, omitted %d", secret, preview.SecretsOmitted)
	}
	// The secret is not even asked of the daemon.
	if slices.Contains(client.reads[0]["kw"], "cluster.secret") {
		t.Fatalf("reads = %v", client.reads)
	}
	data, _ := json.Marshal(preview)
	if strings.Contains(string(data), "s3cret") || strings.Contains(string(data), `"new"`) {
		t.Fatalf("preview exposes a secret: %s", data)
	}
	// The data keys of a secret are hidden the same way.
	client = &previewClient{object: "test/sec/db"}
	preview, err = New(client).PreviewObjectConfigUpdate(t.Context(), UpdateObjectConfigOptions{Path: "test/sec/db", Set: []string{"data.password=x"}})
	if err != nil || changeOf(t, preview, "data.password").Note != secretNote.text || len(client.reads) != 0 {
		t.Fatalf("data key = %+v, %v, reads %v", preview, err, client.reads)
	}
}

func TestPreviewCountsTheSecretsOfADeletedSection(t *testing.T) {
	client := &previewClient{object: "cluster", items: []daemonObjectConfigItem{
		{Keyword: "cluster.secret", Value: "s3cret"},
		{Keyword: "cluster.name", Value: "dev5"},
	}}
	preview, err := New(client).PreviewObjectConfigUpdate(t.Context(), UpdateObjectConfigOptions{Path: "cluster", Delete: []string{"cluster"}})
	if err != nil || len(preview.Deleted) != 1 {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	if deleted := preview.Deleted[0]; strings.Join(deleted.Keys, ",") != "cluster.name" || deleted.SecretsOmitted != 1 || preview.SecretsOmitted != 1 {
		t.Fatalf("deleted = %+v, omitted %d", deleted, preview.SecretsOmitted)
	}
}

func TestPreviewObjectConfigUpdateWarnsOfATruncatedRead(t *testing.T) {
	client := &previewClient{object: "test/svc/web"}
	for index := range maxGetObjectConfigLimit + 1 {
		client.items = append(client.items, daemonObjectConfigItem{Keyword: "env.k" + strings.Repeat("x", index%7) + string(rune('a'+index%26)) + strings.Repeat("y", index/26)})
	}
	preview, err := New(client).PreviewObjectConfigUpdate(t.Context(), UpdateObjectConfigOptions{Path: "test/svc/web", Delete: []string{"env"}})
	if err != nil || len(preview.Warnings) != 1 || !strings.Contains(preview.Warnings[0], "may be more") {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
}

func TestPreviewNodeConfigUpdate(t *testing.T) {
	// The daemon names the node of a node read in Node, the scope of a key
	// in its keyword.
	client := &previewClient{items: []daemonObjectConfigItem{
		{Keyword: "node.env", Node: "n1", Value: "TST"},
		{Keyword: "hb#1.timeout@n1", Node: "n1", Value: "10s"},
		{Keyword: "node.uuid", Node: "n1", Value: "u-1"},
	}}
	preview, err := New(client).PreviewNodeConfigUpdate(t.Context(), UpdateNodeConfigOptions{Node: "n1", Set: []string{"node.env=PRD", "hb#1.timeout@n1=20s"}, Unset: []string{"node.uuid"}})
	if err != nil {
		t.Fatal(err)
	}
	if client.paths[0] != "/api/node/name/n1/config" || preview.Summary.Key != "node_config_update" || preview.Summary.Params["node"] != "n1" {
		t.Fatalf("paths %v, summary %+v", client.paths, preview.Summary)
	}
	if env := changeOf(t, preview, "node.env"); value(env.Before) != "TST" || value(env.After) != "PRD" || env.Note != "" {
		t.Fatalf("env = %+v", env)
	}
	if timeout := changeOf(t, preview, "hb#1.timeout@n1"); value(timeout.Before) != "10s" || timeout.Scope != "n1" {
		t.Fatalf("scoped = %+v", timeout)
	}
	if uuid := changeOf(t, preview, "node.uuid"); uuid.Before != nil || preview.SecretsOmitted != 1 {
		t.Fatalf("uuid = %+v", uuid)
	}
}

func TestPreviewObjectCreate(t *testing.T) {
	config := "[DEFAULT]\nnodes = n1 n2\n\n[fs#1]\ntype = flag\n"
	preview, err := New(&previewClient{}).PreviewObjectCreate(CreateObjectOptions{Path: "test/svc/new", Config: config, Set: []string{"env.a=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Content == nil || preview.Content.MediaType != "text/x-ini" || preview.Content.Text != config || preview.Content.Truncated {
		t.Fatalf("content = %+v", preview.Content)
	}
	if preview.Summary.Key != "object_create" || preview.Summary.Params["kind"] != "svc" || preview.Summary.Params["sections"] != "2" ||
		preview.Summary.Text != "Create test/svc/new, a svc of 2 sections" {
		t.Fatalf("summary = %+v", preview.Summary)
	}
	if set := changeOf(t, preview, "env.a"); set.Before != nil || set.Value != "1" || !slices.Equal(set.NoteKeys, []string{"applied_after_file"}) {
		t.Fatalf("set = %+v", set)
	}
	// The file alone, without set operations, as a model mostly gives it.
	preview, err = New(&previewClient{}).PreviewObjectCreate(CreateObjectOptions{Path: "test/svc/new", Config: config})
	if err != nil || preview.Content == nil || len(preview.Changes) != 0 {
		t.Fatalf("file alone = %+v, %v", preview, err)
	}
	if _, err := New(&previewClient{}).PreviewObjectCreate(CreateObjectOptions{Path: "test/svc/new"}); err == nil {
		t.Fatal("a create without config previewed")
	}
}

func TestPreviewAction(t *testing.T) {
	service := New(&previewClient{})
	preview, err := service.PreviewAction(PreviewActionOptions{Tool: "stop_instance", Arguments: map[string]any{
		"path": "test/svc/web", "node": "n1", "rids": []any{"fs#1", "fs#2"}, "wait_seconds": float64(10),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Summary.Text != "Stop test/svc/web on n1 (fs#1, fs#2)" || preview.Summary.Key != "stop_instance" || preview.Summary.Params["rids"] != "fs#1, fs#2" {
		t.Fatalf("summary = %+v", preview.Summary)
	}
	preview, err = service.PreviewAction(PreviewActionOptions{Tool: "switch_object", Arguments: map[string]any{"path": "web", "to": "n2"}})
	if err != nil || preview.Summary.Text != "Switch web to n2" {
		t.Fatalf("switch = %+v, %v", preview.Summary, err)
	}
	// A value is put in the sentence as given, never read as a placeholder.
	preview, err = service.PreviewAction(PreviewActionOptions{Tool: "stop_object", Arguments: map[string]any{"path": "{node} **x**"}})
	if err != nil || preview.Summary.Text != "Stop {node} **x**" {
		t.Fatalf("untrusted = %+v, %v", preview.Summary, err)
	}
	for _, options := range []PreviewActionOptions{
		{Tool: "get_node_status", Arguments: map[string]any{"node": "n1"}},
		{Tool: "stop_instance", Arguments: map[string]any{"path": "web"}},
		{Tool: "stop_instance", Arguments: map[string]any{"path": "web", "node": map[string]any{}}},
	} {
		if _, err := service.PreviewAction(options); err == nil {
			t.Errorf("%+v previewed", options)
		}
	}
}
