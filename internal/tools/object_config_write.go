package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/om3-mcp/internal/core"
)

type UpdateObjectConfigInput struct {
	Path        string   `json:"path" jsonschema:"the exact canonical OpenSVC object path; no wildcard or selector"`
	Set         []string `json:"set,omitempty" jsonschema:"set operations, each <section>.<keyword><operator><value>, such as fs#1.mnt=/srv/web or env.list+=a"`
	Unset       []string `json:"unset,omitempty" jsonschema:"keywords to remove, each <section>.<keyword>, such as env.foo"`
	Delete      []string `json:"delete,omitempty" jsonschema:"sections to remove with their keywords, each a section name such as fs#2"`
	WaitSeconds int      `json:"wait_seconds,omitempty" jsonschema:"optional seconds, at most 120, to hold the answer until the configuration reached every live node of the object, within the request timeout of the cluster; 0 answers once written"`
}

type CreateObjectInput struct {
	Path      string   `json:"path" jsonschema:"the exact canonical OpenSVC object path to create; no wildcard or selector"`
	Config    string   `json:"config" jsonschema:"the configuration file of the object, as INI text, at most 256 KiB"`
	Set       []string `json:"set,omitempty" jsonschema:"optional set operations applied after the configuration, as update_object_config takes them"`
	WaitLocal *bool    `json:"wait_local,omitempty" jsonschema:"optional, true by default: hold the answer until the daemon knows the object, so that get_object_status finds it"`
}

type UpdateObjectConfigOutput = core.ObjectConfigUpdate
type CreateObjectOutput = core.CreatedObject

// configWriteAccess tells the role the configuration writes require.
const configWriteAccess = "Requires admin on the object namespace; a namespace configuration, nscfg, requires the squatter or root role."

// RegisterObjectConfigWriteTools registers the tools that write the
// configuration of an object. They are registered only when actions are
// enabled.
func RegisterObjectConfigWriteTools(registrar *Registrar, service *core.Service) error {
	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:  "update_object_config",
			Title: "Update object configuration",
			Description: "Change the configuration of one OpenSVC object: delete sections, unset keywords, then apply set operations, in this order, as the daemon does. The cluster configuration is the object cluster, path cluster, which requires admin on the namespace root, admin or root. " +
				"A set operation is <section>.<keyword><operator><value>: = sets the value, += appends one, -= removes one, |= appends one absent, ^= toggles one, and <section>.<keyword>[<index>]=<value> inserts at an index. " +
				"Read the MCP resource om3://resources/object-config when the server lists it, the reference of the format, in the same turn as the call, and the configuration with get_object_config; check the keywords of a resource with one list_object_config_keywords call, its driver and options, rather than from memory. " +
				"The daemon validates the result and writes nothing it refuses, with the reason: 400 for a value it does not accept; a keyword it does not know is written all the same, as a warning it does not return: check the keyword exists first; 403 for a keyword of a resource changed, or its section deleted, while the resource is up on a node, which must be stopped first; 409 when the configuration changed meanwhile, a value is held by a resource of another object on the same node, or a resize runs. " +
				"The configuration changes, not the running instances: a keyword may take effect at the next start or restart only. " +
				"With wait_seconds, the answer is held until the configuration reached every live node; past it, propagation_pending names the nodes not reached yet, the write being done: never repeat it, a += would apply twice. " +
				"Returns is_changed, false and outcome unchanged when the operations left the configuration as it was, and the keywords the operations name, read again: an unset keyword reads back empty, as a keyword never set does. " +
				configWriteAccess,
			Annotations: actionAnnotations(true, false),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input UpdateObjectConfigInput) (*mcp.CallToolResult, UpdateObjectConfigOutput, error) {
			result, err := service.UpdateObjectConfig(ctx, core.UpdateObjectConfigOptions{
				Path: input.Path, Set: input.Set, Unset: input.Unset, Delete: input.Delete, WaitSeconds: input.WaitSeconds,
			})
			if err != nil {
				return nil, UpdateObjectConfigOutput{}, err
			}
			return nil, result, nil
		},
	); err != nil {
		return err
	}
	return addTool(
		registrar,
		&mcp.Tool{
			Name:  "create_object",
			Title: "Create object",
			Description: "Create one OpenSVC object from its configuration file, given as INI text, such as a [DEFAULT] section with nodes = n1 n2 and a [fs#1] section with its keywords. " +
				"The daemon takes the configuration as copied from another object: the id and what it recorded of the things created for that object are left behind, and the object gets an id of its own; the set operations, as update_object_config takes them, apply after. " +
				"Nothing is provisioned or started: provision_object does it next. An object the daemon would start on its own, orchestrate ha or start, is flagged stopped on the node that created it, so that it does not start before anything is asked of it: flagged_stopped says so. " +
				"Read the MCP resource om3://resources/object-config when the server lists it, the reference of the format, in the same turn as the call, and start from the configuration of a similar object, which get_object_config gives, rather than from memory; check the keywords of each resource with one list_object_config_keywords call, its driver and options. " +
				"The daemon refuses, with the reason: an object that exists, whose configuration update_object_config changes; a configuration it does not accept, 400; resources holding a value a resource of another object holds on the same node, a mount point, a block device, a volume group or an address, 409 naming them. " +
				"Returns the configuration created, read again with the secrets redacted. " +
				configWriteAccess,
			Annotations: actionAnnotations(true, false),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input CreateObjectInput) (*mcp.CallToolResult, CreateObjectOutput, error) {
			result, err := service.CreateObject(ctx, core.CreateObjectOptions{
				Path: input.Path, Config: input.Config, Set: input.Set, WaitLocal: input.WaitLocal,
			})
			if err != nil {
				return nil, CreateObjectOutput{}, err
			}
			return nil, result, nil
		},
	)
}
