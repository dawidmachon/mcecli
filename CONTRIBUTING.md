# Contributing

PRs welcome. **Target the `dev` branch** — `main` only receives released
states.

## Dev setup

Go 1.23+. No third-party dependencies — stdlib only.

```bash
go build -o mcecli.exe .
go test ./...
go vet ./...
go run . status      # smoke
```

Install locally: `go build -o "$HOME/go/bin/mcecli.exe" .`

## Workflow

- `dev` is the working branch. Open PRs against `dev`.
- `main` holds released states (squash-merge from dev, tag `vX.Y.Z`).
- Conventional Commits: `feat:`, `fix:`, `docs:`, `test:`, `chore:`.
- Pre-1.0: breaking changes bump MINOR (`0.x.y`); bug fixes bump PATCH.
- Version source of truth: git tags stamped at build time via
  `-ldflags "-X main.version=$(git describe --tags --always)"`.

## Hard invariants

Tests enforce these — review will catch violations.

1. **Write gates**: any non-GET requires `--write`; DELETE also `--confirm`.
2. **Envelope stability**: `ok,status,count,next,hint,error,data,context` —
   field renames are MAJOR semver events.
3. **Zero dependencies**: `go.mod` stays dependency-free.
4. **No secrets in output**: debug dumps redact tokens; no `client_secret`
   or `access_token` in output.
5. Every command prints a JSON envelope on stdout (exceptions: `--raw`,
   `skill`, `version`, `session use`) and exits: 0 ok / 1 API / 2 usage / 3 config.

## Testing

- Unit tests per package + integration tests in `internal/cli` using
  httptest fakes (no network). `go test ./...` must pass before every commit.
- When fixing a bug, add the failing test first.
- Wire-shape changes (request body/params) need wire-assertion tests.

## Docs

- `SKILL.md` is embedded in the binary (`mcecli skill`) — keep it aligned
  with the command surface.
- `docs/dev/endpoint-notes.md` holds live-verified platform findings.
- `docs/dev/api-coverage.md` — REST/SOAP coverage matrix.
- `docs/dev/sql-reference.md` — validated SQL syntax battery.
- `docs/dev/tools/` — corpus inventory scripts (used to generate coverage maps).
