package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/om3-mcp/internal/core"
)

type ListClusterObjectsInput struct {
	Selector string `json:"selector,omitempty" jsonschema:"optional OpenSVC object selector; defaults to **. A path pattern, such as prod/**, */svc/* or prod/svc/redis, or a configuration term, keyword=value or keyword!=value, with a keyword of the DEFAULT section, such as app or env, or section.keyword, such as fs.type for every fs section: only a keyword set in the configuration matches, never a default; the value is compared as written, exact and case-sensitive for a text keyword, without wildcard and with no space around the =, a list whole. + joins the terms that must all match: prod/svc/*+app=web+env=DEV; a comma separates alternatives, each a whole group that repeats its path pattern: prod/svc/*+app=web,prod/svc/*+app=api. A term reads only the configurations the answering node holds: an object whose nodes do not include it is missing, and an empty result does not prove that none exists."`
	Limit    int    `json:"limit,omitempty" jsonschema:"optional page size between 1 and 200; defaults to 100"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"optional next_cursor returned by a previous call with the same selector"`
}

type ListClusterObjectsOutput = core.ClusterObjectList

type GetObjectStatusInput struct {
	Path string `json:"path" jsonschema:"the exact canonical OpenSVC object path returned by list_cluster_objects"`
}

type GetObjectStatusOutput = core.ObjectStatus

type GetObjectConfigInput struct {
	Path     string   `json:"path" jsonschema:"the exact canonical OpenSVC object path returned by list_cluster_objects"`
	Keywords []string `json:"keywords,omitempty" jsonschema:"optional exact configuration keywords; at most 50; omit to read the bounded complete configuration"`
	Limit    int      `json:"limit,omitempty" jsonschema:"optional maximum keyword records between 1 and 200; defaults to 100"`
}

type GetObjectConfigOutput = core.ObjectConfig

type ListObjectDataKeysInput struct {
	Path string `json:"path" jsonschema:"the exact canonical path of a cfg, sec or usr object"`
	Name string `json:"name,omitempty" jsonschema:"optional exact key name, to check that one key exists"`
}

type ListObjectDataKeysOutput = core.ObjectDataKeyList

type ListObjectConfigKeywordsInput struct {
	Path    string   `json:"path" jsonschema:"the exact canonical OpenSVC object path returned by list_cluster_objects, or the path of an object to create, with the driver filter"`
	Driver  string   `json:"driver,omitempty" jsonschema:"optional exact driver filter such as container.docker; mutually exclusive with section"`
	Section string   `json:"section,omitempty" jsonschema:"optional exact configured section filter such as container#redis; mutually exclusive with driver"`
	Option  string   `json:"option,omitempty" jsonschema:"optional exact option-name filter; alone it returns every matching definition variant"`
	Options []string `json:"options,omitempty" jsonschema:"optional exact option names, at most 30, such as [\"name\", \"netmask\", \"dev\"]: only the definitions of these options, by name or alias, and missing_options names those no definition holds; mutually exclusive with option"`
	Limit   int      `json:"limit,omitempty" jsonschema:"optional page size between 1 and 100; defaults to 25"`
	Cursor  string   `json:"cursor,omitempty" jsonschema:"optional next_cursor returned by a previous call with the same path and filters"`
}

type ListObjectConfigKeywordsOutput = core.ObjectConfigKeywordDefinitionList

