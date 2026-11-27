package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/om3-mcp/internal/core"
)

type GetRelayStatusInput struct {
	Remote bool `json:"remote,omitempty" jsonschema:"optional; false, the slots the daemon answering relays for the nodes of other clusters; true, the relays the heartbeats of this cluster go through, read through each of them"`
}

type GetRelayStatusOutput = core.RelayStatusList

type GetClusterStatusInput struct {
	NodeLimit    int    `json:"node_limit,omitempty" jsonschema:"optional node page size between 1 and 200; defaults to 100"`
	NodeCursor   string `json:"node_cursor,omitempty" jsonschema:"optional nodes.next_cursor returned by a previous call"`
	ObjectLimit  int    `json:"object_limit,omitempty" jsonschema:"optional actor object page size between 1 and 200; defaults to 100"`
	ObjectCursor string `json:"object_cursor,omitempty" jsonschema:"optional objects.next_cursor returned by a previous call"`
}

type GetClusterStatusOutput = core.ClusterStatusSnapshot

type GetClusterConfigInput struct{}

type GetClusterConfigOutput = core.ClusterConfig

func RegisterClusterTools(registrar *Registrar, service *core.Service) error {
	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:        "get_cluster_config",
			Title:       "Get cluster configuration",
			Description: "Read the bounded raw OpenSVC cluster configuration file for diagnostic context. The MCP always requests daemon-side secret redaction, returns at most 65536 bytes, and does not interpret the configuration. To read it by keyword, write it or list its keywords, use get_object_config, update_object_config and list_object_config_keywords with path cluster. Requires root access to the daemon endpoint.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ GetClusterConfigInput) (*mcp.CallToolResult, GetClusterConfigOutput, error) {
			config, err := service.GetClusterConfig(ctx)
			if err != nil {
				return nil, GetClusterConfigOutput{}, err
			}
			return nil, config, nil
		},
	); err != nil {
		return err
	}
	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:  "get_cluster_status",
			Title: "Get cluster status snapshot",
			Description: "Read a factual, bounded snapshot of the last-known OpenSVC cluster, node, heartbeat, and visible actor object status. " +
				"Exact daemon values, source timestamps, counts, and truncation metadata are preserved without MCP health verdicts, issue classification, or remediation advice. " +
				"Each object lists in instance_monitors its instances whose monitor state is not idle, such as provision failed or start failed, even when the object is down; state_counts.instance_monitor_state counts the instance monitor states of all pages. " +
				"Use cursors to continue node or actor object pages; this read-only call does not refresh instance drivers. " +
				"provenance.served_by names the node whose daemon answered, usually the node carrying the cluster VIP. " +
				"It does not list the cluster locks: list_cluster_locks does. " +
				"cluster.is_frozen is true when at least one node is frozen, not a freeze of the whole cluster: the frozen_at of each node says which. " +
				"cluster.config_issues are those of the cluster configuration only, never of the objects: validate_object_configs tells the objects whose configuration is in error or warning.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GetClusterStatusInput) (*mcp.CallToolResult, GetClusterStatusOutput, error) {
			status, err := service.GetClusterStatus(ctx, core.GetClusterStatusOptions{
				NodeLimit: input.NodeLimit, NodeCursor: input.NodeCursor,
				ObjectLimit: input.ObjectLimit, ObjectCursor: input.ObjectCursor,
			})
			if err != nil {
				return nil, GetClusterStatusOutput{}, err
			}
			return nil, status, nil
		},
	); err != nil {
		return err
	}
	if err := addTool(
		registrar,
		&mcp.Tool{
			Name:  "get_relay_status",
			Title: "Get heartbeat relay status",
			Description: "Read the heartbeat relay slots: without remote, the slots the daemon answering relays for the nodes of other clusters, root seeing them all, another account its own; with remote, the relays the heartbeats of type relay of this cluster go through, the daemon reading each relay with the credentials of its heartbeat, a relay in error returned with its status. " +
				"Each slot gives the cluster, node and address writing it, the size of its last message, never the message, and updated_at: an old updated_at on a relay alive is the sign of a node no longer beating through it. Requires the heartbeat grant or root.",
			Annotations: readOnlyClosedWorldAnnotations(),
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input GetRelayStatusInput) (*mcp.CallToolResult, GetRelayStatusOutput, error) {
			result, err := service.GetRelayStatus(ctx, input.Remote)
			if err != nil {
				return nil, GetRelayStatusOutput{}, err
			}
			return nil, result, nil
		},
	); err != nil {
		return err
	}
	return nil
}
