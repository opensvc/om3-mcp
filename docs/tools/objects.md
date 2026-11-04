---
domain: object
tools:
  - list_cluster_objects
  - get_object_status
  - get_object_config
  - list_object_config_keywords
  - list_object_data_keys
  - get_object_data_key
stability: experimental
---

# Object Tools

This document describes tools that discover OpenSVC objects and inspect their
aggregate status.

Implementation:

- business logic: `internal/core/object.go`, `internal/core/object_status.go`,
  `internal/core/config_object.go`, `internal/core/config_object_keyword.go`,
  and `internal/core/object_data_key.go`;
- MCP definitions: `internal/tools/object.go`.

## Tool selection

Use `list_cluster_objects` to discover canonical paths visible to the caller.
A path is taken in any form the daemon accepts, `root/svc/web`, `svc/web` or
`web`, and returned in its canonical form, the one the daemon gives, as om3
writes it: `<ns>/<kind>/<name>`, without the root namespace, and there
without the `svc` and `ccfg` kinds, `web` and `cfg/app`; `cluster` for the
cluster configuration; `<ns>/` for a namespace configuration. The MCP
compares what the daemon answers to that form.
Use `get_object_status` after selecting one exact path. For the state of
many objects at once, such as the stopped services, read `get_cluster_status`,
which lists the state of the visible actor objects, rather than one
`get_object_status` per object. Continue with
`list_object_instances` when an aggregate object state requires node-level
diagnosis. Use `get_object_config` when the diagnosis requires declared driver,
resource, placement, or orchestration settings.
Use `list_object_config_keywords` to discover which options an object kind or
configured driver supports, their defaults, constraints, and documentation.
It returns definitions, not the object's configured values.
Use `list_object_data_keys` to check which keys a cfg, sec or usr object
stores, without reading their values.

## Tools

### `list_cluster_objects`

Returns a sorted, paginated inventory of object references visible to the
authenticated OpenSVC user. It deliberately omits status, configuration, instance,
resource, schedule, and secret payloads.

#### OpenSVC API

```text
GET /api/object/path?path=<selector>
```

The MCP removes duplicate paths, parses them into stable references, sorts them
by canonical path, and then applies pagination.

OpenSVC returns only paths visible through the caller's namespace grants. An
empty result can mean either no matching object or no visibility; the MCP does
not distinguish these cases because doing so could disclose unauthorized
inventory.

#### MCP properties

This tool is read-only, non-destructive, closed-world, and has no side effects.

#### Input

| Field | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `selector` | No | `**` | 512 characters | Native OpenSVC object selector |
| `limit` | No | 100 | 1..200 | Maximum references in this page |
| `cursor` | No | Empty | 1024 characters | Previous `next_cursor` for the same selector |

Common selectors:

| Selector | Meaning |
|---|---|
| `**` | All visible objects |
| `*/svc/*` | All visible service objects |
| `prod/**` | All visible objects in namespace `prod` |
| `prod/svc/redis` | One exact object |

Example input:

```json
{
  "selector": "prod/**",
  "limit": 100
}
```

#### Example output

```json
{
  "provenance": {
    "source": "opensvc_daemon",
    "observed_at": "2026-07-15T05:00:00Z"
  },
  "count": 1,
  "objects": [
    {
      "kind": "svc",
      "name": "redis",
      "namespace": "prod",
      "path": "prod/svc/redis"
    }
  ],
  "selector": "prod/**",
  "total": 1,
  "truncated": false
}
```

When `truncated=true`, call the tool again with the same selector and limit and
pass `next_cursor` as `cursor`. Pagination is recalculated from current daemon
inventory and is not snapshot-based.

Path normalization follows OpenSVC conventions:

| Source path | Returned reference |
|---|---|
| `cluster` | namespace `root`, kind `ccfg`, name `cluster` |
| `redis` | namespace `root`, kind `svc`, name `redis` |
| `cfg/app` | namespace `root`, kind `cfg`, name `app` |
| `prod/svc/redis` | namespace `prod`, kind `svc`, name `redis` |
| `prod/` | namespace `prod`, kind `nscfg`, name `namespace` |

Malformed paths returned by the daemon are rejected rather than silently
rewritten or omitted.

### `get_object_status`

Returns a bounded aggregate status, placement, topology, scope, and instance
location view for one exact object.

It does not expose object configuration, resource details, logs, or data stored
in cfg, sec, or usr objects.

#### OpenSVC API and freshness

