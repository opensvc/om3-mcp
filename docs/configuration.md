# Configuration

[Back to README](../README.md)

## Environment variables

| Variable | Default | Description |
|---|---|---|
| OPENSVC_MCP_LISTEN_ADDR | 127.0.0.1:8443 | HTTPS bind address, IPv4:port or [IPv6]:port |
| OPENSVC_MCP_TLS_CERT_FILE | empty | Absolute server certificate PEM path; enables the HTTPS listener |
| OPENSVC_MCP_TLS_KEY_FILE | empty | Absolute server private key PEM path; required with the certificate |
| OPENSVC_MCP_OAUTH_RESOURCE_URL | empty | Public HTTPS MCP URL ending in `/mcp`; expected access-token audience; required with HTTPS |
| OPENSVC_MCP_OAUTH_RESOURCE_NAME | OpenSVC Daemon MCP | Human-readable `resource_name` in public OAuth metadata |
| OPENSVC_MCP_OAUTH_ISSUER | empty | Exact trusted issuer for incoming MCP access JWTs; required with HTTPS |
| OPENSVC_MCP_OAUTH_CA_FILE | empty | Optional absolute PEM trust bundle for SSO HTTPS; replaces system roots |
| OPENSVC_MCP_DELEGATED_CA_FILE | empty | Optional absolute PEM trust bundle for the OpenID providers of the clusters of the Unix socket, replacing the system roots; without it, the socket uses `OPENSVC_MCP_OAUTH_CA_FILE` when the HTTPS listener has one, else the system roots. Requires the socket |
| OPENSVC_MCP_AUTH_CONFIG_FILE | empty | Version 1 confidential SSO profile file; required when a cluster sets `auth`, and with the collector |
| OPENSVC_MCP_CLUSTER_CONFIG_FILE | empty | Cluster catalogue; required for the Unix socket and for exchanged daemon calls |
| OPENSVC_MCP_DELEGATED_SOCKET | empty | Absolute Unix socket path, at most 103 bytes; enables the delegated listener |
| OPENSVC_MCP_MAX_CONCURRENT_CALLS | 10 | Tool calls in progress of one identity on HTTPS; 0 disables the limit. See [call limits](#call-limits) |
| OPENSVC_MCP_MAX_CALLS_PER_MINUTE | 120 | Tool calls per minute of one identity on HTTPS, with a burst of 30; 0 disables the limit |
| OPENSVC_MCP_COLLECTOR_URL | empty | HTTPS URL of the collector API, such as `https://collector.example.com/api`, whose resources and skills the MCP serves; see [resources and skills](content.md) |
| OPENSVC_MCP_COLLECTOR_AUDIENCE | empty | Client ID of the collector, the audience of the exchanged tokens; required with the collector |
| OPENSVC_MCP_COLLECTOR_AUTH_PROFILE | empty | Profile of `OPENSVC_MCP_AUTH_CONFIG_FILE` exchanging the tokens of the callers for collector tokens; required with the collector |
| OPENSVC_MCP_COLLECTOR_CA_FILE | empty | Optional absolute PEM trust bundle of the collector, replacing the system roots |
| OPENSVC_MCP_ACTIONS | disabled | `enabled` registers the [action tools](tools/actions.md), which change the cluster state, on both listeners; `disabled` leaves them out |

The MCP has two optional listeners; configure at least one:

- **HTTPS** for external agents, enabled by the certificate and key. It always
  requires OAuth. Daemon calls use RFC 8693 token exchange for the clusters
  configured with `auth`. Without exchange, incoming OAuth and tool discovery
  work, but daemon calls remain blocked. `OPENSVC_MCP_LISTEN_ADDR`, the OAuth
  variables and `OPENSVC_MCP_MAX_*` are refused without HTTPS, and
  `OPENSVC_MCP_AUTH_CONFIG_FILE` without HTTPS nor a collector.
- **Unix socket** for OpenSVC components such as the AI agent, enabled by
  `OPENSVC_MCP_DELEGATED_SOCKET`. It requires the catalogue, with the OpenID
  provider of each cluster (`delegation`), but no confidential SSO profile,
  except with a collector, whose tokens it exchanges.

See [authentication](authentication.md) and [token delegation](delegation.md)
for the complete contracts.

## HTTPS

Install the listener certificate/key and any SSO TLS CA bundle before starting.
JWT verification uses the SSO public JWKS; no JWT signing private key is needed:

```bash
OPENSVC_MCP_LISTEN_ADDR=0.0.0.0:8443 \
OPENSVC_MCP_TLS_CERT_FILE=/etc/opensvc-mcp/tls/server.crt \
OPENSVC_MCP_TLS_KEY_FILE=/etc/opensvc-mcp/tls/server.key \
OPENSVC_MCP_OAUTH_RESOURCE_URL=https://mcp.example.com:8443/mcp \
OPENSVC_MCP_OAUTH_ISSUER=https://sso.example.com/oauth/opensvc-mcp/ \
OPENSVC_MCP_CLUSTER_CONFIG_FILE=/etc/opensvc-mcp/clusters.yaml \
OPENSVC_MCP_AUTH_CONFIG_FILE=/etc/opensvc-mcp/auth.yaml \
  ./bin/om3-mcp
```

Values are illustrative: use the actual issuer of MCP access tokens, which does
not select a daemon. The process reads its environment only and loads no
environment file itself. Omit both `OPENSVC_MCP_CLUSTER_CONFIG_FILE` and
`OPENSVC_MCP_AUTH_CONFIG_FILE` for discovery-only operation, which needs no
client secret. Optional variables are listed in the table above.

The agent connects to `https://<mcp-host>:8443/mcp`. The bind address must
contain an explicit IP and a numeric port from 1 to 65535. Use `0.0.0.0` or
`[::]` only when intentionally listening on all interfaces. The certificate
must cover the agent-facing hostname or IP; its CA must be trusted by the agent.
Include intermediate certificates in the listener certificate PEM as needed.

TLS 1.2 or later is mandatory. Configuration, public trust files and listener
TLS material are validated before binding; startup never contacts a daemon.
Settings and files are snapshotted once. Restart to apply changes.
HTTP headers are bounded to 64 KiB, authenticated MCP request bodies to 1 MiB,
header reads to 5 seconds and the read of a whole request, its body included,
to 10 seconds: a client sending it slowly is cut off. The processing and the
response of a request are bounded to 5 minutes, above the longest tool, so
that a client reading its response slowly is cut off too. Shutdown drains
requests for up to 30 seconds. These limits apply to both listeners.

### Call limits

The tool calls of each identity on HTTPS (token issuer and subject) are
limited: `OPENSVC_MCP_MAX_CONCURRENT_CALLS` calls in progress and
`OPENSVC_MCP_MAX_CALLS_PER_MINUTE` calls per minute, with a burst of 30 calls
for an idle identity. A call beyond a limit fails at once, before any token
exchange or daemon request, with a tool error that tells the client when to retry;
the audit line records it as `outcome=tool_error`. The limits protect the SSO
and the daemons from an authenticated client that loops, such as an agent repeating
a call. They are not a network protection: limits per address, before
authentication or for all clients belong to the reverse proxy, gateway or
firewall in front of the MCP, where every request otherwise carries the address
of the proxy. The Unix socket is not limited.

The limits are per identity: more users do not share them, but an identity
shared by several people or an automation does. The tools of this MCP target
one cluster and one node, so an LLM question makes a few calls; questions over
the whole fleet belong to oc3-mcp. Raise the limits for an identity whose
legitimate use exceeds them, and size the SSO and the daemons for the total
load of all users, which the limits do not bound.

## Health check

`GET /health` on the same HTTPS listener returns HTTP 200 with
`{"status":"ok"}`, `Content-Type: application/json` and `Cache-Control: no-store`.
`HEAD /health` also returns HTTP 200, without a body. Other methods return 405.
No bearer token or target headers are required. The route reports local server
liveness only: it does not contact daemons or establish user authentication,
tool permissions or cluster health. An unavailable daemon does not affect it.

For a service check, use the hostname covered by the listener certificate:

```bash
curl --silent --show-error --fail --max-time 3 \
  https://mcp.example.com:8443/health >/dev/null
```

Use `--cacert /path/to/public-ca.pem` when the MCP listener uses a private CA.

## Cluster catalogue and exchange profiles

Use [clusters.yaml](../deploy/examples/clusters.yaml) and, for external agents,
[auth.yaml](../deploy/examples/auth.yaml). Install real configuration outside
the repository, for example at `/etc/opensvc-mcp/clusters.yaml`.

```yaml
clusters:
  cluster-a:
    name: cluster-a
    cluster_id: 00000000-0000-4000-8000-000000000001
    endpoint: https://cluster-a-vip.example.com:1215
    auth:
      profile: customer-sso
      audience: opensvc-cluster-a
    delegation:
      issuer: https://sso.example.com/application/o/opensvc-cluster-a/
    tls:
      ca_file: /etc/opensvc-mcp/trust/cluster-a-tls-ca.pem
    request_timeout: 20s
```

- `clusters`: stable administrative references; these are not identities.
- `name`: human-readable name; it need not be unique.
- `cluster_id`: exact OpenSVC cluster ID; unique. It is the `cluster_id` tool
  argument on HTTPS and the `X-OpenSVC-Cluster-ID` header on the socket.
- `endpoint`: HTTPS origin of the cluster VIP, without credentials, path,
  query or fragment. No node inventory is required.
- `auth`: optional. `profile` names an entry of the profile file and
  `audience` the daemon token audience requested by token exchange. A cluster
  without `auth` is reachable only through the Unix socket, and is not listed
  to external agents.
- `delegation`: the OpenID provider of the daemon, required for every cluster
  when the Unix socket is enabled. `issuer` is its exact HTTPS issuer and
  `audience` its client ID, by default `auth.audience`, required without
  `auth`. The tokens of the socket are verified for them before they reach
  the daemon; see [token delegation](delegation.md). Copy both from the OpenID
  configuration of the daemon, which its public `GET /api/auth/info` shows
  (`openid.issuer`, `openid.client_id`); the MCP never queries that route.
- `tls.ca_file`: optional absolute public CA bundle path for daemon TLS.
  Omit `tls` for the system CA roots, for example with Let's Encrypt.
  An explicit bundle replaces, rather than extends, system roots.
- `tls.insecure`: optional boolean, default `false`; see below. It cannot be
  combined with `tls.ca_file`.
- `request_timeout`: required duration between `1s` and `2m`.

The profile file contains confidential client configuration and a secret-file
reference, never the secret itself. It accepts 1–64 profiles, HTTPS discovery
issuers, a confidential `client_id`, absolute `client_secret_file`,
`client_secret_basic` (default) or `client_secret_post`, up to 32 technical
scopes, a `request_timeout` from 1 second to 1 minute (default 10 seconds), and
optional `tls.ca_file`. SSO TLS verification cannot be disabled. Secret files
must be regular, at most 16 KiB, mode 0600 or 0400, and contain one nonempty
line. Unknown profile references prevent startup. Public discovery is lazy and
cached for five minutes.

The MCP verifies the tokens it accepts with the public JWKS of their issuer,
the OAuth issuer on HTTPS and the `delegation` provider on the socket; each
daemon verifies the tokens it receives again. Neither JWT public keys nor
private signing keys belong on the MCP host. The optional TLS CA bundle
validates only the daemon HTTPS certificate chain. With TLS verification
enabled, an IP endpoint works if its certificate covers that IP; a DNS-only
certificate requires an endpoint using that DNS name. Administrators must
ensure each endpoint belongs to the declared cluster. Adding a catalogue entry
does not register or modify a daemon.

See [the deployment guide](token-exchange.md) for SSO setup and daemon
configuration.

### Demo-only TLS bypass

For a demo with an untrusted certificate or a hostname/IP mismatch, set this
inside the target cluster entry, without `ca_file`:

```yaml
tls:
  insecure: true
```

**Strongly discouraged in production.** The connection remains encrypted, but
the daemon is no longer authenticated: an intermediary could steal the JWT,
forge daemon responses and impersonate a user through `whoami`. The MCP logs a
startup warning for each affected cluster. The default is `false`.

This applies to MCP-to-daemon requests, including `whoami`, only. It does not
change listener TLS, HTTPS-only endpoints, origin binding or JWT checks.

### Validation and bounds

The strict loader rejects unknown fields, duplicate keys, multiple documents,
invalid scalar types, duplicate cluster IDs and empty catalogues. Equivalent HTTPS origins are normalized. Cluster
references contain 1–64 ASCII letters, digits, hyphens or underscores. Names
contain 1–128 bytes, IDs and audiences 1–256 bytes, without surrounding
whitespace or control characters.

Limits are 4 MiB of YAML and 4096 clusters. Trust files must be readable regular
files of at most 1 MiB. TLS bundles accept only valid public CA certificates,
not leaf certificates. All files are loaded at startup; restart to apply
changes, which do not alter the running snapshot.

## Runtime account

Run `om3-mcp` under a dedicated unprivileged account, for example
`opensvc-mcp:opensvc-mcp`. It must be able to read the listener certificate/key,
catalogue, confidential secret file and public trust files. Restrict private-key access to that account
and use an unprivileged port such as `8443`. For the Unix socket, see the
directory and group setup in [token delegation](delegation.md#socket-access).

An OpenSVC `app.simple` resource can manage the process with the environment
variables above. The binary does not load an environment file itself and does
not require systemd, a local OpenSVC daemon, its binary, user objects or private
signing keys.
