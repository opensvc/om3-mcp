# Token exchange and multi-cluster calls

[Back to README](../README.md) · [Configuration](configuration.md) · [Authentication](authentication.md)

The external client authenticates to MCP with an access token intended for the
MCP resource. For each daemon tool call, the registrar validates the input
schema, selects the administrator-configured cluster, and exchanges the
caller's access token once (RFC 8693). Only the exchanged token is sent to the
configured cluster VIP. No daemon token is returned to the client.

## Installation

The MCP does not modify the SSO, daemons or remote files. Values below are
fictional; keep deployment configuration outside Git.

1. Adapt [clusters.yaml](../deploy/examples/clusters.yaml) into
   `/etc/opensvc-mcp/clusters.yaml`: one entry per cluster with its
   `cluster_id`, VIP `endpoint`, and `auth.profile` / `auth.audience`.
2. Adapt [auth.yaml](../deploy/examples/auth.yaml) into
   `/etc/opensvc-mcp/auth.yaml`.
3. Store only the confidential exchange client's secret in
   `/etc/opensvc-mcp/secrets/opensvc-mcp-exchange`, owned by the MCP runtime
   account, mode `0600` or `0400`. A final newline is accepted. The process
   reads the secret at startup; restart after rotating it. Do not put it in Git,
   tool arguments, a chat, or shell command-line arguments.
