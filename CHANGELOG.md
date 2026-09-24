# Changelog

## Unreleased (v0.2.1)

### Added (agent field-testing round 2)
- `query list --full` — complete queryText in bulk (was: 80-char preview)
- `query list` now includes targetUpdateTypeName (Overwrite/Update) column
- `query list --pages N` — raise the paged-scan ceiling; count-change
  anomalies ("context switch signature") surface in the hint
- `auto <key> --expand-queries` — automation steps resolved to SQL text +
  target DE; activity objectTypeId labels (query/import/script/wait…)
- `auto <key> --deps` — input/output DE lineage (best-effort from SQL)
- `rest --query '$a=1&$b=2'`, `--page/--size`, `--all-pages` (NDJSON) —
  shell-safe query strings and loop-free bulk pulls
- `--jsonl` (global) — success payloads as newline-delimited JSON items

### Added (from agent field-testing round 1)
- `de create <name> --field "Col:Type(len)" --category ID --write` —
  curated DE creation; builds the complete field object the raw endpoint
  otherwise rejects one property at a time
- `query create <key> --text --target --category` — curated saved-query
  creation (queryText field, flat body shape)
- `query get <key>` — full definition incl. queryText (was unreachable:
  GET /{id} 404s on the key shown by list)
- `users list` — AccountUser reads (users parent ctx)
- `folders --type T` — DataFolder reads → categoryId discovery
  (ContentType filter works server-side)
- `api --search <keyword>` — cross-section discovery search
- `query list --search/--limit/--text` — kill the 291-row firehose;
  queryText previews on demand
- `de list` surfaces when the server ignores $pageSize

### Changed
- `query validate` no longer requires --write — it is provably
  side-effect-free; gate-tier corrected (agent feedback)

### Fixed
- query create error message surfacing (empty message swallowed raw body)

### Quirks documented
- create-time query validation requires target-DE PK/required columns in
  the SELECT ("Field 'AccountID' is required for the Target Data Extension")
- $search 404s for ~8s right after DE create (index lag)
- AccountUser.Delete / DataFolder.ParentFolderID: in docs, not retrievable

## v0.2.0 — released

### Added
- `mcecli dv sent|clicks|opens|bounces|unsubs|notsent|send` — data-view reads
  via SOAP event objects (SentEvent, ClickEvent, OpenEvent, BounceEvent,
  UnsubEvent, NotSentEvent, Send). Read-only, ONE call, server-side
  filters (--since 90m/24h/7d/date, --send-id, --subscriber-key),
  --limit with client-side cap (Opts.MaxRows stops ContinueRequest
  paging), --fields projection, teaching errors for wrong property
  names. `mcecli dv send <SendID>` = the _Job metadata join.
- `mcecli dv list` — objects + verified property sets
- `mcecli query validate` — pre-check SQL server-side without creating/
  running (POST /queries/actions/validate; field is Text, NOT queryText)
- journey lifecycle reads VERIFIED live (interactions/key:{key}?extras,
  eventDefinitions) — roadmap item closed
- `mcecli auto list|<key>|health` — automation operational reads: list with
  client-side --search/--status filters, detail with step activities,
  healthreport CSV→JSON (30DaySuccessRate/30DayErrorCount per automation)
- `mcecli describe <object>` — embedded SOAP object catalog (verified props,
  docs-only-not-retrievable props, quirks). SOAP Describe API is blocked
  on the reference org (empty responses, wire-dumped) — catalog compiled from
  live bisection instead
- `mcecli ens callbacks|subs` — event-notification webhook reads (tenant
  monitors automation started/errored via verified callback)
- `mcecli guc list` — global unsubscribe categories (9)
- `mcecli esd list|get` — email send definitions (SendDefinitionStatus not
  retrievable on tenant)
- `mcecli ts list|get` — triggered send definitions (read-only SOAP);
  statuses Active/Inactive/Deleted/Canceled; `get` scans client-side
  (server-side CustomerKey filter broken on tenant)
- `mcecli lists` + `mcecli lists members <subscriberKey>` — list inventory +
  per-subscriber memberships (ListSubscriber SubscriberKey filter works;
  ListID filter broken tenant-wide)
- `mcecli sub <key|email>` — subscriber operational lookups: subscription
  Status (Active/Bounced/Held/Unsubscribed), UnsubscribedDate, and
  ListSubscriber join (per-list membership status). Same address on
  multiple MIDs → all matches surfaced, never guessed. Quirk:
  Subscriber.ModifiedDate is NOT retrievable (docs list it)

### Hardening (review round 2)
- SOAP parse flattens PartnerProperties Name/Value pairs — SentEvent's
  SubscriberID was silently dropped before
- dv property sets bisected against official docs ON THIS TENANT:
  added EventType everywhere, ListID + TriggeredSendDefinitionObjectID
  (sent), BounceType + BounceCategory (bounces), IsMasterUnsubscribed
  (unsubs); docs overstate — BounceReason/UnsubscribeType/OptOut are
  NOT retrievable here and now documented as such
- refusals replace silently-ignored input: --subscriber-key on send,
  extra positional args, query run --target (start takes no body —
  target override needs a definition PATCH)
- --since accepts full RFC3339 with timezone offset

