# Resources and skills from the collector

[Back to README](../README.md) · [Configuration](configuration.md) · [Token exchange](token-exchange.md) · [Token delegation](delegation.md)

The MCP embeds no resource and no skill. The collector stores them, each
marked for the `om3` backend, the `oc3` backend or both; an `AIManager` of
the collector writes them. The MCP serves those of the `om3` backend on both
listeners, through the MCP protocol:

| MCP request | Collector route |
|---|---|
| `resources/list` | `GET /ai/resources?backend=om3` |
| `resources/read` of `om3://resources/<name>` | `GET /ai/resources/<name>?backend=om3` |
| `skills/list`, `skills/get` | `GET /ai/skills?backend=om3` |
| `resources/read` of `skill://<name>/SKILL.md` | `GET /ai/skills/<name>?backend=om3` |

The skills follow the MCP Skills extension, `io.modelcontextprotocol/skills`
([SEP-2640](https://modelcontextprotocol.io/extensions/skills/overview)),
declared with the resources capability, `directoryRead` false. The routes and
the rules of the objects are those of the collector; see the collector API
document of oc3-mcp, `docs/collector-ai-content.md`.

Each MCP request reads the collector as the user, and the MCP keeps nothing:
the collector applies the groups each object is published to. A 404 of the
collector is an MCP "resource not found"; any other failure is an MCP error
naming the HTTP status only, never a URL or a credential. Without a
collector, the server declares neither the resources capability nor the
extension.

## The token of the collector

The collector accepts its own tokens only. For each MCP request, the MCP
exchanges the token of the caller for a collector token (RFC 8693), with the
exchange profile `OPENSVC_MCP_COLLECTOR_AUTH_PROFILE` of
`OPENSVC_MCP_AUTH_CONFIG_FILE` and the audience
`OPENSVC_MCP_COLLECTOR_AUDIENCE`:

- on HTTPS, the MCP token of the external client, as for a daemon call;
- on the Unix socket, the OpenID Connect token of the user the AI agent
  delegates, once the MCP verified it for its cluster. It is the only use of
  an exchange on the socket: the daemon tools still receive the delegated
  token unchanged, and the collector never receives it.

On the socket, this changes what leaves the MCP: the delegated token, which
otherwise reaches only the daemon of its cluster, is also posted to the token
endpoint of the SSO, as the subject token of the exchange (RFC 8693). The
exchange client then needs the provider of each cluster federated, or the
content cannot be read. The exchange refuses a token that still carries the
audience of the cluster (`delegation.audience`): a collector token never
reaches a daemon.

The exchanged token follows the rules of the [token exchange](token-exchange.md#token-lifetime-and-failures):
kept one minute at most in memory, never past the token it was exchanged
from, forgotten when the collector answers 401 or 403. A refused exchange
fails the request with the safe message of the exchange. Each content read is
audited, `mcp content read`, with the outcome of the exchange (see the
[audit trail](token-exchange.md#audit-trail)), and counts in the call limits
of an HTTPS identity.

In the SSO:

- the exchange client of the profile may issue tokens for the collector
  client;
- on the socket, the exchange client accepts the tokens of the provider of
  each cluster as subject tokens: with Authentik, each cluster provider is a
  federated provider of the exchange client, else the exchange fails with
  `invalid_grant`.

## Configuration

```text
OPENSVC_MCP_COLLECTOR_URL=https://collector.example.com/api
OPENSVC_MCP_COLLECTOR_AUDIENCE=<client ID of the collector>
OPENSVC_MCP_COLLECTOR_AUTH_PROFILE=<profile of OPENSVC_MCP_AUTH_CONFIG_FILE>
OPENSVC_MCP_COLLECTOR_CA_FILE=/etc/opensvc-mcp/collector-ca.pem   # optional
```

A socket-only server takes `OPENSVC_MCP_AUTH_CONFIG_FILE` for the collector
alone. No network contact occurs at startup.

## The objects OpenSVC ships

Two objects were embedded in the MCP before. Their text is kept here, for an
`AIManager` to publish in the collector:

- [object-config.md](object-config.md), the reference of the configuration
  file of an object, which the descriptions of `create_object` and
  `update_object_config` tell the model to read as
  `om3://resources/object-config` when the server lists it. A test checks its
  examples against the keyword catalog of the daemon.
- [skills/node-maintenance/SKILL.md](skills/node-maintenance/SKILL.md), the
  maintenance of a node. A test checks that the tools its `metadata`
  `opensvc/tools` lists, and those its body calls, between backticks, are
  tools of the server, both ways.

```sh
# COLLECTOR_URL as OPENSVC_MCP_COLLECTOR_URL, TOKEN a collector token of an AIManager
jq -n --rawfile body docs/object-config.md '{
  name: "object-config",
  title: "Object configuration",
  description: "The configuration file of an OpenSVC v3 object: its sections, the resources and their driver, the free sections, the scopes and references, examples, the update operators, and what follows a creation. Read it before writing a configuration with create_object or update_object_config.",
  backends: ["om3"],
  body: $body
}' | curl -sS -X POST "$COLLECTOR_URL/ai/resources" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" --data-binary @-

jq -n --rawfile body docs/skills/node-maintenance/SKILL.md '{
  name: "node-maintenance",
  backends: ["om3"],
  body: $body
}' | curl -sS -X POST "$COLLECTOR_URL/ai/skills" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" --data-binary @-
```

A later version replaces an object with `PUT` on `/ai/resources/object-config`
or `/ai/skills/node-maintenance`, the same JSON without `name`.
