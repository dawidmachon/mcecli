# mcecli — Salesforce Marketing Cloud CLI (agent skill)

`mcecli` is a pre-configured local CLI for reading and changing a Salesforce
Marketing Cloud instance over its REST API. Credentials and multi-BU profiles
already live in `~/.mcecli/` — never ask the user for credentials, never print them.

## Response contract (learn once, rely forever)

Every command prints one JSON envelope on stdout:

    ok      {"ok":true,"status":200,"count":3,"next":"2","data":...}
    error   {"ok":false,"status":404,"error":{"message":"..."},"hint":"..."}

- `hint` tells you how to fix your call. Read it; do not guess.
- `next` is the continuation cursor — pass it back as `--page <next>`.
- Exit codes: `0` ok · `1` API/network error · `2` usage error · `3` config error.

## Piping & bulk output

- `--jsonl` (global): success payloads emit one JSON object per line
  (data items only) — piping without jq
- `mcecli rest GET <path> --all-pages` — follows $page, emits items as NDJSON
- `mcecli rest GET <path> --query '$pageSize=500&$page=2'` — shell-safe query strings
- `query list --full` — complete queryText (bulk SQL audit in one call)
- `auto <key> --expand-queries` — automation steps → SQL + target DE
- `auto <key> --deps` — input/output DE lineage (best-effort)

## Multi-agent operation (single install, many concurrent agents)

`mcecli use` state is PER-SESSION:
- `MCECLI_SESSION=agentA mcecli use dev-full bu1` → state.agenta.json
- `MCECLI_SESSION=agentB mcecli use prod-read prod` → state.agentb.json
- sessions never affect each other; unset MCECLI_SESSION = default state.json
- `mcecli session` lists sessions; `mcecli session show` current; `mcecli session use <n>` prints the export line
- session names sanitized to [a-z0-9_-] (no path traversal)
Tokens are SHARED and safe: cache keyed subdomain|mid|client_id, atomic
writes, 401 → auto re-mint. PROD guard: selecting a prod-named profile
prints ⚠️ on stderr; MCECLI_NO_PROD=1 hard-refuses it. Stateless alternative:
pass --profile X --bu Y per call.

## First call of a session

    mcecli status        # which profile / BU am I on? (no network)
    mcecli profile list  # profiles are NOT commands — they are selected via use/--profile
    mcecli bu list       # configured business units
    mcecli bu discover [--add]   # full BU hierarchy + accessible BUs (auto-populates with --add)
    mcecli use <profile> <bu>   # set current BU (tokens are scoped per MID)

BU discovery needs a package with PARENT-ACCOUNT access for full coverage.
With a BU-scoped package it returns only that BU — the tool says so in the
hint; treat it as 'limited visibility', never as the full estate.

Per-call override instead of switching: `mcecli --bu <name|MID> de list`

## Reads (always safe)

    mcecli api data --filter customobject      # EXPLORE the API: discovery-based method index
    mcecli api messaging getMessageSendsCollection   # method detail + ready mcecli rest line
    mcecli de list --search preference         # $search is required by the API
    mcecli de get <deKey>                      # definition + field schema
    mcecli de rows <deKey> --fields email --size 50
    mcecli de rows --next "data/v1/customobjectdata/token/.../rowset?$page=2"
    mcecli de find <key|name>                  # which BU has this DE? (searches all contexts)
    mcecli de dump <deKey> [--max-age 30m]     # ALL rows -> NDJSON file (greppable; cache-first)
    mcecli asset search --name footer          # find assets; --pull downloads bodies too
    mcecli asset pull <id>                     # one asset: meta.json + content file
    mcecli rest GET <any/path/on/rest/host>    # documented OR undocumented endpoints

Keep responses small (costs tokens): use `--fields a.b.c` projection, `--size N`
caps, and follow `next` (token-based continuation path) when present. `--raw`
skips the envelope when piping. Ten API sections publish self-describing
discovery docs — prefer `mcecli api <section>` over guessing paths.

## Writes — HARD RULE: explicit human approval first

Two tiers, both requiring the user's approval of THAT exact operation first:

- **Tier 1 `--write`** — content writes: create/update DEs, rows, assets.
- **Tier 2 `--write --confirm`** — DANGEROUS, hard-gated: anything that SENDS
  messages (`*/send`), changes journey/automation runtime state (`stop`,
  `start`, `pause`, `resume`, `publish`, `unpublish`, `cancel`, `schedule`,
  `run`, `execute`), destroys or clears data (`DELETE`, `cleardata`), or
  overwrites whole journey/event/message definitions (PUT/PATCH).

NEVER pass `--write`, and above all never `--write --confirm`, without the
user's explicit approval. The tool names the reason when it refuses.

    mcecli rest POST   data/v1/async/dataExtensions/{guid}/rows --write --body @rows.json
    mcecli rest DELETE <path> --write --confirm          # destructive
    mcecli rest POST   interaction/v1/interactions/{id}/stop --write --confirm   # stops a LIVE journey

Verified write patterns on this platform:
- Create DE: POST data/v1/customObjects (needs categoryId + FULL field objects;
  set an explicit `key` or the platform assigns a GUID one)
- Insert rows: POST data/v1/async/dataExtensions/{GUID-id}/rows --write
- Upsert rows: PUT  data/v1/async/dataExtensions/{GUID-id}/rows --write
  (body is FLAT: {"items":[{"RowId":"1","Note":"x"}]} — no keys/values split;
  async 202; resolve GUID via: mcecli de list --search <name> --fields id,key)
- DE key vs name: row reads/writes resolve by customerKey — if a name-only DE
  404s, get its key with `mcecli de list --search <name> --fields name,key`

## Writing DE rows — prefer the curated command

    mcecli de add <key|name> --data '{"RowId":"row-9","Note":"hi"}' --write

- Body is ONE FLAT row (field names exactly as in `mcecli de get <key> --fields name`).
- Semantics: UPSERT (async 202; may take a few seconds to appear in reads).
- Accepts customerKey OR name (auto-resolves to the internal id).
- Raw equivalents + sync-insert variant: `mcecli help rest` (verified recipes).
- Field names are case-sensitive; wrong names → 400 with a "check field names" hint.

## Recipe: recently changed assets/templates (last N hours)

    mcecli rest GET "asset/v1/content/assets?$filter=modifiedDate greaterThan '<RFC3339 UTC>'&$orderBy=modifiedDate desc&$fields=id,name,assetType,modifiedDate"
    # combine with: $filter=assetType.name like 'template' and modifiedDate greaterThan '...'
    # for template bodies: mcecli asset pull <id>  (writes body.html)

`$filter` supports: like / equal / greaterThan / lessThan, `and`-combinations;
`$orderBy=modifiedDate desc` works. Use RFC3339 with timezone offset.

## Platform quirks that cost agents time (learned live)

- DE visibility follows the token's account context and is NOT uniform:
  the same DE can be visible from one BU and absent from another, and a
  limited profile scoped to a parent can see MORE than a broad profile at
  its home BU. Never assume; check `mcecli status` (where am I), and
  `mcecli bu discover` (what can these credentials reach — SOAP accounts +
  contacts-schema accessible BUs). `mcecli bu discover --add` populates
  config from that report (contacts-only MIDs become `bu-<mid>`).
- Profile and BU names are case-INSENSITIVE (case-insensitive matching).
- `bu discover` contacts section is a HINT about reachable BUs, not a
  guarantee of full scope: broad credentials may still show only their
  home BU there. Validate access by calling, not by the list.
- `--body @-` reads stdin on Windows; inline JSON also works for small bodies.
- Path templates from `mcecli api` are case-sensitive and use colon forms
  (`key:VALUE`) — copy them exactly.
- Scope mismatches produce 403 on REST ("Insufficient privileges") with
  an advisory hint naming the missing scope. SOAP may return 200 with 0 rows
  instead of 403 (more permissive). Token scope changes require --refresh
  to take effect (cached tokens keep old scopes until expiry).
- Row reads have NO server-side filter via REST; for huge DEs use
  `mcecli de rows <key> --where "Field=value"` (SOAP server-side filter, works on
  millions of rows in <1s). For broader analysis, `mcecli de dump` and grep locally.
- Data views (_Click, _Open, _Sent, _Bounce) are SOAP-only system DEs —
  NOT accessible via the REST customobjectdata API. Read them with
  `mcecli dv sent|clicks|opens|bounces|unsubs|notsent` (SOAP event objects,
  read-only, server-side filters). `mcecli dv send <SendID>` gives the _Job
  metadata (EmailName/Subject/FromName) — the _Sent JOIN _Job pattern.
