---
domain: node
tools:
  - get_node_config
  - get_node_config_keywords
  - list_node_config_keywords
  - get_node_status
  - get_node_logs
  - list_node_properties
  - list_node_hardware
  - list_node_packages
  - list_node_capabilities
  - list_node_drivers
  - get_node_daemon_metrics
  - list_node_disks
  - get_node_san_topology
  - list_node_ip_addresses
  - probe_node_reachability
  - list_node_network_reservations
  - list_node_locks
  - get_node_drbd_config
stability: experimental
---

# Node Tools

This document describes tools that read bounded node configuration evidence,
inspect the last-known state, cached system properties, hardware, packages,
and capabilities of one exact node, read its recent OpenSVC journal entries,
expose advanced daemon-process metrics, and actively verify the proxy path to
one daemon.

Implementation:

- business logic: `internal/core/node.go`, `internal/core/config_node.go`, `internal/core/node_disk.go`,
  `internal/core/node_san.go`, `internal/core/node_ip_address.go`, `internal/core/node_property.go`,
  `internal/core/node_hardware.go`, `internal/core/node_capability.go`, `internal/core/node_driver.go`,
  `internal/core/node_daemon_metric.go`, and `internal/core/node_reachability.go`;
- MCP definitions: `internal/tools/node.go`.

## Tools

### `get_node_config`

Returns the node configuration file for one exact node as bounded UTF-8 text.
Use it when a diagnosis depends on node-local declarations such as listener,
arbitrator, asset, or other node settings. Use `get_node_status` for runtime
state; this tool does not compare the file with live status or interpret its
keywords.

#### OpenSVC API, authorization, and freshness

```text
GET /api/node/name/<node>/config/file?redact-secrets=true
Accept: application/octet-stream
```

The MCP always sends `redact-secrets=true` and exposes no option to disable it.
The daemon performs redaction and requires the global `root` grant. The
contacted daemon reads the named node's current `node.conf` file, proxying the
request when that node is another cluster node. The call does
not read the cluster status cache, refresh drivers, or change configuration.

#### Input and output

