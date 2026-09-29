# Endpoint notes (empirical)

Rule: every endpoint used by a curated command gets verified against a live
instance before we trust it. Official docs are often wrong about parameter
names, pagination and payload shapes — record observed reality here.

Live instance: a dev sandbox org (subdomain <subdomain>), package
on BU 111111114 "<dev-bu>" under enterprise 111111111. Probed 2026-09-10.

Status legend: UNVERIFIED (planned, not probed) · VERIFIED (probed live,
shape encoded in tests) · BROKEN (docs wrong, workaround noted) ·
RANKING (exists but not the right tool for this workload).

## Full REST API reference landscape

Discovered via `GET /{section}/v1/rest` (self-describing discovery docs):
each section answers its own `.../v1/rest` with a method index.

```
Section                 Discovery path              Title
──────────────────────────────────────────────────────────────────────────────
data                    /data/v1/rest              Data Extensions
asset                   /asset/v1/rest             Asset Service
automation              /automation/v1/rest         Automation Studio
interaction             /interaction/v1/rest        Journeys and Events
hub                     /hub/v1/rest               Hub (REST APIs)
messaging               /messaging/v1/rest          Messaging Service
push                    /push/v1/rest              Push Service
platform                /platform/v1/rest           Platform
email                   /email/v1/rest              Email Service
sms                     /sms/v1/rest               SMS Service
──────────────────────────────────────────────────────────────────────────────
Plus (not self-describing, from summary.md reference list):
bulk_ingest            —                         Bulk Data Ingest API
campaigns               —                         Campaigns
contacts                —                         Contacts
import_job_api          —                         Data Extension Imports
data_extension_rows_async —                        DE Rows (Asynchronous)
data_extension_rows_sync   —                        DE Rows (Synchronous)
transactional_messaging_email —                    Transactional Messaging - Email
transactional_messaging_push  —                     Transactional Messaging - Push
transactional_messaging_ott   —                    Transactional Messaging - OTT
transactional_messaging_sms   —                    Transactional Messaging - SMS
event_notification       —                         Event Notification
file_transfer_locations  —                         File Transfer Locations
security                 —                         Security
nested_tags (objects)    —                         Nested Tags
rest_permission_scopes   —                         REST API Permission IDs and Scopes
seedlist                 —                         Seed List
workflowteams            —                         Workflow Teams
chat_messaging           —                         Chat Messaging
domain_verification      —                         Domain Verification
```

## Auth

| Endpoint | Status | Notes |
|---|---|---|
| POST {auth}/v2/token | VERIFIED | account_id numeric; **expires_in=1079** (docs say 1200); scope list in response; rest/soap instance URLs returned |
| Token account scoping | VERIFIED | token authorizes ONLY the account it was minted for; no flow down/up (official docs confirmed live) |
| "Not Authorized" signal | VERIFIED | returned when token lacks the required scope for an operation. NOT a path error — the endpoint exists and is correct; the token's permissions are insufficient. Do NOT retry the same call expecting a different result. |

## SOAP

| Subject | Status | Notes |
|---|---|---|
| Auth: OAuth token | VERIFIED | token as BARE `<fueloauth>TOKEN</fueloauth>` element in SOAP Header (not HTTP header, not wsse:Security). WSSE gate requires exactly this form. |
| Token account scoping | VERIFIED | token authorizes ONLY the account it was minted for; no flow down/up |
| Retrieve Account | VERIFIED (behavior) | returns ONLY the calling context (1 row), even from ENTERPRISE_2 root; RetrieveAllAccounts=true ignored; SimpleFilter ParentID=enterprise → empty. Full-tree discovery NOT possible with BU-scoped package; climb via ParentID chain |
| Full tree: BusinessUnit + QueryAllAccounts=true | VERIFIED | from an ENTERPRISE-scoped token returns the complete hierarchy (all BUs incl. names/types/parents). ROOT-CAUSED: an earlier "unstable" reading was OUR BUG — the extra param (QueryAllAccounts) was silently dropped from the envelope during a refactor, degrading discovery to the calling context. Regression test locks the request body. Works for both broad and enterprise-sourced credentials; limited (no-enterprise) credentials get a graceful auth error |
| Response shape | VERIFIED | `<Results xsi:type="Account">` with UNPREFIXED children (default partnerAPI ns); `<OverallStatus>` sibling of Results |
| Paging | UNVERIFIED | ContinueRequest not yet needed/implemented |

## SOAP DE row retrieve — ROOT-CAUSED + FIXED (2026-09-27, round-8)

