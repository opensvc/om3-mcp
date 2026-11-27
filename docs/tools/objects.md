---
domain: object
tools:
  - list_cluster_objects
  - get_object_status
  - get_object_config
  - validate_object_config
  - validate_object_configs
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
  `internal/core/config_object.go`, `internal/core/config_object_validate.go`,
  `internal/core/config_object_validate_many.go`,
  `internal/core/config_object_keyword.go`, and `internal/core/object_data_key.go`;
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
Use `validate_object_configs` to tell which objects have a configuration in
error or in warning, every object or those of a selector, in one call: the
status tools and `get_cluster_status`, whose `config_issues` are of the
cluster configuration only, never tell it. Use `validate_object_config` for
all the alerts of one object, on a node, or after a write: a keyword the
daemon does not know is written with a warning the write tools do not return.
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

The selector is the native one of om3, passed unchanged: a path pattern, such
as `prod/**` or `*/svc/*`, or a configuration term, `keyword=value` or
`keyword!=value`. The keyword is one of the `DEFAULT` section, such as `app` or
`env`, or `section.keyword`: `fs#1.type` for one resource, `fs.type` for every
`fs` section. `+` joins the terms that must all match:
`prod/svc/*+app=web+env=DEV` gives the services of the app web in development
in one call, rather than one `get_object_config` per object. A comma separates
alternatives, each a whole group of `+`-joined terms, which repeats its path
pattern: `prod/svc/*+app=web,prod/svc/*+app=api`, not
`prod/svc/*+app=web,app=api`, whose second group selects in every namespace.

What a term matches, as the daemon evaluates it:

- only a keyword set in the configuration file, never a default: `orchestrate=no`
  misses the objects left at the default, and `app!=web` the objects without
  `app`;
- the value as written, not evaluated: `env.foo=web` misses `foo = {name}`; a
  scoped value only by its scoped keyword, `DEFAULT.env@n1=DEV` and not
  `env=DEV` for `env@n1 = DEV`; a list whole, `nodes=dev5n1 dev5n2` and not
  `nodes=dev5n2`;
- the value of a text keyword exactly, case included, without wildcard:
  `env=prd` or `app=we*` select nothing; a typed keyword is converted,
  `disable=yes` matching `disable = true`;
- only the configurations held by the node that answers, the node of the
  cluster virtual IP: the daemon evaluates the term on its own files, and a
  node holds only those of the objects whose `nodes` include it, so that the
  others are missing from the answer. The result then carries `term_note`.

The MCP refuses, before the call, with a message saying what to write:

- a term on a keyword it never returns, a secret keyword or a `data.*` key:
  the count of the matching objects would disclose their values one
  comparison at a time;
- a term that compares an order, `<`, `>`, `<=` or `>=`, not offered: om3
  converts the values of a typed keyword, such as `priority` or a size, and
  compares those of the others as text;
- a negation of a term, `!app=web`: it would select the objects whose
  configuration the answering node does not hold, as not matching;
  `app!=web` compares the value, and a negated path pattern,
  `*/svc/*+!prod/**`, stays exact;
- a space around a term, its keyword or its operator, and an empty term: the
  daemon would select nothing, or fail. A space inside a value, a list, is
  kept;
- a keyword scoped to a node name with a dot but no section,
  `env@n1.example.com=DEV`: write `DEFAULT.env@n1.example.com=DEV`.

The MCP removes duplicate paths, parses them into stable references, sorts them
by canonical path, and then applies pagination.

OpenSVC returns only paths visible through the caller's namespace grants. An
empty result can mean no matching object, no visibility or, for a
configuration term, an object whose configuration the answering node does not
hold; the MCP does not distinguish these cases because doing so could disclose
unauthorized inventory.

#### MCP properties

This tool is read-only, non-destructive, closed-world, and has no side effects.

#### Input

| Field | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `selector` | No | `**` | 512 characters | Native OpenSVC object selector: a path pattern or configuration terms |
| `limit` | No | 100 | 1..200 | Maximum references in this page |
| `cursor` | No | Empty | 1024 characters | Previous `next_cursor` for the same selector |

Common selectors:

| Selector | Meaning |
|---|---|
| `**` | All visible objects |
| `*/svc/*` | All visible service objects |
| `prod/**` | All visible objects in namespace `prod` |
| `prod/svc/redis` | One exact object |
| `prod/svc/*+app=web` | The services of `prod` whose configuration sets `app = web` |
| `*/svc/*+env=DEV+fs.type=flag` | The services in development with a flag `fs` resource |
| `prod/svc/*+app=web,prod/svc/*+app=api` | The services of `prod` of the app web or api |

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