`node` is required, as for every [node target](README.md#node-targets).

The output has the same `provenance`, `content`, `size_bytes`,
`returned_bytes`, `truncated`, and `redaction_requested` fields as
`get_cluster_config`, plus `node` containing the requested node. The 65,536-byte MCP output limit, 1 MiB transport ceiling, UTF-8 requirement, and
`application/octet-stream` check are identical.

#### MCP properties

| Property | Value |
|---|---|
| Title | Get node configuration |
| Read-only | Yes |
| Destructive | No |
| Open world | No; only the configured daemon and its OpenSVC proxy path are used |
| Side effects | None |

Annotations are client hints; OpenSVC enforces access using the OpenSVC JWT.

#### Example

Input:

```json
{"node":"node-a"}
```

Output:

```json
{
  "provenance": {"source": "opensvc_daemon", "observed_at": "2026-09-23T10:00:01Z"},
  "node": "node-a",
  "content": "[node]\nsshkey = ********\n",
  "size_bytes": 27,
  "returned_bytes": 27,
  "truncated": false,
  "redaction_requested": true
}
```

#### Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Missing global `root` grant | Tool error containing daemon HTTP `403` |
| Oversized or non-exact non-empty `node` | Tool error before the daemon request |
| Node or node configuration file missing | Tool error containing daemon HTTP `404` |
| Response above 1 MiB | Tool error before any content is returned |
| Invalid UTF-8 or unexpected media type | Tool error; no partial configuration is returned |
| Daemon or remote-node proxy unavailable | Tool error with daemon transport context |

### `get_node_status`

Returns the last-known status of one exact node after `get_cluster_status`
provides the cluster-wide facts. It exposes the reported agent
and compatibility versions, leader and overload flags, freeze and boot times,
monitor state and targets, capacity statistics, memory and swap policy
thresholds, environment, labels and daemon grace periods, factual membership
context, bounded heartbeat data, and the
process and subsystem states of the node daemon. Use
`get_cluster_status` for a factual cluster-wide snapshot. This node-focused
tool does not classify heartbeat health or freshness.

#### OpenSVC API and freshness

```text
GET /api/cluster/status
```

The request has no query parameters. The MCP selects the exact `node` key from
`cluster.node` after the daemon returns its last-known cluster view. The call
does not contact the selected node directly or run status drivers. The endpoint
accepts a global `guest` or higher grant; the OpenSVC JWT and the daemon's
authorization remain authoritative. The result excludes other nodes, objects,
hooks, keys, and other node configuration fields.

`provenance.served_by` is the node whose daemon answered the request: the
daemon receiving it, usually the node carrying the cluster VIP, named by the
daemon itself in its response. It is not a node the result describes, and it
may change on failover. `node` and
`served_by` therefore differ when the selected node is not the one answering:
the selected node is then described by what it last published to the answering
daemon, not by a reading on the node itself.

`provenance.observed_at` dates the MCP collection, while `monitor.updated_at`
dates the monitor state and `heartbeat.updated_at` preserves the published
heartbeat timestamp. The MCP does not compare those values or apply an age
threshold. `stats`, `policy`, or `heartbeat` is `null` when that section is
absent from the daemon response. An unreported scalar field in `status` or
`monitor` appears as its JSON zero value.

#### Input and output

`node` is required and must be one exact name of at most 255 characters.
Leading or trailing spaces and wildcard/selector characters are rejected. No
default node, selector, or pagination is used.

| Output field | Meaning |
|---|---|
| `provenance` | Daemon API source, MCP collection time, and `served_by`, the node whose daemon answered |
| `node` | Selected OpenSVC node name |
| `membership` | Whether the node is configured and a bounded list of distinct configured peer names |
| `status` | Agent/API/compatibility versions, leader and overload flags, boot and freeze times |
| `monitor` | State, global and local targets, orchestration ID/completion, and update time |
| `stats` | Fifteen-minute load, available and total memory/swap, and OpenSVC capacity score; `null` if absent |
| `policy` | Minimum available memory and swap percentages; `null` if absent; zero disables the corresponding check |
| `config` | Environment, labels and three daemon periods the node configuration sets; `null` if absent |
| `heartbeat` | Publication time, messages, secret version numbers, streams, alerts, peers, and their exact values; `null` if absent |
| `daemon` | Daemon `pid`, `started_at`, and `subsystems`: `daemon_data`, `listener`, `dns`, `collector`, `scheduler`, `runner_imon`, each with its exact state and timestamps, or `null` if absent; a subsystem is named by its key, the `id` the daemon reports, which repeats it, is not returned |

`membership.configured_peers` contains distinct configured node names other
than the selected node, sorted by exact name and limited to 200. It returns
`total`, `count`, `items`, and `truncated`. This context lets the agent compare
configured peers with reported heartbeat links without the MCP declaring a
peer missing or stale.

`config` holds `env`, such as `PRD` or `TST`, and `labels`, the labels the
node selectors of objects match: an object whose selector names a label the
node lacks is not placed there. Labels are limited to 100, values to 1,024
characters, reported by `labels_truncated`. The three periods are in
nanoseconds:

| Field | Meaning |
|---|---|
| `maintenance_grace_period_ns` | How long a daemon keeps the data of a peer in maintenance, announced on `daemon stop` and `daemon restart`, and does not take over its instances |
| `rejoin_grace_period_ns` | How long a starting daemon stays in `rejoin` state, with orchestration not allowed, waiting for a heartbeat from every peer |
| `ready_period_ns` | How long the daemon waits before starting an instance in `ready` state; a peer can preempt the start meanwhile |

`daemon` is what the node daemon last published in the cluster status, as the
rest of the result: for a node other than the one receiving the request, it
is not a live reading. Each subsystem keeps its exact `state` and
`configured_at`, `created_at` and `updated_at` timestamps, plus its own facts:
the daemon data queue size, the runtime listener address, port and rate
limiter, the DNS nameservers (at most 32), the scheduler running jobs and
concurrency limit, and the instance-monitor runner concurrency limit. The
collector URL is not returned verbatim: its scheme and host are exposed, while
user information, non-root paths, queries, and fragments are omitted and
reported through redaction flags.

The heartbeat projection is shared with `get_cluster_status`:

| Collection | Limit | Ordering |
|---|---:|---|
| Last messages | 100 | Daemon-provided order |
| Streams | 50 | Exact stream identifier |
| Alerts per stream | 50 | Daemon-provided order |
| Peers per stream | 100 | Exact peer name |

Every bounded heartbeat collection returns `total`, `count`, `items`, and
`truncated`. Alert messages and peer descriptions are limited to 1024 Unicode
characters with an explicit truncation boolean. Unknown states, zero or old
timestamps, `is_beating=false`, and daemon-provided alerts remain source facts.
The MCP does not emit `healthy`, `degraded`, `unknown`, `not_applicable`, or
synthetic heartbeat issues.

#### MCP properties

| Property | Value |
|---|---|
| Title | Get node status |
| Read-only | Yes |
| Destructive | No |
| Open world | No; only the configured daemon is contacted |
| Side effects | None |

Annotations are client hints; OpenSVC enforces access using the OpenSVC JWT.

#### Example

Input:

```json
{"node":"node-a"}
```

Illustrative output for a two-node cluster. Timestamps and capacity values are
representative:

```json
{
  "provenance": {"source": "opensvc_daemon", "observed_at": "2026-09-18T10:00:30Z"},
  "node": "node-a",
  "membership": {
    "is_configured": true,
    "configured_peers": {
      "total": 1,
      "count": 1,
      "items": ["node-b"],
      "truncated": false
    }
  },
  "status": {
    "agent_version": "v3.0.0",
    "api_version": 0,
    "compat_version": 0,
    "is_leader": true,
    "is_overloaded": false,
    "booted_at": "2026-09-18T09:00:00Z",
    "frozen_at": "0001-01-01T00:00:00Z"
  },
  "monitor": {
    "state": "idle",
    "global_expect": "none",
    "local_expect": "none",
    "orchestration_id": "",
    "orchestration_is_done": true,
    "updated_at": "2026-09-18T10:00:00Z"
  },
  "stats": {
    "load_15m": 0.4,
    "mem_available_pct": 82,
    "mem_total_mb": 4096,
    "score": 67,
    "swap_available_pct": 0,
    "swap_total_mb": 0
  },
  "policy": {"min_avail_mem_pct": 5, "min_avail_swap_pct": 0},
  "heartbeat": {
    "updated_at": "2026-09-18T10:00:00Z",
    "last_message": {"from": "node-a", "patch_length": 0, "type": "patch"},
    "last_messages": {
      "total": 2,
      "count": 2,
      "items": [
        {"from": "node-a", "patch_length": 0, "type": "patch"},
        {"from": "node-b", "patch_length": 0, "type": "patch"}
      ],
      "truncated": false
    },
    "secret_version": {"main": 0, "alternate": 0},
    "streams": {
      "total": 2,
      "count": 2,
      "items": [
        {
          "id": "hb#1.rx",
          "type": "unicast",
          "state": "running",
          "configured_at": "2026-09-18T09:00:00Z",
          "created_at": "2026-09-18T09:00:00Z",
          "updated_at": "0001-01-01T00:00:00Z",
          "alerts": {"total": 0, "count": 0, "items": [], "truncated": false},
          "peers": {
            "total": 1,
            "count": 1,
            "items": [{
              "name": "node-b",
              "description": ":10000 ← node-b",
              "description_truncated": false,
              "is_beating": true,
              "changed_at": "2026-09-18T09:00:01Z",
              "last_beating_at": "2026-09-18T10:00:00Z"
            }],
            "truncated": false
          }
        },
        {
          "id": "hb#1.tx",
          "type": "unicast",
          "state": "running",
          "configured_at": "2026-09-18T09:00:00Z",
          "created_at": "2026-09-18T09:00:00Z",
          "updated_at": "0001-01-01T00:00:00Z",
          "alerts": {"total": 0, "count": 0, "items": [], "truncated": false},
          "peers": {
            "total": 1,
            "count": 1,
            "items": [{
              "name": "node-b",
              "description": "→ node-b:10000",
              "description_truncated": false,
              "is_beating": true,
              "changed_at": "2026-09-18T09:00:01Z",
              "last_beating_at": "2026-09-18T10:00:00Z"
            }],
            "truncated": false
          }
        }
      ],
      "truncated": false
    }
  }
}
```

#### Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Insufficient daemon grants | Tool error containing daemon HTTP `403` |
| Empty, oversized, or non-exact `node` | Tool error before the daemon request |
| Configured node with no published data | Tool error naming the node |
| Unknown node | Tool error naming the node and `get_cluster_status` |
| Daemon unavailable or malformed response | Tool error with transport or decoding context |

### `list_node_properties`

Returns typed properties from one node's OpenSVC system cache. Use it to inspect
cached operating-system, hardware, identity, environment, location, and support
metadata such as `os_release`, `cpu_threads`, `manufacturer`, `node_env`, or
`team_support`. The tool preserves source facts and per-property collection
errors; it does not classify a node or compare nodes automatically.

#### OpenSVC API, authorization, and freshness

```text
GET /api/node/name/{node}/system/property
```

The endpoint requires the global `root` grant. It reads the system cache of the
named node, which OpenSVC proxies when necessary. The call does not execute an asset
probe, run `push asset`, register the node with a collector, or update the
cache.

The cache may not exist until asset data has been pushed, which commonly
happens during registration with a collector but can also be initiated by an
operator. In that case OpenSVC returns HTTP `404` with `Load system cache`
context. The MCP preserves that daemon error and never converts it to an empty
property list.

The endpoint exposes no cache timestamp. `provenance.observed_at` dates only
the MCP read and does not establish when the properties were collected.

#### Input and filtering

| Input | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `node` | Yes | — | Exact node name; at most 255 characters | Node whose system cache is read |
| `names` | No | All | At most 32 exact names of at most 255 characters | Include only these property names |
| `sources` | No | All | At most 32 exact sources of at most 255 characters | Include only these collection sources, such as `probe`, `config`, or `default` |
| `limit` | No | 100 | 1..200 | Maximum matching properties returned |
| `cursor` | No | — | At most 255 characters | Exact `next_cursor` from the preceding page with the same node and filters |

Names are combined with OR, sources are combined with OR, and the two filter
families are combined with AND. Filters are applied by the MCP after the full
daemon response has been validated. `reported_total` is the number of daemon
entries before filtering; `total` is the number matching both filters.

Properties are sorted by exact name. Duplicate names are rejected because they
would make the value and pagination cursor ambiguous. Each string value and
per-property error is limited to 4096 Unicode characters, with explicit
truncation flags. A page is additionally bounded to 64 Ki Unicode code points.

#### Typed values

OpenSVC properties can contain strings, numbers, or booleans. The MCP does not
coerce between these types. Exactly one typed field is present in `value`:

```json
{"type":"string","string":"linux"}
{"type":"number","number":4}
{"type":"boolean","boolean":true}
```

An empty string remains a present string value. JSON `null`, arrays, objects,
non-finite numbers, and other response shapes are rejected as malformed daemon
data. The `error` field belongs to one property: a non-empty value is preserved
without failing the whole list.

#### MCP properties

| Property | Value |
|---|---|
| Title | List node properties |
| Read-only | Yes |
| Destructive | No |
| Open world | No; only the configured daemon and its OpenSVC proxy path are used |
| Side effects | None; no asset probe or cache update |

#### Example

Input:

```json
{
  "node": "node-a",
  "names": ["cpu_threads", "os_release", "node_env"],
  "sources": ["probe", "config"]
}
```

Representative output:

```json
{
  "provenance": {"source":"opensvc_daemon","observed_at":"2026-09-24T11:20:00Z"},
  "node": "node-a",
  "reported_total": 41,
  "total": 3,
  "count": 3,
  "properties": [
    {
      "name": "cpu_threads",
      "title": "cpu threads",
      "source": "probe",
      "value": {"type":"number","number":4},
      "value_truncated": false,
      "error": "",
      "error_truncated": false
    },
    {
      "name": "node_env",
      "title": "environment",
      "source": "config",
      "value": {"type":"string","string":"production"},
      "value_truncated": false,
      "error": "",
      "error_truncated": false
    },
    {
      "name": "os_release",
      "title": "os release",
      "source": "probe",
      "value": {"type":"string","string":"13"},
      "value_truncated": false,
      "error": "",
      "error_truncated": false
    }
  ],
  "truncated": false
}
```

#### Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Missing global `root` grant | Tool error containing daemon HTTP `403` |
| System cache not yet populated | Tool error preserving daemon HTTP `404` and `Load system cache` detail |
| Invalid node, filters, page size, or cursor | Tool validation error before the daemon request |
| Cursor no longer present after a cache change | Explicit stale-cursor tool error |
| Unexpected list/item kind, node, duplicate name, or value type | Tool error; no partial list |
| Daemon or remote-node proxy unavailable | Tool error with transport context |

### `list_node_hardware`

Returns the hardware entries stored in one node's OpenSVC system cache. Use it
to inventory device types, classes, drivers, hardware paths, and descriptions
when investigating a missing dependency or a difference between nodes. The
tool does not decide whether a device or driver is available, required, or
healthy.

#### OpenSVC API, authorization, and freshness

```text
GET /api/node/name/{node}/system/hardware
```

The endpoint requires the global `root` grant. It reads the system cache of the
named node, using the OpenSVC proxy path for another node. It
does not scan buses, load drivers, execute `push asset`, register with a
collector, or update the cache.

As with `list_node_properties`, the cache may not exist until asset data has
been pushed. OpenSVC then returns HTTP `404` with `Load system cache` context;
the MCP preserves this error instead of returning an empty inventory. The API
exposes no cache timestamp, so `provenance.observed_at` dates only the MCP read.

#### Input and filtering

| Input | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `node` | Yes | — | Exact node name; at most 255 characters | Node whose cached hardware is read |
| `types` | No | All | At most 32 exact values of at most 255 characters | Include hardware types such as `pci` or `mem` |
| `classes` | No | All | At most 32 exact values of at most 1024 characters | Include only the reported hardware classes |
| `drivers` | No | All | At most 32 exact values of at most 1024 characters | Include only the reported drivers; `""` selects entries with an empty driver |
| `limit` | No | 100 | 1..200 | Maximum matching hardware entries returned |
| `cursor` | No | — | Opaque value of at most 128 characters | Exact `next_cursor` from the preceding page with the same node and filters |

Values within one filter family are combined with OR. The three families are
combined with AND and applied after the full daemon response has been
validated. `reported_total` is the number of daemon entries before filtering;
`total` is the number matching all filters.

Entries are sorted by exact `type`, `path`, `class`, `driver`, and description.
The cursor is derived from the complete source record and its occurrence. It
must be treated as opaque. Exact duplicate records are preserved because they
can represent separate physical devices. Each description is limited to 4096
Unicode characters with an explicit flag, and a page is additionally bounded
to 128 Ki Unicode code points.

`class` is descriptive source data and is not normalized by the MCP. An empty
`driver` is also preserved as a fact; it does not prove that a required driver
is missing.

#### MCP properties

| Property | Value |
|---|---|
| Title | List node hardware |
| Read-only | Yes |
| Destructive | No |
| Open world | No; only the configured daemon and its OpenSVC proxy path are used |
| Side effects | None; no scan, driver load, or cache update |

#### Example

Input:

```json
{
  "node": "node-a",
  "types": ["pci"],
  "classes": ["Network controller", "Mass storage controller"],
  "limit": 20
}
```

Representative output:

```json
{
  "provenance": {"source":"opensvc_daemon","observed_at":"2026-09-25T10:00:00Z"},
  "node": "node-a",
  "reported_total": 25,
  "total": 2,
  "count": 2,
  "hardware": [
    {
      "type": "pci",
      "class": "Mass storage controller",
      "driver": "ahci",
      "path": "00:1f.2",
      "description": "SATA controller",
      "description_truncated": false
    },
    {
      "type": "pci",
      "class": "Network controller",
      "driver": "virtio-pci",
      "path": "01:00.0",
      "description": "Virtio network device",
      "description_truncated": false
    }
  ],
  "truncated": false
}
```

#### Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Missing global `root` grant | Tool error containing daemon HTTP `403` |
| System cache not yet populated | Tool error preserving daemon HTTP `404` and `Load system cache` detail |
| Invalid node, filters, page size, or cursor | Tool validation error before the daemon request |
| Cursor no longer present after a cache change | Explicit stale-cursor tool error |
| Unexpected list/item kind, node, required field, or control character | Tool error; no partial list |
| Daemon or remote-node proxy unavailable | Tool error with transport context |

### `list_node_packages`

Returns the package entries stored in one node's OpenSVC package cache. Use it
to establish whether a named package family, version, architecture, or package
manager type was reported when investigating a driver or workload dependency.
The tool does not compare versions or decide whether a package is current,
vulnerable, compatible, correctly signed, or trusted.

#### OpenSVC API, authorization, and freshness

```text
GET /api/node/name/{node}/system/package
```

The endpoint requires the global `root` grant. It reads the package cache of the
named node, using the OpenSVC proxy path for another node. It
does not inventory the operating system, execute `push pkg`, contact a
collector, install or remove packages, or update the cache.

The cache may not exist until OpenSVC has executed a package push. The daemon
then returns HTTP `404` with `Load package cache` context; the MCP preserves
this error instead of returning an empty inventory. The API exposes no cache
timestamp, so `provenance.observed_at` dates only the MCP read. Each
`installed_at` value belongs to its package record and does not establish the
freshness of the cache.

#### Input and filtering

| Input | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `node` | Yes | — | Exact node name; at most 255 characters | Node whose cached packages are read |
| `names` | No | All | At most 32 exact values of at most 255 characters | Include exact package names |
| `name_prefixes` | No | All | At most 32 case-sensitive prefixes of at most 255 characters | Include package families such as `opensvc-` or `linux-image-` |
| `types` | No | All | At most 32 exact values of at most 255 characters | Include package manager types such as `deb`, `rpm`, or `snap`; `""` selects an unreported type |
| `architectures` | No | All | At most 32 exact values of at most 255 characters | Include architectures such as `amd64` or `all`; `""` selects an unreported architecture |
| `limit` | No | 100 | 1..200 | Maximum matching package entries returned |
| `cursor` | No | — | Opaque value of at most 128 characters | Exact `next_cursor` from the preceding page with the same node and filters |

Exact names and name prefixes are combined with OR. Package name, type, and
architecture filter families are combined with AND and applied after the full
daemon response has been validated. `reported_total` is the number of daemon
entries before filtering; `total` is the number matching all filters.

Entries are sorted by exact name, architecture, type, version, installation
timestamp, and signature. The cursor is derived from the complete source
record and its occurrence and must be treated as opaque. Exact duplicates are
preserved. A page is additionally bounded to 128 Ki Unicode code points.

The daemon field `installedat` is exposed as `installed_at` after RFC 3339
validation. Some package collectors do not obtain an installation time and
can report the Go all-zero timestamp. Version, architecture, and type are also
preserved when a backend reports an empty value. `signature` preserves the
exact `sig` field; an empty value is expected for package backends that do not
populate it and is not a signature-validation result.

#### MCP properties

| Property | Value |
|---|---|
| Title | List node packages |
| Read-only | Yes |
| Destructive | No |
| Open world | No; only the configured daemon and its OpenSVC proxy path are used |
| Side effects | None; no inventory, cache update, collector push, or package operation |

#### Example

Input:

```json
{
  "node": "node-a",
  "name_prefixes": ["opensvc-"],
  "types": ["deb"],
  "architectures": ["amd64"],
  "limit": 20
}
```

Representative output:

```json
{
  "provenance": {"source":"opensvc_daemon","observed_at":"2026-09-25T10:00:00Z"},
  "node": "node-a",
  "reported_total": 420,
  "total": 2,
  "count": 2,
  "packages": [
    {
      "name": "opensvc-client",
      "version": "3.0.0",
      "architecture": "amd64",
      "type": "deb",
      "installed_at": "2026-09-24T10:15:30+02:00",
      "signature": ""
    },
    {
      "name": "opensvc-server",
      "version": "3.0.0",
      "architecture": "amd64",
      "type": "deb",
      "installed_at": "2026-09-24T10:15:35+02:00",
      "signature": ""
    }
  ],
  "truncated": false
}
```

#### Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Missing global `root` grant | Tool error containing daemon HTTP `403` |
| Package cache not yet populated | Tool error preserving daemon HTTP `404` and `Load package cache` detail |
| Invalid node, filters, page size, or cursor | Tool validation error before the daemon request |
| Cursor no longer present after a cache change | Explicit stale-cursor tool error |
| Unexpected list/item kind, node, required field, timestamp, or control character | Tool error; no partial list |
| Daemon or remote-node proxy unavailable | Tool error with transport context |

### `list_node_capabilities`

Returns the exact capability markers stored in the OpenSVC capability cache of
one node. The list can contain base driver capabilities, driver sub-features,
and node-level environment detections. Use it to establish what OpenSVC last
detected before investigating configuration, resources, or missing runtime
dependencies.

#### OpenSVC API, authorization, and freshness

```text
GET /api/node/name/{node}/capabilities
```

The endpoint requires the global `root` grant. It loads the capability cache
of the named node, which OpenSVC proxies when necessary. This read does not execute scanners, probe dependencies, or modify
the cache.

The endpoint provides no scan timestamp. `provenance.observed_at` dates only
the MCP read, not the capability scan. A missing marker can therefore mean
that a dependency was absent during the last scan, that a scanner failed, or
that the cache is stale. Conversely, a present marker does not prove that a
feature is configured, used, reachable, or currently healthy. Some scanners
report built-in support unconditionally, while others inspect commands or OS
features.

#### Input and pagination

| Input | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `node` | Yes | — | Exact node name; at most 255 characters | Node whose cache is read |
| `limit` | No | 100 | 1..200 | Maximum distinct capability names returned |
| `cursor` | No | — | At most 1024 characters | Exact `next_cursor` from the preceding page for the same node |

The MCP validates every `CapabilityItem`: `meta.node` must match the requested
node, returned in the output `node` field. Names are preserved exactly; only exact duplicates are removed before
lexicographic sorting. `reported_total` is the raw number of entries returned
by OpenSVC; `total` is the number of distinct names. A page is additionally
bounded to 64 Ki Unicode code points, and each name to 1024.

#### MCP properties

| Property | Value |
|---|---|
| Title | List node capabilities |
| Read-only | Yes |
| Destructive | No |
| Open world | No; only the configured daemon and its OpenSVC proxy path are used |
| Side effects | None; no capability scan |

#### Example

Input:

```json
{"node":"node-a","limit":3}
```

Representative capability output:

```json
{
  "provenance": {"source":"opensvc_daemon","observed_at":"2026-09-24T11:30:00Z"},
  "node": "node-a",
  "reported_total": 65,
  "total": 65,
  "count": 3,
  "capabilities": [
    "drivers.array.freenas",
    "drivers.array.hds",
    "drivers.array.hoc"
  ],
  "next_cursor": "drivers.array.hoc",
  "truncated": true
}
```

Examples of other marker classes include
`drivers.resource.container.docker`,
`drivers.resource.container.docker.registry_creds`, and `node.x.systemd`.
The MCP does not translate these markers into availability or health verdicts.

#### Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Missing global `root` grant | Tool error containing daemon HTTP `403` |
| Oversized or non-exact non-empty `node` | Tool error before the daemon request |
| Invalid page size or cursor | Tool validation error |
| Cursor no longer present after a cache change | Explicit stale-cursor tool error |
| Unexpected list/item kind, node, or capability name | Tool error; no partial list |
| Daemon or remote-node proxy unavailable | Tool error with transport context |

### `list_node_drivers`

Returns the driver catalog of one OpenSVC node, as its daemon describes it:
for each driver, what it does, its features, whether the node has what it
needs, the grants using it and provisioning it need, the drivers a virtual
one selects, and the driver an alias names. Use it to choose the driver of a
resource, and to tell whether the node and the user can use it, before
checking its configuration or its resource status.

A question on the drivers of the cluster names no node: the model reads the
nodes with `get_cluster_status`, then calls the tool for each. On 2026-10-29,
asked "which drivers are available?", a model once called it with an empty
node, then asked the user for one. The refusal of an invalid node, shared by
every node tool, now names `get_cluster_status`. The same day, 70 runs of six
such questions, on six node tools, with the refusal as it was and as it is,
called no node tool with an empty node.

#### OpenSVC API and authorization

```text
GET /api/node/name/{node}/drivers
```

The endpoint requires the global `root` grant. It reads the driver catalog of
the named node's daemon, which OpenSVC proxies when necessary. This read does
not execute drivers, scan capabilities, probe dependencies, or change daemon
state.

#### What the daemon describes

Since om3 v3.0.0-rc53-169 (2026-10), the daemon describes each driver; the
MCP passes its fields through, renamed in snake case, and the vocabulary of
the features with their meanings, once per page:

| Field | Meaning |
|---|---|
| `name` | `<group>.<type>`, or `<group>` for a driver of no type; the `type` keyword of a resource is the type within its group |
| `group`, `type` | The driver group, the section of the resource, such as `fs`, and its type, such as `ext4` |
| `doc` | A line saying what the driver does |
| `features` | Words of the vocabulary, such as `provision`, `resize`, `snapshot`, `move` or `encap`; a virtual driver has those of the driver it selects on the node; empty for the drivers of other groups |
| `selects` | For a virtual driver, the drivers it selects among on the node, in the order it prefers them, as `container.oci` selects `container.docker`, then `container.podman` |
| `alias_of` | The driver this one is another name of, kept for the configurations written with it, as `disk.lvm` for `disk.vg`: new configurations use that driver |
| `available` | True when the node has what the driver needs, as its capabilities say; a virtual driver is available when one of those it selects is |
| `grant` | The grant a service needs to use the driver at all, such as `root`; absent when any user granted on the namespace may |
| `provision_grant` | The grant only the provision needs, beyond `grant`: a user not holding it may still use the driver on what a sysadmin provisioned, with `provision = false`; absent when the provision needs no more |

`available` is the daemon's verdict from the capabilities it detected; the
MCP computes none. The catalog does not tell whether a driver is configured,
used or healthy: `get_object_config` and the status tools do.

A daemon of an older version gives the names only, without the vocabulary:
the result says `described: false`, every other field of the drivers is then
unknown, and the `feature` and `available` filters are refused, the `group`
filter reading the group from the name.

#### Input and pagination

| Input | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `node` | Yes | — | Exact node name; at most 255 characters | Node whose driver catalog is read |
| `group` | No | — | One lowercase word | Keeps the drivers of that group, such as `fs` or `container` |
| `feature` | No | — | One word of the vocabulary | Keeps the drivers having that feature, such as `resize`; a word the vocabulary does not hold is refused with the vocabulary |
| `available` | No | — | Boolean | Keeps the drivers the node has, or has not, what they need for |
| `limit` | No | 100 | 1..200 | Maximum drivers returned |
| `cursor` | No | — | At most 1024 characters | Exact `next_cursor` from the preceding page for the same node and filters |

The MCP validates the live response shape observed from OpenSVC:
`DriverList` containing `DriverItem` entries. `meta.node` must match the
requested node, returned in the output `node` field. Names are preserved
exactly; a name repeated keeps its first entry, and the drivers are sorted by
name. `reported_total` is the raw number of entries returned by OpenSVC;
`total` is the number of distinct drivers the filters keep. A page is
additionally bounded to 64 Ki Unicode code points of driver text, and each
name to 1024.

The OpenAPI schema spells the kinds `DriversList` and `DriversItem`, while
both the daemon handler and the live response use `DriverList` and
`DriverItem`. The MCP follows the actual daemon contract and rejects the
schema-only spelling.

#### MCP properties

| Property | Value |
|---|---|
| Title | List node drivers |
| Read-only | Yes |
| Destructive | No |
| Open world | No; only the configured daemon and its OpenSVC proxy path are used |
| Side effects | None; no driver execution or capability scan |

#### Example

Input:

```json
{"node":"dev5n1","group":"container"}
```

Representative output, the vocabulary and the drivers cut:

```json
{
  "provenance": {"source":"opensvc_daemon","observed_at":"2026-10-28T16:00:00Z"},
  "node": "dev5n1",
  "described": true,
  "features": [
    {"name":"provision","doc":"The provision makes what the resource stands for, as a logical volume or a filesystem, and the resource can be configured on one made by a sysadmin instead."},
    {"name":"encap","doc":"The resource is a guest om also runs in, which holds encapsulated resources."}
  ],
  "reported_total": 132,
  "total": 6,
  "count": 6,
  "drivers": [
    {"name":"container.docker","group":"container","type":"docker","doc":"Runs a container with docker.","features":["start","stop","signal","encap"],"available":true},
    {"name":"container.oci","group":"container","type":"oci","doc":"Runs an OCI container with the first container runtime the node has.","features":["start","stop","signal","encap"],"selects":["container.docker","container.podman"],"available":true},
    {"name":"container.podman","group":"container","type":"podman","doc":"Runs a container with podman.","features":["start","stop","signal","encap"],"available":false}
  ],
  "truncated": false
}
```

#### Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Missing global `root` grant | Tool error containing daemon HTTP `403` |
| Oversized or non-exact non-empty `node` | Tool error before the daemon request |
| Invalid page size, cursor, group or feature word | Tool validation error |
| Feature the vocabulary does not hold | Tool error naming the vocabulary |
| `feature` or `available` on a daemon giving the names only | Tool error saying to filter by group or call without them |
| Cursor no longer present after a catalog change | Explicit stale-cursor tool error |
| Unexpected list/item kind, node, or driver name | Tool error; no partial list |
| Daemon or remote-node proxy unavailable | Tool error with transport context |

### `get_node_daemon_metrics`

Returns bounded Prometheus metric families exported by one running OpenSVC
daemon. This is an advanced diagnostic tool for hypotheses about daemon
activity, API latency or errors, scheduler activity, internal queues, Go
runtime behavior, and process resource consumption. It is not the primary
source for object, instance, resource, heartbeat, or cluster status.

#### OpenSVC API and scope

```text
GET /api/node/name/{node}/metrics
Accept: text/plain
```

OpenSVC proxies the read to the named node when necessary. The daemon authorizes requests using the request-scoped token of the caller. The request does not probe resources or
change daemon state.

The endpoint exports metrics of the OpenSVC daemon process, including its Go
runtime and process collector. It does not represent general host, workload,
or cluster metrics. A counter is cumulative: one isolated value usually does
not establish a current rate or an anomaly. The MCP deliberately does not
calculate rates, compare thresholds, or emit a health conclusion.

#### Input, filtering, and pagination

| Input | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `node` | Yes | — | Exact node name; at most 255 characters | Daemon whose metrics are read |
| `names` | No | All | At most 32 | Exact metric family names |
| `prefixes` | No | All | At most 32 | Metric family name prefixes |
| `limit` | No | 100 | 1..200 | Maximum families in the page |
| `cursor` | No | — | At most 1024 characters | Exact `next_cursor` from the preceding call with identical node and filters |

`names` and `prefixes` are combined using OR. Filtering is performed after
parsing the endpoint response. Families are sorted by exact name and paginated;
the response reports totals before filtering, after filtering, and for samples.
The source text is limited to 256 KiB, the filtered result to 5,000 samples,
and one family to 1,000 samples. These bounds make accidental full metric dumps
fail explicitly instead of silently dropping measurements.

Each family preserves its `name`, `help`, and Prometheus `type`. Samples expose
sorted labels and an optional source timestamp. Counter, gauge, and untyped
values are strings; summaries expose count, sum, and quantiles; histograms
expose count, sum, and cumulative buckets. Numeric strings preserve Prometheus
special values such as `NaN` and `+Inf` in valid JSON.

`target_node` is the requested node.

#### MCP properties

| Property | Value |
|---|---|
| Title | Get node daemon metrics |
| Read-only | Yes |
| Destructive | No |
| Open world | No; only the configured daemon and its OpenSVC proxy path are used |
| Side effects | None |

#### Example

Input:

```json
{"node":"node-a","prefixes":["opensvc_api_"],"limit":20}
```

Abbreviated output:

```json
{
  "target_node":"node-a",
  "reported_total":59,
  "total":2,
  "sample_total":5,
  "count":2,
  "returned_sample_count":5,
  "metrics":[{
    "name":"opensvc_api_requests_total",
    "help":"API requests.",
    "type":"counter",
    "sample_count":2,
    "samples":[{
      "labels":[{"name":"code","value":"200"},{"name":"method","value":"GET"}],
      "value":"12"
    }]
  }],
  "truncated":false
}
```

#### Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Daemon refuses the OpenSVC JWT | Tool error preserving the daemon status and bounded problem detail |
| Invalid node, filter, page size, or cursor | Tool validation error |
| Cursor no longer present with the same filters | Explicit stale-cursor tool error |
| Unexpected media type or malformed Prometheus text | Tool error; no partial metrics are returned |
| Source or filtered sample bounds exceeded | Explicit tool error suggesting narrower filters |
| Daemon or remote-node proxy unavailable | Tool error with daemon transport context |

### `probe_node_reachability`

Actively verifies that one exact OpenSVC daemon answers through the contacted
daemon's node proxy path.

#### OpenSVC API and interpretation

```text
GET /api/node/name/<node>/ping
```

The endpoint requires the global `root` grant. The target `node` is mandatory.

For a remote target, the contacted daemon first verifies that it has status
data for the node and that the node belongs to the cluster, then calls the
remote daemon with the OpenSVC JWT. A successful result means the complete
path returned HTTP `204 No Content`:

```text
MCP -> contacted daemon -> OpenSVC proxy -> target daemon -> 204
```

This proves neither heartbeat health nor the state of daemon subsystems,
objects, instances, or resources. The endpoint is an HTTP application probe,
not ICMP. `round_trip_ms` measures the complete path above, including local MCP
and proxy overhead; it is not a pure network latency measurement.

#### Input and output

`node` is required and must be one exact OpenSVC node name of at most 255
characters. Paths, whitespace, wildcards, and selectors are rejected before
the daemon request.

Successful output fields are:

| Field | Meaning |
|---|---|
| `provenance` | Daemon API source and MCP completion time |
| `node` | Exact requested node |
| `reachable` | `true`, because only an exact `204` produces a successful result |
| `status_code` | `204` |
| `round_trip_ms` | End-to-end elapsed milliseconds measured by the MCP |

Failures remain explicit tool errors rather than successful
`reachable=false` results. This preserves the difference between a refused
JWT, an unknown node, missing status data, and a remote connection failure.

#### MCP properties

| Property | Value |
|---|---|
| Title | Probe node reachability |
| Read-only | Yes |
| Destructive | No |
| Open world | No; only the configured daemon and its OpenSVC proxy path are used |
| Side effects | One active HTTP request to the selected daemon |

#### Example

Input:

```json
{"node":"node-b"}
```

Output:

```json
{
  "provenance":{"source":"opensvc_daemon","observed_at":"2026-09-25T10:00:00Z"},
  "node":"node-b",
  "reachable":true,
  "status_code":204,
  "round_trip_ms":1.42
}
```

#### Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Missing global `root` grant | Tool error containing daemon HTTP `403` |
| Empty, oversized, or non-exact `node` | Tool validation error before the daemon request |
| Node has no status data | Tool error containing daemon HTTP `404` |
| Target is not a configured cluster node | Tool error containing daemon HTTP `400` |
| Target connection or request fails | Tool error containing daemon HTTP `500 Request peer` |
| Target returns anything other than `204` | Tool error; no reachability success is claimed |

### `get_node_logs`

Returns a finite, bounded list of recent OpenSVC `om` journal entries for one
exact node. Use it after `get_node_status` to inspect daemon activity around a
reported node, heartbeat, listener, scheduler, or orchestration issue. The
optional `component` selects one exact OpenSVC `PKG` value such as
`daemon/hbctrl`.

The daemon stamps what it runs with three ids: the execution, the session it
belongs to, and the orchestration the session is a step of. `exec_id`,
`session_id` and `orchestration_id` keep the entries logged under one of them.
To read why an execution failed, take its `exec_id` and `node` from
`list_daemon_executions`: an execution runs on one node. A session or an
orchestration may span several nodes, whose journals are read one at a time.

```text
GET /api/node/name/<node>/log?follow=false&lines=<lines+1>[&filter=PKG=<component>][&filter=EXEC_ID=<id>][&filter=SESSION_ID=<id>][&filter=ORCHESTRATION_ID=<id>]
Accept: text/event-stream
```

Each filter is a journal match; matches on distinct fields must all hold.

The endpoint reads journald entries selected by `_COMM=om`. It does not filter
by systemd unit: an entry may have `systemd_unit=opensvc-server.service`, or it
may come from another `om` execution. It does not include messages emitted by
the systemd manager about the service or workload stdout and stderr. OpenSVC
requires the global `root` grant for this endpoint.

| Input | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `node` | Yes | — | Exact hostname using letters, digits, `.`, `_`, or `-`; at most 255 characters | Node whose journal is read |
| `lines` | No | 50 | 1..100 | Maximum entries returned |
| `component` | No | — | Exact value, at most 255 characters | `PKG` filter |
| `exec_id` | No | — | Canonical UUID | `EXEC_ID` filter |
| `session_id` | No | — | Canonical UUID | `SESSION_ID` filter |
| `orchestration_id` | No | — | Canonical UUID | `ORCHESTRATION_ID` filter |

The ids are sent in lower case, as the daemon logs them.

The MCP requests one extra entry to detect omission of older entries. It
consumes the SSE stream to EOF, retains at most `lines` entries in chronological
order, and returns ordinary JSON. `follow` is always `false`.

The output contains `provenance`, `node`, the effective `lines`, `count`,
`entries`, `truncated`, and `ids` (the id filters applied); `component`
appears when requested. Each entry contains `timestamp`, `message`,
`message_truncated`, and optional `level`, `priority`, `component`,
`systemd_unit`, `object_path`, `resource_id`, `session_id`, `exec_id`,
`event_id`, `request_id`, and `orchestration_id`. `session_id` is read from
the `SESSION_ID` field, or `SID` as logged by earlier agents. OpenSVC may omit
`level`; the journald `priority` is preserved separately when present. A raw
journald microsecond timestamp is converted to RFC 3339 when the OpenSVC
payload has no timestamp.

Messages are limited to 2,048 Unicode code points each and 64 Ki code points
across the response. Other fields are limited to 255 code points. Control and
formatting characters are normalized. Raw journald metadata such as machine
identifiers, command lines, and user IDs is omitted. `truncated` reports older
entries omitted by the requested line limit or message content shortened by
these bounds. Invalid inputs, malformed SSE or JSON, unexpected node,
component or id values, oversized streams, and daemon errors become MCP tool
errors.

### `list_node_disks`

Lists the disks of one node, with the OpenSVC objects claiming disk regions.
Use it to find a LUN missing on a node, by listing the disks of two nodes, or
the disks an object uses.

#### OpenSVC API, authorization, and freshness

```text
GET /api/node/name/{node}/system/disk
```

The daemon requires the global `root` grant, and proxies the request to the
named node. It serves the disk inventory the node caches on its `pushdisks`
schedule, or after `om node push disks --dry-run`, which writes the cache
without contacting a collector. The API gives no cache date: the inventory can
be as old as the last push. A node that never pushed has no cache, and the
daemon error ("waiting for cached value") is returned as is.

The inventory holds presence, size, identity and claims, not the state of
multipath paths nor disk health.

#### Input

| Input | Meaning |
|---|---|
| `node` | Required exact node name |
| `type` | Optional exact disk type, such as `mpath`, `disk` or `rom` |
| `claimed_only`, `unclaimed_only` | Optional, mutually exclusive: the disks an OpenSVC object claims, or the others |
| `limit`, `cursor` | Page size between 1 and 200, default 100, and the `next_cursor` of a previous call |

#### Output

`provenance`, `node`, the filters, `reported_total`
(disks in the inventory), `total` (matching disks), `count`, `disks`,
`next_cursor` and `truncated`. Disks are sorted by type, then identifier. Each
disk holds `id` (such as the WWID of a multipath LUN), `devpath`, `size_bytes`,
`vendor` and `model` (unmodified, trailing spaces included), `type`, `claimed`,
and `regions` (at most 64): `id`, `devpath`, `object` (the claiming object path,
empty when none), `group` and `size_bytes`.

### `get_node_san_topology`

Reads the SAN topology of one node: its initiators, such as iSCSI initiators
or HBA ports, the initiator to target pairs, and how many targets each
initiator reaches. Use it to find an initiator that lost a target.

#### OpenSVC API, authorization, and freshness

```text
GET /api/node/name/{node}/system/san/initiator
GET /api/node/name/{node}/system/san/path
```

The daemon requires the global `root` grant, and proxies the requests to the
named node. Both serve the system inventory the node caches on its `pushasset`
schedule, or after `om node push asset --dry-run`, with the same freshness
limits as `list_node_disks`. The inventory holds the topology, not the state
of the paths: a degraded multipath is not reported, a lost target is.

#### Input and output

The only input is `node`, as for `list_node_disks`. The output holds
`provenance`, `node`, `initiators` (`name`, `type`, `target_count`: the
distinct targets the initiator reaches), sorted by type then name, and `paths`
(`initiator` and `target`, each `name` and `type`), sorted by initiator then
target. Initiators and paths are bounded to 512 and 4096 entries, reported by
`initiators_truncated` and `paths_truncated`.

### `list_node_ip_addresses`

Lists the addresses held by the network interfaces of one node. Use it as an
inventory of the node network: which interfaces and subnets the node has, for
example to choose the `dev` of an `ip` resource or to check the addresses a
heartbeat uses.

#### OpenSVC API, authorization, and freshness

```text
GET /api/node/name/{node}/system/ipaddress
```

The daemon requires the global `root` grant, and proxies the request to the
named node.

**The addresses come from a cache, not from the interfaces.** The node writes
its system cache on its `pushasset` schedule, or after
`om node push asset --dry-run`, and the daemon serves that file as is. The API
gives no cache date: the list can be as old as the last push. An address
added or removed since, such as an OpenSVC service address that moved to
another node, is not reflected. For where an OpenSVC address is up now, use
`list_cluster_ip_resources`, which reads the live resource status. A node that
never pushed has no cache, and the daemon error ("waiting for cached value")
is returned as is.

#### Input

| Input | Meaning |
|---|---|
| `node` | Required exact node name |
| `interface` | Optional exact interface name |
| `family` | Optional `ipv4` or `ipv6` |
| `unicast_only` | Optional, `true` by default: leaves out the multicast group memberships the cache lists (such as `224.0.0.1` or `ff02::1`) and the link-local addresses (`fe80::/10`, `169.254.0.0/16`). `false` lists every cached address |

#### Output

`provenance`, `node`, the filters applied, `reported_total` (addresses in the
cache), `total` (matching addresses) and `addresses`, sorted by interface then
address. Each address holds `interface`, `address`, `prefix_length` (as
reported; empty for a multicast group membership), `family`, `mac` and
`deprecated`. The cache holds at most a few hundred entries: the list is not
paginated, and a response over 4096 entries is an error.

### `list_node_network_reservations`

```text
GET /api/node/name/<node>/network/reservation[?name=<network>]
```

The addresses one node holds reserved on the cluster backend networks,
`root`, the daemon relaying the read: `network`, `ip`, and the `path` and
`rid` the address is reserved for, by network then address. They are read
from the reservation store: an address shows as soon as it is drawn, before
the status of its object reports it, which `list_network_ips` does not see.
A peer in maintenance or starting answers 503 with `Retry-After`, a peer
without heartbeat 404: relayed, they are facts about the node.

### `list_node_locks`

```text
GET /api/node/name/<node>/lock
```

The cluster locks one node knows, `root`, the daemon relaying the read:
what the clients of the node hold, and what it granted while it spoke for
the cluster, which its clients may not have heard yet. A node taking the
speaking over rebuilds its lock table from these. The projection is the one
of `list_cluster_locks`; this read serves when the node speaking answers
503. It takes and releases nothing; `DELETE …/lock` is not offered.

### `get_node_drbd_config`

```text
GET /api/node/name/<node>/drbd/config?name=<resource>
```

The file `/etc/drbd.d/<name>.res` of one DRBD resource on one node, `root`,
the daemon relaying the read; 404 for a file missing. `name` holds no `/`
nor `\`, refused before the call. The files om3 writes hold no
`shared-secret`; one written by hand may, and its value is masked,
`secrets_masked` counting it. The text is returned up to 64 KiB.

`GET …/drbd/allocation` is not offered: it is not a read, each call reserves
a minor and a port for 5 seconds, for a provision to follow.

## Node configuration by keyword

### `get_node_config_keywords`

Reads keywords of the configuration of one node, raw and non-evaluated.

```text
GET /api/node/name/<node>/config?kw=…&evaluate=false
```

The daemon relays the read to the node, and answers the keys `node.conf`
sets, or the keywords asked, a keyword not set, or unknown, with an empty
value. The values are read on the merged configuration. The inputs and bounds
are those of `get_object_config`: exact `keywords`, 50 at most, `limit` up to
200, 4096 characters per value and 64 KiB in all. `node` is one exact node
name, never `_`; a name outside the cluster is the 400 of the daemon. The
secret `node.uuid`, the key the node authenticates to the collector with, not
its identifier there (the collector knows the node by its name), is left out
before
the sort and the bound, and counted in `secrets_omitted`: the daemon gives it
in clear to root through this read, which takes no `redact-secrets`
parameter. `get_node_config` reads the whole file, redacted. Requires `root`.

### `list_node_config_keywords`

Lists the keywords the configuration of a node supports.

```text
GET /api/node/name/<node>/config/keywords?driver=…|section=…&option=…
```

The filters, the cursor pagination and the bounds are those of
`list_object_config_keywords`, whose core reads either catalog. The catalog
is the same on every node, and the daemon reads neither the node name of the
path nor resolves it: `node` is optional, and without it the tool asks the
`_` alias, the daemon answering, and returns `node` empty. The alias stays
refused as an input, as for every node tool; only an empty `node` is
admitted. The daemon does not relay this route: a `section` filter is
resolved on the configuration of the node answering, not on that of `node`.