`RetrieveRequest` with `ObjectType=DataExtensionObject[KEY]`, SimpleFilterPart
→ returned OverallStatus=OK with 0 Results on EVERY DE. ROOT CAUSE
(isolated by controlled matrix — filter casing × column list):

**`<tns:Properties>*</tns:Properties>` (the wildcard) returns 0 results.**
With an EXPLICIT column list the identical request returns rows; filter
property casing is irrelevant (lowercase schema names work). Every earlier
"known broken" test inherited the wildcard from a shared helper — a
false-negative chain, not a platform defect.

Additional hardening (docs-listed, kept): `<tns:Client><tns:ClientID>`
(DE-owning MID, or JWT enterprise id for account-level contexts) is now sent
on DE retrieves. WS-Addressing headers were NOT needed.

mcecli now resolves the column list from the fields API and never sends the
wildcard (`de rows --where`, `de add` before-images, `de diff`).

### DOC-SOURCED facts (sf-docs-scrap, NOT yet validated live)
| Fact | Source |
|---|---|
| max 2,500 records per RetrieveRequest on DataExtensionObject | retrieving_data_from_a_data_extension.md |
| LIKE operator NOT supported in DE retrieve filters | retrieving_data_from_a_data_extension.md |
| Date columns round to nearest second + AM/PM → never use Date as PK | retrieving_data_from_a_data_extension.md |
| official sample sends fueloauth + a:Action/a:To headers, NO Client block in body | retrieving_data_from_a_data_extension.md |

## Journey lifecycle reads + Query validate — VERIFIED LIVE 2026-09-16

| Endpoint | Status | Notes |
|---|---|---|
| GET /interaction/v1/interactions/key:{key}?extras=activities | VERIFIED | journey detail: id/key/name/status; `key:` prefix form works; extras=activities accepted |
| GET /interaction/v1/eventDefinitions?$pageSize=2 | VERIFIED | {count,page,items}; this context: a small number of definitions |
| POST /automation/v1/queries/actions/validate | VERIFIED | body field is **Text** (NOT queryText — create uses queryText!); + targetKey + targetUpdateTypeId 0 (categoryId optional) → {queryValid,errors[],warnings[]}; bogus view → real server error text. Now `mcecli query validate` |

Property-set reality vs docs (probed by bisection 2026-09-16):
- SentEvent +: EventType, ListID, TriggeredSendDefinitionObjectID, Client.ID (Client.ID returns empty via direct-child parse)
- BounceEvent +: BounceType, BounceCategory, EventType (BounceReason NOT retrievable — docs list it)
- UnsubEvent +: IsMasterUnsubscribed, EventType (UnsubscribeType, OptOut, ListID NOT retrievable)
- OpenEvent/NotSentEvent +: EventType
- SentEvent returns SubscriberID inside PartnerProperties pairs → mcecli parse flattens them into the row

## Query API (automation/v1) — VERIFIED LIVE (dev org, account-level token)

Full lifecycle proven: create DE → create query → start → poll → 4 rows in target.