```text
GET /api/object?path=<exact-path>
```

The MCP requires exactly one returned object. This read does not execute status
drivers: compare `updated_at` with the current time before relying on the state.
Use `list_object_instances` to inspect per-node timestamps,
`get_instance_status` for one instance's cached configuration, monitor, and
resource details, and `refresh_instance_status` when an explicit probe is
necessary.

#### MCP properties

This tool is read-only, non-destructive, closed-world, and has no side effects.

#### Example input

```json
{
  "path": "prod/svc/redis"
}
```

Wildcard paths are intentionally unsupported.

#### Example output

```json
{
  "provenance": {
    "source": "opensvc_daemon",
    "observed_at": "2026-07-15T05:31:03Z"
  },
  "availability": "up",
  "frozen": "unfrozen",
  "instance_count": 1,
  "instance_nodes": ["node-a"],
  "object": {
    "kind": "svc",
    "name": "redis",
    "namespace": "prod",
    "path": "prod/svc/redis"
  },
  "orchestrate": "no",
  "overall": "up",
  "placement_policy": "nodes order",
  "placement_state": "optimal",
  "priority": 50,
  "provisioned": "n/a",
  "scope": ["node-a"],
  "topology": "failover",
  "up_instances_count": 1,
  "updated_at": "2026-07-15T14:31:02.780259555+09:00"
}
```

`availability` is present only when the daemon publishes `avail` for the
object. The MCP does not derive an actor classification from its presence or
absence. Scope and instance node names are sorted.

### `list_object_config_keywords`

Returns configuration keyword definitions supported by one exact object. It
is intended for configuration discovery and validation: use
`get_object_config` to read the values actually declared on the object.

#### OpenSVC API

```text
GET /api/object/path/<namespace>/<kind>/<name>/config/keywords[?driver=<driver>|section=<section>][&option=<option>]
```

The daemon applies all definition filters before returning the catalog:

- `driver` selects one exact driver, such as `container.docker`;
- `section` selects one exact configured section, such as `container#redis`,
  and lets OpenSVC resolve its driver from the object configuration;
- `option` selects one exact option name. Used alone, it returns every
  matching definition variant across drivers;
- `driver` and `section` are mutually exclusive.

An unknown option returns an empty successful list. Invalid drivers, sections,
empty filters, or the `driver`/`section` conflict are daemon errors. OpenSVC
requires at least `guest` visibility on the namespace named by the object path.

The daemon returns the complete filtered catalog without pagination. The MCP
validates and deterministically sorts that catalog, keeps the definitions of
the `options` asked, if any, then applies bounded cursor pagination locally.

`options`, a list of at most 30 exact option names, is filtered by the MCP,
not sent to the daemon: it keeps the definitions whose option or one of its
aliases is asked, and `missing_options` names the options no definition
holds. No option name holds a dot: an `option` or `options` value written as
a keyword, `<section>.<option>` such as `ai.agent_url`, is refused before the
daemon is called, the error naming the `section` and the option to give
instead, as the same filter of `list_node_config_keywords` is. With `driver`, one call checks the keywords a resource of that driver
uses before `create_object` or `update_object_config` writes them, where
paging through the catalog of the driver took several calls: the daemon
writes a keyword it does not know with a warning it does not return. The
`driver` filter does not read the object, and works for an object not
created yet; the `section` filter reads it. A cursor is tied to the path, filters, and returned
definition; pagination is recalculated from the current catalog and is not a
snapshot.

#### MCP properties

This tool is read-only, non-destructive, closed-world, and has no side effects.

#### Input

| Field | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `path` | Yes | — | 512 characters | Exact canonical object path |
| `driver` | No | Empty | 255 characters | Exact driver; mutually exclusive with `section` |
| `section` | No | Empty | 255 characters | Exact configured section; mutually exclusive with `driver` |
| `option` | No | Empty | 255 characters | Exact option name; mutually exclusive with `options` |
| `options` | No | Empty | 30 names of 255 characters | Exact option names, by name or alias, filtered by the MCP |
| `limit` | No | 25 | 1..100 | Maximum definitions in this page |
| `cursor` | No | Empty | 128 characters | Previous `next_cursor` for the same path and filters |

Example input:

```json
{
  "path": "prod/svc/redis",
  "section": "container#redis",
  "option": "image",
  "limit": 25
}
```

#### Example output

