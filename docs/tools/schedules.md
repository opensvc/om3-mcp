---
domain: scheduler
tools:
  - list_schedules
stability: experimental
---

# Scheduler Tools

This document describes the tool that reads OpenSVC scheduler entries without
executing actions or predicting scheduler behavior.

Implementation:

- business logic: `internal/core/schedule.go`;
- MCP definition: `internal/tools/schedule.go`.

## Tool selection

Use `list_schedules` to inspect the configured scheduler entries of one exact
node, object, or object instance. Select the scope explicitly:

- `node` reads node-level schedules and requires `node`;
- `object` aggregates schedules for an object and requires `path`;
- `instance` reads one object instance and requires both `path` and `node`.

## `list_schedules`

Returns bounded scheduler entries with their raw expressions, requirements,
last execution timestamps, and next execution timestamps.

### OpenSVC API

```text
node:     GET /api/node/name/<node>/schedule
object:   GET /api/object/path/<namespace>/<kind>/<name>/schedule
instance: GET /api/node/name/<node>/instance/path/<namespace>/<kind>/<name>/schedule
```

The daemon endpoints return complete lists without pagination and do not
contractually guarantee their order. The MCP validates the response, sorts it
by object path, node, key, action, and schedule expression, then applies local
cursor pagination. The cursor is tied to the selected scope and entry identity;
pagination is recalculated from the current daemon response and is not a
snapshot.

An empty `schedule` is a task OpenSVC does not run on a schedule, such as the
backup of a store set to none: `scheduled` is `false`, a normal state, not an
error; it is `true` for every entry with an expression.

`last_run_at` is `null` when an action has never run. `next_run_at` is `null`
when OpenSVC does not currently publish a next execution. The MCP preserves
these values and the raw `schedule` and `require` expressions. It does not
calculate a future date, classify an entry as overdue, or infer scheduler
health.

### MCP properties

This tool is read-only, non-destructive, closed-world, and has no side effects.

### Input

| Field | Required | Default | Bounds | Meaning |
|---|---:|---:|---:|---|
| `scope` | Yes | — | `node`, `object`, or `instance` | Exact schedule scope |
| `path` | Conditional | Empty | 512 characters | Required for object and instance scopes; forbidden for node scope |
| `node` | Conditional | Empty | 255 characters | Required for node and instance scopes; forbidden for object scope |
| `limit` | No | 100 | 1..200 | Maximum entries in this page |
| `cursor` | No | Empty | 64 characters | Previous opaque `next_cursor` for the same target |

Object example:

```json
{
  "scope": "object",
  "path": "prod/svc/redis",
  "limit": 100
}
```

Instance example:

```json
{
  "scope": "instance",
  "path": "prod/svc/redis",
  "node": "node-a"
}
```

### Example output

```json
{
  "provenance": {
    "source": "opensvc_daemon",
    "observed_at": "2026-09-28T12:00:00Z"
  },
  "scope": "object",
  "object": {
    "path": "prod/svc/redis",
    "namespace": "prod",
    "kind": "svc",
    "name": "redis"
  },
  "total": 1,
  "count": 1,
  "schedules": [
    {
      "object": "prod/svc/redis",
      "node": "node-a",
      "key": "status_schedule",
      "action": "status",
      "schedule": "@10m",
      "scheduled": true,
      "last_run_at": null,
      "next_run_at": "2026-09-28T12:10:00Z",
      "max_parallel": 1,
      "require": "",
      "require_collector": false,
      "require_provisioned": false
    }
  ],
  "truncated": false
}
```

### Authorization

Node scope requires the OpenSVC `root` grant. Object and instance scopes
require `guest`, `operator`, or `admin` access on the object's namespace, or
the global `root` grant. The daemon remains authoritative for authorization.