| Subject | Status | Notes |
|---|---|---|
| GET /automation/v1/queries | VERIFIED | $pageSize server-CAPPED at 25 ($pageSize=400 → 25); $page iterates; count = context total (all queries in context). mcecli pages via queryFetchAll |
| POST /automation/v1/queries (create) | VERIFIED | body is FLAT: name, key, description, **queryText** (NOT "text"), targetKey, targetDescription, targetUpdateTypeId (0=Overwrite), categoryId REQUIRED. Nested {target:{...}} → HTTP 500; wrong text field name → 400 naming the expected field |
| POST /data/v1/customObjects (create DE) | VERIFIED | categoryId REQUIRED (folder id — grab from an existing DE's raw record); fields must be COMPLETE objects (mirror a real field: type,length,isNullable,isPrimaryKey,isHidden,isInheritable,isOverridable,isReadOnly,isTemplateField,mustOverride,ordinal,storageType,maskType,description) |
| POST /{qid}/actions/start | VERIFIED | **EMPTY body required** — JSON body ({targetKey}) silently fails to run; empty body → 200 `"OK"`. Target override happens on the DEFINITION (PATCH), not at start |
| GET /automation/v1/queries/{id} | VERIFIED | definition carries NO status field at all |
| GET /{qid}/actions/isrunning | VERIFIED | {"queryDefinitionId","isRunning"} — the poll endpoint |
| GET /{qid}/log | VERIFIED (quirk) | empty on SUCCESS on the reference org; error-shape still unobserved — treat empty as success, surface items when present |
| data views in SQL | VERIFIED | `_Sent` reachable in BU SQL context; platform wraps queryText with `C{MID}.` prefix + INSERT INTO target (visible in validatedQueryText) |
| DELETE /automation/v1/queries/{id} | VERIFIED | cleanup worked; mcecli auto-snapshotted before-image |

## DATA-VIEW READS for agents — VERIFIED (dev org, account-level token)

**Direct SOAP event-object retrieve REPLACES Query Activity for record-level
tracking reads.** One read-only call, no DE/automation/query creation, no
polling, no cleanup. Validated live (scratch probe, wire-inspected):

| ObjectType | Status | Result |
|---|---|---|
| SentEvent | VERIFIED | rows returned for a 14-day window in 1 call. Props: SubscriberKey, EventDate, SendID, BatchID, SubscriberID |
| ClickEvent | VERIFIED | OK, 0 rows in window (quiet tenant). Props: SubscriberKey, EventDate, SendID, BatchID, URL (NO SubscriberID) |
| OpenEvent | VERIFIED | OK, 0 rows in window. Props: SubscriberKey, EventDate, SendID, BatchID (NO SubscriberID) |
| Send (= _Job metadata) | VERIFIED | rows returned. Props: ID, EmailName, Subject, FromName, SentDate. Filter ID equals {SendID} = the _Sent JOIN _Job pattern (2 calls total) |
| BounceEvent / UnsubEvent / NotSentEvent | UNVERIFIED | same pattern, expected to work (objects documented in corpus) |

Wire notes:
- request: RetrieveRequest + ObjectType + Properties + SimpleFilterPart
  (EventDate greaterThan ISO datetime works); fueloauth header; NO Client
  block needed; SOAPAction: Retrieve
- response: `<Results xsi:type="SentEvent">` UNPREFIXED children;
  OverallStatus=OK sibling; same shape as BusinessUnit retrieve
- wrong property name → OverallStatus="Error: The Request Property(s) X do
  not match with the fields of {Obj} retrieve" (HTTP 200; error names the
  bad property — encode as teaching error)
- 2-filter ComplexFilterPart wire shape (VERIFIED live 2026-09-16):
  operands are ABSTRACT FilterPart → each LeftOperand/RightOperand MUST
  carry xsi:type="tns:SimpleFilterPart" AND wrap its condition content
  directly. Failure ladder observed: bare alternation → "Incorrect syntax
  near the keyword 'AND'"; operand without xsi:type → "Unknown filter of
  type FilterPart encountered"; double-wrapped operand → "Invalid argument
  for the equals operator. Filter array cannot be null."
- Send object QUIRK: ComplexFilterPart (SentDate AND ID) answers OK with
  0 rows even when the row exists; bare ID-equals works → mcecli dv send
  <id> uses ID-only filter
- still-open: child-BU scoping — CLOSED FOR CURRENT PACKAGES 2026-09-16:
  bu discover shows only the root BU (<enterprise-mid>) is accessible, so the
  enterprise→child comparison is impossible with dev-full/limit. BUT
  BU-scoped scoping verified via dev-limited (tracking_events_read only,
  MID <child-mid>): dv sent works (scope sufficient) and returns THAT BU's
  own events only (different ListID/BatchID rows); dv send <send-id> → 0 rows
  (send belongs to another MID). Data-view reads are tracking-scoped and
  per-BU — consistent with the scoping model.
- 403 catalog (dev-limited, live 2026-09-16):
  * GET /data/v1/customObjects without data_extensions_read → HTTP 403
    {"message":"Insufficient privileges to complete this action."}
  * PUT /data/v1/async/dataextensions/key:{key}/rows without
    data_extensions_write → same 403 shape, and authz fires BEFORE key
    resolution/body validation (403, not 404/validation error)
  * mcecli hint classifies by method: "insufficient scope for GET/PUT —
    extend the installed package scopes"
- transactional messaging paging (messaging/v1/messageSends, live):
  count = TOTAL, page/pageSize respected; quirk known platform quirk NOT reproduced
  on the reference org (list semantics sane)

Agent guidance (SKILL.md candidate): tracking records → SOAP event objects
first (mcecli dv, to implement); Query Activity ONLY for SQL-only needs
(JOINs beyond SentEvent⋈Send, aggregates, _Subscribers/_Job-only columns,
bulk exports). REST rowset NEVER reaches data views (verified 404).

## Discovery documents

