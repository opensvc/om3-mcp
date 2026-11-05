---
domain: config-writes
tools:
  - update_object_config
  - create_object
  - update_node_config
stability: experimental
---

# Configuration Write Tools

This document describes the tools that write the configuration of an OpenSVC
object. They are action tools: registered only when
`OPENSVC_MCP_ACTIONS=enabled`, on the HTTPS listener and on the Unix socket
alike.

Implementation:

- business logic: `internal/core/config_object_update.go` and
  `internal/core/config_object_create.go`;
- MCP definitions: `internal/tools/object_config_write.go`;
- daemon client: `PatchJSON` and `PostFile` in `internal/client/client.go`.

## Contract

Both tools target one exact object, and leave the content to the daemon,
which validates the configuration and refuses what it does not accept with
the reason, a title and a detail naming the sections, keywords and nodes:
the MCP checks the form of the input only. The caller needs `admin` on the
object namespace; a namespace configuration, `nscfg`, needs the `squatter`
or `root` role. `force`, which only `root` may use and which overrides the
protection of a running resource, is not offered.

## The reference of the format: `om3://resources/object-config`

The reference of the configuration file of an object,
[`docs/object-config.md`](../object-config.md), is no longer embedded: an
`AIManager` publishes it in the collector as the resource `object-config` for
the `om3` backend, which the MCP serves as `om3://resources/object-config`
(see [resources and skills](../content.md)). Read from the daemon code
(`core/xconfig`, `core/object`, `core/keyop`) and its keyword catalog, it
gives the sections of a file, a resource and the driver its `type` selects,
the free sections `env`, `labels` and `data`, and `subset#`; the syntax of the
scopes and of the references, with an example of each; three examples; the
update operators; and what follows a creation, the flag stopped, then
`provision_object`, which provisions and starts the object. The descriptions
of `create_object` and `update_object_config` tell the model to read it, when
the server lists it, in the same turn as the call: the OpenSVC AI agent keeps
no string longer than 256 bytes of the earlier turns, so that a reference read
in one turn is gone in the next.

A test keeps the examples honest: each section of each example is checked
against a snapshot of the keyword catalog of the daemon,
`cmd/om3-mcp/testdata/keywords.json`, taken from dev5 for the `DEFAULT`
section, a subset section and the drivers the examples use; a keyword
missing from it, a driver it does not hold, or a scope on a keyword that
takes none fails the test. The daemon holds a keyword it does not know for
a warning it does not return, so that the reference, and the check of each
keyword with `list_object_config_keywords`, are what keeps the model from
writing one.

## `update_object_config`

`PATCH /api/object/path/<ns>/<kind>/<name>/config`, the operations in the
query, applied by the daemon in this order:

| Input | Meaning |
|---|---|
| `delete` | Sections to remove with their keywords, such as `fs#2` |
| `unset` | Keywords to remove, `<section>.<keyword>` |
| `set` | `<section>.<keyword><operator><value>`: `=` sets, `+=` appends, `-=` removes, `|=` appends an absent value, `^=` toggles; `<keyword>[<index>]=<value>` inserts at an index |
| `wait_seconds` | Optional, at most 120: hold the answer until the configuration reached every live node |

- **Bounds.** 1 to 50 operations, keywords of at most 255 characters without
  space, values of at most 4 KiB.
- **Form.** A set operation splits on its first operator, as the daemon
  splits it: one without operator, or with a comparison operator, `!=`,
  `>=`, `<=`, `>`, `<`, is refused before the call, rather than taken by
  the daemon for something else. An index takes `=` only. An empty section
  and `DEFAULT`, the whole object, are not deleted.
- **Unknown keywords.** The daemon validation holds a keyword it does not
  know for a warning, not an error: the keyword is written, and the PATCH
  answer does not return the warning. The description of the tool tells the
  model to check a keyword with `list_object_config_keywords` first.
