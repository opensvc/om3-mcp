package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/om3-mcp/internal/core"
)

const (
	maxToolNameBytes        = 128
	maxToolTitleBytes       = 256
	maxToolDescriptionBytes = 4 << 10
)

var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type Registrar struct {
	server *mcp.Server
	names  map[string]struct{}
	router ClusterRouter
}

type ClusterRouter interface {
	Prepare(context.Context, string) (context.Context, context.CancelFunc, error)
}

// SetClusterRouter is called before domain tools are registered. Without a
// router, as on the delegated Unix socket, the cluster comes from the request
// context and tools take no cluster_id argument.
func (r *Registrar) SetClusterRouter(router ClusterRouter) { r.router = router }

func NewRegistrar(server *mcp.Server) (*Registrar, error) {
	if server == nil {
		return nil, fmt.Errorf("tool registrar MCP server is nil")
	}
	return &Registrar{server: server, names: make(map[string]struct{})}, nil
}

func addTool[In, Out any](registrar *Registrar, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) (err error) {
	return registerTool(registrar, tool, handler, true)
}

func registerTool[In, Out any](registrar *Registrar, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out], routed bool) (err error) {
	if registrar == nil || registrar.server == nil {
		return fmt.Errorf("tool registrar is nil")
	}
	if err := validateToolDeclaration(tool); err != nil {
		return err
	}
	if handler == nil {
		return fmt.Errorf("tool %q handler is nil", tool.Name)
	}
	if !isObjectType(reflect.TypeFor[In]()) {
		return fmt.Errorf("tool %q input type must be a typed struct or map", tool.Name)
	}
	if !isObjectType(reflect.TypeFor[Out]()) {
		return fmt.Errorf("tool %q output type must be a typed struct or map", tool.Name)
	}
	if _, exists := registrar.names[tool.Name]; exists {
		return fmt.Errorf("tool name %q is already registered", tool.Name)
	}
	// The registrar sets the search metadata; a declaration sets none.
	if tool.Meta, err = searchMeta(tool.Name); err != nil {
		return err
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("register tool %q: %v", tool.Name, recovered)
		}
	}()
	if routed && registrar.router != nil {
		schema, err := jsonschema.For[In](nil)
		if err != nil {
			return fmt.Errorf("tool input schema: %w", err)
		}
		if schema.Properties == nil {
			schema.Properties = make(map[string]*jsonschema.Schema)
		}
		schema.Properties["cluster_id"] = &jsonschema.Schema{Type: "string", Description: "Required exact cluster_id from list_clusters. Ask the operator to identify the cluster when ambiguous; never infer it from a node name alone."}
		schema.Required = append(schema.Required, "cluster_id")
		tool.InputSchema = schema
		tool.Description += " Requires an explicit cluster_id from list_clusters. The configured cluster VIP is the endpoint; node arguments select logical nodes, not network destinations."
		original := handler
		handler = func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
			var target struct {
				ClusterID string `json:"cluster_id"`
			}
			var zero Out
			if err := json.Unmarshal(req.Params.Arguments, &target); err != nil || strings.TrimSpace(target.ClusterID) == "" || len(target.ClusterID) > 256 {
				return nil, zero, fmt.Errorf("cluster_id is required; use list_clusters")
			}
			ctx, cancel, err := registrar.router.Prepare(ctx, target.ClusterID)
			if err != nil {
				return nil, zero, err
			}
			defer cancel()
			return original(ctx, req, in)
		}
	}
	// Every tool taking a node refuses an unknown one with the next step.
	explained := handler
	handler = func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		result, out, err := explained(ctx, req, in)
		return result, out, core.ExplainUnknownNode(err)
	}
	mcp.AddTool(registrar.server, tool, handler)
	registrar.names[tool.Name] = struct{}{}
	return nil
}

func isObjectType(valueType reflect.Type) bool {
	if valueType == nil {
		return false
	}
	return valueType.Kind() == reflect.Struct || valueType.Kind() == reflect.Map
}

func validateToolDeclaration(tool *mcp.Tool) error {
	if tool == nil {
		return fmt.Errorf("tool declaration is nil")
	}
	if !toolNamePattern.MatchString(tool.Name) {
		return fmt.Errorf("tool name %q must be lower snake_case", tool.Name)
	}
	if len(tool.Name) > maxToolNameBytes {
		return fmt.Errorf("tool name %q exceeds %d bytes", tool.Name, maxToolNameBytes)
	}
	if strings.TrimSpace(tool.Title) == "" {
		return fmt.Errorf("tool %q title is empty", tool.Name)
	}
	if len(tool.Title) > maxToolTitleBytes {
		return fmt.Errorf("tool %q title exceeds %d bytes", tool.Name, maxToolTitleBytes)
	}
	if strings.TrimSpace(tool.Description) == "" {
		return fmt.Errorf("tool %q description is empty", tool.Name)
	}
	if len(tool.Description) > maxToolDescriptionBytes {
		return fmt.Errorf("tool %q description exceeds %d bytes", tool.Name, maxToolDescriptionBytes)
	}
	if tool.Annotations == nil {
		return fmt.Errorf("tool %q annotations are missing", tool.Name)
	}
	if tool.Annotations.DestructiveHint == nil {
		return fmt.Errorf("tool %q destructive annotation is missing", tool.Name)
	}
	if tool.Annotations.ReadOnlyHint && *tool.Annotations.DestructiveHint {
		return fmt.Errorf("tool %q cannot be both read-only and destructive", tool.Name)
	}
	if tool.Annotations.OpenWorldHint == nil {
		return fmt.Errorf("tool %q open-world annotation is missing", tool.Name)
	}
	if *tool.Annotations.OpenWorldHint {
		return fmt.Errorf("tool %q must be closed-world", tool.Name)
	}
	if len(tool.Meta) != 0 {
		return fmt.Errorf("tool %q custom metadata is unsupported", tool.Name)
	}
	return nil
}