| Endpoint | Status | Notes |
|---|---|---|
| GET {section}/v1/rest | VERIFIED | self-describing method indexes. Sections live: **data, asset, automation, interaction, hub, messaging, push, platform, email, sms**. Shapes vary: data = flat {methods:{}}; others = Google-style discovery#restDescription (+basePath field — IGNORE it, paths resolve against /{section}/v1/). Use: `mcecli api <section>` |

## Data Extensions

### DE reads
| Endpoint | Status | Notes |
|---|---|---|
| GET /data/v1/customObjects?$search=X | VERIFIED | $search REQUIRED (400 without); matches name/key/description. Shape {count,page,pageSize,links,items:[{id,name,key,rowCount,...}]} |
| GET /data/v1/customObjects/{id} | VERIFIED | definition by GUID id (not key!) |
| GET /data/v1/customObjects/{id}/fields | VERIFIED | {id, fields:[{name,type,length,isNullable,isPrimaryKey,ordinal,...}]} |
| GET /data/v1/customobjectdata/key/{key}/rowset | VERIFIED | $-params; token-based continuation via requestToken; links.next gives next page path |
| GET /data/v1/customobjectdata/token/{token}/rowset | VERIFIED | continuation; pass --next "data/v1/customobjectdata/token/..." from envelope |
| GET /data/v1/customobjectdata (bare) | BROKEN | 404 — no bare collection |
| DELETE /data/v1/customObjects/{id} | DISCOVERY-VERIFIED | deletes the DE object; id (GUID) not key — curated as `mcecli de delete <key> --write --confirm` with an auto-captured undo image (de get before the delete). Destructive: live-fire reserved for owner-approved ops |

### DE writes
| Endpoint | Status | Notes |
|---|---|---|
| POST /data/v1/customObjects | VERIFIED | create DE; fields must be COMPLETE objects (every boolean required) |
| POST /data/v1/async/dataExtensions/{id}/rows | VERIFIED | INSERT (async 202); resolve by GUID id, NOT key |
| PUT /data/v1/async/dataExtensions/{id}/rows | VERIFIED | UPSERT (async 202); flat body {items:[{RowId:"..",...}]} |
| POST/PUT /customobjectdata/.../rowset | BROKEN | 404 on this host |
| /hub/v1/dataevents/key:{key}/rows | UNVERIFIED | "Not Authorized" with current token — scope gap, not path gap |

### Bulk Data Ingest (CLOSED v1.1)
The staged-ingest flow guessed here (create → stage → complete) does NOT
exist in this platform's discovery index — neither in the data section
(93 methods audited) nor in hub (103 methods audited). The working bulk
path is the one already curated as `de add`:
- PUT/POST /data/v1/async/dataextensions/key:{key}/rows — chunked async
  upserts (202 + job id; poll /data/v1/async/{id}/status)
The hub analogue is /hub/v1/dataeventsasync/key:{key}/rowset (bulk rows;
POST rowset/delete exists too — destructive) — see "DE sync rows" below:
same package-scope caveats, left passthrough-only.

### DE sync rows (RANKING)
`/hub/v1/dataevents/key:{key}/rows` — synchronous insert (immediate, not async).
Currently returns "Not Authorized" with the installed package token (scope gap). When to prefer:
- Sync: low row counts, need confirmation before moving on
- Async (current): anything over ~100 rows or when you don't need immediate commit

## Journeys and Events

Journey VERSION semantics (VERIFIED live, round 5): the collection returns
ONE item per key (newest version only); each publish bumps `version`. The
FULL history lives on the status endpoint:
GET /interaction/v1/interactions/status/key:{key}?AllVersions=true →
[{id, status, versionNumber}, …] (a bare call 400s with the teaching
message "AllVersions=true or VersionNumber required"; ?VersionNumber=N
narrows to one version; an unknown version returns an empty array).
Older versions are immutable history with their own status (Stopped/…).
Curated: `mcecli journey list` + `mcecli journey versions <key>`.

Journey PERFORMANCE (VERIFIED live, round 6): the status endpoint's `id`
is STABLE across versions (journey id, not per-version). Two summaries:
- GET /interaction/v1/interactions/{id}/summary → {id, activities:
  [{type, count}]} — ACTIVE activity counts across versions
- POST /interaction/v1/interactions/journeyhistory/summary body
  {"objectId": "<journey-id>"} → population counters {total,
  totalContactCount, waiting, expired, cameOffWait, successCount,
  errorCount, warningCount}. A READ query despite being a POST (validate
  precedent — no gate in the curated command; body wire-asserted)
