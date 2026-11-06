// Package content serves the resources and the Agent Skills of the AI
// assistant that the collector stores for the om3 backend: each MCP request
// reads the collector, with the token of the caller exchanged for a collector
// token, and the MCP keeps nothing. The resources are listed and read
// with the MCP methods resources/list and resources/read; the skills with the
// MCP Skills extension, io.modelcontextprotocol/skills (SEP-2640), skills/list
// and skills/get, their SKILL.md read with resources/read.
package content

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// Extension is the MCP Skills extension the server declares.
	Extension = "io.modelcontextprotocol/skills"
	// backend is the backend of the agent this MCP serves: the collector
	// gives the objects marked for it.
	backend = "om3"
	// resourcePrefix is the URI of a resource, its name after it.
	resourcePrefix = "om3://resources/"
	skillScheme    = "skill://"
	skillFile      = "SKILL.md"
)

// Methods are the MCP methods this package answers through the collector:
// the middleware that exchanges the token of the caller for a collector token
// prepares them.
var Methods = []string{"resources/list", "resources/read", "skills/list", "skills/get"}

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Collector reads the collector API as the caller of the request.
type Collector interface {
	GetJSON(ctx context.Context, path string, query url.Values, output any) error
}

// httpStatus is an error of the collector answering an HTTP status.
type httpStatus interface{ HTTPStatus() int }

func isNotFound(err error) bool {
	var status httpStatus
	return errors.As(err, &status) && status.HTTPStatus() == http.StatusNotFound
}

// The objects as the collector gives them.
type (
	resource struct {
		Name        string    `json:"name"`
		Title       string    `json:"title"`
		Description string    `json:"description"`
		MimeType    string    `json:"mime_type"`
		Size        int64     `json:"size"`
		Digest      string    `json:"digest"`
		Updated     time.Time `json:"updated"`
		Body        string    `json:"body"`
	}
	skill struct {
		Name        string         `json:"name"`
		Frontmatter map[string]any `json:"frontmatter"`
		Size        int64          `json:"size"`
		Digest      string         `json:"digest"`
		Body        string         `json:"body"`
	}
)

// Entry is a skill as skills/list and skills/get give it: the URI of its
// SKILL.md, its frontmatter as written, and the manifest of its files, the
// SKILL.md only.
type Entry struct {
	URI         string         `json:"uri"`
	Frontmatter map[string]any `json:"frontmatter"`
	Resources   []File         `json:"resources"`
}

// File is a file of a skill: its URI, the SHA-256 digest of its bytes,
// "sha256:<hex>", and their count.
type File struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// ListParams are the parameters of skills/list; the skills fit one page, the
// cursor is accepted and ignored.
type ListParams struct {
	mcp.ParamsBase
	Cursor string `json:"cursor,omitempty"`
}

// ListResult is the result of skills/list.
type ListResult struct {
	mcp.ResultBase
	mcp.Cacheable
	ResultType string  `json:"resultType"`
	Skills     []Entry `json:"skills"`
}

// GetParams are the parameters of skills/get: the URI of a SKILL.md.
type GetParams struct {
	mcp.ParamsBase
	URI string `json:"uri"`
}

// GetResult is the result of skills/get.
type GetResult struct {
	mcp.ResultBase
	mcp.Cacheable
	ResultType string `json:"resultType"`
	Skill      Entry  `json:"skill"`
}

// noCache tells the client not to keep a listing: it is the caller's, read
// from the collector at each request.
var noCache = mcp.Cacheable{TTLMs: 0, CacheScope: "private"}

