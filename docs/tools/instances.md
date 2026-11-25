---
domain: instance
tools:
  - get_instance_status
  - get_instance_logs
  - list_object_instances
  - refresh_instance_status
  - list_instance_sync_snapshots
  - get_instance_resource_file
stability: experimental
---

# Instance Tools

This document describes tools that inspect logs and status, and actively
refresh the status of one OpenSVC object instance.

Implementation:

- business logic: `internal/core/instance.go`,
  `internal/core/instance_status.go`, and `internal/core/instance_logs.go`;
- MCP definitions: `internal/tools/instance.go`.

## Tool selection

Use `list_object_instances` after `get_object_status` to locate the node behind
an aggregate problem and inspect status age. Use `get_instance_status` for the
selected instance's published configuration, monitor, and detailed resource
status. Use `get_instance_logs` to inspect recent OpenSVC activity for that
exact node instance. Use
`refresh_instance_status` only when the selected instance's `updated_at` is too
old for the diagnosis.

A monitor state ending in `failed`, such as `start failed` or `provision
failed`, keeps the state, not the error of the resource that failed: the
cause, a command not found, a device missing or an image not pulled, is in the
logs of the instance. `get_instance_status` then adds `failure_note`, which
names `get_instance_logs` for that path and node.

## Tools

### `get_instance_status`

Returns the cached configuration descriptor, monitor facts, and detailed
instance and resource status for one exact object and node. Use it after
`list_object_instances` when the instance summary needs more detail.

Use `get_object_config` for configuration keywords, `list_object_resources`
for paginated resource status, and `list_schedules` for scheduler execution
timestamps. The `config` block here is the daemon's published descriptor;
`config.schedules` contains configured expressions and requirements. Workload
output is available through `get_container_logs`.

#### OpenSVC API, authorization, and freshness

```text
GET /api/node/name/<node>/instance/path/<namespace>/<kind>/<name>
```

The tool performs one GET without query parameters. The configured daemon
looks up that object and node in its instance cache. It can return a peer
node's instance when the peer's data is published in that cache. Selecting
`node` does not open another daemon connection.

The daemon accepts namespace `guest`, `operator`, or `admin` grants, their
global equivalents, and global `join` or `root` grants. A namespace `guest`
JWT is sufficient for this read. The daemon checks namespace access before
looking up the configuration. An unauthorized namespace returns
`403`; an authorized lookup with no published configuration returns `404`.
Missing monitor or status data is represented by `null` on a successful read.

This is a passive read. Assess each block using its own `updated_at`:
`config.updated_at`, `monitor.updated_at`, and `status.updated_at` date
different publications. The three blocks are read separately and can have
different ages. `provenance.observed_at` records the MCP collection time.
Use `refresh_instance_status` when a new driver probe is needed.

#### MCP properties

The runtime title is **Get instance status**. The tool declares
`readOnlyHint=true`, `destructiveHint=false`, and `openWorldHint=false`.
Annotations are client hints; JWT authentication and daemon authorization
remain authoritative.

#### Input

| Field | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `path` | Yes | — | 512 bytes after trimming surrounding whitespace | Canonical object path discovered with `list_object_instances` |
| `node` | Yes | — | 255 ASCII characters | Exact node name reported for that instance |

`node` accepts letters, digits, `_`, `.`, and `-`, with no surrounding
whitespace. Empty names, `.`, `..`, the local alias `_`, selectors, and
multiple nodes are rejected locally. Empty paths, malformed path structures,
and more than three path components also fail locally. Path components are escaped
individually in the HTTP route. Object kinds and names that pass the local
path parser are validated by the daemon; object selector syntax is never
expanded by the MCP.

Use the canonical path returned by discovery: the response's object path and
node must match the requested identity. There are no `limit` or `cursor`
arguments. Nested data is bounded as described below.

Example input:

```json
{
  "path": "prod/svc/redis",
  "node": "node-a"
}
```

#### Example output

This is a complete representative result with the optional fields published
for this instance. Other optional fields can be absent.

