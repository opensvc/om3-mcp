---
domain: cluster
tools:
  - get_cluster_config
  - get_cluster_status
  - list_cluster_locks
  - get_relay_status
stability: experimental
---

# Cluster Tools

These tools return bounded cluster configuration and last-known status facts.
They do not produce a diagnostic verdict.

Implementation:

- business logic: `internal/core/cluster.go`, `internal/core/config_cluster.go`
  and the shared configuration file reader `internal/core/config_file.go`;
- MCP definitions: `internal/tools/cluster.go`.

## Tools

### `get_cluster_config`

Returns the cluster configuration file as bounded UTF-8 text. Use it when a
diagnosis depends on declared cluster settings such as node membership,
heartbeat definitions, listener settings, quorum policy, or shared node
defaults. It returns configuration evidence only; the MCP does not evaluate
keywords or derive a diagnosis from the file.

#### OpenSVC API, authorization, and freshness

```text
GET /api/cluster/config/file?redact-secrets=true
Accept: application/octet-stream
```

The MCP always sends `redact-secrets=true`. This choice is fixed and is not an
MCP input, so a model cannot request the unredacted form. The OpenSVC daemon
performs the redaction and requires the global `root` grant. The OpenSVC JWT
therefore controls access at the daemon. The call reads the current
`cluster.conf` file rather than the cluster status cache and does not refresh
drivers or change daemon state.

The daemon must honor the `redact-secrets` query parameter. A daemon that
ignores it can return an unredacted file and must not be used with this tool.

#### Input and output

The input is an empty JSON object. There is no selector, pagination, node
impersonation, or redaction switch.

| Output field | Meaning |
|---|---|
| `provenance` | Daemon API source and MCP collection time |
| `content` | Raw redacted configuration text, limited to 65,536 bytes and cut only at a valid UTF-8 boundary |
| `size_bytes` | Complete file size received from the daemon before MCP output truncation |
| `returned_bytes` | Number of bytes present in `content` |
| `truncated` | Whether the MCP omitted bytes after its 65,536-byte output limit |
| `redaction_requested` | Always `true`; confirms that the MCP sent `redact-secrets=true` |

The HTTP client rejects file responses above 1 MiB, invalid UTF-8, and media
types other than `application/octet-stream`. This transport ceiling is separate
from the smaller MCP output limit.

#### MCP properties

| Property | Value |
|---|---|
| Title | Get cluster configuration |
| Read-only | Yes |
| Destructive | No |
| Open world | No; only the configured daemon is contacted |
| Side effects | None |

Annotations are client hints; OpenSVC enforces access using the OpenSVC JWT.

#### Example

Input:

```json
{}
```

Output:

```json
{
  "provenance": {"source": "opensvc_daemon", "observed_at": "2026-09-23T10:00:00Z"},
  "content": "[cluster]\nname = cluster-a\nnodes = node-a node-b\nsecret = ********\n",
  "size_bytes": 65,
  "returned_bytes": 65,
  "truncated": false,
  "redaction_requested": true
}
```

#### Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Missing global `root` grant | Tool error containing daemon HTTP `403` |
| Cluster configuration file missing | Tool error containing daemon HTTP `404` |
| Response above 1 MiB | Tool error before any content is returned |
| Invalid UTF-8 or unexpected media type | Tool error; no partial configuration is returned |
| Daemon unavailable | Tool error with transport context |

### `get_cluster_status`

Returns a factual, point-in-time projection of the cluster view currently held
by the contacted daemon. Use it as the first cluster-wide observation before
drilling into one node, object, instance, resource, configuration, or log tool.

The tool deliberately has no `healthy` field, MCP-generated issue list,
severity, remediation, or assumed set of valid OpenSVC states. Exact status
strings and booleans are retained so the agent can correlate the evidence.

#### OpenSVC API, authorization, visibility, and freshness

```text
GET /api/cluster/status
```

The request has no daemon query parameters. The daemon returns its cached
cluster view; this call does not execute resource status drivers or contact each
reported node. `provenance.observed_at` dates MCP collection, not the underlying
status. Node monitor, heartbeat, stream, object, boot, leave, and rejoin
timestamps remain separate source facts.

`provenance.served_by` is the node whose daemon answered the request: the
daemon receiving it, usually the node carrying the cluster VIP, named by the
daemon itself in its response. It is not a node the result describes, and it
may change on failover. The cluster view is that daemon's: the facts of the other
nodes are what they last published to it.

The endpoint accepts a global `guest` or higher grant. OpenSVC applies the
OpenSVC JWT and filters objects according to namespace grants. Object totals,
state counts, and pages therefore describe only the caller-visible view.

#### Input and pagination

