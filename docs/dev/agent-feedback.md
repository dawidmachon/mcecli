# Agent feedback rounds — evaluation log

Agents run mcecli against real tasks and report friction. Every round is
**evaluated against the tool's vision before anything is implemented** —
not blindly. Dispositions: SHIPPED / DESIGNED (pending probe or owner go) /
DEFERRED / REJECTED (with rationale).

Rules for this log (and all dev docs):

- Report only platform-common findings. Numbers, names and identifiers
  observed on our testing instance (DE counts, BU counts, folder ids, call
  counts) are generalized to ranges or placeholders before they enter the
  repo — other users have their own instances and ours are confidential.
- Envelope contract is stable: behavior fixes must not rename fields.
- Any change to default behavior gets a wire-assertion test.

---

## Round 1 — pre-v0.2.1 (de create / query create batch)

Agent hit walls automating a DE+query pipeline. EVALUATED & SHIPPED:
`de create`, `query create|get|validate`, `query list --search/--limit`,
`de list` $pageSize transparency, `describe` additions, `users list`,
`folders --type`, `api --search`, per-command help.
DEFERRED: de list default projection curation; journey versioning
(documented only).
REJECTED: none.

## Round 2 — post-v0.2.1 (audit-driven gap batch)

Self-audit + agent report overlapped. SHIPPED: `de field add` (SOAP Update
is the only working path — REST PATCH silently ignores fields), `query
update`, `users list` completion, `folders --type` server-side filter,
`api --search` cross-section, helpTopics for all families.

## Round 3 — post-v1.0.0 field test (2026-09-25)

Agent ran an estate-scale audit; DE enumeration dominated call volume and
two default behaviors nearly produced silent wrong answers. EVALUATED:

1. **No full-DE enumeration** — `de list` is search-only ($search required,
   server caps pages at 25, ignores $pageSize). Estate-scale audits
   ("biggest DE", "schema audit", "orphan detection") need dozens of calls.
   → **SHIPPED** (same session, after live probe): `de list --all` — SOAP
   DataExtension Retrieve, ONE round trip for a full BU inventory
   (sub-second for a four-digit DE count vs dozens of REST calls).
   --category filters server-side; --search client-side; --limit caps
   loudly. Probe findings recorded in endpoint-notes.md: RowCount is NOT
   retrievable via SOAP on this object; CategoryID equals works as a
   server-side filter; MaxRows does not trim single-page responses.

2. **`auto health` silently truncated** — default `--limit 100` cut a
   larger report with `count` equal to returned rows and no hint: looks
   complete, isn't. For a command whose job is "what's failing", a silent
   cap can ship false answers. The report arrives as ONE response, so the
   cap was client-side and cost-free to remove.
   → **SHIPPED**: default is now the full report; `--limit` becomes an
   opt-in cap that is LOUDLY hinted when it truncates; counters coerced
   from CSV strings to JSON numbers.

3. **Hint contract lied for `de list --category`** — bare `de list` hint
   said "$search **or** categoryId", but the platform requires $search even
   when categoryId narrows results (AND, not OR). Sent the agent down a
   dead folder-id path.
   → **SHIPPED**: client-side fail-fast whenever --search is missing, with
   the truthful contract; help text corrected. Platform fact recorded in
   endpoint-notes.md.

4. **Sibling-command scope mismatch** — `auto list` = current BU context;
   `auto health` = estate-wide (all BUs under the enterprise account).
   Neither was flagged; agents reasonably assume both measure "here".
   → **SHIPPED**: usage text marks scope on both. No behavior change.

5. **Small stuff** — (a) health counters arrived as strings, every consumer
   casts → SHIPPED (numeric coercion); (b) `rowCount` on customObjects
   listings: semantics (fresh? cached? BU-shared?) unverified → documented
   as approximate in endpoint-notes.md, live probe pending; (c) validate
   hint still pointed at raw `rest POST automation/v1/queries` although
   `query create` exists → SHIPPED (hints now point to `query create`;
   `query` help text finally documents create/get/update).

Round-3 verdict: 5 shipped, 0 rejected.
Guiding lesson recorded: **defaults must not silently decide correctness
questions, and a hint must never state a contract the platform doesn't
honor.**

---

## Round 4 — v1.1 roadmap batch (2026-09-25)

First roadmap-driven iteration after round 3. Each v1.1 item was probed
live before implementation; two roadmap assumptions were corrected by the
platform itself:

1. **Per-recipient send status** — the roadmap guessed
   /messaging/v1/emailSends/{jobId}; that path 404s live with a real job id
   AND is absent from the messaging discovery index. The real path is
   GET /messaging/v1/jobs/{id}/stats/sends: items are
   {subscriberId, stats:[{id, transactionTime, domain}]}, a recipient's
   stats array carries one entry per send transaction, and the jobId
   namespace equals the _Sent.SendID (same id answers `dv sent --send-id`).
   Server pages items at 25 and ignores $pageSize.
   → **SHIPPED** as `dv recipients <jobId>`: flattened rows (one per
   transaction), $page walk until the server count is consumed, row caps
   and scan ceilings LOUDLY hinted (round-3 rule). The related
   emailstatstracking over-time family was probed: sends-kind verified,
   other kinds 404 on no-data (indistinguishable from missing) — left
   passthrough-only, **DEFERRED** until a real use shows up.
