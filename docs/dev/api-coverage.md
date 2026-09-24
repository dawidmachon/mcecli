# mcecli API coverage map — vs sf-docs-scrap corpus

Source of truth for "what can mcecli do, what's missing, what's next".
Corpus: local copy of the official SFMC documentation (markdown scrape).
Audience: local AI agents doing operational debugging. Ranking = agent
usefulness ÷ (cost × risk), given THIS tenant's packages (limited-scope packages).

Legend: ✅ verified live · 🟡 implemented, unverified · 🔲 passthrough only
(`mcecli rest`) · ❌ not covered

## REST coverage

| Endpoint group | mcecli | State |
|---|---|---|
| /data/v1/customObjects (list/get/fields, $search) | de list/get | ✅ |
| /data/v1/customObjects (create/cleardata) | rest --write | ✅ |
| /data/v1/customobjectdata/{key,token}/rowset | de rows/dump | ✅ |
| /data/v1/async/dataextensions/{id,key}/rows PUT/POST | de add | ✅ |
| /asset/v1/content/assets(+/{id},/file) | asset search/pull | ✅ |
| /automation/v1/queries (list/create/validate/delete) | query list/create* | ✅ (*create via rest) |
| /automation/v1/queries/{id}/actions/{start,isrunning} | query run/status | ✅ |
| /automation/v1/queries/{id}/log | query run | ✅ (empty-on-success quirk) |
| /interaction/v1/interactions (+key:{key}?extras) | rest / md pull | ✅ |
| /interaction/v1/eventDefinitions | rest | ✅ |
| /interaction/v1/interactions/{id} lifecycle (stop/pause/publish/event) | ❌ | gated writes — deferred (sendout risk) |
| /automation/v1/automations (list/detail/healthreport) | auto list/detail/health | ✅ (start/stop stay gated via rest) |
| /messaging/v1/messageSends (list) | rest | ✅ (paging sane; known platform quirk n/r) |
| /messaging/v1/emailSends (per-recipient send status) | ❌ | 🔲 — P2 |
| /contacts/v1/schema (+attributeGroups) | bu discover | ✅ (partial) |
| /contacts/v1/attributeSets deep probing | ❌ | 🔲 — P3 |
| /hub/v1/dataevents rows (sync insert) | ❌ | 🚫 403 scope gap on the installed package |
| /hub/v1/async bulk ingest (create/stage/complete) | ❌ | ranked note only — P3 |
| /interaction/v1/eventNotification (callbacks/subscriptions) | ❌ | 🔲 — P2 (webhook monitoring) |
| /platform/v1/token introspection | ❌ | scope-debug use — P3 |
| discovery docs /{section}/v1/rest | api | ✅ (10 sections) |
| everything else | rest | 🔲 generic passthrough |

## SOAP coverage (RetrieveRequest)

| ObjectType | mcecli | State |
|---|---|---|
| BusinessUnit (+QueryAllAccounts) | bu discover | ✅ |
| Account | bu discover | ✅ (calling context only — platform rule) |
| SentEvent / ClickEvent / OpenEvent / BounceEvent / UnsubEvent / NotSentEvent | dv sent|clicks|… | ✅ (property sets bisected vs docs) |
| Send (_Job metadata) | dv send | ✅ (ComplexFilterPart → silent 0 rows quirk) |
| Subscriber | sub | ✅ (ModifiedDate n/r; email ambiguity across MIDs) |
| ListSubscriber | sub | ✅ (per-list status) |
| DataExtensionObject[key] rows | de rows --where | 🚫 0-rows on this tenant (root-cause candidates documented; use query) |
| Describe (ObjectDefinitionRequest) | describe (embedded catalog) | 🚫 BLOCKED on this tenant: empty DefinitionResponseMsg every style (wire-dumped 2026-09-17) — catalog compiled from live bisection instead |
| TriggeredSendDefinition | ts list\|get | ✅ (CustomerKey/Name/TriggeredSendStatus/CategoryID/CreatedDate; IsPaused n/r) |
| List / ListSubscriber | lists / sub | ✅ (ID/ListName/Type; SubscriberKey filter works; ListID filter broken) |
| AutomationInstance | auto (REST) | ✅ (via /instance/{id} REST; SOAP requires a filter) |
| EmailSendDefinition | esd list\|get | ✅ (CustomerKey/Name/CreatedDate; SendDefinitionStatus n/r) |
| GlobalUnsubscribeCategory | guc list | ✅ (9 enterprise categories: Activist/Legacy-Unsub/FBL/etc.) |
| DataFolder / Category | ❌ | — P3 (folder browsing; md covers some) |
| AccountUser / Role / PublicKeyManagement | ❌ | — P3 (admin debugging, rare) |
| ExtractDefinition (bulk to FTP) | ❌ | — P3 (big jobs only; async FTP flow) |
| ContentArea / Portfolio / Template | ❌ | — niche, asset REST covers reads |
| SuppressionList*, FileTrigger, VoiceTriggeredSend, DeliveryProfile, SenderProfile, SendClassification, MessagingVendorKind, PropertyDefinition, ProgramManifestTemplate, FilterDefinition, FileTransferActivity, HiveQueryDefinition, PlatformApplication | ❌ | — not on ops roadmap |

