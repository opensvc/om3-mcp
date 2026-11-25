package tools

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/om3-mcp/internal/core"
)

type ListInstanceSyncSnapshotsInput struct {
	Path string `json:"path" jsonschema:"the exact canonical OpenSVC svc or vol object path; no wildcard or selector"`
	Node string `json:"node" jsonschema:"the exact name of the node of the instance; no wildcard, selector or the _ alias"`
	Rid  string `json:"rid,omitempty" jsonschema:"optional sync resource, such as sync#1; omitted, sync, every sync resource of the instance"`
}

type GetInstanceResourceFileInput struct {
	Path string `json:"path" jsonschema:"the exact canonical OpenSVC svc or vol object path; no wildcard or selector"`
	Node string `json:"node" jsonschema:"the exact name of the node of the instance; no wildcard, selector or the _ alias"`
	Rid  string `json:"rid" jsonschema:"the exact resource id declaring the file, such as disk#2"`
	Name string `json:"name" jsonschema:"the file, as get_instance_status gives it in resources[rid].files[].name, or its base name, such as sub_devs, when one file only of the resource has it"`
}

type ListInstanceSyncSnapshotsOutput = core.InstanceSyncSnapshotList

type GetInstanceResourceFileOutput = core.InstanceResourceFileContent

type ListObjectInstancesInput struct {
	Path   string `json:"path" jsonschema:"the exact canonical OpenSVC object path returned by list_cluster_objects"`
	Node   string `json:"node,omitempty" jsonschema:"optional exact OpenSVC node name used to filter instances"`
	Limit  int    `json:"limit,omitempty" jsonschema:"optional page size between 1 and 100; defaults to 50"`
	Cursor string `json:"cursor,omitempty" jsonschema:"optional next_cursor returned by a previous call with the same path and node filter"`
}

type ListObjectInstancesOutput = core.ObjectInstanceList

type GetInstanceStatusInput struct {
	Path string `json:"path" jsonschema:"the exact canonical object path discovered with list_object_instances"`
	Node string `json:"node" jsonschema:"the exact node hosting the instance; local aliases and selectors are not accepted"`
}

type GetInstanceStatusOutput = core.InstanceStatusSnapshot

type RefreshInstanceStatusInput struct {
	Path           string `json:"path" jsonschema:"the exact canonical OpenSVC object path returned by list_cluster_objects"`
	Node           string `json:"node" jsonschema:"the exact node name hosting the instance to refresh"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"optional polling timeout in seconds between 5 and 120; defaults to 30"`
}

type RefreshInstanceStatusOutput = core.RefreshInstanceStatusResult

type GetInstanceLogsInput struct {
	Path  string `json:"path" jsonschema:"the exact canonical OpenSVC object path returned by list_cluster_objects"`
	Node  string `json:"node" jsonschema:"the exact node name hosting the instance whose OpenSVC logs are requested"`
	Lines int    `json:"lines,omitempty" jsonschema:"optional maximum number of recent log entries between 1 and 100; defaults to 50"`
}

type GetInstanceLogsOutput = core.InstanceLogList