```json
{
  "provenance": {
    "source": "opensvc_daemon",
    "observed_at": "2026-09-29T10:05:00Z"
  },
  "object": {
    "path": "prod/svc/redis",
    "namespace": "prod",
    "kind": "svc",
    "name": "redis"
  },
  "node": "node-a",
  "config": {
    "csum": "7ec47b82a85a191ef90a251083488a36",
    "priority": 50,
    "scope": ["node-a", "node-b"],
    "updated_at": "2026-09-28T11:40:57.064043402+02:00",
    "orchestrate": "no",
    "placement_policy": "nodes order",
    "topology": "failover",
    "is_disabled": false,
    "claims": {"cpu": -1, "memory": -1},
    "resources": {
      "container#redis": {
        "is_disabled": false,
        "is_monitored": false,
        "is_standby": false,
        "restart_delay": 500000000
      }
    },
    "schedules": [
      {
        "action": "status",
        "key": "status_schedule",
        "schedule": "@10m",
        "max_parallel": 1,
        "require_collector": false,
        "require_provisioned": false
      }
    ],
    "subsets": {}
  },
  "monitor": {
    "global_expect": "none",
    "global_expect_updated_at": null,
    "global_expect_options": null,
    "is_leader": true,
    "is_ha_leader": true,
    "local_expect": "none",
    "local_expect_updated_at": null,
    "orchestration_id": "00000000-0000-0000-0000-000000000000",
    "orchestration_is_done": false,
    "session_id": "00000000-0000-0000-0000-000000000000",
    "state": "idle",
    "state_updated_at": "2026-09-28T19:39:31.564638488+02:00",
    "monitor_action_executed_at": null,
    "preserved": false,
    "updated_at": "2026-09-28T19:39:32.153701097+02:00"
  },
  "status": {
    "avail": "down",
    "overall": "down",
    "provisioned": "n/a",
    "frozen_at": null,
    "last_started_at": null,
    "stopped_at": null,
    "updated_at": "2026-09-29T12:04:25.561354248+02:00",
    "resources": {
      "container#redis": {
        "type": "container.docker",
        "label": "docker redis:7-alpine",
        "status": "down",
        "provisioned": {"state": "n/a", "mtime": null},
        "info": {"name": "prod..redis.container.redis"}
      }
    }
  },
  "truncated": false,
  "truncations": []
}
```

#### Output fields