- Debugging a weird result? MCECLI_REST_DEBUG=<path> / MCECLI_SOAP_DEBUG=<path>
  dump the last raw request (auth redacted).

## Which SFMC data API to use (decision table)

| Task | Use | Why |
|---|---|---|
| read a few rows | `mcecli de rows <key> --size N` | sync, cheap, projected |
| scan/analyze many rows | `mcecli de dump <key>` then grep locally | data stays out of context |
| locate a DE across BUs | `mcecli de find <name>` | DEs are per-BU contexts |
| recently changed assets/templates | see recipe below | asset API supports date filters + orderBy |
| insert/update ONE row | `mcecli de add <key> --data '{...}' --write` | async upsert, idempotent |
| bulk update known rows | `mcecli rest PUT data/v1/async/dataextensions/key:{key}/rows` with many items | batched async |
| filter rows in huge DE (100k+) | `mcecli de rows <key> --where "Field=value"` — SOAP server-side filtering | works on millions of rows in <1s; field names case-sensitive; use exact schema field names from `mcecli de get` |
| filter journeys by name | NOT supported server-side — /interactions ignores $filter | use mcecli de find / de list --search instead |
| webhook monitoring | `mcecli ens callbacks` + `mcecli ens subs <id>` — which platform events stream where (tenant monitors automation started/errored) — VERIFIED live | writes gated via rest |
| global unsubscribe categories | `mcecli guc list` — enterprise unsubscribe categories (platform defaults + org config) | |
| email send definitions | `mcecli esd list|get` — user-initiated send setup (SendDefinitionStatus not retrievable) — VERIFIED live | |
| create a data extension | `mcecli de create <name> --field "Col:Text(100)" --field "Created:Date" --category <folderID> --write` — builds the COMPLETE field object (raw endpoint rejects incomplete ones one property at a time) | folder ids: `mcecli folders --type dataextension` |
| create a saved query | `mcecli query create <key> --text "SQL" --target DE --category <id> --write --confirm` | field is queryText; validate first: `mcecli query validate` (side-effect-free, no gate) |
| inspect a saved query | `mcecli query get <key>` — full definition incl. queryText (GET /{id} 404s on the key — resolved internally) | |
| list platform users | `mcecli users list --search X` — all platform users, client-side search | |
| find folder ids | `mcecli folders --type dataextension|queryactivity` — ContentType server-side filter works | |
| find endpoints by keyword | `mcecli api --search <keyword>` — cross-section discovery search | |
| triggered sends (silently not going out?) | `mcecli ts list` / `mcecli ts get <key>` — TriggeredSendStatus Canceled/Inactive/Deleted = NOT sending (VERIFIED live; IsPaused not retrievable) | |
| lists / who-is-on-what | `mcecli lists` (list inventory) + `mcecli lists members <subscriberKey>` (per-subscriber memberships) — VERIFIED live | server-side ListID filter unreliable on some orgs; per-list membership = full scan, on demand |
| automations ops (what is failing?) | `mcecli auto health` (30-day success/error per automation) → `mcecli auto list --search` → `mcecli auto <key>` (steps) — VERIFIED live | start/stop writes stay gated |
| SOAP object properties | `mcecli describe <object>` — embedded catalog of live-verified properties (SOAP Describe is not usable on every org) | offline, zero cost |
| subscriber state / list memberships | `mcecli sub <key|email>` — VERIFIED live: Status (Active/Bounced/Unsubscribed/Held) + ListSubscriber join; same address can exist on multiple MIDs → command surfaces ambiguous_matches, re-run with exact SubscriberKey | use `mcecli dv bounces --subscriber-key K` for bounce reasons |
| data views (_Click, _Sent, _Open, …) | `mcecli dv sent|clicks|opens|bounces|unsubs|notsent` — VERIFIED live (1 SOAP call, server-side --since/--send-id filters); `mcecli dv send <SendID>` = _Job metadata | REST rowset NEVER reaches data views (404); use `mcecli query run` ONLY for SQL-only needs (aggregates, joins, _Subscribers, bulk) |
| SQL syntax reference | docs/sql-reference.md — 64-construct battery validated live via query validate (2026-09-17): joins/UNION/subqueries/EXISTS/ROW_NUMBER work; single statement only, no DECLARE/INTO/EXEC | always mcecli query validate before create |
| SQL on platform | `mcecli query list` / `mcecli query run <key> --write --confirm` / `mcecli query validate --text "SQL" --target DE --write` — VERIFIED end-to-end (validate pre-checks SQL: field is Text, NOT queryText) | key→GUID resolved across ALL pages |
| millions of rows / imports | Bulk Data Ingest API or ImportUserBehavior — OUT of mcecli scope; tell the user | wrong tool otherwise |
| DE definitions | `mcecli de list/get` (customObjects) | metadata API |
| account/BU hierarchy | `mcecli bu discover` (SOAP + contacts/v1/schema) | discover also reports enterprise_id + accessible BUs via REST |
| bulk imports (huge volumes) | OUT of mcecli scope — /data/v1/bulk/ingest* exists; tell the user | wrong tool otherwise |

