package core

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"testing"
)

type objectJSONGetter struct {
	t        *testing.T
	payload  string
	selector string
	calls    int
}

func (f *objectJSONGetter) GetJSON(_ context.Context, path string, query url.Values, output any) error {
	f.t.Helper()
	f.calls++
	if path != "/api/object/path" {
		f.t.Errorf("got path %q, want /api/object/path", path)
	}
	if got := query.Get("path"); got != f.selector {
		f.t.Errorf("got selector %q, want %q", got, f.selector)
	}
	return json.Unmarshal([]byte(f.payload), output)
}

func TestListClusterObjects(t *testing.T) {
	client := &objectJSONGetter{
		t:        t,
		selector: "example/**",
		payload: `[
			"system/sec/ca",
			"example/vol/data",
			"example/svc/cache",
			"cluster",
			"example/svc/api",
			"example/svc/cache"
		]`,
	}
	service := New(client)

	result, err := service.ListClusterObjects(context.Background(), ListClusterObjectsOptions{
		Selector: " example/** ",
		Limit:    2,
		Cursor:   "example/svc/a",
	})
	if err != nil {
		t.Fatalf("list cluster objects: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("got %d daemon calls, want 1", client.calls)
	}
	if result.Selector != "example/**" || result.Total != 5 || result.Count != 2 {
		t.Errorf("got list metadata %+v, want selector example/**, total 5, count 2", result)
	}
	if !result.Truncated || result.NextCursor != "example/svc/cache" {
		t.Errorf("got pagination truncated=%v next_cursor=%q", result.Truncated, result.NextCursor)
	}
	want := []ClusterObjectReference{
		{Path: "example/svc/api", Namespace: "example", Kind: "svc", Name: "api"},
		{Path: "example/svc/cache", Namespace: "example", Kind: "svc", Name: "cache"},
	}
	if !reflect.DeepEqual(result.Objects, want) {
		t.Errorf("got objects %+v, want %+v", result.Objects, want)
	}
}

func TestListClusterObjectsDefaultsAndEmptyResult(t *testing.T) {
	client := &objectJSONGetter{t: t, selector: "**", payload: `[]`}
	result, err := New(client).ListClusterObjects(context.Background(), ListClusterObjectsOptions{})
	if err != nil {
		t.Fatalf("list cluster objects: %v", err)
	}
	if result.Selector != "**" || result.Total != 0 || result.Count != 0 || result.Truncated {
		t.Fatalf("got unexpected empty result %+v", result)
	}
	if result.Objects == nil {
		t.Fatal("objects must be an empty array, not nil")
	}
}

func TestListClusterObjectsRejectsInvalidLimitBeforeDaemonCall(t *testing.T) {
	client := &objectJSONGetter{t: t, selector: "**", payload: `[]`}
	_, err := New(client).ListClusterObjects(context.Background(), ListClusterObjectsOptions{Limit: 201})
	if err == nil {
		t.Fatal("expected invalid limit error")
	}
	if client.calls != 0 {
		t.Fatalf("got %d daemon calls, want 0", client.calls)
	}
}

func TestParseClusterObjectReference(t *testing.T) {
	tests := []struct {
		path string
		want ClusterObjectReference
	}{
		{path: "cluster", want: ClusterObjectReference{Path: "cluster", Namespace: "root", Kind: "ccfg", Name: "cluster"}},
		{path: "cache", want: ClusterObjectReference{Path: "cache", Namespace: "root", Kind: "svc", Name: "cache"}},
		{path: "cfg/app", want: ClusterObjectReference{Path: "cfg/app", Namespace: "root", Kind: "cfg", Name: "app"}},
		{path: "example/svc/cache", want: ClusterObjectReference{Path: "example/svc/cache", Namespace: "example", Kind: "svc", Name: "cache"}},
		{path: "example/", want: ClusterObjectReference{Path: "example/", Namespace: "example", Kind: "nscfg", Name: "namespace"}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			got, err := parseClusterObjectReference(test.path)
			if err != nil {
				t.Fatalf("parse path: %v", err)
			}
			if got != test.want {
				t.Errorf("got %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestParseClusterObjectReferenceRejectsInvalidPath(t *testing.T) {
	for _, path := range []string{"", "/svc/app", "example//app", "example/svc/app/extra"} {
		t.Run(path, func(t *testing.T) {
			if _, err := parseClusterObjectReference(path); err == nil {
				t.Fatalf("expected %q to be rejected", path)
			}
		})
	}
}

// Since om3 rc62 the daemon answers the paths a selector matches as a list,
// {kind, items}: read the same as the bare array of the former daemons.
func TestListClusterObjectsReadsTheItemsOfAList(t *testing.T) {
	client := &objectJSONGetter{
		t:        t,
		selector: "example/**",
		payload:  `{"kind":"ObjectPathList","items":["example/svc/cache","example/svc/api","example/svc/cache"]}`,
	}
	result, err := New(client).ListClusterObjects(context.Background(), ListClusterObjectsOptions{Selector: "example/**"})
	if err != nil {
		t.Fatalf("list cluster objects: %v", err)
	}
	paths := make([]string, 0, len(result.Objects))
	for _, item := range result.Objects {
		paths = append(paths, item.Path)
	}
	if want := []string{"example/svc/api", "example/svc/cache"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("got paths %v, want %v", paths, want)
	}
}