4. Set the MCP process environment (see [environment variables](configuration.md#environment-variables)):

   ```text
   OPENSVC_MCP_OAUTH_RESOURCE_URL=https://mcp.example.com:8443/mcp
   OPENSVC_MCP_OAUTH_ISSUER=https://sso.example.com/oauth/opensvc-mcp/
   OPENSVC_MCP_CLUSTER_CONFIG_FILE=/etc/opensvc-mcp/clusters.yaml
   OPENSVC_MCP_AUTH_CONFIG_FILE=/etc/opensvc-mcp/auth.yaml
   ```

5. Configure each daemon to trust its target issuer and audience:

   ```ini
   [listener]
   openid_issuer = https://sso.example.com/oauth/opensvc-cluster-a/
   openid_client_id = opensvc-cluster-a
   ```

   This replaces the previously trusted provider. Follow the daemon's
   configuration reload procedure and check that its OpenID strategy initialized
   successfully on the node carrying the VIP once the provider is reachable.

## SSO requirements

The SSO must support Authorization Code with PKCE and OAuth 2.0 Token Exchange
(RFC 8693). Three client roles are involved:

| Role | Example client ID | Purpose |
|---|---|---|
| Incoming, public | `opensvc-mcp` | Agent login with PKCE; the access token audience contains the MCP resource URL |
| Exchange, confidential | `opensvc-mcp-exchange` | Server-side RFC 8693 requests authenticated with its client secret |
| Target, one per cluster | `opensvc-cluster-a` | Issues daemon tokens for its own audience, carrying the user's OpenSVC grants |

Configure in the SSO:

- the exchange client allowed to use the token-exchange grant and to accept
  subject tokens issued for the incoming client;
- each target allowing tokens to be issued for the exchange client;
- the target token carrying the user's OpenSVC grants in the `entitlements`
  claim expected by the daemon, emitted for the technical scopes requested in
  `auth.yaml`. Exchange mappings are not necessarily copied from the incoming
  token: verify the claims of an actually exchanged token, not a preview;
- the user or group entitlements and the target application access policies.

Some SSO products require the exchange client to appear in the incoming token
audience; add it next to the MCP resource URL when needed. The `audience`
request parameter generally selects among audiences the SSO is configured to
issue; it does not create a missing one.

The MCP uses the exchange profile issuer's discovery document, verifies its
issuer, then posts a form to the discovered HTTPS token endpoint:

```text
grant_type=urn:ietf:params:oauth:grant-type:token-exchange
subject_token=<authenticated incoming MCP access token>
subject_token_type=urn:ietf:params:oauth:token-type:access_token
requested_token_type=urn:ietf:params:oauth:token-type:access_token
audience=opensvc-cluster-a
scope=opensvc-mcp
```

The confidential client authenticates using `client_secret_basic` (default) or
`client_secret_post`. No browser redirects, `resource`, actor token,
client-credentials grant, or service-account fallback is used for the exchange.

## Tool contracts

`list_clusters(query?, limit?, cursor?)` returns all configured clusters, without
checking grants or contacting the SSO or daemons. Search matches a display-name
substring without case sensitivity. Pages default to 100 entries, maximum 200.
The opaque cursor is bound to the query and public catalogue snapshot.

```json
{
  "items": [{"cluster_id": "00000000-0000-4000-8000-000000000001", "name": "cluster-a"}],
  "next_cursor": null,
  "provenance": {"source": "opensvc_mcp_catalog", "observed_at": "<UTC timestamp>"}
}
```

Every daemon tool has an additional required `cluster_id` argument, for example:

```json
{"cluster_id":"00000000-0000-4000-8000-000000000001","node":"node-a"}
```

This target field is added centrally to each typed tool's schema and handled by
the registrar; business inputs and core use cases remain unchanged. An omitted
or unknown cluster fails before exchange. Target headers cannot override it.
The tool's `node` retains its existing logical API semantics and never changes
the configured network endpoint. Node diagnostics require an exact node name:
the daemon alias `_` would name the node carrying the VIP, which may move.

## Token lifetime and failures

Only public discovery metadata is cached (five minutes). Concurrent calls share
one discovery request per profile, made without holding the lock; each waiter
stays bounded by its own deadline. A failed discovery is reused for five seconds
before a new attempt, so an SSO outage does not multiply discovery requests. A
caller's own cancellation is not cached as an SSO failure.

An exchanged token is kept in the memory of the MCP process, never on disk,
and reused by the next tool calls of the same MCP token for the same cluster:
for at most one minute, and never within 30 seconds of its expiry. The cache
key is a hash of the MCP token and the target, never the MCP token itself.
Concurrent calls share one exchange. A failed exchange is returned again for
five seconds without contacting the SSO. When the target
refuses the token (HTTP 401 or 403), it is forgotten and the next call
exchanges again. A claim change in the SSO therefore applies within a minute.

There is no refresh-token storage. One tool call can make several API requests
with its exchanged token. Its context expires at the earliest of the parent
request deadline, incoming JWT expiry, outgoing JWT expiry and `expires_in`.

The SSO response is bounded, and must return a Bearer access JWT for the selected
audience with a valid lifetime. Returning the incoming token or a token still
carrying the MCP resource audience is rejected. These checks are sanity checks
on a response from the authenticated SSO: the daemon remains responsible for
cryptographic validation of the outgoing signature and issuer and enforcement
of grants. The MCP does not rewrite claims or compare subject strings across
providers, whose subject modes may differ.

Discovery and exchange follow no redirects or environment proxies. Credentials
are sent only to the configured SSO's discovered endpoint, and the daemon token
only to the selected VIP. SSO TLS requires system roots or a configured CA;
daemon `tls.insecure` does not apply to the SSO. TLS and timeouts are per profile
and per cluster. No network contact occurs at startup.

SSO errors expose only recognized OAuth error codes, never response descriptions
or bodies. A refused exchange stops the call; a daemon refusal is returned using
the existing API error contract. Health and tool discovery continue to work
while the SSO token endpoint or a daemon is unavailable, provided the incoming
MCP JWT can still be verified.

Access-token lifetime and revocation are SSO policy. MCP verifies JWTs locally
without introspection: revoking a refresh token prevents renewal, but an access
token already issued remains usable until it expires. Short access-token
lifetimes with client-side renewal bound this window.

## Audit trail

When exchange is configured, and always on the Unix socket, every `tools/call` emits one `INFO` line
`mcp tool call` through the process logger (standard error), for example:

```text
INFO mcp tool call tool=get_node_status cluster_id=00000000-0000-4000-8000-000000000001 issuer=https://sso.example.com/oauth/opensvc-mcp/ subject=<sub> client_id=opensvc-mcp exchange=ok daemon_subject=<sub> outcome=ok duration=212ms
```

| Field | Meaning |
|---|---|
| `tool`, `cluster_id` | Requested tool and target argument, bounded to 256 bytes; `cluster_id` is empty for `list_clusters` |
| `issuer`, `subject` | Verified incoming MCP token identity |
| `client_id` | Client application from the incoming `client_id` (RFC 9068) or `azp` claim; empty if absent |
| `exchange` | `none` when no exchange was attempted (discovery, missing or unknown target), `ok`, the safe error message returned to the client, or `delegated` on the [Unix socket](delegation.md) |
| `daemon_subject` | Subject of the exchanged token, to correlate with daemon logs; subject modes may differ between providers |
| `outcome`, `detail` | `ok`, `tool_error` (exchange refusal, daemon refusal such as HTTP 403, or validation error) or `error` (protocol error); `detail` is the bounded error text already returned to the client |
| `duration` | Total call duration, including exchange and daemon requests |
| `request_id` | On the Unix socket, the `X-Request-ID` the AI agent sends, which its own audit lines carry; empty on HTTPS, or when absent or not 32 lowercase hexadecimal characters |

With a collector, every read of its [resources and skills](content.md),
`resources/list`, `resources/read`, `skills/list` or `skills/get`, emits one
`INFO` line `mcp content read`, on both listeners:

```text
INFO mcp content read method=resources/list cluster_id=00000000-0000-4000-8000-000000000001 issuer=https://sso.example.com/application/o/opensvc-cluster-a/ subject=<sub> client_id="" exchange=ok collector_subject=<sub> outcome=ok duration=148ms request_id=<id>
```

| Field | Meaning |
|---|---|
| `method` | The MCP method |
| `cluster_id` | On the Unix socket, the cluster of the delegated token; empty on HTTPS |
| `issuer`, `subject`, `client_id` | The verified identity of the caller, as for a tool call |
| `exchange` | `ok`, `none` when no exchange was attempted, or the safe error of the exchange, such as `token exchange refused by SSO (invalid_grant)`: on the socket, the provider of the cluster is not federated in the exchange client |
| `collector_subject` | Subject of the collector token, to correlate with the collector logs |
| `outcome`, `detail` | `ok` or `error`, with the error returned to the client |
| `duration`, `request_id` | As for a tool call |

On the [Unix socket](delegation.md), every `GET /mcp/auth/whoami` emits one
`INFO` line `mcp whoami`:

```text
INFO mcp whoami cluster_id=00000000-0000-4000-8000-000000000001 issuer=https://sso.example.com/application/o/opensvc-cluster-a/ subject=<sub> outcome=daemon_refused status=401 duration=12ms daemon_status=401
```

| Field | Meaning |
|---|---|
| `cluster_id` | Cluster selected by the `X-OpenSVC-Cluster-ID` header |
| `issuer`, `subject` | Claims of the delegated token, verified by the MCP |
| `outcome` | `ok`; `daemon_refused` (the daemon answered 401 or 403); `daemon_failed` (any other daemon failure, answered 502); `identity_mismatch` (the daemon authenticated another method than OpenID or another username than predicted); `expired`; `bad_request` |
| `status` | The HTTP status the bridge answered |
| `duration` | Total check duration, including the daemon request |
| `daemon_status`, `detail`, `auth` | When relevant: the daemon HTTP status, the bounded error of a daemon failure, the strategy the daemon reported |
| `request_id` | As for `mcp tool call` |

A missing or unknown cluster, or a token refused by the local verification of
the MCP (malformed, expired, or not signed by the provider of the cluster for
its audience), is refused with 401 before the bridge and emits no line.

Tokens, secrets, raw arguments other than `cluster_id`, and tool results are
never logged. Values come from verified claims or are quoted by the structured
logger. Retention and shipping of these logs belong to the deployment.

References: [RFC 8693](https://www.rfc-editor.org/rfc/rfc8693.html),
[MCP authorization](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization).