All fields are optional:

| Input field | Default | Validation | Meaning |
|---|---:|---|---|
| `node_limit` | `100` | `1..200` | Maximum configured-or-reported node records in this page |
| `node_cursor` | empty | At most 1024 characters | Exact `nodes.next_cursor` from the preceding page |
| `object_limit` | `100` | `1..200` | Maximum visible actor objects in this page |
| `object_cursor` | empty | At most 1024 characters | Exact `objects.next_cursor` from the preceding page |

Nodes and objects are independently paginated. A caller can continue one page
while resetting or continuing the other. Each call obtains a new daemon
snapshot, so compare source timestamps when state may have changed between
pages.

#### Cluster facts

`cluster` contains the exact identifier and name, configured quorum flag, and
daemon-reported `is_compatible` and `is_frozen` flags. The daemon sets
`is_frozen` as soon as one node is frozen (`daemon/cstat`): it is no freeze
of the whole cluster, and the `frozen_at` of each node says which one; the
tool description says so, a model reading `true` having answered that the
cluster was frozen instead of naming the node. Configuration issues are
OpenSVC-provided strings, not MCP conclusions. They are limited to 50 with
`config_issues_total` and `config_issues_truncated`.

#### Node facts

`nodes` contains:

- `configured_total`, `reported_total`, and union `total` counts;
- `count`, `items`, `next_cursor`, and `truncated` page metadata;
- one record for each configured or reported name in the selected page.

Each record has explicit `configured` and `reported` booleans. When
`reported=false`, `status`, `monitor`, `stats`, `policy`, `daemon`, and
`heartbeat` are `null`; the MCP does not label this condition as unhealthy.

Reported node fields include:

- agent, API and compatibility versions, leader and overload flags;
- boot, freeze, leave, and rejoin timestamps;
- the generation vector, sorted by node and limited to 200;
- arbitrator name, URL, exact status, and weight, limited to 50;
- exact monitor state and expectations, their timestamps, session and
  orchestration identifiers, and completion flag;
- load, memory, swap, score, memory/swap thresholds, and daemon PID/start time;
- OpenSVC-provided node configuration issues, limited to 50.

The MCP exposes `is_overloaded`, capacity values, and configured thresholds as
separate facts. It does not reproduce the daemon policy, explain the overload,
or suggest a configuration change.

#### Heartbeat facts and bounds

When heartbeat data exists, the output preserves its publication timestamp,
last-message data, secret version numbers, streams, alerts, and peer links.
Secret version numbers are counters; no heartbeat secret material is returned.

| Collection | Limit | Ordering |
|---|---:|---|
| Last messages | 100 | Daemon-provided order |
| Streams | 50 | Exact stream identifier |
| Alerts per stream | 50 | Daemon-provided order |
| Peers per stream | 100 | Exact peer name |

Every bounded collection returns `total`, `count`, `items`, and `truncated`.
Alert messages and peer descriptions are limited to 1024 Unicode characters
and include an explicit truncation boolean.

The MCP does not classify heartbeat as healthy, degraded, stale, unknown, or
not applicable. It does not apply an age threshold, infer missing RX peers, or
combine redundant links. The agent receives exact stream `state`, alert
severity, peer `is_beating`, and timestamps.

#### Object facts

`objects.reported_total` counts all visible entries in `cluster.object`.
`actor_total` counts entries containing `avail`; non-actor configuration and
secret objects are excluded from the detailed page because they have no actor
status.

Actor records preserve path, availability, overall, provisioned, frozen,
placement state and policy, orchestration mode, topology, priority, scope,
up-instance count, update timestamp, and the instances whose monitor is not
idle. Paths and scope names are sorted only
for stable output.

`instance_monitors` lists, for each actor record, the instances whose exact
monitor state is not `idle`, read from `cluster.node.<node>.instance.<path>`
of the same snapshot: node, state, state update timestamp, global target
state and, when the daemon gives one, `state_reason`, sorted by node.
`state_reason` is the daemon's own words on why an instance is in a failure
state the state alone does not explain, such as a wait for a hard affinity
given up; it is cut after 500 characters with `state_reason_truncated`, and
absent from daemons older than the field. `instance_monitors_total` gives their number before
the limits. The list holds at most 50 entries per object and 200 per page,
spent in path order: an orchestration of every instance at once, a cluster
boot for instance, would otherwise fill a page with up to 10,000 entries.
An object cut by either limit sets `instance_monitors_truncated`, and the
page sets `objects.instance_monitors_truncated` when its own limit cut one.
The three fields are absent when no instance of the object reports a monitor
other than `idle`: every instance is `idle`, or none reports a monitor.
Leaving `idle` out only bounds the size: a `provision failed`, a `starting` or
an unknown state are listed alike, and an instance in failure is visible here
even when the object's aggregate `overall` is `down`.