NEVER chain mutations without reading results. If `de add` reports async 202,
verify with `mcecli rest GET data/v1/async/{requestId}/status` before claiming success.

## Ambiguity policy (strict on purpose)

DE arguments are customerKeys; names are accepted ONLY when exactly one DE
matches. If `<arg>` is the name of one DE and the key of another, mcecli REFUSES
with both listed — resolve manually. This prevents silent wrong-DE writes.

## Rate limits (handled by the tool — don't fight it)

Token caching (one token / ~20 min per MID) and 429/Retry-After backoff are
built in. If you still see 429: stop retrying in a loop, wait, reduce page
sizes. For long dumps use `mcecli de dump` (few large pages) not many small reads.

## Endpoint families (paths relative to the REST host)

    data/v1/customobjectdata/...    data extensions, rowsets
    asset/v1/content/...            content blocks / assets
    interaction/v1/...              journeys, events
    automation/v1/...               automations, queries, activities
    hub/v1/...                      contacts, campaigns
    sms/v1/..., messaging/v1/...    SMS & messaging
    push/v1/...                     push

Official docs cover only part of the surface; unknown paths are normal — probe
read-only first: `mcecli rest GET <path>` and inspect the envelope/error.

## Rollback — DELETE captures a before-image automatically

Every DELETE snapshots the current resource to
`~/.mcecli/work/<profile>/undo/<stamp>-DELETE/` (request + response JSON).
The envelope note (stderr) and `mcecli journal` point to it.

    mcecli undo list [--profile P]    # snapshots, newest first
    mcecli undo show <stamp>          # saved request + response JSON

Rollback is MANUAL (never auto-executed): re-create the resource from the
saved JSON with gated commands (mcecli rest POST ... --write). DELETE of DE
definitions restores via re-POST of the saved definition JSON.

## Writes are audited — every gated write is journaled

    mcecli journal --last 20         # recent writes: ts, profile/BU, method, URL, status
    mcecli journal --profile dev --json

Entries record WHO (profile/BU), WHAT (method+URL), and the RESULT (status,
request id) — request bodies are intentionally not stored (privacy). Use this
to answer "what did the agent change" — do not re-query SFMC for the same.

## Diagnostics

    mcecli doctor                    # config, credentials, tokens, caches, journal — one shot
    mcecli doctor --all-profiles     # also mint a token for every profile
    mcecli doctor --offline          # skip network checks

## Self-help

    mcecli help            # command list
    mcecli help <command>  # extended help (rest, de, use)
    mcecli skill           # this document
    mcecli auth test       # verify credentials + show scopes
    mcecli auth test --all-bus   # validate account + every configured BU pairing
    mcecli auth test --bu -      # validate the account-level token explicitly

## Config (do not edit unless asked)

`~/.mcecli/config.json` profiles: `{subdomain, client_id, client_secret,
bus:{name: mid}}`. A BU may carry its own credential pair (packages scoped
to one BU): `{name: {mid, client_id, client_secret}}` — mcecli picks it
automatically for that BU and says `"creds":"per-BU"` in status.
`~/.mcecli/state.json`: current profile/BU. `~/.mcecli/tokens.json`: cached OAuth
tokens keyed per subdomain+MID+credential (auto-refreshed; 401 retries once).