A selector holding a configuration term adds `term_note`, which recalls that
the objects whose configuration the answering node does not hold are missing.

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

The daemon expands the `path` of this request as a selector, configuration
terms included: the MCP refuses before the call an exact path holding a
character of a selector, `* ? [ ] , \ + ! = : < >` or a space, as it does for
every tool taking one exact object path. The MCP requires exactly one returned
object. This read does not execute status drivers: compare `updated_at` with
the current time before relying on the state. Use `list_object_instances` to inspect per-node timestamps,
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

The keywords om3 declares secret, `cluster.secret`, `node.uuid` and
`store.ssh_private_key`, are never returned either, whatever their scope and
the index of their section (`store#1.ssh_private_key`): `cluster.secret` is scopable,
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

### `validate_object_config`

Validates the configuration of one exact object on one node, as
`om <path> config validate` does there, and returns its alerts. It works for
every object holding a configuration: svc, vol, cfg, sec, usr, nscfg, and the
cluster configuration (`path: cluster`).

#### OpenSVC API and safety

```text
GET /api/node/name/<node>/instance/path/<namespace>/<kind>/<name>/config/validate
```

The daemon of the node validates the configuration it holds: the alerts of
the keywords, unknown, unsupported, not evaluating or missing a keyword a
strategy requires, and those of the resources refusing their configuration
when the object loads them, by checks their drivers make on that node. The
references are evaluated there, so the result may differ from one node to
another.

A write refuses a configuration holding an error (HTTP `400` from
`update_object_config` or `create_object`), and writes an unknown keyword with
a warning it does not return. An object can still be in error: a
configuration edited by hand, older than a rule, or evaluating otherwise on
another node. This tool finds them.

Without `node`, the MCP reads the object status and validates on the first
node by name of those holding its configuration, as the webapp does, else of
its scope, and says so with `node_chosen`. `level` is that of this node: give
`node` to check another. An object it cannot choose a node for, such as a
namespace configuration the object status may not list, asks for one.

The daemon answers the administrators of the namespace, the cluster
administrators and root only: an alert may quote a value. It refuses the
others with HTTP `403`, its reason returned as it gives it. The cluster
configuration takes root or join, which the tool adds to the refusal.
Comments are untrusted text. The comment of an alert of a secret keyword,
`cluster.secret`, `node.uuid`, `store.ssh_private_key`, or a data key of a sec
or usr, is left out all the same, as every read of the MCP leaves the secrets
out.

The answer of the daemon is checked: a list of another kind, without its
items, or an alert of another node, another object or a level neither
`error` nor `warning` fails the call, rather than read as valid.

#### MCP properties

This tool is read-only, non-destructive, closed-world, and has no side effects.

#### Input

| Field | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `path` | Yes | — | 512 characters | Exact canonical object path, no selector |
| `node` | No | The first node by name holding the configuration, else of the scope | One exact node name, not `_` | The node to validate on; required for a nscfg the object status does not list |

Example input:

```json
{
  "path": "data/vol/lake-01-data"
}
```

#### Example output

```json
{
  "provenance": {
    "source": "opensvc_daemon",
    "observed_at": "2026-11-26T16:40:00Z"
  },
  "object": {
    "kind": "vol",
    "name": "lake-01-data",
    "namespace": "data",
    "path": "data/vol/lake-01-data"
  },
  "node": "dev5n1",
  "node_chosen": true,
  "level": "warning",
  "error_count": 0,
  "warning_count": 1,
  "alerts": [
    {
      "level": "warning",
      "kind": "unknown keyword",
      "key": "disk#0.name",
      "driver": "disk.loop",
      "comment": "keyword does not exist: \"disk#0.name\""
    }
  ],
  "truncated": false
}
```

`level` tells the object on that node as the webapp does: `error` when one
alert at least is an error, `warning` when warnings only, `valid` when none.
The alerts come errors first, then by key, at most 200, and at most 64 KiB of
comments together: `truncated` says some were left out, `error_count` and
`warning_count` count them all. A comment is at most 1 KiB, its cut counted
in `comments_truncated`; a kind, a key or a driver at most 255 bytes. A
keyword of `DEFAULT` comes without its section, `a` for `DEFAULT.a`.

The errors are: an unscopable keyword, an unsupported value, an evaluation
error, an invalid section name, a resource configuration error (a warning for
a disabled resource), a key source error, an unsupported resource group, a
missing strategy and a missing keyword. The warnings: unknown keyword, section,
driver or resource, deprecated or moved keyword or value, ignored keyword,
conflicting strategies, and a driver unusable on the node.

