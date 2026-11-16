package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// daemonProblemError is an error answer of the daemon, as the client gives it.
type daemonProblemError struct {
	status int
	detail string
}

func (e daemonProblemError) Error() string {
	return fmt.Sprintf("OpenSVC daemon returned HTTP %d: %s", e.status, e.detail)
}
func (e daemonProblemError) HTTPStatus() int       { return e.status }
func (e daemonProblemError) ProblemDetail() string { return e.detail }

// configWriteClient records the configuration write and answers it, then
// answers the read of the keywords or of the file created.
type configWriteClient struct {
	path     string
	query    url.Values
	body     string
	writes   int
	response string
	header   http.Header
	err      error
	// keywords are the keyword records the read gives, file the file.
	keywords string
	reads    url.Values
	file     string
}

func (c *configWriteClient) GetJSON(_ context.Context, path string, query url.Values, output any) error {
	recordRoute("GET", path)
	c.reads = query
	return json.Unmarshal([]byte(`{"kind":"KeywordList","items":[`+c.keywords+`]}`), output)
}

func (c *configWriteClient) GetFile(_ context.Context, path string, _ url.Values) ([]byte, error) {
	recordRoute("GET", path)
	return []byte(c.file), nil
}

func (c *configWriteClient) PatchJSON(_ context.Context, path string, query url.Values, output any) (http.Header, error) {
	recordRoute("PATCH", path)
	c.writes++
	c.path, c.query = path, query
	if c.err != nil {
		return c.header, c.err
	}
	return c.header, json.Unmarshal([]byte(c.response), output)
}

func (c *configWriteClient) PostFile(_ context.Context, path string, query url.Values, body []byte) (http.Header, error) {
	recordRoute("POST", path)
	c.writes++
	c.path, c.query, c.body = path, query, string(body)
	return c.header, c.err
}

func TestSplitConfigOperationAsTheDaemon(t *testing.T) {
	for operation, want := range map[string]string{
		"env.foo=bar":       "env.foo|=|bar",
		"env.list+=a":       "env.list|+=|a",
		"env.a-b=1":         "env.a-b|=|1",
		"env.x=a+=b":        "env.x|=|a+=b",
		"fs#1.mnt=/srv/web": "fs#1.mnt|=|/srv/web",
		"nodes[1]=n3":       "nodes[1]|=|n3",
		"env.x!=1":          "env.x|!=|1",
		"env.foo":           "env.foo||",
	} {
		key, operator, value := splitConfigOperation(operation)
		if got := key + "|" + operator + "|" + value; got != want {
			t.Errorf("%s: %s, want %s", operation, got, want)
		}
	}
}

func TestUpdateObjectConfigRefusesMalformedOperationsBeforeWriting(t *testing.T) {
	many := make([]string, 51)
	for index := range many {
		many[index] = fmt.Sprintf("env.k%d=v", index)
	}
	for name, tc := range map[string]struct {
		options UpdateObjectConfigOptions
		want    string
	}{
		"nothing":            {UpdateObjectConfigOptions{Path: "test/svc/web"}, "at least one"},
		"selector":           {UpdateObjectConfigOptions{Path: "test/svc/*", Set: []string{"env.a=1"}}, "exact"},
		"too many":           {UpdateObjectConfigOptions{Path: "test/svc/web", Set: many}, "at most 50"},
		"no operator":        {UpdateObjectConfigOptions{Path: "test/svc/web", Set: []string{"env.foo"}}, "operator one of"},
		"comparison":         {UpdateObjectConfigOptions{Path: "test/svc/web", Set: []string{"env.foo>=1"}}, "operator must be one of"},
		"index with append":  {UpdateObjectConfigOptions{Path: "test/svc/web", Set: []string{"nodes[1]+=n3"}}, "indexed keyword"},
		"empty key":          {UpdateObjectConfigOptions{Path: "test/svc/web", Set: []string{"=1"}}, "empty"},
		"space in key":       {UpdateObjectConfigOptions{Path: "test/svc/web", Unset: []string{"env foo"}}, "space"},
		"large value":        {UpdateObjectConfigOptions{Path: "test/svc/web", Set: []string{"env.a=" + strings.Repeat("x", 4097)}}, "exceeds 4096"},
		"delete DEFAULT":     {UpdateObjectConfigOptions{Path: "test/svc/web", Delete: []string{"default"}}, "DEFAULT"},
		"delete empty":       {UpdateObjectConfigOptions{Path: "test/svc/web", Delete: []string{""}}, "empty"},
		"wait out of bounds": {UpdateObjectConfigOptions{Path: "test/svc/web", Set: []string{"env.a=1"}, WaitSeconds: 121}, "wait_seconds"},
	} {
		client := &configWriteClient{}
		if _, err := New(client).UpdateObjectConfig(context.Background(), tc.options); err == nil || !strings.Contains(err.Error(), tc.want) || client.writes != 0 {
			t.Errorf("%s: %v, writes %d", name, err, client.writes)
		}
	}
}