```json
{
  "provenance": {
    "source": "opensvc_daemon",
    "observed_at": "2026-09-27T08:00:00Z"
  },
  "object": {
    "path": "prod/svc/redis",
    "namespace": "prod",
    "kind": "svc",
    "name": "redis"
  },
  "filters": {
    "section": "container#redis",
    "option": "image"
  },
  "total": 1,
  "count": 1,
  "definitions": [
    {
      "section": "container",
      "option": "image",
      "scopable": false,
      "converter": "string",
      "text": "Container image reference.",
      "default_text": "",
      "example": "redis:7-alpine",
      "default": "",
      "default_option": "",
      "candidates": [],
      "depends": [],
      "kinds": ["svc"],
      "provisioning": true,
      "types": ["docker"],
      "aliases": [],
      "inherit": "leaf2head",
      "since": "v3.0.0",
      "deprecated": "",
      "replaced_by": "",
      "redact_secret": false,
      "recorded": false,
      "arithmetic": false,
      "required": true,
      "minimal": true
    }
  ],
  "truncated": false
}
```

Definitions are ordered by section, option, driver types, object kinds, and a
stable full-record tie-breaker. `types` identifies driver variants and `kinds`
identifies applicable object kinds. `redact_secret=true` describes how OpenSVC
must redact a future configured value; this tool itself returns no configured
secret value. An empty definition `section` identifies a generic object keyword
such as `comment`; it is preserved as a source fact.

The MCP requires `aliases`, `candidates`, `depends`, `kinds`, and `types` to be
JSON arrays, including when empty. A daemon regression that serializes one of
them as `null` is rejected instead of silently changing the contract.

### `get_object_config`

Returns sorted raw, non-evaluated configuration keyword records for one exact
object. Use it to compare declared settings with the operational state reported
by object, instance, and resource tools.

Configuration values can contain sensitive operational data. Request exact
keywords whenever the diagnosis does not require the complete configuration.

#### OpenSVC API and safety

```text
GET /api/object/path/<namespace>/<kind>/<name>/config?evaluate=false[&kw=<keyword>...]
```

The MCP always sends `evaluate=false`. It never dereferences references,
converts evaluated values, impersonates a node, or returns the raw INI file.
If the daemon unexpectedly returns evaluated data, the tool fails instead of
exposing it.

OpenSVC remains authoritative for namespace visibility. The example assumes
`guest` access on the object namespace.

#### MCP properties

This tool is read-only, non-destructive, closed-world, and has no side effects.

#### Input

| Field | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `path` | Yes | — | 512 characters | Exact canonical object path |
| `keywords` | No | All matching | At most 50, each at most 255 characters | Exact configuration keywords sent as repeated `kw` parameters |
| `limit` | No | 100 | 1..200 | Maximum keyword records returned by the MCP |

`limit=0` is the omitted Go zero value and selects the default 100; it never
means unlimited. The daemon endpoint itself has no `limit` parameter.

Example input:

```json
{
  "path": "prod/svc/redis",
  "keywords": [
    "container#redis.image",
    "container#redis.image_pull_policy"
  ],
  "limit": 100
}
```

#### Example output

```json
{
  "provenance": {
    "source": "opensvc_daemon",
    "observed_at": "2026-07-15T05:35:00Z"
  },
  "object": {
    "kind": "svc",
    "name": "redis",
    "namespace": "prod",
    "path": "prod/svc/redis"
  },
  "keyword_filter": [
    "container#redis.image",
    "container#redis.image_pull_policy"
  ],
  "total": 2,
  "count": 2,
  "items": [
    {
      "keyword": "container#redis.image",
      "value": "redis:7-alpine",
      "node": "",
      "value_truncated": false
    },
    {
      "keyword": "container#redis.image_pull_policy",
      "value": "once",
      "node": "",
      "value_truncated": false
    }
  ],
  "truncated": false,
  "values_truncated": 0
}
```

Records are sorted by keyword and node. Each value is limited to 4096 Unicode
characters, and all returned values share a 64-KiB character budget.
`value_truncated` identifies an individual shortened value;
`values_truncated` provides the aggregate count.

`truncated=true` means additional keyword records were omitted by `limit`.
Because the daemon endpoint has no pagination contract, request the required
omitted values explicitly through `keywords` rather than requesting an
unbounded response.

