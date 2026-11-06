# OpenSVC object configuration

The configuration file of an OpenSVC v3 object, as `create_object` sends it
and `update_object_config` changes it. Read from the daemon code
(`core/xconfig`, `core/object`, `core/keyop`) and from the keyword catalog
of the daemon, which `list_object_config_keywords` gives.

The daemon validates a configuration, and refuses a value it does not accept,
but it holds a keyword it does not know for a warning: the keyword is
written, and nothing says so. Write only the keywords the catalog gives.
OpenSVC v2 wrote some keywords and references otherwise: do not rely on
memory.

## The file

An INI file: sections in brackets, then `keyword = value` lines. A value runs
to the end of its line; a list is separated by spaces.

| Section | Holds |
|---|---|
| `[DEFAULT]` | The keywords of the object: `nodes`, `orchestrate`, `topology`, `placement`, `priority`, `flex_target`, … |
| `[<group>#<n>]` | One resource, such as `[ip#1]`, `[fs#1]`, `[app#1]`, `[disk#1]`, `[container#1]`, `[volume#1]`, `[sync#1]`, `[task#1]`; its `type` keyword selects the driver, `<group>.<type>`, such as `ip.host` or `fs.ext4` |
| `[env]` | Free keywords, the values the other keywords reference as `{env.<key>}`; the catalog does not list them |
| `[labels]` | Free keywords too, labels of the object |
| `[subset#<group>:<name>]` | The resources of one group whose `subset` keyword names the subset, such as `parallel = true` |
| `[data]` | The keys of a `cfg`, `sec` or `usr` object: never write it with these tools, the daemon keeps it apart |

- The `id` of `[DEFAULT]` is the daemon's: `create_object` drops the one the
  file gives, and the daemon gives the object its own.
- An object kind takes its own keywords: a `svc` holds resources, a `vol` is a
  volume of a pool, its `pool` and `size`, most often created by a `volume`
  resource of a service, a `cfg` or `sec` holds data
  keys, and a namespace configuration, `nscfg`, holds the quotas of a
  namespace.
- `list_node_drivers` gives the drivers a node has, what each does, whether
  the node can run it (`available`), the grant using or provisioning it
  needs, and the driver an alias names, to write instead. To check the keywords of
  a resource, call `list_object_config_keywords` once for it, its `driver`,
  `<group>.<type>`, and in `options` the keywords it uses: the answer gives
  their definitions, and `missing_options` the keywords the driver does not
  hold. The driver filter works for an object not created yet. Do not page
  through the catalog of a driver. A keyword the catalog marks `scopable`
  takes a scope, below.

## Scopes

A scopable keyword can hold a value per node: `<keyword>@<scope> = <value>`.

| Scope | Applies to |
|---|---|
| `@<node>` | The node named, such as `dev@n1` |
| `@nodes` | The nodes of the `nodes` keyword |
| `@drpnodes` | The nodes of the `drpnodes` keyword |
| `@encapnodes` | The nodes of the `encapnodes` keyword |

A node reads the first value found: its own name, then `@nodes`, then
`@drpnodes`, then `@encapnodes`, then the keyword without scope.

## References

A value can reference another between braces, evaluated on each node.

| Reference | Value |
|---|---|
| `{name}`, `{namespace}`, `{kind}`, `{path}`, `{id}` | The object |
| `{fqdn}` | `<name>.<namespace>.<kind>.<cluster name>`, a name unique in the cluster |
| `{nodename}`, `{short_nodename}` | The node evaluating the value |
| `{rid}`, `{rgroup}`, `{rindex}` | The section of the keyword: `fs#1`, `fs`, `1` |
| `{clustername}`, `{clusternodes}` | The cluster |
| `{<keyword>}` | A keyword of the same section, such as `{mnt}` in `[fs#1]` |
| `{<section>.<keyword>}` | A keyword of another section, such as `{fs#1.mnt}` or `{env.port}` |
| `{node.env.<key>}` | A key of the `env` section of the node configuration |
| `{#<keyword>}` | The number of words of a value, such as `{#nodes}` |
| `{<keyword>[<n>]}` | The word `n` of a value, from 0, such as `{nodes[0]}` |
| `{upper:<ref>}`, `{lower:<ref>}` | The value in upper or lower case |
| `$(<expression>)` | Arithmetic, for a keyword holding a number, such as `$({env.base}+1)` |

## Examples

A failover service: one node at a time runs an address, a filesystem on a
block device and an application, started in that order.

```ini
[DEFAULT]
nodes = n1 n2
orchestrate = ha
topology = failover

[env]
port = 8080

[ip#1]
type = host
name = web.example.com
netmask = 24
dev = eth0
dev@n2 = ens3

[fs#1]
type = ext4
dev = /dev/vg_web/lv_web
mnt = /srv/{fqdn}

[app#1]
type = forking
start = /usr/local/bin/web start --port {env.port} --data {fs#1.mnt}
stop = /usr/local/bin/web stop
check = /usr/local/bin/web status
```

A flex service: an application on two of three nodes, each instance spread
on a node of its own.

```ini
[DEFAULT]
nodes = n1 n2 n3
topology = flex
flex_target = 2
placement = spread
orchestrate = ha

[app#1]
type = forking
start = /usr/local/bin/worker start --id {nodename}
stop = /usr/local/bin/worker stop
check = /usr/local/bin/worker status
```

Two applications of a subset, started together rather than in order.

```ini
[DEFAULT]
nodes = n1

[subset#app:workers]
parallel = true

[app#1]
type = forking
subset = workers
start = /usr/local/bin/worker start --id {rindex}
stop = /usr/local/bin/worker stop --id {rindex}

[app#2]
type = forking
subset = workers
start = /usr/local/bin/worker start --id {rindex}
stop = /usr/local/bin/worker stop --id {rindex}
```

## Updating

`update_object_config` deletes sections, unsets keywords and applies set
operations, in this order: `<section>.<keyword><operator><value>`, the
operator `=` to set, `+=` to append a word, `-=` to remove one, `|=` to append
a word absent, `^=` to toggle one; `<section>.<keyword>[<n>]=<value>` inserts
a word at an index. A scope is part of the keyword: `ip#1.dev@n2=ens4`.

A keyword of a resource that holds something on a node, such as the `mnt` of
a filesystem or the address of an ip, cannot change while the resource is up:
the daemon answers 403, stop it first. The running instances keep their
configuration until restarted.

## After create_object

`create_object` writes the configuration, and nothing else:

1. An object orchestrated `ha` or `start` is flagged stopped on the node that
   created it, `flagged_stopped`, so that it does not start before anything is
   asked of it.
2. Its resources are not provisioned: a disk, a filesystem, a volume does not
   exist yet, and `start_object` answers 409 until they are.
   `provision_object` creates them, then starts the leader instance and lifts
   the stopped flag; it refuses, on every node, what something else holds.
3. `get_object_status` and `list_object_instances` follow it, `stop_object`
   stops it, `freeze_object` keeps the daemon from acting on it.
4. An object created by mistake, never provisioned nor started, is removed
   with `delete_object`.

Start from the configuration of a similar object, `get_object_config`,
rather than from memory, then check the keywords of each resource in one
`list_object_config_keywords` call, its `driver` and `options`.
A value the user did not give, such as a mount point, an address or a
command, ask for it, or propose it explicitly before writing: never invent
one.