### Fixed
- query run: first isrunning check fires immediately (fast queries no
  longer wait a full poll interval) with a race guard — an isrunning=false
  right after start is not accepted as completion until the run was seen
  running or 20s grace passes
- dv/query envelopes: empty list results now serialize as data:[] instead
  of data:null (consistent shape for agents)
- query: key→queryDefinitionId resolve + `query list` now page past the
  server's 25-item cap (a many-query context hid page-2 rows)
- query: start endpoint takes an EMPTY body (JSON body failed silently
  live); start response status is now validated (fail fast, no poll)
- query: polling moved to /actions/isrunning (the definition endpoint
  carries no status field); run log checked for Error records
- SOAP: ComplexFilterPart operands now carry xsi:type and wrap their
  condition directly (official wire shape; bare alternation failed live)
  — also fixes multi-PK `de rows --where`

### Known quirks (documented in docs/dev/endpoint-notes.md)
- Send object + ComplexFilterPart → silent 0 rows on the reference org;
  `mcecli dv send <id>` therefore uses ID-only filter
- Query API /{id}/log stays empty on success; isrunning is the state source

## v0.2.0-rc.1 — release candidate (dev)

Accountability, diagnostics, and SQL-on-platform orchestration.
Quality gate at tag time: go vet clean, full -count=1 test suite green,
coverage: journal 84% / output 90% / auth 75% / httpc 75% / config 71% /
cli 50% / soap 51% (weakest = live-API paths, expected).

### Added
- `query list|run|status` — SQL-on-platform orchestration; resolves
  key→queryDefinitionId (Query API needs GUIDs); poll until complete;
  `run` is --write --confirm gated (writes target DE). Lifecycle
  verified live end-to-end.
- `de diff <key> --against dump.ndjson` — live-vs-dump drift check by PK
- `de find` — cross-BU DE search
- batched `de add` — NDJSON / @-stdin / array JSON, chunked 200 rows
- `md types|pull` — metadata family retrieves to per-item JSON + index
  (journeys, automations, queries, imports, dataExtensions, assets)
- `journal` — audit trail of every gated write (~/.mcecli/journal.ndjson)
- `undo list|show` — before-image snapshots auto-captured on DELETE
  (--no-snapshot to skip for bulk ops)
- `doctor` — self-diagnostic: config, credentials, tokens, caches
- `explain` — error knowledge base (17 live-probed patterns)
- final-429 Retry-After surfaced in httpc Result

### Fixed
- atomic token-cache writes (no torn files on concurrent refresh)
- SOAP ContinueRequest paging variable shadowing (paging stopped after
  one page) + guard test
- missing SOAP QueryAllAccounts wiring (silent param drop) + wire-echo
  guard test
- double-wrapped SOAP Filter element; duplicate ObjectType in body
- help text: duplicate `doctor` entry; `query`, `de diff`, `de find`
  missing from usage

### Changed
- SOAP filtered DE retrieve (`de rows --where`) marked NOT WORKING on
  this tenant: returns ok:true with 0 rows everywhere (evidence-dumped
  false negative on a 12.2M-row DE). Docs updated; Query Activity is the
  reliable server-side path.
- removed cmd_soap.go (SOAP passthrough; was committed in non-compiling
  state — redesign before re-adding)
- scoping model verified + documented: DEs/data strictly per-BU; the
  enterprise token does NOT see child-BU data
- SKILL.md consolidated (platform facts, SOAP findings, scoping)

## v0.1.0 — first functional release

Agent-first CLI for Salesforce Marketing Cloud: deterministic executor +
JSON envelope contract, designed for local AI agents (and humans).

### Core
- OAuth v2 server-to-server auth; per-MID tokens cached on disk
  (subdomain|mid|credential), auto-refresh, 401 retry-once
- Multi-BU profiles with named BUs; per-BU credential overrides;
  case-insensitive profile/BU names; env-var overrides (MCECLI_HOME isolates)
- Two-tier write gates: `--write` (content) / `--write --confirm`
  (DANGEROUS: sends, journey/automation lifecycle, DELETE, cleardata,
  definition overwrites) — refusals name the reason

### Commands
- `status` `use` `profile add|list` `bu add|list|discover` `auth test
  [--all-bus]`
- `de list|get|rows|add|dump|find` — curated, live-verified
  (customObjects metadata, token-paged rowsets, cross-BU search,
  cache-first NDJSON delivery)
- `asset search|pull` — delivery to local files
- `api <section>` — discovery-document explorer (10 sections, precached)
- `rest` — generic passthrough to any documented or undocumented endpoint
- `skill` `doctor`-style `status`/`auth test` diagnostics

### Notable verified platform facts (docs vs reality)
- token expires_in=1079 (not 1200); JWT payload carries enterprise MID
- SOAP auth: bare `<fueloauth>` header element only
- DE metadata: /data/v1/customObjects ($search REQUIRED)
- row writes: async only (/data/v1/async/dataextensions/key:{key}/rows,
  flat body); hub rowset POST is insert-only
- journeys live at /interaction/v1/interactions
- full details: docs/dev/endpoint-notes.md

### Safety
- strict key/name ambiguity refusal (never guesses the target DE)
- cross-host URL refusal; secrets never printed; local-only config
- scope advisories (informational), BU-visibility hints, teaching
  errors for profile/BU-like unknown commands