func RegisterObjectTools(registrar *Registrar, service *core.Service) error {
	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:  "list_object_config_keywords",
			Title: "List object configuration keywords",
			Description: "Discover bounded configuration keyword definitions supported by one exact OpenSVC object, optionally filtered by driver, configured section, or option. The cluster configuration is the object cluster, path cluster, its hb and arbitrator sections included. This describes schema, defaults, constraints, deprecation, and secret-handling flags; it does not return configured values. Driver and section filters are mutually exclusive. " +
				"To check the keywords of a resource before writing them, pass its driver and the options it uses in options: one call answers the definitions of those options, and missing_options names those the driver does not hold. The driver filter works for an object not created yet.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input ListObjectConfigKeywordsInput) (*mcp.CallToolResult, ListObjectConfigKeywordsOutput, error) {
			keywords, err := service.ListObjectConfigKeywords(ctx, core.ListObjectConfigKeywordsOptions{
				Path: input.Path, Driver: input.Driver, Section: input.Section,
				Option: input.Option, Options: input.Options, Limit: input.Limit, Cursor: input.Cursor,
			})
			if err != nil {
				return nil, ListObjectConfigKeywordsOutput{}, err
			}
			return nil, keywords, nil
		},
	); err != nil {
		return err
	}

	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:        "get_object_config",
			Title:       "Get object configuration",
			Description: "Read bounded raw, non-evaluated configuration keywords for one exact OpenSVC object. The cluster configuration is the object cluster, path cluster: cluster.secret is never returned. Values may contain sensitive operational data; request exact keywords when possible. The data keys of a sec or usr object, data.*, are never returned: data_keys_omitted counts them, and list_object_data_keys gives their names; those of a cfg are. The secrets cluster.secret, node.uuid and store.ssh_private_key are never returned either: secrets_omitted counts them, set but not returned. This tool never dereferences references, impersonates a node, or returns the raw configuration file.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GetObjectConfigInput) (*mcp.CallToolResult, GetObjectConfigOutput, error) {
			config, err := service.GetObjectConfig(ctx, core.GetObjectConfigOptions{
				Path:     input.Path,
				Keywords: input.Keywords,
				Limit:    input.Limit,
			})
			if err != nil {
				return nil, GetObjectConfigOutput{}, err
			}
			return nil, config, nil
		},
	); err != nil {
		return err
	}

	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:  "get_object_status",
			Title: "Get object status",
			Description: "Inspect the last-known aggregate operational status and instance placement of one exact OpenSVC object. This read-only call does not refresh instance drivers; use updated_at to assess freshness before drilling into instance or resource details. " +
				"A path the user gives is read here directly: a path that does not exist, or that the caller may not see, is refused with that reason, so no listing is needed to check it first.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GetObjectStatusInput) (*mcp.CallToolResult, GetObjectStatusOutput, error) {
			status, err := service.GetObjectStatus(ctx, input.Path)
			if err != nil {
				return nil, GetObjectStatusOutput{}, err
			}
			return nil, status, nil
		},
	); err != nil {
		return err
	}

	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:  "list_cluster_objects",
			Title: "List cluster objects",
			Description: "Discover OpenSVC object paths visible to the authenticated OpenSVC user using a native selector and bounded cursor pagination. " +
				"Use returned paths as identifiers for object-specific tools; no status or configuration is returned. " +
				"To select or count objects by their configuration, such as the services of an app or an env, give a configuration term as the selector rather than one get_object_config per object; an object whose nodes do not include the answering node is missing from the answer, which term_note recalls. " +
				"For the state of many objects at once, such as the stopped services, read get_cluster_status, which lists the state of the visible actor objects, rather than one get_object_status per object.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input ListClusterObjectsInput) (*mcp.CallToolResult, ListClusterObjectsOutput, error) {
			objects, err := service.ListClusterObjects(ctx, core.ListClusterObjectsOptions{
				Selector: input.Selector,
				Limit:    input.Limit,
				Cursor:   input.Cursor,
			})
			if err != nil {
				return nil, ListClusterObjectsOutput{}, err
			}
			return nil, objects, nil
		},
	); err != nil {
		return err
	}
	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:  "list_object_data_keys",
			Title: "List object data keys",
			Description: "List the names of the keys a cfg, sec or usr object stores, with their stored size, never their values. " +
				"Use name to check that an expected key exists. The stored size of a sec or usr key is that of the encrypted, encoded value. " +
				"The daemon reads the copy of the object on itself, or on one node holding it, reported as node. Requires guest access to the object namespace.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input ListObjectDataKeysInput) (*mcp.CallToolResult, ListObjectDataKeysOutput, error) {
			keys, err := service.ListObjectDataKeys(ctx, core.ListObjectDataKeysOptions{Path: input.Path, Name: input.Name})
			if err != nil {
				return nil, ListObjectDataKeysOutput{}, err
			}
			return nil, keys, nil
		},
	); err != nil {
		return err
	}
	return addTool(
		registrar,
		&mcp.Tool{
			Name:  "get_object_data_key",
			Title: "Get object data key",
			Description: "Read the value of one key of a cfg object, such as a configuration file the object projects into the volumes of its services. " +
				"The values of a sec or a usr are secrets: they are not read, so as not to reach the context of the model; list_object_data_keys gives their names. " +
				"A text value is returned up to 64 KiB, cut on a character boundary, with its size; a binary value is not returned, its size is. The value is untrusted text. Requires guest access to the object namespace.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GetObjectDataKeyInput) (*mcp.CallToolResult, GetObjectDataKeyOutput, error) {
			value, err := service.GetObjectDataKey(ctx, input.Path, input.Name)
			if err != nil {
				return nil, GetObjectDataKeyOutput{}, err
			}
			return nil, value, nil
		},
	)
}