| Field | Meaning |
|---|---|
| `provenance` | Required API source and MCP collection time; see the [shared contract](README.md#freshness-model) |
| `object` | Required canonical reference with `path`, `namespace`, `kind`, and `name` |
| `node` | Required exact instance node reported by the daemon |
| `config` | Required non-null published configuration descriptor |
| `monitor` | Required monitor block, or `null` when unpublished |
| `status` | Required instance status block, or `null` when unpublished |
| `truncated` | Whether a supported collection or text field was shortened |
| `truncations` | Required array of reductions sorted by JSON Pointer and kind; `[]` when none |

Within these blocks, daemon JSON names are retained, including `csum`,
`avail`, `preserved`, `restart_delay`, and resource status flags. Only the
fields documented here are projected into the typed result. Unknown fields
are omitted without a truncation record. Optional scalar fields are omitted
when absent, while supplied `false`, `0`, and empty strings remain present.
Supplied empty collections remain `[]` or `{}`; absent optional collections
remain omitted.

Known date fields are RFC3339 strings preserving their supplied precision and
offset. Missing dates, JSON `null`, and Go zero dates such as
`0001-01-01T00:00:00Z` become `null`. This also applies to nested provisioning,
restart, file, running-action, and encapsulated-status dates, and to
`global_expect_options.config_updated_at` when that key is supplied. Zero UUID
strings remain strings. State values preserve case, whitespace, and unknown
values.

The core validates integer fields as signed 64-bit integers. MCP output uses
the SDK's native JSON serialization: integers outside the exact binary64
range, `[-(2^53-1), 2^53-1]`, can be rounded.

##### `config`

`csum`, `priority`, `scope`, and `updated_at` are always returned. Other
configuration fields are optional.

| Field | Meaning |
|---|---|
| `csum` | Published configuration checksum |
| `priority` | Configured object priority |
| `scope` | Configured nodes in their original placement priority order |
| `updated_at` | Configuration publication date, or `null` |
| `labels` | Configured label map |
| `app`, `env` | Configured application and environment |
| `drp` | Configured disaster recovery flag |
| `children`, `parents` | Object relations sorted by exact path |
| `monitor_action` | Configured monitor actions in source order |
| `pre_monitor_action` | Configured pre-monitor action text |
| `orchestrate`, `placement_policy`, `topology` | Exact configured modes and policy |
| `resources` | Resource configuration map keyed by RID; fields below |
| `schedules` | Configured schedule entries; fields below |
| `stonith` | Configured fencing flag |
| `subsets` | Subset map; each entry can contain optional boolean `parallel` |
| `flex` | Optional integer `min`, `max`, and `target` instance counts |
| `is_disabled` | Object disabled flag from configuration |
| `claims` | Compute claim map: CPU in thousandths and memory in bytes; `-1` means uncapped |
| `pool`, `size`, `charges` | Volume pool, size in bytes, and volume charges keyed by pool |

Each `config.resources[RID]` requires boolean `is_disabled`, `is_monitored`,
and `is_standby`. It can also contain integer `restart` and `restart_delay`.
`restart_delay` retains the daemon's nanosecond unit. These configuration
flags are distinct from the resource flags in `status`.

| Schedule field | Meaning |
|---|---|
| `key`, `action` | Required configuration key and scheduled action |
| `schedule` | Required raw schedule expression; empty when the task is not scheduled |
| `max_parallel` | Required configured maximum parallel runs |
| `require` | Optional raw resource requirement |
| `require_collector`, `require_provisioned` | Required collector and provisioning flags |

##### `monitor`

When the monitor block is published, all fields below are returned except
the optional `resources`, `parents`, and `children` maps.

| Field | Meaning |
|---|---|
| `global_expect`, `local_expect` | Exact monitor targets |
| `global_expect_updated_at`, `local_expect_updated_at` | Target update dates, or `null` |
| `global_expect_options` | Bounded JSON object of target options, or `null` |
| `is_leader`, `is_ha_leader` | Provisioning and HA leader flags |
| `orchestration_id`, `session_id` | Reported UUID strings, including zero UUIDs |
| `orchestration_is_done` | Reported orchestration completion flag |
| `state`, `state_updated_at` | Exact monitor state and its date, or `null` for the date |
| `monitor_action_executed_at` | Last monitor action date, or `null` |
| `preserved` | Reported preserved flag |
| `updated_at` | Monitor publication date, or `null` |
| `resources` | RID map; an entry can contain `restart` with optional integer `remaining` and nullable `last_at` |
| `parents`, `children` | Exact parent and child status strings keyed by object path |

##### `status` and encapsulated status

`avail`, `overall`, `provisioned`, and the four date fields are always
returned in a published status block. Other status fields are optional.

| Field | Meaning |
|---|---|
| `avail`, `overall`, `provisioned` | Exact instance availability, overall, and provisioning states |
| `frozen_at`, `last_started_at`, `stopped_at`, `updated_at` | Freeze, start, intentional stop, and cached status dates, or `null` |
| `optional` | Exact optional-resource aggregate state |
| `resources` | Resource status map keyed by RID; fields below |
| `running` | Running actions with required `rid`, `session_id`, integer `pid`, and nullable `at` |
| `hostname` | Reported hostname when supplied |
| `encap` | Encapsulated status map using the same fields, limited to two nested levels |

Each `status.resources[RID]` requires `type`, `label`, `status`, and
`provisioned`; other resource fields are optional.

| Resource field | Meaning |
|---|---|
| `type`, `label`, `status` | Exact driver type, bounded label, and exact resource status |
| `provisioned` | Required provisioning `state` and nullable `mtime` |
| `disable`, `monitor`, `optional`, `standby`, `encap`, `stopped` | Boolean flags published with resource status |
| `subset` | Reported subset name |
| `tags`, `datastores` | Tags and datastore references sorted by exact value |
| `log` | Status messages with `level` and `message`, in daemon-provided order |
| `info` | Driver facts as bounded JSON values retaining strings, numbers, booleans, arrays, objects, and `null` |
| `files` | File metadata with required `name`, `csum`, nullable `mtime`, and boolean `ingest`; no file content is read |

#### Ordering, bounds, and truncation

Maps are selected by lexical key order before truncation. Relations, tags,
and datastore references are sorted by exact string value. Schedules are
sorted by key, action, and expression. File metadata is sorted by name,
checksum, timestamp, and ingest flag; running actions by RID, timestamp, PID,
and session. Remaining source fields break ties deterministically. Duplicates
are retained. Placement scope, monitor actions, status messages, and arrays
inside arbitrary JSON values retain their source order.

| Data | Maximum returned |
|---|---:|
| Resources in each configuration, monitor, or status map | 200 |
| Configuration schedules and subsets | 200 each |
| Placement scope, object relations, monitor parent/child maps, and running actions | 200 each |
| Labels, compute claims, and volume charges | 100 each |
| Tags, datastores, driver facts, and files per resource | 100 each |
| Monitor actions | 20 |
| Status messages per resource | 20 |
| Encapsulated statuses | 32 per map, two nested levels |
| Ordinary text, including JSON string values | 4,096 Unicode code points |
| Each status message | 2,048 Unicode code points |
| Arbitrary JSON options and each driver-fact value | Depth 4, 128 visited values, 100 entries per object or array |
| Truncation records | 256 |
| Encoded core result, before MCP framing | 256 KiB |

Text reductions include a final `…` within the size limit. Each collection
or text reduction adds a record with `path` (a JSON Pointer), `kind`
(`collection` or `text`), `original_count`, and `returned_count`. Counts are
entries for collections and Unicode code points for text. Non-empty
encapsulation beyond the depth limit is omitted with a collection reduction.

Identifiers and map keys longer than 512 Unicode code points fail instead of
being shortened. Date strings longer than 128 bytes, arbitrary JSON number
tokens longer than 512 bytes, malformed dates, invalid field types, excessive
JSON depth or visited values, too many truncation records, and a core result
exceeding 256 KiB also fail. Nested truncation does not provide a continuation
cursor. Use the specialized paginated
resource, resource-info, or schedule tools for larger inventories.

#### Errors

Missing, invalid or expired access tokens (OAuth on HTTPS, OpenID Connect on
the socket) return HTTP `401`. With a valid token, daemon errors become tool
results with
`isError=true`, preserving the daemon HTTP status and bounded RFC 7807 detail.
Examples include invalid object kinds or names (`400`), namespace access
denied (`403`), and a missing object/node cache entry (`404`). These tool
errors are carried by a successful MCP HTTP exchange, not by an HTTP status
matching the daemon's error. Errors contain no successful structured result.

Unexpected `InstanceItem` kind or identity, absent configuration, malformed
published blocks, exceeded limits, transport failures, and caller
cancellation are also MCP tool errors.

### `get_instance_logs`

Returns recent OpenSVC daemon and orchestration log records associated with one
exact object instance. Use it to correlate monitor transitions, resource
checks, daemon API activity, and orchestration identifiers after identifying
the affected node with `list_object_instances`.

This tool does not return the stdout or stderr of the Redis process, container,
or another workload. Container output is exposed by a different daemon
endpoint and is outside this tool's contract.

#### OpenSVC API and stream behavior

```text
GET /api/node/name/<node>/instance/path/<namespace>/<kind>/<name>/log?follow=false&lines=<lines+1>
Accept: text/event-stream
```

OpenSVC uses Server-Sent Events for this endpoint even when `follow=false`.
Each historical journal record is delivered as one finite SSE `log` event. The
MCP consumes the stream to EOF and returns ordinary structured JSON; it does
not follow, reconnect, or expose SSE envelopes.

The request reads journal records available when the call starts. It does not
run resource drivers, refresh instance status, or change daemon state. The tool
is read-only, non-destructive, and closed-world.

OpenSVC delegates this instance route to its node-log handler, which requires
the global `root` grant. A namespace `guest`, `operator`, or `admin` grant is
not sufficient, even though the outer instance handler first checks namespace
visibility.

#### Input

| Field | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `path` | Yes | — | Exact object path | Canonical object path |
| `node` | Yes | — | 255 characters | Exact node hosting the instance |
| `lines` | No | 50 | 1..100 | Maximum recent entries returned |

The MCP requests one extra daemon record to determine whether older entries
were omitted. It then retains at most the requested number of most recent
entries in chronological order.

Example input:

```json
{
  "path": "prod/svc/redis",
  "node": "node-a",
  "lines": 3
}
```

#### Example output

```json
{
  "provenance": {
    "source": "opensvc_daemon",
    "observed_at": "2026-07-15T07:18:12Z"
  },
  "count": 3,
  "entries": [
    {
      "component": "daemon/daemonapi",
      "event_id": "b04fe7aa-c70c-4c30-a4a9-e7d2ef477061",
      "level": "info",
      "message": "daemon: api: inet: GET /api/object/path/prod/svc/redis/config/file: serve config file prod/svc/redis to om3-mcp",
      "message_truncated": false,
      "request_id": "7ae41b79-d9fd-4c0b-80c1-9368e88dbd93",
      "session_id": "e24f692e-fd28-4b4a-a606-13e037e03c36",
      "timestamp": "2026-07-15T14:57:09.712819569+09:00"
    },
    {
      "component": "daemon/daemonapi",
      "event_id": "b04fe7aa-c70c-4c30-a4a9-e7d2ef477061",
      "level": "info",
      "message": "daemon: api: ux: GET /api/object/path/prod/svc/redis/config/file: serve config file prod/svc/redis to root",
      "message_truncated": false,
      "request_id": "fd76415e-a716-4fd2-a765-a20f32b9d0cc",
      "session_id": "e24f692e-fd28-4b4a-a606-13e037e03c36",
      "timestamp": "2026-07-15T15:11:08.635069636+09:00"
    },
    {
      "component": "daemon/daemonapi",
      "event_id": "b04fe7aa-c70c-4c30-a4a9-e7d2ef477061",
      "level": "info",
      "message": "daemon: api: ux: GET /api/object/path/prod/svc/redis/config/file: serve config file prod/svc/redis to root",
      "message_truncated": false,
      "request_id": "5af7dc44-923d-48fd-8bb6-a327b1c7a453",
      "session_id": "e24f692e-fd28-4b4a-a606-13e037e03c36",
      "timestamp": "2026-07-15T16:18:11.055054155+09:00"
    }
  ],
  "lines": 3,
  "node": "node-a",
  "object": {
    "kind": "svc",
    "name": "redis",
    "namespace": "prod",
    "path": "prod/svc/redis"
  },
  "truncated": true
}
```

#### Output and bounds

| Field | Meaning |
|---|---|
| `object` | Canonical object reference |
| `node` | Exact queried node |
| `lines` | Effective requested maximum, including the default |
| `count` | Number of entries returned |
| `entries` | Recent entries in chronological order |
| `truncated` | Older entries or message content were omitted |

Each entry can contain `timestamp`, normalized `level`, bounded `message`,
`message_truncated`, `component`, `resource_id`, `session_id`, `exec_id`,
`event_id`, `request_id`, and `orchestration_id`. Optional identifiers are
omitted when the record does not provide them. `session_id` is read from the
`SESSION_ID` field, or `SID` as logged by earlier agents.

Messages are limited to 2,048 Unicode code points each and 64 Ki Unicode code
points across the response. Correlation fields are limited to 255 code points.
Control and formatting characters are normalized. Raw journald metadata such
as UID/GID, machine identifiers, systemd fields, and raw grant data is never
returned.

Malformed SSE, unexpected event types, invalid nested OpenSVC JSON, object or
node mismatches, oversized streams, transport failures, and caller
cancellation become MCP tool errors. Daemon `401` and `403` responses preserve
their bounded RFC 7807 details.

### `list_object_instances`

Returns a sorted, paginated status and monitor view for instances of one exact
object. It exposes availability, monitor targets, orchestration state,
leadership, timestamps, and resource status counts without returning full
resource or configuration payloads.

#### OpenSVC API and freshness

```text
GET /api/instance?path=<exact-path>[&node=<node>]
```

This endpoint returns the last-known daemon status and does not execute the
instance `status` action. `updated_at` is the authoritative age indicator.

OpenSVC filters instances according to OpenSVC JWT namespace grants. The tool
is read-only, non-destructive, closed-world, and has no side effects.

#### Input

| Field | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `path` | Yes | — | 512 characters | Exact canonical object path |
| `node` | No | Empty | 255 characters | Exact node filter |
| `limit` | No | 50 | 1..100 | Maximum instances in this page |
| `cursor` | No | Empty | 255 characters | Previous `next_cursor` with unchanged filters |

Example input:

```json
{
  "path": "prod/svc/redis",
  "node": "node-a",
  "limit": 50
}
```

#### Example output

```json
{
  "provenance": {
    "source": "opensvc_daemon",
    "observed_at": "2026-07-15T05:31:03Z"
  },
  "count": 1,
  "instances": [
    {
      "availability": "up",
      "frozen_at": "0001-01-01T00:00:00Z",
      "global_expect": "none",
      "is_ha_leader": true,
      "is_leader": true,
      "last_started_at": "2026-07-15T13:36:32.905515501+09:00",
      "local_expect": "started",
      "monitor_state": "idle",
      "node": "node-a",
      "orchestration_id": "00000000-0000-0000-0000-000000000000",
      "orchestration_is_done": false,
      "overall": "up",
      "provisioned": "n/a",
      "resource_summary": {
        "total": 1,
        "distinct_total": 1,
        "count": 1,
        "status_counts": [
          {"status": "up", "count": 1}
        ],
        "truncated": false
      },
      "updated_at": "2026-07-15T14:31:02.747625761+09:00"
    }
  ],
  "node_filter": "node-a",
  "object": {
    "kind": "svc",
    "name": "redis",
    "namespace": "prod",
    "path": "prod/svc/redis"
  },
  "total": 1,
  "truncated": false
}
```

Instances are sorted by node. `resource_summary` groups resources by the exact
status string reported by OpenSVC. It does not merge `up` with `stdby up`,
merge `down` with `stdby down`, normalize case or whitespace, or place unknown
values in an `other` bucket. Empty and future status values remain distinct.

`total` counts all resources in the daemon status map. `distinct_total` counts
all distinct exact status values. `status_counts` is sorted by exact status and
limited to 100 entries; `count` and `truncated` describe the returned counters.
Use `list_object_resources` when resource identifiers and individual details
are required. Instance pagination is recalculated from current daemon inventory
and is not a snapshot.

### `refresh_instance_status`

Actively runs the OpenSVC status probe for one exact object instance, waits for
a newer `status.updated_at`, and returns that refreshed instance.

Use it after a read-only status tool when an out-of-band failure or recovery may
not yet be reflected. It never fans out automatically. The tool is
non-destructive but is not read-only or idempotent: every call executes resource
status drivers and updates daemon state.

#### OpenSVC API and authorization

```text
GET  /api/instance?path=<path>&node=<node>
POST /api/node/name/<node>/instance/path/<namespace>/<kind>/<name>/action/status
GET  /api/instance?path=<path>&node=<node>
```

The authenticated OpenSVC user needs `operator`, `admin`, or `root` access for the
namespace. A `guest` JWT can inspect instances but cannot trigger the action.

#### Input

| Field | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `path` | Yes | — | 512 characters | Exact canonical object path |
| `node` | Yes | — | 255 characters | Exact node hosting the instance |
| `timeout_seconds` | No | 30 | 5..120 | Maximum polling duration after action acceptance |

Discover the exact node with `list_object_instances`; do not guess it.

Example input:

```json
{
  "path": "prod/svc/redis",
  "node": "node-a",
  "timeout_seconds": 30
}
```

#### Integrated workflow

1. Read the exact instance and capture `status.updated_at`.
2. Submit the status action and retain its `session_id`.
3. Poll after 250 ms, then 500 ms, then every second.
4. Stop when `updated_at` changes or the timeout expires.
5. Return the latest observed instance.

The timestamp change is the completion signal because this status action is not
a CRM orchestration and its session is not reliably represented in instance
monitor fields.

#### Example output

```json
{
  "provenance": {
    "source": "opensvc_daemon",
    "observed_at": "2026-07-15T05:35:59Z"
  },
  "current_updated_at": "2026-07-15T14:35:58.608127647+09:00",
  "duration_ms": 251,
  "instance": {
    "availability": "up",
    "frozen_at": "0001-01-01T00:00:00Z",
    "global_expect": "none",
    "is_ha_leader": true,
    "is_leader": true,
    "last_started_at": "2026-07-15T13:36:32.905515501+09:00",
    "local_expect": "started",
    "monitor_state": "idle",
    "node": "node-a",
    "orchestration_id": "00000000-0000-0000-0000-000000000000",
    "orchestration_is_done": false,
    "overall": "up",
    "provisioned": "n/a",
    "resource_summary": {
      "total": 1,
      "distinct_total": 1,
      "count": 1,
      "status_counts": [
        {"status": "up", "count": 1}
      ],
      "truncated": false
    },
    "updated_at": "2026-07-15T14:35:58.608127647+09:00"
  },
  "node": "node-a",
  "object": {
    "kind": "svc",
    "name": "redis",
    "namespace": "prod",
    "path": "prod/svc/redis"
  },
  "previous_updated_at": "2026-07-15T14:31:02.747625761+09:00",
  "refresh_observed": true,
  "session_id": "269b40e1-fe5c-4e61-81d4-aeb4a1629a8f",
  "timed_out": false
}
```

`refresh_observed=true` means a different non-empty `updated_at` was read. It
does not assert that the instance is healthy and does not prove that this
specific action caused the timestamp change. If no such change becomes visible
before the deadline, the tool returns structured success with `timed_out=true`,
the accepted `session_id`, and the latest observed instance. The action may
still complete later.

#### Authorization error example

A caller holding only `guest` receives an MCP tool error similar to:

```text
request instance status refresh: OpenSVC daemon POST ... returned HTTP 403 Forbidden: need one of [operator:prod admin:prod operator admin root] grant
```

The message comes from the daemon's bounded RFC 7807 response. The MCP does not
invent or bypass grant decisions.

### `list_instance_sync_snapshots`

```text
GET /api/node/name/<node>/instance/path/<ns>/<kind>/<name>/sync/snapshots?rid=<rid>
```

The snapshots the sync resources of an instance hold, the restorable points
a `sync.store` keeps; `guest` on the namespace. `rid` defaults to `sync`,
every sync resource; one, such as `sync#1`, is accepted. Only `sync.store`
implements the listing: the other sync drivers return none. A sync resource
in error fails the read, 500 "List the snapshots", with the resource and the
cause, relayed: a store whose directory is missing answers `stat …: no such
file or directory`.

Each snapshot holds `rid`, `id`, `created_at`, `node`, `members`, such as
`fs#1` or `config`, and `location`. The `id` is the session of the `sync
update` that took the snapshot, what a restore, not offered, would take. At
most 1000, the most recent first.

### `get_instance_resource_file`

```text
GET /api/node/name/<node>/instance/path/<ns>/<kind>/<name>/resource/file?rid=<rid>&name=<file>
```

One file a resource declares to replicate to its peers, `root`. The list is
closed: the files of `ToSync`, the domain definition of a `container.kvm`,
`lxc` or `vbox`, the metadata of a shared `disk.md`, the `sub_devs` of a
`disk.zpool`. `get_instance_status` gives them in `resources[rid].files`,
with their `csum` and `mtime`: comparing the csum between nodes tells a
drift without reading the files.

The daemon serves a file while the instance is `up`, and refuses with 400 a
resource, or a file, its status does not declare. The MCP reads the
instance status first and refuses before the call a file the resource does
not declare, naming those it does, or the resources declaring files. It
takes the full name, or a base name one declared file only has, such as
`sub_devs`: in the recette, a model asked `sub_devs`, read the refusal
naming the full path, and answered without calling again. The
text is returned up to 64 KiB, cut on a character boundary, its `passwd`
attributes masked, such as the graphics console password a kvm domain
definition may hold, `secrets_masked` counting them; a binary file returns
its size and `csum` only.

The daemon dereferences the file information before testing the error of
its `stat`: a file declared but missing, such as the `sub_devs` of a zpool
before its first `sync update`, makes it panic rather than answer 404, an
om3 defect.

`GET …/config/file` of an instance is not offered: the route takes no
`redact-secrets`; the drift of a configuration reads through `config.csum`
and `updated_at`. `POST …/state/file` is a write.

## Errors

Invalid paths, filters, limits, cursors, nodes, line counts, or timeouts fail
before daemon access where possible. Missing or invisible instances,
authorization failures, transport errors, malformed responses, missing action
session identifiers, and caller cancellation are MCP tool errors. No JWT or
raw error body is exposed.
