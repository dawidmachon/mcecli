# mcecli

Salesforce Marketing Cloud CLI for local agents — and humans.

`mcecli` turns SFMC into a deterministic, scriptable surface: every command
prints a stable JSON envelope (`ok/status/count/next/hint/error/data`) and
signals through exit codes (`0` ok, `1` API error, `2` usage, `3` config).
Built for LLM agents that need to *read* a Marketing Cloud org, audit it,
and only write when explicitly told to. Zero third-party dependencies
(pure Go stdlib).

## Install

### Download a ready binary

Static builds for Windows, Linux and macOS (amd64 + arm64) are attached to
every [GitHub release](https://github.com/dawidmachon/mcecli/releases/latest).
Download one, rename it to `mcecli` (Windows: `mcecli.exe`) and put it on
your `PATH`:

```bash
# macOS / Linux
chmod +x mcecli-v1.0.0-linux-amd64
sudo mv mcecli-v1.0.0-linux-amd64 /usr/local/bin/mcecli
```

```powershell
# Windows (PowerShell)
Move-Item mcecli-v1.0.0-windows-amd64.exe mcecli.exe
```

### With Go

Requires [Go](https://go.dev/dl/) 1.23+:

```bash
go install github.com/dawidmachon/mcecli@latest
```

### From source

```bash
git clone https://github.com/dawidmachon/mcecli && cd mcecli
go build -ldflags "-X main.version=$(git describe --tags --always)" -o mcecli .
```

Binaries are static — copy `mcecli` (or `mcecli.exe`) anywhere on `PATH`.

## Minimal setup

Create an installed package in SFMC (Setup → Installed Packages) with
**Server-to-Server** authentication, then:

```bash
mcecli profile add dev --subdomain <subdomain> --client-id <id> --client-secret <secret>
mcecli use dev
mcecli status          # verify
mcecli auth test       # mint a token, show scopes
```

That's it — reads work immediately. Writes require explicit gates (below).

## What it does

| Area | Commands | Notes |
|---|---|---|
| Data extensions | `de list/get/rows/dump/add/diff/find` | reads, NDJSON dumps, gated writes, drift checks |
| DE creation & schema | `de create`, `de field add` | complete field objects built for you; add columns to existing DEs (SOAP) |
| Data views (tracking) | `dv sent/clicks/opens/bounces/unsubs/notsent/send` | `_Sent`, `_Clicks`, `_Job`… via SOAP event objects — 1 read-only call |
| Subscribers | `sub <key\|email>` | status + list memberships |
| Lists | `lists`, `lists members <key>` | list inventory, per-subscriber memberships |
| Triggered sends | `ts list/get` | which definitions are Canceled/Inactive |
| Automations | `auto list/health/<id>`, `--expand-queries`, `--deps` | 30-day success/error rates; SQL audit; DE lineage |
| Queries (SQL on platform) | `query list/validate/create/get/update/run/status` | full lifecycle: validate server-side, create, run + poll; see [docs/sql-reference.md](docs/sql-reference.md) |
| Send definitions | `esd list/get`, `guc list` | user-initiated sends, global unsubscribe categories |
| Journeys | `md pull journeys`, `rest` | metadata + lifecycle reads |
| Assets | `asset search/pull` | binary download, CDN URLs |
| Webhooks | `ens callbacks/subs` | which events stream where |
| Users & folders | `users list`, `folders --type T` | platform users; folder ids for create commands |
| API exploration | `api <section>`, `api --search <kw>`, `rest` | discovery docs, cross-section search, generic passthrough |
| Introspection | `describe [object]` | verified SOAP object properties + quirks (offline) |
| Ops hygiene | `journal`, `doctor`, `explain`, `undo`, `session` | audit trail, self-diagnostics, error KB, snapshots, multi-agent |

Output modes: `--jsonl` (global) emits data items as newline-delimited
JSON for piping; `rest --all-pages` follows pagination automatically. All
envelopes carry `context` (profile/mid/bu/session) so every call is
self-describing — critical when several agents share one machine.

Write safety is structural: **any non-GET needs `--write`**; dangerous
operations (DELETE, sends, lifecycle) additionally need `--confirm`.
Deletes auto-snapshot a before-image (`mcx undo`). PROD-named profiles
warn loudly; `MCECLI_NO_PROD=1` hard-refuses them.

## Multi-agent

`mcx use`-style state is per-session. Run isolated agents from one install:

```bash
MCECLI_SESSION=agentA mcecli use dev bu1
MCECLI_SESSION=agentB mcecli use prodread prod
```

Tokens are shared safely (keyed per `subdomain|mid|client_id`, atomic
writes, auto re-mint on 401).

## SQL on the platform

Query Activities run server-side SQL against data extensions and data
views. A 64-construct syntax battery was validated live on a real org —
see [docs/sql-reference.md](docs/sql-reference.md) for what works
(joins, UNION, subqueries, window functions) and what is rejected
(no variables, single statement only).

## Documentation

- [docs/sql-reference.md](docs/sql-reference.md) — validated SQL syntax for Query Activities
- [docs/dev/api-coverage.md](docs/dev/api-coverage.md) — REST/SOAP surface covered vs planned
- [docs/dev/endpoint-notes.md](docs/dev/endpoint-notes.md) — endpoint-by-endpoint live findings and quirks
- [docs/dev/endpoint-inventory.md](docs/dev/endpoint-inventory.md) — full corpus inventory (147 REST + 88 SOAP objects)
- `mcecli skill` — the full agent skill document (embedded in the binary)
- [CONTRIBUTING.md](CONTRIBUTING.md) — how to contribute (PRs target `dev`)
- [AGENTS.md](AGENTS.md) — full development guide (human or AI)

## License

[MPL-2.0](LICENSE). Commercial products may be built on top of it, but
mcecli's own source files (and any modifications to them) must remain
available under MPL-2.0 — the underlying source stays visible. Keep the
attribution (see [NOTICE](NOTICE)).