- POST /interaction/v1/interactions/journeyhistory/search — paged contact
  history items ({count,page,pageSize:100}); BODY SCHEMA UNDOCUMENTED in
  discovery ({} works = full scan); left passthrough-only
Curated: `mcecli journey stats <key>` (resolves key → id → both
summaries in one envelope).

| Endpoint | Status | Notes |
|---|---|---|
| GET /interaction/v1/interactions | VERIFIED | journeys; /interactions (NOT /journeys); {count,page,items}; newest version only; $pageSize NOT honored (server pages at 50); $filter/name filtering NOT honored — filter client-side |
| GET /interaction/v1/interactions/status/key:{key} | VERIFIED | version history with ?AllVersions=true or ?VersionNumber=N (see notes above) |
| GET /interaction/v1/interactions/{id} | UNVERIFIED | single journey detail ({id} = definitionId) |
| POST /interaction/v1/interactions/stop/{id} | UNVERIFIED | stops running journey; --confirm gated |
| POST /interaction/v1/interactions/pause/{id} | UNVERIFIED | pauses journey; --confirm gated |
| POST /interaction/v1/interactions/resume/{id} | UNVERIFIED | resumes journey; --confirm gated |
| POST /interaction/v1/interactions/publishAsync/{id} | UNVERIFIED | async publish; --confirm gated |
| DELETE /interaction/v1/interactions/{id} | UNVERIFIED | delete journey; --confirm gated |
| GET /interaction/v1/eventDefinitions | UNVERIFIED | list event definitions |
| POST /interaction/v1/events | UNVERIFIED | fire event (inject into journey); --confirm gated |

## Messaging (Transactional sends)

| Endpoint | Status | Notes |
|---|---|---|
| GET /messaging/v1/messageSends | VERIFIED | list send definitions; {count,page,items} |
| POST /messaging/v1/messageSends/send | UNVERIFIED | fire send to recipients; --confirm gated (real people!) |
| POST /messaging/v1/messageSends | UNVERIFIED | create send definition; --write gated |
| GET /messaging/v1/jobs/{id}/stats/sends | VERIFIED | per-recipient send status for ONE email job. Items: {subscriberId, stats:[{id, transactionTime, domain}]} — a recipient's stats array carries ONE entry per send transaction (re-sends repeat). jobId namespace == _Sent.SendID (same id answers dv sent --send-id and this path). Server pages ITEMS at 25; $pageSize not honored (echoes default 25); $page works. No email address — subscriberId only. Curated: `dv recipients <jobId>` |
| GET /messaging/v1/emailSends/{jobId} | NOT AVAILABLE | 404 live with a real job id AND absent from the messaging discovery index — the documented "per-recipient send status" path does not exist on this platform; use jobs/{id}/stats/sends instead |
| GET /messaging/v1/emailstatstracking/{sends\|clicks\|opens\|unsubscribes}/{jobId} | PARTIAL | sends-kind VERIFIED: [{sendCount, lastSendDateTime, timePosted, jobID}] — send volume over time per job (throttling/batching diagnosis). clicks/opens/unsubscribes kinds 404 on a job with no such events (no-data reads as 404 — indistinguishable from missing endpoint); left passthrough-only, unverified |

## Assets

| Endpoint | Status | Notes |
|---|---|---|
| GET /asset/v1/content/assets | VERIFIED | assets; $filter, $fields, $pageSize |
| GET /asset/v1/content/assets/{id} | VERIFIED | single asset detail |
| GET /asset/v1/content/assets/{id}/file | VERIFIED | binary download; content-type-aware |
| POST /asset/v1/content/assets | UNVERIFIED | create asset; --write gated |

## Automation Studio

| Endpoint | Status | Notes |
|---|---|---|
| GET /automation/v1/automations | UNVERIFIED | list; discovery doc available via `mcecli api automation` |
| POST /automation/v1/automations/{id}/start | UNVERIFIED | start automation; --confirm gated |
| POST /automation/v1/automations/{id}/stop | UNVERIFIED | stop automation; --confirm gated |

## Key-vs-name quirk

rowset reads AND async writes resolve by DE **customerKey**; DE **name** is not
matched. When name == key it works; when a DE was created with only a name, the
platform assigns a GUID key. Resolve via `mcecli de list --search <name>
--fields name,key`.

## "Not Authorized" — what it means

When SFMC returns `{"message":"Not Authorized"}` on a write:
- The endpoint PATH is CORRECT
- The token lacks the required scope/permission for this operation
- Do NOT retry expecting a different result — the operation requires a different
  package or elevated permissions