`state_counts` groups all visible actors independently by their exact
`availability`, `overall`, `provisioned`, `frozen`, and `placement_state`
strings. `state_counts.instance_monitor_state` counts the instances of those
actors that report a monitor, `idle` included, by exact monitor state; it
covers every page, so a count other than `idle` tells to read the next object
pages. Empty and previously unknown values remain separate entries. The MCP
does not merge `up` with `stdby up`, create an `other` bucket, or select
"problem" objects.

#### MCP properties

| Property | Value |
|---|---|
| Title | Get cluster status snapshot |
| Read-only | Yes |
| Destructive | No |
| Open world | No; only the configured daemon is contacted |
| Side effects | None |

Annotations are client hints; authentication, authorization, and visibility
remain enforced by OpenSVC.

#### Example

Input:

```json
{"node_limit":100,"object_limit":100}
```

Abbreviated representative output for a two-node cluster:

```json
{
  "provenance": {"source":"opensvc_daemon","observed_at":"2026-09-23T15:00:33Z"},
  "cluster": {
    "id":"00000000-0000-4000-8000-000000000001",
    "name":"cluster-a",
    "quorum_enabled":false,
    "is_compatible":true,
    "is_frozen":false,
    "config_issues_total":0,
    "config_issues":[],
    "config_issues_truncated":false
  },
  "nodes": {
    "configured_total":2,
    "reported_total":2,
    "total":2,
    "count":2,
    "items":[{
      "name":"node-a",
      "configured":true,
      "reported":true,
      "status":{"is_leader":true,"is_overloaded":true},
      "stats":{"mem_available_pct":74,"mem_total_mb":3902,"swap_available_pct":0,"swap_total_mb":0},
      "policy":{"min_avail_mem_pct":2,"min_avail_swap_pct":10},
      "heartbeat":{"updated_at":"2026-09-23T15:00:32.259435971+02:00","streams":{"total":2,"count":2,"items":[],"truncated":false}}
    }],
    "truncated":false
  },
  "objects": {
    "reported_total":5,
    "actor_total":1,
    "count":1,
    "items":[{"path":"prod/svc/redis","availability":"down","overall":"down","provisioned":"false","placement_state":"non-optimal",
      "instance_monitors":[{"node":"node-a","state":"provision failed","state_updated_at":"2026-09-23T14:58:02.112Z","global_expect":"none"}],"instance_monitors_total":1}],
    "state_counts":{"availability":[{"value":"down","count":1}],"instance_monitor_state":[{"value":"idle","count":1},{"value":"provision failed","count":1}]},
    "truncated":false
  }
}
```

The example abbreviates nested required structures for readability; the MCP
output schema and implementation always return their complete typed shapes.

#### Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Insufficient daemon grants | Tool error containing daemon HTTP `403` |
| Invalid page limit or oversized cursor | Tool error before the daemon request |
| Daemon unavailable or malformed status | Tool error; no partial snapshot |

## `list_cluster_locks`

Lists the locks the cluster holds.

```text
GET /api/cluster/lock
```

A lock serializes what reads the state of the cluster to change it, such as
`network/<name>` for the address management of a network. The node speaking
for the cluster, the leader of the speaker election, answers after rebuilding
its table; the daemon answers 503 when no node speaks, or a peer it hears does
not answer, which the tool passes on as a fact about the cluster. Each lock
holds `name`, `id`, `node`, `holder`, `acquired_at`, `expires_at` and
`expires_in`: a lock never released is freed at the end of its lease. The
locks are sorted by name then acquisition time, 1000 at most. The tool takes
and releases nothing: `POST` and `DELETE /api/cluster/lock` are not offered,
nothing justifying that an agent takes a lock. Requires `root`.

## `get_relay_status`

```text
GET /api/relay/status[?remote=true]
```

The heartbeat relay slots, the heartbeat grant or `root`, served by the
daemon the MCP talks to. Without `remote`, the slots it relays for the
nodes of other clusters: `root` sees them all, another account its own.
With `remote`, the relays the heartbeats of type `relay` of this cluster go
through: the daemon reads each relay with the credentials its heartbeat
configuration holds, and returns a relay in error as a slot with `status`
set.

Each slot holds `relay`, its user information left out, `cluster_id`,
`cluster_name`, `nodename`, `node_addr`, `msg_len`, `updated_at` and
`username`; never the message, which `GET /relay/message`, not offered,
carries. An old `updated_at` on a relay alive is the sign of a node no
longer beating through it. At most 1000 slots.
