# Roadmap

Public releases and the full feature history live in [CHANGELOG.md](CHANGELOG.md).
This document covers what's coming next.

## v1.1 (next release)

- [x] journey version modeling — shipped as `journey list` +
      `journey versions <key>`: curated collection read plus the FULL
      version history from the status endpoint (AllVersions=true /
      VersionNumber=N, probed live); lifecycle writes stay gated behind
      rest (sendout risk)
- [x] `de list` default output curation — lean default projection
      (name/key/rowCount), `--full` for complete objects; request wire
      unchanged
- [x] per-recipient send status — shipped as `dv recipients <jobId>`;
      the documented /messaging/v1/emailSends/{jobId} path does not exist
      (404 live + absent from discovery) — the real endpoint is
      /messaging/v1/jobs/{id}/stats/sends (jobId = dv-sent SendID)
- [x] `query update` polish — `--diff` dry run shows current vs proposed
      before applying (read-only, no PATCH on the wire)
- [x] bulk ingest — CLOSED as specified: /hub/v1/async (create/stage/
      complete) does not exist in discovery (data + hub audited); the
      working bulk path was already shipped as `de add`; hub's
      /hub/v1/dataeventsasync documented as passthrough-only (scope)
- [x] SOAP passthrough redesign — shipped as `soap retrieve`: Retrieve
      is the only verb reachable (structured flags, no body passthrough),
      --props required, equals-only filters, read-only tier
- [x] journey stats — population + activity summary across ALL versions
      (gap-audit find; history POST is a wire-asserted read-query)
- [x] de delete / query delete — lifecycle completeness for the curated
      creators; gated, undo-imaged, journaled
- [x] work-cache pruning — `work prune` (report default, --do deletes,
      journal never touched)
- [x] token identity — auth test surfaces tokenContext ids

## Ideas (unscheduled)

- `de find --all-profiles` — cross-profile visibility map
- journey create/publish/stop — needs careful sendout-safety design;
  deliberately not exposed yet
- `work prune` SHIPPED in v1.1 (completeness batch)

## Known platform limitations (not mcecli bugs)

Things the platform itself makes hard or different — documented so agents
don't waste time rediscovering them. Full details in
[docs/dev/endpoint-notes.md](docs/dev/endpoint-notes.md).

- SOAP row retrieval on `DataExtensionObject[KEY]` may return 0 rows —
  Query Activities are the reliable server-side filter path
- Some server-side retrieve filters are unreliable on metadata objects
  (TriggeredSendDefinition.CustomerKey, ListSubscriber.ListID, List.ID) —
  curated commands match client-side instead
- Sync row insert (`/hub/v1/dataevents`) needs a package scope many
  installs don't grant; async upsert is the working path
- Official docs overstate retrievable properties on several objects
  (BounceEvent.BounceReason, TriggeredSendDefinition.IsPaused,
  Subscriber.ModifiedDate, etc.) — `mcecli describe` reflects verified reality
- The SOAP Describe call is blocked on some orgs — verified property
  knowledge ships as the embedded `describe` catalog
