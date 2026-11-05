package content

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/opensvc/om3-mcp/internal/client"
)

// fakeCollector answers each path with its JSON, or its error.
type fakeCollector struct {
	answers map[string]string
	errs    map[string]error
}

func (f fakeCollector) GetJSON(_ context.Context, path string, _ url.Values, output any) error {
	if err, ok := f.errs[path]; ok {
		return err
	}
	answer, ok := f.answers[path]
	if !ok {
		return &client.APIError{Method: "GET", Path: path, StatusCode: 404}
	}
	return json.Unmarshal([]byte(answer), output)
}

// connect serves collector on a server holding one resource of its own,
// file:///own, and connects a client to it.
func connect(t *testing.T, collector Collector) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}}})
	server.AddResource(&mcp.Resource{URI: "file:///own", Name: "own"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "file:///own", Text: "own"}}}, nil
	})
	if err := Register(server, collector); err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0"}, nil)
	if err := mcp.AddSendingCustomMethod[*ListParams, *ListResult](c, "skills/list"); err != nil {
		t.Fatal(err)
	}
	session, err := c.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestCollectorFailureNamesNoURL fails a read of the collector with its HTTP
// status only: neither the URL nor the message of the collector.
func TestCollectorFailureNamesNoURL(t *testing.T) {
	session := connect(t, fakeCollector{errs: map[string]error{
		"/ai/resources": &client.APIError{Method: "GET", Path: "https://collector.example/api/ai/resources", StatusCode: 500, Detail: "db at 10.0.0.5 is down"},
		"/ai/skills":    &url.Error{Op: "Get", URL: "https://collector.example/api/ai/skills", Err: errors.New("connection refused")},
	}})
	_, err := session.ListResources(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "the collector could not be read: HTTP 500") ||
		strings.Contains(err.Error(), "collector.example") || strings.Contains(err.Error(), "10.0.0.5") {
		t.Errorf("a collector 500: %v", err)
	}
	_, err = mcp.CallCustomMethod[*ListParams, *ListResult](t.Context(), session, "skills/list", &ListParams{})
	if err == nil || !strings.Contains(err.Error(), "the collector could not be read: request failed") || strings.Contains(err.Error(), "collector.example") {
		t.Errorf("a collector out of reach: %v", err)
	}
}

// TestUnknownMimeTypeReadsAsMarkdown gives a resource of a type the MCP does
// not know as Markdown, and one of a known type as it is.
func TestUnknownMimeTypeReadsAsMarkdown(t *testing.T) {
	session := connect(t, fakeCollector{answers: map[string]string{
		"/ai/resources/script": `{"name":"script","mime_type":"application/x-sh","body":"echo"}`,
		"/ai/resources/notes":  `{"name":"notes","mime_type":"text/plain","body":"notes"}`,
	}})
	for uri, want := range map[string]string{"om3://resources/script": "text/markdown", "om3://resources/notes": "text/plain"} {
		read, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
		if err != nil || read.Contents[0].MIMEType != want {
			t.Errorf("%s: %+v, %v, want %s", uri, read, err, want)
		}
	}
}

// TestNamesOutsideThePatternAreIgnored lists the objects whose name makes a
// URI only, and reads none other.
func TestNamesOutsideThePatternAreIgnored(t *testing.T) {
	asked := &recordingCollector{fakeCollector: fakeCollector{answers: map[string]string{
		"/ai/resources": `{"data":[{"name":"naming"},{"name":"Bad_Name"},{"name":"../etc"},{"name":"a/b"}]}`,
		"/ai/skills":    `{"data":[{"name":"maintenance"},{"name":"x y"}]}`,
	}}}
	session := connect(t, asked)
	list, err := session.ListResources(t.Context(), nil)
	if err != nil || len(list.Resources) != 1 || list.Resources[0].URI != "om3://resources/naming" {
		t.Errorf("resources %+v, %v", list, err)
	}
	skills, err := mcp.CallCustomMethod[*ListParams, *ListResult](t.Context(), session, "skills/list", &ListParams{})
	if err != nil || len(skills.Skills) != 1 || skills.Skills[0].URI != "skill://maintenance/SKILL.md" {
		t.Errorf("skills %+v, %v", skills, err)
	}
	asked.paths = nil
	for _, uri := range []string{"om3://resources/../etc", "om3://resources/Bad_Name", "skill://../SKILL.md", "skill://maintenance/other.md"} {
		if _, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("%s was read", uri)
		}
	}
	if len(asked.paths) != 0 {
		t.Errorf("the collector was asked %v", asked.paths)
	}
}

// TestOtherURIsGoToTheServer leaves the resources/read of another URI to the
// server.
func TestOtherURIsGoToTheServer(t *testing.T) {
	asked := &recordingCollector{}
	read, err := connect(t, asked).ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "file:///own"})
	if err != nil || read.Contents[0].Text != "own" || len(asked.paths) != 0 {
		t.Errorf("read %+v, %v, collector asked %v", read, err, asked.paths)
	}
}

// recordingCollector records the paths asked.
type recordingCollector struct {
	fakeCollector
	paths []string
}

func (r *recordingCollector) GetJSON(ctx context.Context, path string, query url.Values, output any) error {
	r.paths = append(r.paths, path)
	return r.fakeCollector.GetJSON(ctx, path, query, output)
}
