package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
)

// Recorded from dev5, /api/object/path/system/sec/hb/data/keys.
const objectDataKeysPayload = `{"items":[
	{"name":"secret","node":"node-a","object":"system/sec/hb","size":262},
	{"name":"version","node":"node-a","object":"system/sec/hb","size":202},
	{"name":"alt_secret","node":"node-a","object":"system/sec/hb","size":262},
	{"name":"alt_version","node":"node-a","object":"system/sec/hb","size":202}],"kind":"DataKeyList"}`

func TestListObjectDataKeysSortsTheNames(t *testing.T) {
	client := &recordingJSONGetter{t: t, path: "/api/object/path/system/sec/hb/data/keys", query: url.Values{}, payload: objectDataKeysPayload}
	list, err := New(client).ListObjectDataKeys(context.Background(), ListObjectDataKeysOptions{Path: "system/sec/hb"})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list.Keys))
	for _, key := range list.Keys {
		names = append(names, key.Name)
	}
	if strings.Join(names, ",") != "alt_secret,alt_version,secret,version" || list.Node != "node-a" || list.Total != 4 ||
		list.Object.Path != "system/sec/hb" || list.Keys[0].StoredSizeBytes != 262 {
		t.Fatalf("unexpected list %+v", list)
	}
	data, _ := json.Marshal(list)
	if strings.Contains(string(data), `"value`) {
		t.Fatalf("a value field appeared: %s", data)
	}
}

func TestListObjectDataKeysFiltersOneName(t *testing.T) {
	client := &recordingJSONGetter{t: t, path: "/api/object/path/system/sec/hb/data/keys", query: url.Values{}, payload: objectDataKeysPayload}
	list, err := New(client).ListObjectDataKeys(context.Background(), ListObjectDataKeysOptions{Path: "system/sec/hb", Name: "secret"})
	if err != nil || list.Total != 1 || list.Keys[0].Name != "secret" || list.NameFilter != "secret" {
		t.Fatalf("got %+v, %v", list, err)
	}
	client = &recordingJSONGetter{t: t, path: "/api/object/path/system/sec/hb/data/keys", query: url.Values{}, payload: objectDataKeysPayload}
	list, err = New(client).ListObjectDataKeys(context.Background(), ListObjectDataKeysOptions{Path: "system/sec/hb", Name: "absent"})
	if err != nil || list.Total != 0 || list.Keys == nil {
		t.Fatalf("got %+v, %v", list, err)
	}
}

func TestListObjectDataKeysAcceptsAnObjectWithoutKeys(t *testing.T) {
	client := &recordingJSONGetter{t: t, path: "/api/object/path/test/cfg/empty/data/keys", query: url.Values{}, payload: `{"kind":"DataKeyList","items":[]}`}
	list, err := New(client).ListObjectDataKeys(context.Background(), ListObjectDataKeysOptions{Path: "test/cfg/empty"})
	if err != nil || list.Total != 0 || list.Node != "" {
		t.Fatalf("got %+v, %v", list, err)
	}
}

type emptyBodyGetter struct{ calls int }

func (g *emptyBodyGetter) GetJSON(_ context.Context, path string, _ url.Values, _ any) error {
	recordRoute("GET", path)
	g.calls++
	return fmt.Errorf("decode OpenSVC daemon response: %w", io.EOF)
}

func TestListObjectDataKeysReportsAnAbsentObject(t *testing.T) {
	_, err := New(&emptyBodyGetter{}).ListObjectDataKeys(context.Background(), ListObjectDataKeysOptions{Path: "system/sec/absent"})
	if err == nil || !strings.Contains(err.Error(), "object system/sec/absent not found") {
		t.Fatalf("got %v", err)
	}
}

func TestListObjectDataKeysRejectsInvalidInputAndData(t *testing.T) {
	for _, options := range []ListObjectDataKeysOptions{
		{}, {Path: "system/svc/vip"}, {Path: "system/sec/*"}, {Path: "a,b"}, {Path: "system/sec/hb", Name: " secret"},
	} {
		getter := &emptyBodyGetter{}
		if _, err := New(getter).ListObjectDataKeys(context.Background(), options); err == nil || getter.calls != 0 {
			t.Fatalf("options %+v were accepted or reached the daemon", options)
		}
	}
	for _, payload := range []string{
		`{"kind":"KeywordList","items":[]}`,
		`{"kind":"DataKeyList","items":[{"name":"a","node":"node-a","object":"system/sec/other","size":1}]}`,
		`{"kind":"DataKeyList","items":[{"name":"a","node":"node-a","object":"system/sec/hb","size":1},{"name":"b","node":"node-b","object":"system/sec/hb","size":1}]}`,
		`{"kind":"DataKeyList","items":[{"name":"","node":"node-a","object":"system/sec/hb","size":1}]}`,
	} {
		client := &recordingJSONGetter{t: t, path: "/api/object/path/system/sec/hb/data/keys", query: url.Values{}, payload: payload}
		if _, err := New(client).ListObjectDataKeys(context.Background(), ListObjectDataKeysOptions{Path: "system/sec/hb"}); err == nil {
			t.Fatalf("payload %s was accepted", payload)
		}
	}
}
