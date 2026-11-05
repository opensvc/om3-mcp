---
name: node-maintenance
description: Use when the user plans to reboot, patch, update or repair an OpenSVC node, or to stop a service cleanly on one node before such work.
metadata:
  opensvc/tools: get_node_status get_object_status list_object_instances get_cluster_status drain_node freeze_instance shutdown_instance unfreeze_node unfreeze_instance giveback_object switch_object start_instance start_instance_standby
---

# Node maintenance

A maintenance needs the instances on the node down, their standby resources
included, and the daemon kept from starting them again before the work is
done. A plain stop is not enough: stop_instance leaves the standby resources
up, and an unfrozen ha failover service moves to another node.

The tools named here are listed among yours: call them directly, never
search for them.

On every action of this skill, leave the config_updated_at argument out: it
takes only the time update_object_config returns, and a status time there
makes the daemon refuse the action with 409.

## 1. Settle the scope

Ask, or read from the request, which of the two cases applies:

- **The whole node** goes down (reboot, hardware): every instance on it.
- **One service** must be off the node before the work, the others staying.

Name the node exactly. Never extend the work to another node, to the whole
object, or to the cluster.

## 2. Read before acting

- `get_node_status` for the node: whether it is already frozen, and its
  local expect: a value other than none means a drain is already under way,
  and the daemon refuses a second drain while one runs.
- For the service concerned, `get_object_status`: its orchestration (ha, start
  or no) and its topology. Then `list_object_instances`: which instances are
  up on the node.
- For the whole node, `get_cluster_status`: the objects and their state.

When the request settles the scope and the reads hold no surprise, go on to
the actions without asking in text: the confirmation of a destructive call is
a card the client shows on the call itself, not a question you write. Ask
first, once, naming each action and what will stop, only when the request
does not settle the scope, or when the reads show a surprise:

- the node is already frozen, or a drain is under way;
- for one service, its instance on the node under maintenance is up, not
  only stdby up: the service will move to another node;
- for the whole node, a service on it is orchestrated start or no: it will
  stay down after the maintenance.

After such a question, wait for the answer before any action.

## 3. Take it out of service

**One service on the node**, in this order, always:

1. `freeze_instance` with the object path and the node, even when the service
   is not orchestrated ha: harmless then, and lifted at step 5.
   `shutdown_instance` refuses the unfrozen instance of an ha service. Do not
   freeze the node instead: every other instance on it would lose its
   orchestration too.
2. `shutdown_instance` with the object path and the node. It stops every
   resource, the standby ones too, and runs the shutdown triggers.

**The whole node**: `drain_node`. It freezes the node, then shuts down every
instance on it, standby resources included, in one call. Do not freeze or shut
down the instances one by one first. Its instances up list is read before the
drain: the instances the drain is to stop, not a proof they stopped.

The drain is not over when the tool answers. Read its completed, monitor
state and local expect: a drain of several services often outlasts the wait,
and completed false is then normal. Follow it with `get_node_status` until
the local expect is none before going on to the check.

Never use for a maintenance: stop_instance (standby resources stay up),
stop_object (every node), freeze_cluster (every object), nor any unprovision,
purge or delete.

## 4. Check

`list_object_instances` for each service concerned: the instance on the node
is down, and an ha service runs on another node when its placement allows
it. Say plainly what is still up and where.

End with the state, the instance frozen and down; do not ask to be told when
the maintenance ends.

## 5. After the maintenance

Only when the user says the work is done: an ok or a go ahead does not say
it.

- After a drain: `unfreeze_node`. A service orchestrated start or no stays
  down, even after the unfreeze: name those services.
- After one service: `unfreeze_instance` on that node.

Then, for each service the user wants back on the node:

- If it runs on another node, as a failover service does once its instance
  here is down: `giveback_object` brings it back to its preferred nodes, or
  `switch_object` to this node. `start_instance` refuses a failover service
  whose instance on another node is up or warn.
- If no instance of it is up anywhere: `start_instance` on the node.
- If its standby resources are not back in stdby up after the unfreeze:
  `start_instance_standby` on the node.

## When an action tool is missing

The action tools exist only when the MCP server enables actions. If a tool
named here is not among your tools, search your tool catalog for it when your
client offers a search. If it is not there, tell the user that this server
does not allow actions, and list the steps above for an operator to run.
Never replace a missing action with another tool.