- **Refusals of the daemon**, given as it reports them: 400 for a value it
  does not accept; 403 for a keyword of a resource changed,
  its type or `disable`, or its section deleted, while the resource is up on
  a node, to be stopped first; 409 when the configuration changed between
  its read and its write, when a value is held by a resource of another
  object on the same node, or when a size changes during a resize.
- **Result.** `is_changed`, and `outcome` `unchanged` when the operations
  left the configuration as it was; `last_modified`, the `OM-Last-Modified`
  time of the configuration; the `warnings` of the daemon; and the keywords
  the set and unset operations name, read again with `get_object_config`:
  the daemon reads an unset keyword back with an empty value, as it does a
  keyword never set. A read that fails gives `keywords_error`, the write
  being done. A secret keyword written, `cluster.secret` or `node.uuid`, is
  left out of that read and counted in `secrets_omitted`: written, its value
  is never returned.
- **Wait.** `wait_seconds` is held under the request timeout of the cluster,
  less 3 seconds, or not sent when that leaves less than a second. A wait
  that expires, a 408, is a write done: `outcome` `written`, and
  `propagation_pending` names the nodes not reached yet. It is never to be
  repeated: a `+=` would apply twice.
- Annotated destructive, and not idempotent.

### The cluster configuration

The cluster configuration is the object `cluster`, which om3-mcp reads as
`root/ccfg/cluster`, the path the daemon serves its configuration by:
`GET` and `PATCH /api/cluster/config` call the handlers of the object
configuration. `update_object_config` with `path: cluster` writes it, with
`wait_seconds` and the read back, which `PATCH /api/cluster/config` would not
offer. The daemon requires `admin` on the namespace `root`, a global `admin`
or `root`. `cluster.secret`, written, is never read back: `secrets_omitted`.

## `create_object`

`POST /api/object/path/<ns>/<kind>/<name>/config/file`, the configuration
file as `application/octet-stream`:

| Input | Meaning |
|---|---|
| `config` | The configuration file, INI text, at most 256 KiB, valid UTF-8 |
| `set` | Optional operations applied after it, as `update_object_config` takes them |
| `wait_local` | Optional, `true` by default: hold the answer until the daemon knows the object, so that `get_object_status` finds it |

- **A copy.** The daemon takes the configuration as copied from another
  object: the id and what it recorded of the things created for that object,
  a disk, an address, are left behind, and the object gets an id of its own.
  `restore`, which keeps them, is not offered.
- **Nothing started.** Nothing is provisioned or started:
  `provision_object` does it next. An object orchestrated `ha` or `start` is
  flagged stopped on the node that created it: `flagged_stopped`, read from
  the `orchestrate` keyword of the file created.
- **Refusals.** An object that exists is refused, 409: the error names
  `update_object_config`. A configuration the daemon does not accept, 400 or
  403; resources holding a value a resource of another object holds on the
  same node, 409 naming them.
- **Result.** `created`, `last_modified`, `flagged_stopped`, and the file
  created read again through the daemon with secret redaction, bounded as
  `get_cluster_config` bounds its file.
- Annotated destructive, and not idempotent: a client asks the user to
  confirm it, with its preview, the file as given (see
  [preview tools](previews.md)); the resources it declares may take a value
  another object holds.

## `update_node_config`

Changes the configuration of one node: deletes sections, unsets keywords,
then applies set operations, as `update_object_config` does.

```text
PATCH /api/node/name/<node>/config?delete=…&unset=…&set=…
```

The daemon relays the write to the node. The operations are checked as for an
object, 50 at most, 4 KiB per value. Unlike an object:

- the configuration of a node does not propagate: there is no `wait`, and the
  daemon gives no `OM-Last-Modified`;
- the answer is `{"is_changed"}` only;
- a configuration that does not validate is refused with 400, and a keyword
  the daemon does not know is written with a warning it does not return.

The tool returns `outcome`, `written` or `unchanged`, `is_changed`, and the
keywords named, read again with `get_node_config_keywords`; `node.uuid`,
written, is not read back and is counted in `secrets_omitted`. Destructive,
not idempotent. Requires `root`.