## Priority plan (next up first)

**P1 — status 2026-09-17 ✅ DONE**
1. ✅ DONE (pivoted): `mcecli describe <object>` — embedded catalog of
   tenant-verified props + quirks. SOAP Describe is BLOCKED on this
   tenant: every envelope style (unprefixed, tns-prefixed, with
   ContinueRequest) returns empty DefinitionResponseMsg + RequestID;
   ContinueRequest → "RequestID does not exist". Evidence wire-dumped.
   Embedded catalog = zero-cost, always right for this tenant.
2. ✅ DONE: `mcecli auto list|<key>|health` — list (count = total, client-side
   --search/--status, --all for full scan), detail (steps + activities,
   key→id resolution), healthreport (CSV→JSON: 30DaySuccessRate/
   30DayErrorCount per automation — the "what is failing" view).
   /instance/{id} → 404 (guid format differs; not pursued).
3. ✅ DONE: `mcecli ts list|get` — TriggeredSendDefinition reads via SOAP
   Retrieve (CustomerKey/Name/TriggeredSendStatus/CategoryID/CreatedDate).
   IsPaused NOT retrievable on this tenant despite docs. Statuses: Canceled/Inactive.
4. ✅ DONE: `mcecli lists` + `mcecli lists members <subscriberKey>` — List reads
   (ID/ListName/Type); ListSubscriber via SubscriberKey filter. ListID filter
   NREs on this tenant (wire-style-sensitive); SubscriberKey path works reliably.

**P2 — status 2026-09-17**
5. 🚫 AutomationInstance — BLOCKED: instances on child-BU contexts;
   healthreport (REST) is the working ops view.
6. ✅ DONE: `mcecli ens callbacks|subs` — ENS reads (1 verified callback
   streaming AutomationInstanceStarted/Errored on this tenant).
7. ✅ DONE: `mcecli guc list` (unsubscribe categories). Publication left passthrough.
8. ✅ DONE: `mcecli esd list|get` (send definitions; SendDefinitionStatus n/r).
   SendSummary verified (SendID filter works, TotalSent only) — kept
   passthrough-only, low standalone value.
9. 🔲 /messaging/v1/emailSends/{jobId} per-recipient — needs a live jobId
   from a real send; on demand.

**P3 — on demand only**
10. Extract API bulk flow; /contacts/v1/attributeSets deep probe;
    DataFolder browse; platform token introspection; AccountUser/Role.

## Tenant gates to remember
- limited-scope packages scope gaps: hub 403; limit lacks data_extensions_* (403
  catalog in endpoint-notes.md)
- SOAP DataExtensionObject retrieve → 0 rows (use query instead)
- No child BUs accessible → cross-BU comparisons impossible today
- Lifecycle writes (journey stop/publish, sends to recipients) stay
  deferred/confirm-gated by design