func TestUpdateObjectConfigWritesAndReadsTheKeywordsAgain(t *testing.T) {
	client := &configWriteClient{
		response: `{"is_changed":true,"warnings":["namespace test holds more than it claims"]}`,
		header:   http.Header{"Om-Last-Modified": {"2026-10-22T12:00:00.123Z"}},
		keywords: `{"object":"test/svc/web","keyword":"env.list","value":"a a","node":""}`,
	}
	result, err := New(client).UpdateObjectConfig(context.Background(), UpdateObjectConfigOptions{
		Path: "test/svc/web", Delete: []string{"fs#2"}, Unset: []string{"env.foo"}, Set: []string{"env.list+=a", "nodes[1]=n3"}, WaitSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{"delete": {"fs#2"}, "unset": {"env.foo"}, "set": {"env.list+=a", "nodes[1]=n3"}, "wait": {"30s"}}
	if client.path != "/api/object/path/test/svc/web/config" || client.query.Encode() != want.Encode() {
		t.Fatalf("PATCH %s %v", client.path, client.query)
	}
	if result.Outcome != "written" || !result.IsChanged || result.LastModified != "2026-10-22T12:00:00.123Z" || len(result.Warnings) != 1 ||
		len(result.Keywords) != 1 || result.Keywords[0].Value != "a a" || result.Provenance.Source == "" {
		t.Fatalf("result %+v", result)
	}
	// The keywords named, without index, are read again.
	if strings.Join(client.reads["kw"], ",") != "env.foo,env.list,nodes" {
		t.Fatalf("read again %v", client.reads)
	}
}

func TestUpdateObjectConfigTellsAnUnchangedConfiguration(t *testing.T) {
	client := &configWriteClient{response: `{"is_changed":false}`}
	result, err := New(client).UpdateObjectConfig(context.Background(), UpdateObjectConfigOptions{Path: "test/svc/web", Delete: []string{"env"}})
	if err != nil || result.Outcome != "unchanged" || result.IsChanged || result.Keywords == nil || len(result.Keywords) != 0 {
		t.Fatalf("result %+v, %v", result, err)
	}
	if client.reads != nil {
		t.Fatal("a delete alone read keywords again")
	}
}

// A wait that expired is a write done, still propagating: never a failure.
func TestUpdateObjectConfigReadsAnExpiredWaitAsWritten(t *testing.T) {
	client := &configWriteClient{err: daemonProblemError{http.StatusRequestTimeout,
		"the configuration of test/svc/web is committed as of 2026-10-22T12:00:00Z, and has not reached dev5n2, dev5n3 before the wait expired"}}
	result, err := New(client).UpdateObjectConfig(context.Background(), UpdateObjectConfigOptions{Path: "test/svc/web", Set: []string{"env.a=1"}, WaitSeconds: 5})
	if err != nil || result.Outcome != "written" || !result.IsChanged || result.LastModified != "2026-10-22T12:00:00Z" ||
		strings.Join(result.PropagationPending, ",") != "dev5n2,dev5n3" {
		t.Fatalf("result %+v, %v", result, err)
	}
}

func TestUpdateObjectConfigGivesTheRefusalOfTheDaemon(t *testing.T) {
	for _, problem := range []daemonProblemError{
		{http.StatusForbidden, "fs#1: the resource is up on dev5n2: stop it first"},
		{http.StatusConflict, "the configuration changed meanwhile"},
		{http.StatusBadRequest, "env.x: unknown keyword"},
		{http.StatusRequestTimeout, "an answer the MCP does not read"},
	} {
		client := &configWriteClient{err: problem}
		_, err := New(client).UpdateObjectConfig(context.Background(), UpdateObjectConfigOptions{Path: "test/svc/web", Set: []string{"env.a=1"}})
		if err == nil || !strings.Contains(err.Error(), problem.detail) || !errors.As(err, new(daemonProblemError)) {
			t.Errorf("%d: %v", problem.status, err)
		}
	}
}

func TestCreateObjectPostsTheFileAndReadsItAgain(t *testing.T) {
	config := "[DEFAULT]\nnodes = dev5n1 dev5n2\norchestrate = ha\n\n[fs#1]\ntype = flag\n"
	client := &configWriteClient{header: http.Header{"Om-Last-Modified": {"2026-10-22T12:00:00Z"}}, file: "[DEFAULT]\nid = new\norchestrate = ha\n"}
	result, err := New(client).CreateObject(context.Background(), CreateObjectOptions{Path: "test/svc/mcp-config", Config: config, Set: []string{"env.owner=mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{"set": {"env.owner=mcp"}, "wait_local": {"true"}}
	if client.path != "/api/object/path/test/svc/mcp-config/config/file" || client.query.Encode() != want.Encode() || client.body != config {
		t.Fatalf("POST %s %v %q", client.path, client.query, client.body)
	}
	if !result.Created || !result.FlaggedStopped || result.LastModified != "2026-10-22T12:00:00Z" || result.Content != client.file || !result.RedactionRequested {
		t.Fatalf("result %+v", result)
	}
	off := false
	if _, err := New(client).CreateObject(context.Background(), CreateObjectOptions{Path: "test/cfg/settings", Config: "[DEFAULT]\n", WaitLocal: &off}); err != nil || client.query.Has("wait_local") {
		t.Fatalf("wait_local off: %v %v", client.query, err)
	}
}

func TestCreateObjectRefusals(t *testing.T) {
	for name, options := range map[string]CreateObjectOptions{
		"no config":   {Path: "test/svc/web"},
		"large":       {Path: "test/svc/web", Config: strings.Repeat("x", 256<<10+1)},
		"not UTF-8":   {Path: "test/svc/web", Config: "\xff"},
		"bad set":     {Path: "test/svc/web", Config: "[DEFAULT]\n", Set: []string{"env.a"}},
		"wildcard":    {Path: "test/svc/web*", Config: "[DEFAULT]\n"},
		"not a path":  {Path: "", Config: "[DEFAULT]\n"},
		"bad section": {Path: "test/svc/web", Config: " "},
	} {
		client := &configWriteClient{}
		if _, err := New(client).CreateObject(context.Background(), options); err == nil || client.writes != 0 {
			t.Errorf("%s: %v, writes %d", name, err, client.writes)
		}
	}
	// An object that exists is the update's to change.
	client := &configWriteClient{err: daemonProblemError{http.StatusConflict, "Use the PUT method instead of POST to update the object config"}}
	if _, err := New(client).CreateObject(context.Background(), CreateObjectOptions{Path: "test/svc/web", Config: "[DEFAULT]\n"}); err == nil || !strings.Contains(err.Error(), "use update_object_config") {
		t.Fatalf("existing object: %v", err)
	}
	// Another conflict is the daemon's, as it says it.
	client = &configWriteClient{err: daemonProblemError{http.StatusConflict, "fs#1.mnt /srv is held by test/svc/other on dev5n1"}}
	if _, err := New(client).CreateObject(context.Background(), CreateObjectOptions{Path: "test/svc/web", Config: "[DEFAULT]\n"}); err == nil ||
		strings.Contains(err.Error(), "update_object_config") || !strings.Contains(err.Error(), "held by test/svc/other") {
		t.Fatalf("held: %v", err)
	}
}

func TestOrchestratesOnItsOwn(t *testing.T) {
	for content, want := range map[string]bool{
		"[DEFAULT]\norchestrate = ha\n":                   true,
		"orchestrate = start\n":                           true,
		"[DEFAULT]\norchestrate = no\n":                   false,
		"[DEFAULT]\nnodes = a\n[app#1]\norchestrate = ha": false,
		"": false,
	} {
		if got := orchestratesOnItsOwn(content); got != want {
			t.Errorf("%q: %v", content, got)
		}
	}
}

// A secret keyword written is left out of the read back, and counted.
func TestUpdateObjectConfigCountsTheSecretWritten(t *testing.T) {
	client := &configWriteClient{
		response: `{"is_changed":true}`, header: http.Header{},
		keywords: `{"object":"cluster","node":"","keyword":"cluster.secret","value":"n3w-s3cret"}`,
	}
	result, err := New(client).UpdateObjectConfig(context.Background(), UpdateObjectConfigOptions{Path: "cluster", Set: []string{"cluster.secret=n3w-s3cret"}})
	if err != nil || len(result.Keywords) != 0 || result.SecretsOmitted != 1 || result.KeywordsError != "" {
		t.Fatalf("%+v %v", result, err)
	}
	if out, _ := json.Marshal(result); strings.Contains(string(out), "n3w-s3cret") {
		t.Fatalf("the secret was returned: %s", out)
	}
	// A secret scoped to a node, cluster.secret is scopable.
	client = &configWriteClient{
		response: `{"is_changed":true}`, header: http.Header{},
		keywords: `{"object":"cluster","node":"","keyword":"cluster.secret@dev5n1","value":"sc0ped-s3cret"}`,
	}
	result, err = New(client).UpdateObjectConfig(context.Background(), UpdateObjectConfigOptions{Path: "cluster", Set: []string{"cluster.secret@dev5n1=sc0ped-s3cret"}})
	if err != nil || len(result.Keywords) != 0 || result.SecretsOmitted != 1 {
		t.Fatalf("scoped: %+v %v", result, err)
	}
	if out, _ := json.Marshal(result); strings.Contains(string(out), "sc0ped-s3cret") {
		t.Fatalf("the scoped secret was returned: %s", out)
	}
}
