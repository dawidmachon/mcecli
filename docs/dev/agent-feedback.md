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