func RegisterInstanceTools(registrar *Registrar, service *core.Service) error {
	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:        "get_instance_status",
			Title:       "Get instance status",
			Description: "Read the cached configuration, monitor and detailed resource status of one exact OpenSVC object instance after list_object_instances. Nested inventories are sorted and bounded with explicit truncation metadata; placement and message order are preserved. This passive read does not run status drivers; inspect each block's updated_at for freshness. A monitor state ending in failed, such as start failed, keeps no error: its cause is in get_instance_logs, which failure_note recalls. Requires guest access on the object's namespace.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GetInstanceStatusInput) (*mcp.CallToolResult, GetInstanceStatusOutput, error) {
			result, err := service.GetInstanceStatus(ctx, core.GetInstanceStatusOptions{Path: input.Path, Node: input.Node})
			return nil, result, err
		},
	); err != nil {
		return err
	}

	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:        "get_instance_logs",
			Title:       "Get instance logs",
			Description: "Read bounded recent OpenSVC orchestration and daemon logs for one exact object instance: they hold the errors of its actions, such as the cause of a start failed, a command not found, a device missing or an image not pulled, which its status does not keep. This finite read does not follow the stream and does not return workload stdout or stderr; the daemon currently requires root access for this endpoint.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GetInstanceLogsInput) (*mcp.CallToolResult, GetInstanceLogsOutput, error) {
			logs, err := service.GetInstanceLogs(ctx, core.GetInstanceLogsOptions{
				Path:  input.Path,
				Node:  input.Node,
				Lines: input.Lines,
			})
			if err != nil {
				return nil, GetInstanceLogsOutput{}, err
			}
			return nil, logs, nil
		},
	); err != nil {
		return err
	}

	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:        "refresh_instance_status",
			Title:       "Refresh instance status",
			Description: "Actively run an OpenSVC status probe for one exact object instance, then poll until a different non-empty updated_at is observed or the bounded timeout expires. A changed timestamp does not assert instance health or prove that this action caused the update. Requires operator access on the namespace; it is non-destructive but executes resource drivers and updates daemon state.",
			Annotations: activeNonDestructiveClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input RefreshInstanceStatusInput) (*mcp.CallToolResult, RefreshInstanceStatusOutput, error) {
			result, err := service.RefreshInstanceStatus(ctx, core.RefreshInstanceStatusOptions{
				Path:    input.Path,
				Node:    input.Node,
				Timeout: time.Duration(input.TimeoutSeconds) * time.Second,
			})
			if err != nil {
				return nil, RefreshInstanceStatusOutput{}, err
			}
			return nil, result, nil
		},
	); err != nil {
		return err
	}

	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:        "list_object_instances",
			Title:       "List object instances",
			Description: "Inspect the last-known monitor state, target state, availability, and bounded counts of exact resource status values for the instances of one exact OpenSVC object. The MCP does not merge standby states or classify unknown values. This read-only call does not run a status refresh; compare updated_at with the current time before diagnosing a node instance.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input ListObjectInstancesInput) (*mcp.CallToolResult, ListObjectInstancesOutput, error) {
			instances, err := service.ListObjectInstances(ctx, core.ListObjectInstancesOptions{
				Path:   input.Path,
				Node:   input.Node,
				Limit:  input.Limit,
				Cursor: input.Cursor,
			})
			if err != nil {
				return nil, ListObjectInstancesOutput{}, err
			}
			return nil, instances, nil
		},
	); err != nil {
		return err
	}
	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:  "list_instance_sync_snapshots",
			Title: "List instance sync snapshots",
			Description: "List the snapshots the sync resources of the instance of one OpenSVC svc or vol object on one node hold, the restorable points a sync.store keeps, the most recent first: rid, id, created_at, node, members and location. " +
				"Only sync.store lists snapshots; the other sync drivers return none. The id is the session of the sync update that took the snapshot, what a restore, not offered, would take. A sync resource in error fails the read with its cause, such as a store whose directory is missing. Requires guest access on the object namespace.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input ListInstanceSyncSnapshotsInput) (*mcp.CallToolResult, ListInstanceSyncSnapshotsOutput, error) {
			result, err := service.ListInstanceSyncSnapshots(ctx, core.ListInstanceSyncSnapshotsOptions{Path: input.Path, Node: input.Node, RID: input.Rid})
			if err != nil {
				return nil, ListInstanceSyncSnapshotsOutput{}, err
			}
			return nil, result, nil
		},
	); err != nil {
		return err
	}
	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:  "get_instance_resource_file",
			Title: "Get instance resource file",
			Description: "Read one file a resource of the instance of one OpenSVC svc or vol object declares to replicate to its peers: the domain definition of a kvm, lxc or vbox container, the metadata of a shared md disk, the sub_devs of a zpool. " +
				"The list is closed: get_instance_status gives the files of each resource in resources[rid].files, with their csum, which compared between nodes tells a drift without reading the files; the MCP takes the full name or a base name one declared file only has, such as sub_devs, and refuses a file the resource does not declare, naming those it does. The daemon serves the file while the instance is up. " +
				"Returns the text, at most 64 KiB, its passwd attributes masked; a binary file, its size and csum only. Requires root.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GetInstanceResourceFileInput) (*mcp.CallToolResult, GetInstanceResourceFileOutput, error) {
			result, err := service.GetInstanceResourceFile(ctx, input.Path, input.Node, input.Rid, input.Name)
			if err != nil {
				return nil, GetInstanceResourceFileOutput{}, err
			}
			return nil, result, nil
		},
	); err != nil {
		return err
	}
	return nil
}