// Register serves the resources and the skills of the collector on server:
// a middleware answers resources/list and resources/read of the om3://resources/
// and skill:// URIs, custom methods
// skills/list and skills/get. Call it before adding the middlewares that
// prepare the credential: the first middleware added runs innermost, after
// them. The caller declares the resources capability and Extension.
func Register(server *mcp.Server, collector Collector) error {
	if server == nil || collector == nil {
		return fmt.Errorf("content: server or collector is nil")
	}
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			switch method {
			case "resources/list":
				return listResources(ctx, collector)
			case "resources/read":
				params, ok := request.GetParams().(*mcp.ReadResourceParams)
				if !ok || params == nil {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "resources/read takes the uri of a resource"}
				}
				// The other URIs are the server's own resources, if any.
				if strings.HasPrefix(params.URI, resourcePrefix) || strings.HasPrefix(params.URI, skillScheme) {
					return readResource(ctx, collector, params.URI)
				}
			}
			return next(ctx, method, request)
		}
	})
	if err := mcp.AddReceivingCustomMethod(server, "skills/list", func(ctx context.Context, _ *mcp.ServerSession, _ *ListParams) (*ListResult, error) {
		skills, err := listSkills(ctx, collector)
		if err != nil {
			return nil, err
		}
		return &ListResult{Cacheable: noCache, ResultType: "complete", Skills: skills}, nil
	}); err != nil {
		return err
	}
	return mcp.AddReceivingCustomMethod(server, "skills/get", func(ctx context.Context, _ *mcp.ServerSession, params *GetParams) (*GetResult, error) {
		name, ok := skillName(paramURI(params))
		if !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown skill: give the URI of a SKILL.md skills/list lists"}
		}
		var s skill
		if err := collector.GetJSON(ctx, "/ai/skills/"+name, backendQuery(), &s); err != nil {
			if isNotFound(err) {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown skill: give the URI of a SKILL.md skills/list lists"}
			}
			return nil, collectorError(err)
		}
		return &GetResult{Cacheable: noCache, ResultType: "complete", Skill: entry(s)}, nil
	})
}

func paramURI(params *GetParams) string {
	if params == nil {
		return ""
	}
	return params.URI
}

func backendQuery() url.Values {
	return url.Values{"backend": {backend}}
}

// collectorError is a failed read of the collector: an MCP error naming no
// URL and no credential.
func collectorError(err error) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the collector could not be read: " + safeMessage(err)}
}

// safeMessage keeps the status of a collector answer, never its URL.
func safeMessage(err error) string {
	var status httpStatus
	if errors.As(err, &status) {
		return fmt.Sprintf("HTTP %d", status.HTTPStatus())
	}
	return "request failed"
}

func listResources(ctx context.Context, collector Collector) (*mcp.ListResourcesResult, error) {
	var list struct {
		Data []resource `json:"data"`
	}
	if err := collector.GetJSON(ctx, "/ai/resources", backendQuery(), &list); err != nil {
		return nil, collectorError(err)
	}
	result := &mcp.ListResourcesResult{Resources: []*mcp.Resource{}}
	for _, r := range list.Data {
		if !namePattern.MatchString(r.Name) {
			continue
		}
		result.Resources = append(result.Resources, &mcp.Resource{
			URI: resourcePrefix + r.Name, Name: r.Name, Title: r.Title, Description: r.Description,
			MIMEType: r.MimeType, Size: r.Size,
		})
	}
	return result, nil
}

func listSkills(ctx context.Context, collector Collector) ([]Entry, error) {
	var list struct {
		Data []skill `json:"data"`
	}
	if err := collector.GetJSON(ctx, "/ai/skills", backendQuery(), &list); err != nil {
		return nil, collectorError(err)
	}
	entries := []Entry{}
	for _, s := range list.Data {
		if namePattern.MatchString(s.Name) {
			entries = append(entries, entry(s))
		}
	}
	return entries, nil
}

func entry(s skill) Entry {
	uri := skillScheme + s.Name + "/" + skillFile
	return Entry{URI: uri, Frontmatter: s.Frontmatter, Resources: []File{{URI: uri, Digest: s.Digest, Size: s.Size}}}
}

// skillName is the name of the skill whose SKILL.md uri names.
func skillName(uri string) (string, bool) {
	rest, ok := strings.CutPrefix(uri, skillScheme)
	if !ok {
		return "", false
	}
	name, file, ok := strings.Cut(rest, "/")
	if !ok || file != skillFile || !namePattern.MatchString(name) {
		return "", false
	}
	return name, true
}

func readResource(ctx context.Context, collector Collector, uri string) (*mcp.ReadResourceResult, error) {
	if name, ok := skillName(uri); ok {
		var s skill
		if err := collector.GetJSON(ctx, "/ai/skills/"+name, backendQuery(), &s); err != nil {
			if isNotFound(err) {
				return nil, mcp.ResourceNotFoundError(uri)
			}
			return nil, collectorError(err)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "text/markdown", Text: s.Body}}}, nil
	}
	name, ok := strings.CutPrefix(uri, resourcePrefix)
	if !ok || !namePattern.MatchString(name) {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	var r resource
	if err := collector.GetJSON(ctx, "/ai/resources/"+name, backendQuery(), &r); err != nil {
		if isNotFound(err) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return nil, collectorError(err)
	}
	mime := r.MimeType
	if !slices.Contains([]string{"text/markdown", "text/plain"}, mime) {
		mime = "text/markdown"
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: mime, Text: r.Body}}}, nil
}
