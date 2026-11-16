# Contract with the daemon API

The daemon API moves on, each cluster at its own version. This directory
says which part of it om3-mcp uses, and CI checks it against the API of the
day.

| File | What it is |
|---|---|
| `om3-api.yaml` | The API of opensvc/om3 this MCP follows, `daemon/api/api.yaml` at the commit of `om3-api.ref` |
| `om3-api.ref` | That commit |
| `routes.txt` | The operations the core calls, `METHOD /path`, as `om3-api.yaml` names them |
| `check.sh` | Compares `om3-api.yaml` with the API of om3 at a branch or a tag, on those operations only |

## How they are kept

- **`routes.txt` comes from the tests, not by hand.** The tests of
  `internal/core` record every request the core sends its fake daemon clients,
  and map it to an operation of `om3-api.yaml`. A request no operation
  matches fails the tests; so does a `routes.txt` that is not those
  operations. After a change of the routes the core calls:

  ```sh
  OM3MCP_UPDATE_ROUTES=1 go test ./internal/core/
  ```

- **`check.sh` runs in the Contract workflow**, on every pull request, on
  `main` and daily, against the `main` branch of om3, which fails the run,
  and against its latest release, which reports only. It fails on what
  `oasdiff breaking` calls an error for a client of those operations: an
  operation gone, a parameter now required, a response of another shape.

- **Following a change of om3**: adapt the code and its tests, then take its
  API as the new reference, in the same pull request:

  ```sh
  ref=$(gh api repos/opensvc/om3/commits/main -q .sha)
  curl -fsSL -o contract/om3-api.yaml "https://raw.githubusercontent.com/opensvc/om3/$ref/daemon/api/api.yaml"
  echo "$ref" > contract/om3-api.ref
  ```

## What it does not catch

It compares API descriptions. A requirement of the MCP the API never made,
such as a field the API declares optional and the MCP read as always there,
does not show: the tests and the reading of the MCP have to follow the API
as it is declared.
