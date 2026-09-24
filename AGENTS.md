# AGENTS.md — mcecli contributor guide

Single-binary Go CLI for Salesforce Marketing Cloud. Zero third-party
dependencies (stdlib only).

## Build / test / vet

```bash
go build -o mcecli.exe .
go test ./...        # unit + integration (httptest fakes, no network needed)
go vet ./...
go run . status      # smoke
```

Stamped build (version from git tag):

```bash
go build -ldflags "-X main.version=$(git describe --tags --always)" -o mcecli.exe .
```

## Hard invariants (tests enforce these)

1. **Write gates**: any non-GET requires `--write`; DELETE also `--confirm`.
2. **Envelope stability**: `ok,status,count,next,hint,error,data,context` — field
   renames are MAJOR semver events.
3. **Zero dependencies**: `go.mod` stays dependency-free.
4. **No secrets in output**: never print `client_secret` or `access_token`.
5. Every command prints a JSON envelope on stdout (except `--raw`, `skill`,
   `version`, `session use`) and exits: 0 ok / 1 API / 2 usage / 3 config.

## Change safety

- **Wire assertions**: when a command builds a request, a test must assert
  the actual request body/params (not only response handling). See
  `TestRetrieveBusinessUnitsSendsQueryAllAccounts`,
  `TestDeListCategoryParamReachesWire`.
- **Diff the wire first**: when live behavior contradicts expectations,
  dump the actual request (`MCECLI_REST_DEBUG` / `MCECLI_SOAP_DEBUG` env
  vars) before blaming the API.
- **No silently-ignored flags**: every defined flag must change behavior.
- Run `go vet ./...` + `go test ./... -count=1` before every commit.

## Branching

- `dev` — working branch. PRs target `dev`.
- `main` — released states only (merge from dev, tag `vX.Y.Z`).

## Multi-agent sessions

Multiple agents can run concurrently against different profiles/BUs from one
install. State isolation uses `MCECLI_SESSION`:

| Environment | State file | Use case |
|---|---|---|
| unset | `~/.mcecli/state.json` | single-agent, human use |
| `MCECLI_SESSION=agent-a` | `~/.mcecli/state.agent-a.json` | agent A |
| `MCECLI_SESSION=agent-b` | `~/.mcecli/state.agent-b.json` | agent B |

Session names sanitized: alphanumeric + `-` only (no path traversal).
Tokens are shared safely: cache keyed `subdomain|mid|client_id`, atomic
writes, 401 → auto re-mint. `MCECLI_NO_PROD=1` hard-refuses production-named
profiles in the default session.

## Layout

```
main.go                  entry; embeds SKILL.md
SKILL.md                 agent-facing contract (mcecli skill prints it)
internal/
  config/                profiles, BUs, state files (~/.mcecli)
  auth/                  OAuth v2 S2S, disk token cache
  httpc/                HTTP client, retries/backoff
  soap/                 generic SOAP Retrieve (OAuth token, stdlib xml)
  output/               envelope + --fields projection
  cli/                  commands (dispatcher + one file per area)
    cmd_*.go            one file per command family
    *_test.go           integration tests (httptest fakes)
docs/dev/
  endpoint-notes.md     empirical platform knowledge
  api-coverage.md       REST/SOAP coverage matrix
  sql-reference.md      validated SQL syntax battery
  tools/                corpus inventory scripts
```

## Adding a curated command

1. Probe the endpoint live (`mcecli rest GET ... --raw`); record findings
   in `docs/dev/endpoint-notes.md`.
2. Add handler in `internal/cli/cmd_*.go` (envelope, --fields, paging).
3. Add httptest fake + tests (success, error, write-gate).
4. Update `SKILL.md` and the `usage` text in `cli.go`.
5. When fixing a bug, add the failing test first.