2. **`de list` default output curation** (roadmap) — the raw listing
   carries ~25 properties; audits almost always want the name/key/rowCount
   triad. → **SHIPPED**: lean default projection, `--full` for raw objects,
   `--fields` overrides both (combining --full and --fields is a usage
   error, not a silent precedence). Output-only change — the request wire
   is asserted unchanged.
3. **`query update` polish** (roadmap) → **SHIPPED** as `--diff`: a
   read-only dry run that GETs the current definition and reports
   field-by-field from→to before anything is patched. Mutually exclusive
   with --write/--confirm (a dry run that also wrote would be a
   silently-ignored flag). Also closed a test gap: query update shipped in
   round 2 with NO PATCH wire assertion — added, plus a gate re-check.
4. **Docs debt found while iterating** — the CHANGELOG lost its v1.0.0
   section in the history rebuild (top section still said "Unreleased
   (v0.2.1)"), and the usage text still claimed `query validate` needs
   --write (gate removed in v1.0.0). → **SHIPPED** (both fixed).

Round-4 verdict: 4 shipped, 1 deferred (tracking family), 0 rejected.
Lesson reinforced: probe the endpoint before designing the command — the
roadmap's endpoint guess was wrong, and the discovery index settled it in
one call.

---

## Round 5 — v1.1 roadmap completion (2026-09-25)

The three remaining v1.1 items, each probed live before implementation.
Two of three roadmap assumptions corrected by the platform:

1. **Journey version modeling** → **SHIPPED** as `journey list` +
   `journey versions <key>`. Probes: the interactions collection returns
   ONE item per key (newest version only) and ignores $pageSize (pages at
   50) and $filter; the version history is on the STATUS endpoint —
   /status/key:{key}?AllVersions=true (a bare call 400s with the teaching
   message "AllVersions=true or VersionNumber required"; VersionNumber=N
   narrows; unknown version → empty array, which must be hinted rather
   than mistaken for success). Lifecycle writes stay gated behind `rest`
   (sendout risk — same posture as round 1).
2. **Bulk ingest /hub/v1/async** → **CLOSED as specified**: the staged
   flow (create/stage/complete) does not exist in the platform's discovery
   index — data (93 methods) and hub (103 methods) audited. The working
   bulk path was already shipped as `de add` (data/v1/async rows). Hub's
   /hub/v1/dataeventsasync (row/rowset upsert + bulk delete) documented as
   passthrough-only — same package-scope caveats as the sync variant.
3. **SOAP passthrough redesign** → **SHIPPED** as `soap retrieve`. The
   removed first attempt allowed raw body passthrough (un-reviewable,
   un-gateable). The redesign keeps only the safe core: Retrieve is the
   only verb reachable — the request is built from structured flags by
   the shared soap package, so writes are inexpressible and the command
   needs no gate; --props required (no accidental full pulls);
   equals-only repeatable filters; loud caps; property typos surface the
   platform's teaching error pointed at the describe catalog. Wire
   assertions lock the RetrieveRequest-only shape.

Also genericized two live-instance numbers (subscriber id, send job id)
that had leaked into test fixtures pre-v1.0.0 — replaced with clearly
fake values.

Round-5 verdict: 2 shipped, 1 closed-by-probe, 0 rejected.
Lesson repeated from round 4: the discovery index is the cheapest way to
correct a roadmap guess before a line of code is written.

---

## Round 6 — completeness & quality audit (2026-09-25)

Question driving this round: "what's MISSING for a complete ops tool?"
Method: full pass over the coverage matrix + endpoint inventory +
discovery sections (data 93 / hub 103 / interaction / platform / asset
methods), then live probes for every candidate before any code.

Evaluated and dispositioned:

1. **Journey performance reads** — agents could see journey structure but
   not health. → **SHIPPED** `journey stats <key>`: key → journey id
   (stable across versions — probe finding) → GET activity summary + POST
   journeyhistory/summary population counters. The POST is a read query
   (validate precedent); the body is wire-asserted so it can never drift
   into a mutation. journeyhistory/search: works with an empty body but
   the filter schema is undocumented → **DEFERRED** passthrough-only
   (guessing filter shapes would be speculative).
2. **DE lifecycle asymmetry** — de create existed, no delete. → **SHIPPED**
   `de delete` (gated, key-resolved, undo image BEFORE the delete,
   journaled). Same rails for `query delete`. Live-fire deliberately not
   exercised this session — destructive operations need owner approval of
   the exact operation.
3. **Token identity debugging** — multi-profile setups raise "WHOSE token
   is this?" → **SHIPPED** inside auth test (tokenContext ids; best-effort
   read, failure never fails the command).
4. **Work-cache growth** — undo images and dumps accumulate silently →
   **SHIPPED** `work prune` (report default, --do deletes, cutoff flags;
   journal never touched; undo deletion explicitly warned about).
5. **Rejected/deferred after audit**: row-level DE delete (no REST path;
   hub dataeventsasync delete is scope-gated passthrough), asset-folder
   create (content folders only — DE/query folders have no REST path),
   AccountUser/Role/ExtractDefinition (not on ops roadmap, unchanged).
6. **Docs debt**: stale coverage row claimed /interaction/v1/
   eventNotification was P2 — the ENS API the tool covers is
   /messaging/v1/eventNotificationCallbacks (ens commands, verified);
   corrected.

Round-6 verdict: 5 shipped, 2 deferred, several rejected with rationale.
Lesson: a coverage matrix is only useful if stale rows are corrected the
moment they're noticed — a wrong "❌ missing" row costs a future session
the same probe twice.