- The installed package is BU-scoped and missing most write scopes

## Subscriber operational lookups — VERIFIED LIVE 2026-09-16 (mcecli sub)

| Subject | Status | Notes |
|---|---|---|
| RetrieveRequest ObjectType=Subscriber | VERIFIED | filter SubscriberKey OR EmailAddress equals; retrievable: SubscriberKey, EmailAddress, Status, UnsubscribedDate, CreatedDate, EmailTypePreference. **ModifiedDate NOT retrievable** (docs list it) |
| Status values | VERIFIED | returns NAMES ("Active") not numbers on the reference org; official enum: Active/Bounced/Held/Unsubscribed (Held = 3+ sequential bounces, 14+ days apart) |
| ObjectType=ListSubscriber | VERIFIED | [ListID, SubscriberKey, Status, CreatedDate] filtered SubscriberKey equals → per-list memberships (Active/Unsubscribed) |
| email ambiguity | VERIFIED | same address exists on MULTIPLE MID contexts (3 matches for one address) → surface all, join by exact SubscriberKey only |

## Automations (REST) — VERIFIED LIVE 2026-09-17 (mcecli auto)

| Endpoint | Status | Notes |
|---|---|---|
| GET /automation/v1/automations?$pageSize&$page | VERIFIED | count=TOTAL (all automations in context); items: id,key,name,status,lastRunTime,type,fileTrigger,categoryId; statuses seen: InactiveTrigger, Ready |
| GET /automation/v1/automations/{id} | VERIFIED | detail + steps[] with activities[] (name, objectTypeId); key is NOT the id — resolve key→id by scan (mcecli auto does it) |
| GET /automation/v1/automations/healthreport | VERIFIED | CSV with BOM: EID,MID,BusinessUnitName,AutomationName,Type,StartSource,ScheduledFrequency,LastRunTime,30DaySuccessRate,30DayCompletionCount,30DayErrorCount,30DaySkipCount,30DayRunCount,12MonthRunCount,FolderPath — THE ops "what is failing" view; mcecli parses to JSON |
| GET /automation/v1/automations/instance/{instanceId} | UNVERIFIED | 404 with automation id (instance ids differ; lastRunInstanceId field exists on list items) |
| POST /{key}/actions/runallonce, /trigger | NOT IMPLEMENTED | gated writes, deliberately deferred |

### Scheduling, pause, start — VERIFIED LIVE 2026-09-27 (round-7 field test)

- **There is NO `/schedule` route** and **no pause/resume route** — proven via
  the discovery index (only `actions/start` and `actions/runallonce` exist).
  Scheduling is done by PATCHing the automation itself.
- **WORKING schedule recipe** (live-verified):
  `PATCH /automation/v1/automations/{guid}` body:
  `{"startSource":{"typeId":1,"schedule":{"startDate":"<RFC3339>","timezoneId":5,"iCalRecur":"FREQ=HOURLY;INTERVAL=1;COUNT=2"}}}`
  - `timezoneId` is an UNDOCUMENTED NUMERIC ENUM (timezone names fail with
    `JSON Deserialization Exception: Location Unknown`); 5 = Central US.
  - `startDate` (inside startSource.schedule) — the top-level
    `startDateTime`/`recurrenceType` model is silently IGNORED.
- **PLATFORM LENIENCY HAZARD**: malformed/ignored schedule payloads (wrong
  model, `scheduleStatus` toggles) return `200 OK` with NO effect — the
  automation detail (`mcecli auto <key>`) is the only truth for whether a
  schedule attached.
- **API-created schedules start PAUSED** (statusId 4, scheduleStatus
  "paused") — that is the platform default, not an action.
- **Pause/resume via API: not available.** `POST .../actions/pause` → 404;
  `PATCH scheduleStatus` → silent no-op; `POST actions/start` → 400 `field
  is required: 'Steps'` (undocumented step serialization) even with
  `Steps: []`. Runtime start/pause/resume stay UI-only paths.
- **REST-created query activities carry objectTypeId 43** (live-verified;
  legacy program-era query = 300). Step `name` may come back empty for
  REST-created automations even when set at create (platform quirk).
- **healthreport only covers automations that RAN in the last 30 days** —
  never-run automations do not appear (use `mcecli auto list`).

