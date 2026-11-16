package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// nodeConfigClient answers the keyword reads and the catalog of a node, and
// records the write.
type nodeConfigClient struct {
	keywords string
	catalog  string
	reads    []string
	query    url.Values
	patch    url.Values
	patches  int
	response string
	err      error
}

func (c *nodeConfigClient) GetJSON(_ context.Context, path string, query url.Values, output any) error {
	recordRoute("GET", path)
	c.reads = append(c.reads, path)
	c.query = query
	if strings.HasSuffix(path, "/config/keywords") {
		return json.Unmarshal([]byte(c.catalog), output)
	}
	return json.Unmarshal([]byte(`{"kind":"KeywordList","items":[`+c.keywords+`]}`), output)
}

func (c *nodeConfigClient) PatchJSON(_ context.Context, path string, query url.Values, output any) (http.Header, error) {
	recordRoute("PATCH", path)
	c.patches++
	c.patch = query
	c.reads = append(c.reads, "PATCH "+path)
	if c.err != nil {
		return nil, c.err
	}
	return http.Header{}, json.Unmarshal([]byte(c.response), output)
}

func nodeKeyword(keyword, value string) string {
	data, _ := json.Marshal(map[string]string{"object": "", "node": "n2", "keyword": keyword, "value": value})
	return string(data)
}

func TestGetNodeConfigKeywordsLeavesOutTheUUID(t *testing.T) {
	client := &nodeConfigClient{keywords: nodeKeyword("node.uuid", "0f5c3a1e-uuid-value") + "," + nodeKeyword("node.env", "DEV") + "," + nodeKeyword("hb#1.type", "unicast")}
	result, err := New(client).GetNodeConfigKeywords(context.Background(), GetNodeConfigKeywordsOptions{Node: "n2", Keywords: []string{"node.env", "node.uuid", "hb#1.type"}})
	if err != nil {
		t.Fatal(err)
	}
	if client.reads[0] != "/api/node/name/n2/config" || client.query.Get("evaluate") != "false" || len(client.query["kw"]) != 3 {
		t.Fatalf("read %v %v", client.reads, client.query)
	}
	if result.Node != "n2" || result.Total != 2 || result.Items[0].Keyword != "hb#1.type" || result.Items[1].Value != "DEV" || result.SecretsOmitted != 1 {
		t.Fatalf("result = %+v", result)
	}
	if out, _ := json.Marshal(result); strings.Contains(string(out), "uuid-value") {
		t.Fatalf("the uuid was returned: %s", out)
	}
	for _, node := range []string{"_", "n1,n2", ""} {
		client := &nodeConfigClient{}
		if _, err := New(client).GetNodeConfigKeywords(context.Background(), GetNodeConfigKeywordsOptions{Node: node}); err == nil || len(client.reads) != 0 {
			t.Fatalf("node %q accepted", node)
		}
	}
	// A record of an object or of another node is refused.
	client = &nodeConfigClient{keywords: `{"object":"root/svc/x","node":"n2","keyword":"env.a","value":"1"}`}
	if _, err := New(client).GetNodeConfigKeywords(context.Background(), GetNodeConfigKeywordsOptions{Node: "n2"}); err == nil {
		t.Fatal("a keyword of an object was taken")
	}
}

