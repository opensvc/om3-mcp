# OpenSVC Daemon MCP Tools

On the HTTPS listener, with clusters configured with `auth` and exchange
profiles, every daemon tool has a required `cluster_id` input added by the
registrar. The domain-specific inputs below are unchanged.
`list_clusters(query?, limit?, cursor?)` discovers those names and IDs without
contacting daemons or checking grants.
See [token exchange](../token-exchange.md#tool-contracts) for the shared target
contract. Without exchange configuration, daemon tools remain listed but blocked.

Beside the tools, the server serves Agent Skills, procedures that combine
them, as the MCP Skills extension: see [skills](../../skills/README.md).

This directory documents the human-facing contracts of the OpenSVC daemon MCP
tools. Names, descriptions, annotations and JSON Schemas are defined in
`internal/tools` and exposed by bearer-checked `tools/list` calls. These
documents describe the implemented tool contracts.

## Domains

| Domain | Tools | Documentation |
|---|---|---|
| Catalogue | `list_clusters` | [Multi-cluster tools](../token-exchange.md#tool-contracts) |
| Daemon | `list_daemon_executions`, `list_daemon_orchestrations`, `list_dns_records`, `get_daemon_log_level` | [Daemon tools](daemon.md) |
| Cluster | `get_cluster_config`, `get_cluster_status`, `list_cluster_locks`, `get_relay_status` | [Cluster tools](cluster.md) |
| Node | `get_node_config`, `get_node_config_keywords`, `list_node_config_keywords`, `get_node_status`, `get_node_logs`, `list_node_properties`, `list_node_hardware`, `list_node_packages`, `get_node_daemon_metrics`, `probe_node_reachability`, `list_node_capabilities`, `list_node_drivers`, `list_node_disks`, `get_node_san_topology`, `list_node_ip_addresses`, `list_node_network_reservations`, `list_node_locks`, `get_node_drbd_config` | [Node tools](node.md) |
| Objects | `list_cluster_objects`, `get_object_status`, `get_object_config`, `list_object_config_keywords`, `list_object_data_keys`, `get_object_data_key` | [Object tools](objects.md) |
| Instances | `list_object_instances`, `get_instance_status`, `get_instance_logs`, `refresh_instance_status`, `list_instance_sync_snapshots`, `get_instance_resource_file` | [Instance tools](instances.md) |
| Resources | `list_cluster_ip_resources`, `list_object_resources`, `list_resource_info`, `get_container_logs` | [Resource tools](resources.md) |
| Scheduler | `list_schedules` | [Scheduler tools](schedules.md) |
| Pools | `list_storage_pools`, `list_pool_volumes` | [Pool tools](pools.md) |
| Networks | `list_networks`, `list_network_ips` | [Network tools](networks.md) |
| Auth | `get_caller_identity` | [Auth tools](auth.md) |
| Previews, for the client only | `preview_object_config_update`, `preview_node_config_update`, `preview_object_create`, `preview_action` | [Preview tools](previews.md) |
| Actions (`OPENSVC_MCP_ACTIONS=enabled`) | `freeze_object`, `unfreeze_object`, `abort_object_orchestration`, `start_object`, `stop_object`, `restart_object`, `switch_object`, `giveback_object`, `provision_object`, `resize_object`, `cap_object`, `enable_object`, `disable_object`, `delete_object`, `unprovision_object`, `purge_object` | [Action tools](actions.md) |
| Configuration writes (`OPENSVC_MCP_ACTIONS=enabled`) | `update_object_config`, `create_object`, `update_node_config` | [Configuration write tools](config-writes.md) |
| Instance actions (`OPENSVC_MCP_ACTIONS=enabled`) | `start_instance`, `stop_instance`, `restart_instance`, `freeze_instance`, `unfreeze_instance`, `clear_instance`, `unprovision_instance`, `mark_instance_unprovisioned`, `run_task`, `start_instance_standby`, `shutdown_instance`, `update_instance_caps`, `reset_instance_caps` | [Instance action tools](instance-actions.md) |
| Node actions (`OPENSVC_MCP_ACTIONS=enabled`) | `freeze_node`, `unfreeze_node`, `drain_node`, `abort_node`, `clear_node`, `refresh_node_capabilities`, `refresh_node_hardware`, `refresh_node_disks`, `refresh_node_packages`, `scan_node_scsi`, `dequeue_node_actions`, `refresh_resource_info` | [Node action tools](node-actions.md) |
| Data writes (`OPENSVC_MCP_ACTIONS=enabled`) | `set_object_data_key`, `delete_object_data_key`, `create_object_certificate`, `renew_object_certificate` | [Data write tools](data-writes.md) |
| Cluster actions (`OPENSVC_MCP_ACTIONS=enabled`) | `freeze_cluster`, `unfreeze_cluster`, `abort_cluster_orchestration` | [Cluster action tools](cluster-actions.md) |
| Daemon actions (`OPENSVC_MCP_ACTIONS=enabled`) | `set_daemon_log_level`, `start_heartbeat`, `restart_heartbeat`, `restart_daemon_listener`, `restart_daemon` | [Daemon action tools](daemon-actions.md) |

## Tool search

Each tool carries in its `_meta` the keys the tool search of an AI client
reads, such as the OpenSVC AI agent, which shows the model a part of a large
catalog:

- `opensvc/keywords`: the words a question may use for the tool that its
  description does not hold, such as `ping` for `probe_node_reachability` or
  `secret` for `list_object_data_keys`. They only rank the tool in a search;
  the model does not see them.
- `opensvc/core`: true for the tools always shown, the others being found by
  a search: `get_cluster_status`, `list_cluster_objects`, `get_object_status`,
  `get_instance_status`, `get_node_status` and `get_caller_identity`.

Two more keys concern the confirmation of a call, see
[preview tools](previews.md): `opensvc/preview_tool` and
`opensvc/preview_wraps_arguments` on the tools the user confirms, and
`opensvc/hidden` on the preview tools, which a client never shows the model
and which carry no keywords.

The registrar sets them from `internal/tools/search_meta.go` and refuses a
tool without keywords, a hidden one aside; a tool declaration sets no `_meta`. The descriptions of
the core tools are kept as replays measured them: changing them changes what
the model does on the first turn of a question.

## Authentication and visibility

Remote agents supply an OAuth access token intended for the MCP resource on
every `/mcp` request. The MCP verifies its issuer, audience, signature and
lifetime. No MCP business scope is required. Each daemon tool then requires
an explicit catalogue `cluster_id` and a successful confidential token exchange.
Only the exchanged token is sent to the configured VIP. Grants and namespace
visibility remain authoritative at the daemon.

Missing or invalid incoming credentials return HTTP 401 with the OAuth
discovery challenge. Tool target errors, SSO exchange refusals and daemon
refusals return `isError=true`. SSO descriptions and response bodies are not
exposed. Daemon errors use the existing bounded API error contract. No request
or session can replace another call's identity or target.

On the delegated Unix socket, tools take no `cluster_id` argument and
`list_clusters` is not offered: the `X-OpenSVC-Cluster-ID` header binds every
call to one cluster. See [token delegation](../delegation.md).

## Node targets

A tool that diagnoses one node requires `node`: one exact OpenSVC node name of
at most 255 characters, using letters, digits, `.`, `_` or `-`. Empty values,
`.`, `..`, surrounding whitespace, wildcards, selectors and the daemon alias
`_` are rejected before any daemon request. The alias names the daemon
receiving the request, which is the node carrying the cluster VIP and may
change on failover. Optional node filters, such as those of
`list_storage_pools`, `list_cluster_ip_resources` or `list_network_ips`, follow
the same rule when set.

A name the daemon has no status of, which it answers with a 404
`node status data not found`, is refused by every tool with the daemon error
followed by `get_cluster_status lists the nodes of the cluster`, as the
refusal of an invalid node is. On 2026-10-30, with no node named in the
conversation, a model gave `list_daemon_orchestrations` a made-up node, read
the bare 404 and asked the user for a node: replayed 20 times after that
refusal, it gave up 13 times, and 2 times with the refusal naming
`get_cluster_status`. Naming `get_cluster_status` in the description of every
required `node` was left out: it shifted the ranking of `search_tools`, which
indexes the descriptions of the arguments, for no measured gain.

## Freshness model

Every successful tool result includes a `provenance` object:

```json
{
  "source": "opensvc_daemon",
  "observed_at": "2026-09-16T10:00:00Z"
}
```

For `list_clusters`, source is `opensvc_mcp_catalog`. For daemon tools,
`source` identifies the daemon API that supplied the MCP result, not
necessarily the original data store (for example, instance logs originate in
the node journal). `observed_at` is the UTC time when the MCP finished
collecting and normalizing the result. It is not a status update timestamp and
does not guarantee that the underlying data is fresh. Tool errors have no
successful result provenance.

`get_cluster_status` and `get_node_status` add `served_by`: the node whose
daemon answered the request, usually the node carrying the cluster VIP. Only
their daemon response names it; the other tools omit the field.

Read-only status tools return the last-known state held by the daemon. They do
not execute resource drivers. An out-of-band runtime failure or recovery may be
absent until OpenSVC refreshes the instance status.

Always inspect daemon-provided `updated_at`, where available, before relying on
status for a diagnosis. Unlike `observed_at`, it dates the OpenSVC status
itself. Use `refresh_instance_status` only for one exact object instance when
a fresher probe is required. It is non-destructive, but it executes status
drivers and updates daemon state.

## Examples

The JSON examples use representative values and illustrate the documented
response shape. Timestamps and provenance values are illustrative:

| Value | Example |
|---|---|
| Cluster | `cluster-a` |
| Node | `node-a` |
| Object | `prod/svc/redis` |
| Resource | `container#redis` |

Domain examples show the business arguments; add the required `cluster_id`
for external OAuth calls. They show the `arguments` object passed to `tools/call`, not the complete
JSON-RPC envelope. Timestamps, process identifiers, and UUIDs
are representative and will differ between calls.

## Safety summary

| Tool | Read-only | Destructive | Required OpenSVC access |
|---|---:|---:|---|
| `list_daemon_executions` | Yes | No | `root` |
| `list_daemon_orchestrations` | Yes | No | `root` |
| `get_daemon_log_level` | Yes | No | `root` |
| `get_cluster_config` | Yes | No | `root` |
| `get_cluster_status` | Yes | No | `guest` or higher |
| `list_cluster_locks` | Yes | No | `root` |
| `get_relay_status` | Yes | No | `heartbeat` grant or `root` |
| `get_node_config` | Yes | No | `root` |
| `get_node_config_keywords` | Yes | No | `root` |
| `list_node_config_keywords` | Yes | No | Any authenticated caller |
| `get_node_status` | Yes | No | `guest` or higher |
| `list_node_properties` | Yes | No | `root` |
| `list_node_hardware` | Yes | No | `root` |
| `list_node_packages` | Yes | No | `root` |
| `list_node_capabilities` | Yes | No | `root` |
| `list_node_drivers` | Yes | No | `root` |
| `get_node_daemon_metrics` | Yes | No | OpenSVC JWT; daemon endpoint policy |
| `probe_node_reachability` | Yes | No | `root` |
| `get_node_logs` | Yes | No | `root` |
| `list_node_network_reservations` | Yes | No | `root` |
| `list_node_locks` | Yes | No | `root` |
| `get_node_drbd_config` | Yes | No | `root` |
| `list_cluster_objects` | Yes | No | Visible namespaces |
| `get_object_status` | Yes | No | Visibility on the object namespace |
| `get_object_config` | Yes | No | Visibility on the object namespace |
| `list_object_config_keywords` | Yes | No | `guest` or higher on the object namespace |
| `get_object_data_key` | Yes | No | `guest` or higher on the namespace of the cfg; sec and usr refused |
| `list_object_instances` | Yes | No | Visibility on the object namespace |
| `get_instance_status` | Yes | No | Namespace `guest` or higher |
| `get_instance_logs` | Yes | No | `root` |
| `list_instance_sync_snapshots` | Yes | No | Namespace `guest` or higher |
| `get_instance_resource_file` | Yes | No | `root` |
| `refresh_instance_status` | No | No | `operator`, `admin`, or `root` |
| `freeze_object` | No | No | `operator` on the object namespace |
| `unfreeze_object` | No | Yes | `operator` on the object namespace |
| `abort_object_orchestration` | No | No | `operator` on the object namespace |
| `start_object` | No | No | `operator` on the object namespace |
| `stop_object` | No | Yes | `operator` on the object namespace |
| `restart_object` | No | Yes | `operator` on the object namespace |
| `switch_object` | No | Yes | `operator` on the object namespace |
| `giveback_object` | No | Yes | `operator` on the object namespace |
| `provision_object` | No | No | `admin` on the object namespace |
| `resize_object` | No | No | `admin` on the object namespace |
| `cap_object` | No | Yes | `admin` on the object namespace |
| `enable_object` | No | No | `admin` on the object namespace |
| `disable_object` | No | Yes | `admin` on the object namespace |
| `delete_object` | No | Yes | `admin` on the object namespace |
| `unprovision_object` | No | Yes | `admin` on the object namespace |
| `purge_object` | No | Yes | `admin` on the object namespace |
| `start_instance` | No | No | `operator` on the object namespace |
| `stop_instance` | No | Yes | `operator` on the object namespace |
| `restart_instance` | No | Yes | `operator` on the object namespace |
| `freeze_instance` | No | No | `operator` on the object namespace |
| `unfreeze_instance` | No | Yes | `operator` on the object namespace |
| `clear_instance` | No | No | `operator` on the object namespace |
| `unprovision_instance` | No | Yes | `admin` on the object namespace |
| `mark_instance_unprovisioned` | No | No | `admin` on the object namespace |
| `run_task` | No | Yes | `admin` on the object namespace |
| `start_instance_standby` | No | No | `operator` on the object namespace |
| `shutdown_instance` | No | Yes | `operator` on the object namespace |
| `update_instance_caps` | No | Yes | `admin` on the object namespace |
| `reset_instance_caps` | No | No | `admin` on the object namespace |
| `freeze_node` | No | No | `root` |
| `unfreeze_node` | No | Yes | `root` |
| `drain_node` | No | Yes | `root` |
| `abort_node` | No | No | `root` |
| `clear_node` | No | No | `root` |
| `refresh_node_capabilities` | No | No | `root` |
| `refresh_node_hardware` | No | No | `root` |
| `refresh_node_disks` | No | No | `root` |
| `refresh_node_packages` | No | No | `root` |
| `scan_node_scsi` | No | No | `root` |
| `dequeue_node_actions` | No | Yes | `root` |
| `refresh_resource_info` | No | No | `operator` on the object namespace |
| `set_object_data_key` | No | Yes | `admin` on the object namespace; for a usr, its grants too, or `root` |
| `delete_object_data_key` | No | Yes | `admin` on the object namespace; for a usr, its grants too, or `root` |
| `create_object_certificate` | No | Yes | `admin` on the namespace of the sec and of its ca; `root` for ACME through `acme.webroot` |
| `renew_object_certificate` | No | No | `admin` on the namespace of the sec and of its ca; `root` for ACME through `acme.webroot` |
| `update_object_config` | No | Yes | `admin` on the object namespace; `squatter` or `root` for an `nscfg`; `admin` on `root`, a global `admin` or `root` for `cluster` |
| `update_node_config` | No | Yes | `root` |
| `freeze_cluster` | No | No | `root` |
| `unfreeze_cluster` | No | Yes | `root` |
| `abort_cluster_orchestration` | No | No | `root` |
| `set_daemon_log_level` | No | No | `root` |
| `start_heartbeat` | No | No | `root` |
| `restart_heartbeat` | No | Yes | `root` |
| `restart_daemon_listener` | No | Yes | `root` |
| `restart_daemon` | No | Yes | `root` |
| `create_object` | No | No | `admin` on the object namespace; `squatter` or `root` for an `nscfg` |
| `list_cluster_ip_resources` | Yes | No | Visible namespaces |
| `list_object_resources` | Yes | No | Visibility on the object namespace |
| `list_resource_info` | Yes | No | Namespace `guest` or higher |
| `get_container_logs` | Yes | No | `root` |
| `list_schedules` | Yes | No | `root` for node scope; namespace `guest` or higher for object and instance scopes |

Annotations are client hints. The daemon's authorization decision is always
authoritative.