## SOAP Describe — BLOCKED on the reference org (2026-09-17)
Every envelope style (unprefixed xmlns body, tns-prefixed, outer
DescribeRequest wrapper, +ContinueRequest variants) → HTTP 200,
`DefinitionResponseMsg` with ONLY a RequestID, zero ObjectDefinition.
ContinueRequest with that RID → "The RequestID sent through ContinueRequest
does not exist." Describe is unusable here; property knowledge is compiled
into `mcecli describe` (embedded catalog, live-bisected + doc-marked).

## TriggeredSendDefinition + List/ListSubscriber — VERIFIED LIVE 2026-09-17 (mcecli ts / mcecli lists)

| Subject | Status | Notes |
|---|---|---|
| Retrieve TriggeredSendDefinition | VERIFIED | props: CustomerKey, Name, TriggeredSendStatus, CategoryID, CreatedDate. **IsPaused NOT retrievable** (docs list it). Statuses on tenant: Active, Inactive, Deleted, Canceled. all definitions — note: that scan ran against prod-read by accident; DEV count may differ (context gotcha) |
| TSD CustomerKey server-side filter | BROKEN | equals filter → 0 rows even for existing keys (unreliable-filter family) → `mcecli ts get` scans client-side over all definitions in context |
| TSD context anomaly | RESOLVED 2026-09-17 | "example_ts absent from mcecli scan" was NOT request-shape: the mcecli session had silently switched to the prod-read (PROD!) profile — different tenant, different TSD estate. Always mcecli status first; result-set diffs between sessions = context switch first |
| Retrieve List (no filter) | VERIFIED | lists in parent context; ID/ListName/Type/Description all retrievable |
| List.ID server-side filter | BROKEN | "Incorrect syntax near the keyword 'ORDER'" |
| Retrieve ListSubscriber | VERIFIED | SubscriberKey filter WORKS (per-subscriber memberships); ListID filter → 0 rows on every list (broken). Unfiltered: 2500 rows/page + MoreDataAvailable (membership scan viable) |
| ListSubscriber envelope sensitivity | 🚨 | unprefixed scratch envelope → server NRE "Object reference not set"; mcecli canonical tns envelope works — wire style matters per object |

## GlobalUnsubscribeCategory + EmailSendDefinition — VERIFIED LIVE 2026-09-17

| Subject | Status | Notes |
|---|---|---|
| Retrieve GlobalUnsubscribeCategory | VERIFIED | unsubscribe categories in parent context; props: ID, Name, CategoryType, CreatedDate. ID=0 for all (enterprise-level categories). Categories: Activist, Legacy Unsubscribe, Unsub via Reply Mail, Known Spamtrap, Requested, Unsub via FBL, etc. |
| Retrieve EmailSendDefinition | VERIFIED | send definitions in parent context; props: CustomerKey, Name, CreatedDate. **SendDefinitionStatus NOT retrievable** on the reference org (docs overstate). keys are arbitrary strings set at creation |
| ESD server-side filter | BROKEN | key filter → 0 rows even for existing keys (same unreliable-filter family as TSD/List) → `mcecli esd get` scans client-side (send definitions = trivial cost) |
| ESD unfiltered count | VERIFIED | send definitions total in context — full scan is cheap |

## ENS (Event Notification Subscriptions) — VERIFIED LIVE 2026-09-17

| Subject | Status | Notes |
|---|---|---|
| GET /messaging/v1/eventNotificationCallbacks | VERIFIED | a registered callback (callbackId: <callback-guid>), url: <subdomain>.pub.sfmc-content.com/<path>, status: verified, maxBatchSize: 1000 |
| GET /messaging/v1/eventNotificationCallbacks/{id}/subscriptions | VERIFIED | 1 subscription: streams SendEvents.AutomationInstanceStarted + SendEvents.AutomationInstanceErrored to the monitoring URL. subscriptionId: <subscription-guid>, status: active |
| POST/DELETE/PATCH /messaging/v1/eventNotificationCallbacks | GATED | write operations available but gated; no mcecli command yet |
| OSS + SendEvents.AutomationInstanceStarted | VERIFIED (live) | the callback IS receiving these events — tenant actively monitors automation starts and errors |

## GUC / ESD / ENS / AutomationInstance — VERIFIED LIVE 2026-09-17 (P2 batch)