### `validate_object_configs`

Tells which objects have a configuration in error or in warning, in one call:
the configurations of the objects a selector names, every object by default,
each validated on its first node, as the webapp shows them in its list. It is
one of the tools the model sees without a search: a question such as "which
services have a bad configuration?" is a question on all of them, which one
`validate_object_config` an object would not answer within the bounds of a
turn.

#### OpenSVC API and safety

```text
GET /api/object?path=<selector>
GET /api/node/name/<node>/instance/path/<namespace>/<kind>/<name>/config/validate
```

One read of the object list gives the objects of the selector, the nodes
holding the configuration of each and its scope; the cluster configuration is
left out, `get_cluster_status` telling its issues and the daemon validating it
for root and join only. Each object is validated on the first by name of those
nodes, else of its scope, four at a time, as the webapp does, at most 50 a
call: past them, `truncated` and `next_cursor` continue, by path. The objects
not validated yet may be those in error: the description tells the model to
continue until `truncated` is false, adding the counts of the calls, before
concluding, or to narrow the selector, `*/svc/*` for the services. The
validation of each is that of `validate_object_config`, its answer checked and
the comments of its secret keywords left out.

The daemon listener grants an address 20 requests a second, in a burst of 100
(`listener.throttle_rate`, `listener.throttle_burst`), and the MCP is
one address for all its users: a call of 51 requests stays well under the
burst. A call stops sending at the first `429` of the daemon, and after 30
seconds: it answers the objects validated in order before the first one not
validated, `stopped` telling `throttled` or `deadline`, and `next_cursor`
continuing there; an object not validated is never counted. A request that
does not reach a node, its timeout spent, fails that object; after 3 of them,
the other objects of that node fail without a request.

The daemon validates for the administrators of a namespace, the cluster
administrators and root only. An object it refuses is counted in
`refused_objects` and named in `refused`, never counted valid; one whose
validation fails otherwise, such as a node not answering, in `failed_objects`,
and `failed` groups them by reason. The call fails instead of answering counts
that would read as nothing to tell: a token the daemon refuses (`401`), a
daemon this MCP cannot use (`daemon_incompatible`, such as a daemon older than
v3.0.0-rc55 without the validation route), a `429` or the deadline before any
object, and a page whose validations all failed. A selector naming no object
the caller sees answers a `note`; a configuration term in the selector reads
only the configurations the answering node holds (`term_note`).

The object list holds the whole status of each object, about 4 KiB: past
about 2 000 objects, it exceeds the 10 MiB the MCP reads of an answer, and a
call on `**` fails; a narrower selector holds fewer.

#### MCP properties

This tool is read-only, non-destructive, closed-world, and has no side effects.

#### Input

| Field | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `selector` | No | `**` | 512 characters | Object selector, as `list_cluster_objects` takes it |
| `cursor` | No | — | 1024 characters | `next_cursor` of a previous call with the same selector |

#### Example output

```json
{
  "provenance": {"source": "opensvc_daemon", "observed_at": "2026-11-26T18:00:00Z"},
  "selector": "**",
  "total": 84,
  "validated": 50,
  "error_objects": 0,
  "warning_objects": 3,
  "valid_objects": 47,
  "refused_objects": 0,
  "failed_objects": 0,
  "objects": [
    {
      "path": "data/vol/lake-01-data",
      "node": "dev5n1",
      "level": "warning",
      "error_count": 0,
      "warning_count": 1,
      "alerts": [
        {"level": "warning", "kind": "unknown keyword", "key": "disk#0.name", "driver": "disk.loop", "comment": "keyword does not exist: \"disk#0.name\""}
      ],
      "alerts_truncated": false
    }
  ],
  "truncated": true,
  "next_cursor": "hr/svc/db-01"
}
```

Every object of the call in error or in warning is listed, in error first,
then by path. 100 alerts are shared among them, one each at least, errors
first then by key (`alerts_truncated`), within 32 KiB of comments together:
the counts are of all. The counts are of the call: those of a selector
validated in several calls add up. `validate_object_config` gives all the
alerts of one object, or validates it on another node.

## Errors

| Condition | Result |
|---|---|
| Missing, invalid or expired access token (OAuth on HTTPS, OpenID Connect on the socket) | MCP HTTP `401` |
| Invisible or unauthorized object | Tool error from the daemon; commonly HTTP `403` or an empty selection |
| `validate_object_config` by a caller neither administrator of the namespace nor root | Tool error with the HTTP `403` of the daemon and its reason |
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