func TestUpdateNodeConfigWritesAndReadsTheKeywordsAgain(t *testing.T) {
	client := &nodeConfigClient{response: `{"is_changed":true}`, keywords: nodeKeyword("node.comment", "mcp lot 6") + "," + nodeKeyword("node.uuid", "n3w-uuid-value")}
	result, err := New(client).UpdateNodeConfig(context.Background(), UpdateNodeConfigOptions{Node: "n2", Set: []string{"node.comment=mcp lot 6", "node.uuid=n3w-uuid-value"}, Unset: []string{"node.env"}, Delete: []string{"hb#2"}})
	if err != nil {
		t.Fatal(err)
	}
	if client.reads[0] != "PATCH /api/node/name/n2/config" || strings.Join(client.patch["set"], "|") != "node.comment=mcp lot 6|node.uuid=n3w-uuid-value" ||
		client.patch.Get("unset") != "node.env" || client.patch.Get("delete") != "hb#2" || client.patch.Get("wait") != "" {
		t.Fatalf("patch %v %v", client.reads, client.patch)
	}
	if result.Outcome != "written" || !result.IsChanged || len(result.Keywords) != 1 || result.Keywords[0].Value != "mcp lot 6" || result.SecretsOmitted != 1 {
		t.Fatalf("result = %+v", result)
	}
	if out, _ := json.Marshal(result); strings.Contains(string(out), "n3w-uuid-value") {
		t.Fatalf("the uuid was returned: %s", out)
	}
	client = &nodeConfigClient{response: `{"is_changed":false}`}
	if result, err := New(client).UpdateNodeConfig(context.Background(), UpdateNodeConfigOptions{Node: "n2", Set: []string{"node.comment=x"}}); err != nil || result.Outcome != "unchanged" {
		t.Fatalf("unchanged: %+v %v", result, err)
	}
	client = &nodeConfigClient{err: daemonProblemError{http.StatusBadRequest, "node.env: invalid value"}}
	if _, err := New(client).UpdateNodeConfig(context.Background(), UpdateNodeConfigOptions{Node: "n2", Set: []string{"node.env=x"}}); err == nil || !strings.Contains(err.Error(), "invalid value") {
		t.Fatalf("refused: %v", err)
	}
	for label, options := range map[string]UpdateNodeConfigOptions{
		"no operation": {Node: "n2"},
		"bad set":      {Node: "n2", Set: []string{"no operator"}},
		"alias":        {Node: "_", Set: []string{"node.env=x"}},
	} {
		client := &nodeConfigClient{}
		if _, err := New(client).UpdateNodeConfig(context.Background(), options); err == nil || client.patches != 0 {
			t.Fatalf("%s accepted", label)
		}
	}
}

func TestListNodeConfigKeywordsReadsTheNodeCatalog(t *testing.T) {
	definition := validDaemonObjectConfigKeywordDefinition()
	definition.Section, definition.Option = "node", "comment"
	client := &nodeConfigClient{catalog: marshalObjectConfigKeywordResponse(t, []daemonObjectConfigKeywordDefinition{definition})}
	result, err := New(client).ListNodeConfigKeywords(context.Background(), ListNodeConfigKeywordsOptions{Node: "n2", Option: "comment"})
	if err != nil {
		t.Fatal(err)
	}
	if client.reads[0] != "/api/node/name/n2/config/keywords" || client.query.Get("option") != "comment" || result.Node != "n2" || result.Total != 1 || result.Definitions[0].Option != "comment" {
		t.Fatalf("%v %v %+v", client.reads, client.query, result)
	}
	if _, err := New(&nodeConfigClient{}).ListNodeConfigKeywords(context.Background(), ListNodeConfigKeywordsOptions{Node: "n2", Driver: "hb", Section: "hb#1"}); err == nil {
		t.Fatal("driver and section accepted together")
	}
	// Without a node, the daemon answering serves the catalog, the same on
	// every node; the alias stays refused as an input.
	client = &nodeConfigClient{catalog: marshalObjectConfigKeywordResponse(t, []daemonObjectConfigKeywordDefinition{definition})}
	result, err = New(client).ListNodeConfigKeywords(context.Background(), ListNodeConfigKeywordsOptions{Option: "comment"})
	if err != nil || client.reads[0] != "/api/node/name/_/config/keywords" || result.Node != "" || result.Total != 1 {
		t.Fatalf("no node: %v %v %+v", err, client.reads, result)
	}
	for _, node := range []string{"_", "n*"} {
		client := &nodeConfigClient{}
		if _, err := New(client).ListNodeConfigKeywords(context.Background(), ListNodeConfigKeywordsOptions{Node: node}); err == nil || len(client.reads) != 0 {
			t.Fatalf("node %q accepted", node)
		}
	}
}