The data keys of a sec or usr object, the `data.*` keywords, are never
returned. The daemon replaces their values with a redaction for a reader
without `admin`, but gives an `admin` or `root` their encrypted `crypt:…`
blocks, which the model has no use for and which would spend the bound of
items. The tool leaves them out before the sort and the bound, so that
`total` and `truncated` count what is returned, gives their number in
`data_keys_omitted` with a `data_keys_note` pointing to
`list_object_data_keys`, and refuses a `keywords` filter naming one before
the call. The data keys of a cfg, plain text, are returned.

The cluster configuration is the object `cluster`, `root/ccfg/cluster`:
`get_object_config` and `list_object_config_keywords` read it with `path:
cluster`, its `hb` and `arbitrator` sections included; `get_cluster_config`
reads its file, redacted.

The two keywords om3 declares secret, `cluster.secret` and `node.uuid`, are
never returned either, whatever their scope: `cluster.secret` is scopable,
and the daemon gives a value scoped to a node in the keyword itself,
`cluster.secret@n1`, which the MCP compares without its scope, as the daemon
redaction of a file compares the base option. The daemon gives them in clear to `root` through the
keyword reads, which take no `redact-secrets` parameter, unlike the file
reads `get_cluster_config` and `get_node_config` use. The tool leaves them
out before the sort and the bound and counts them in `secrets_omitted`: a
filter naming `cluster.secret` answers an empty list with `secrets_omitted:
1`, set but not returned, rather than `total: 0` alone, which would read as
not set. The read back of `update_object_config` goes through the same read,
and returns the same count in its own `secrets_omitted`: a secret written is
not read back.

## Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Invisible or unauthorized object | Tool error from the daemon; commonly HTTP `403` or an empty selection |
| Invalid selector, path, limit, cursor, or keyword filter | Tool validation or daemon error |
| Conflicting definition `driver` and `section` filters | Tool validation error before calling the daemon |
| Missing object or unexpected selection | Tool error; no partial result |
| Evaluated configuration returned unexpectedly | Tool error; values are not exposed |
| Malformed daemon path, keyword definition, or required array | Tool error with parsing context |

Errors preserve bounded OpenSVC RFC 7807 details and never include the JWT.

### `list_object_data_keys`

Lists the names of the keys a cfg, sec or usr object stores in its `data`
section, never their values. Use it when a service fails on a missing
configuration or secret key: it tells whether the key exists, without
exposing what it holds.

#### OpenSVC API and authorization

```text
GET /api/object/path/<namespace>/<kind>/<name>/data/keys
```

The daemon requires `guest` access on the object namespace. It reads the copy
of the object on itself when it holds one, else on one node holding it; `node`
reports which copy was read. The daemon `filter` parameter is not used: the
optional `name` is matched exactly by the MCP.

The keys are the `data` section only. Configuration keywords of the object,
such as the `grant` of a usr object, are not keys; `get_object_config` reads
them.

#### Input and output

| Input | Meaning |
|---|---|
| `path` | Required exact path of a cfg, sec or usr object; other kinds are refused before the daemon call |
| `name` | Optional exact key name |

The output holds `provenance`, `object`, `node`, `name_filter`, `total`, and
`keys`, sorted by name. Each key holds `name` and `stored_size_bytes`, the
length of the value as stored: a sec or usr value is stored encrypted and
encoded, so its stored size is not the length of the secret, only a hint of an
empty or unusual value. An object without keys returns an empty list. For an
object no node holds, the daemon answers an empty body, which the tool reports
as an error naming the object.

### `get_object_data_key`

Reads the value of one key of a cfg object.

```text
GET /api/object/path/<ns>/cfg/<name>/data/key?name=<key>
```

The daemon reads a cfg key for a `guest` of the namespace, a sec key for an
`admin` and a usr key for `root`. The tool refuses a sec and a usr before the
call: their values are secrets, which would reach the context of the model,
and an admin would be allowed to read a sec. `list_object_data_keys` gives
their names.

The daemon answers `text/plain` for a value that is valid UTF-8,
`application/octet-stream` otherwise, and relays the value a peer holds, for
a cfg the node of the MCP does not hold, as `application/json`: the tool
takes any type of a 200 and tells text by its UTF-8 validity. It returns a
text value up to
64 KiB, cut on a character boundary with `truncated`, and for a binary value
`binary: true` and `size_bytes` only. A key absent is the 404 of the daemon;
an object no node holds is answered with an empty 200, as for the list of the
keys, which the tool reports as `object not found`. The value is untrusted
text. A value over 1 MiB, the bound of the client, is read in part:
`read_whole` is false and `size_bytes` is that part.