| Subject | Status | Notes |
|---|---|---|
| Retrieve GlobalUnsubscribeCategory | VERIFIED | rows: ID/Name/CategoryType/CreatedDate (names: Activist, Legacy Unsubscribe, Unsub via Reply Mail, Known Spamtrap, Requested…) → mcecli guc list |
| Retrieve EmailSendDefinition | VERIFIED | CustomerKey/Name/CreatedDate; SendDefinitionStatus NOT retrievable. send definitions via mcecli (parent-BU ctx) → mcecli esd list/get |
| ESD context anomaly | RESOLVED 2026-09-17 | same cause as TSD anomaly: mcecli session was on prod-read while probes used dev-full — DEV vs PROD estates, not request shape |
| GET /platform/v1/ens-callbacks | VERIFIED | registered callbacks → mcecli ens callbacks |
| GET /platform/v1/ens-subscriptions-by-cb/{id} | VERIFIED | subscribed to SendEvents.AutomationInstanceStarted + AutomationInstanceErrored → mcecli ens subs |
| GET /platform/v1/tokenContext | VERIFIED | {enterprise:{id}, organization:{id}, user:{id}} behind the CURRENT token — answers "which user/org is this token?" in multi-profile setups; surfaced in mcecli auth test (best-effort, failure never fails the command) |
| Retrieve AutomationInstance | 🚫 BLOCKED | needs AutomationID filter (unfiltered → PartnerProperties error); even with valid recent id → "No rows were found" — instances live on child-BU contexts unreachable with current packages. Healthreport (REST) is the working ops view |
| Retrieve SendSummary | VERIFIED | SendID filter WORKS (1 exact row); TotalSent retrievable; Delivered/Bounces NOT — TotalSent-only value, kept passthrough-only |

## de create / query create / users / folders — VERIFIED LIVE 2026-09-18

| Subject | Status | Notes |
|---|---|---|
| POST /data/v1/customObjects via `de create` | VERIFIED | 201; complete field objects built by the command (endpoint rejects incomplete objects one property at a time; uses length NOT maxLength). Field specs Name:Type(len[,scale]); PK flags isPrimaryKey + isNullable=false |
| $search right after create | QUIRK | de get via $search 404s for ~8s after create (index lag) — retry |
| POST /automation/v1/queries via `query create` | VERIFIED | flat body queryText/targetKey/targetUpdateTypeId/categoryId; **create-time validation requires ALL target-DE PK/required columns in the SELECT** ("Field 'AccountID' is required for the Target Data Extension") |
| targetUpdateTypeId | VERIFIED | 0=Overwrite (targetUpdateTypeName confirms); 1 seen only as "Update" in docs — unverified live |
| SOAP Retrieve AccountUser | VERIFIED | ID/UserID/Name/Email/ActiveFlag/DefaultBusinessUnit retrievable; **Delete NOT retrievable**; no server filter → client-side search; users parent ctx |
| SOAP Retrieve DataFolder | VERIFIED | ContentType server-side filter WORKS (queryactivity → folder list, dataextension → 34); ParentFolderID NOT retrievable |

## customObjects listing + healthreport semantics — agent-feedback round 3 (2026-09-25)

- data/v1/customObjects: **$search is REQUIRED even when categoryId is
  supplied** (VERIFIED via agent field test: categoryId alone → platform
  400 "$search is a required parameter"). categoryId is an AND-narrowing
  filter, NOT an alternative to $search. de list now fails fast
  client-side with the truthful contract.
- $pageSize is ignored by this endpoint; server pages are capped at 25
  rows (both already surfaced by de list transparency hints).
- rowCount on customObjects items: platform-reported; semantics UNVERIFIED
  (fresh vs cached, per-BU vs shared) — treat as approximate. For exact
  counts use the rowset (`de rows`) or a platform-side query.
- automation/v1/automations/healthreport: **ESTATE-WIDE** — rows cover all
  BUs under the enterprise account, not just the context BU (unlike
  automation/v1/automations list, which is context-scoped). Arrives as ONE
  CSV response — no server-side paging; any truncation is client-side
  (auto health default is now full-scan).
- Full-DE enumeration — VERIFIED 2026-09-25 via SOAP DataExtension Retrieve
  (shipped as `de list --all`): Name/CustomerKey/CategoryID/CreatedDate/
  IsSendable all retrievable; a full BU inventory returns in ONE round trip
  (~sub-second for a four-digit DE count); CustomerKeys are unique.
  NOT retrievable on this object: RowCount ("do not match with the fields
  of DataExtension retrieve" — the REST listing's rowCount has no SOAP
  counterpart). CategoryID `equals` filters SERVER-side (bogus value →
  0 rows, status OK). Scope: a BU-context token returns that BU's DEs —
  do NOT set QueryAllAccounts for this path. CAVEAT: soap.Opts.MaxRows
  does not trim a single-page response (it only stops continuation) —
  client-side caps must trim in the command (`--limit` does, loudly).
