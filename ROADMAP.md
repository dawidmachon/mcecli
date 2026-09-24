# Roadmap

Public releases and the full feature history live in [CHANGELOG.md](CHANGELOG.md).
This document covers what's coming next.

## v1.1 (next release)

- [ ] journey version modeling — journeys have versions; surface
      list/count/compare instead of raw REST + client-side grouping
- [ ] `de list` default output curation — lean default projection
      (name/key/rowCount), full detail on demand
- [ ] per-recipient send status — `messaging/v1/emailSends/{jobId}`
- [ ] `query update` polish — diff current definition vs proposed change
      before applying
- [ ] bulk ingest for very large loads (`/hub/v1/async` flow)
- [ ] SOAP passthrough redesign — a safe generic SOAP command (the first
      attempt was removed; needs a clean design)

## Ideas (unscheduled)

- `de find --all-profiles` — cross-profile visibility map
- work-cache pruning (TTL-based cleanup of `~/.mcecli/work/`)
- journey create/publish/stop — needs careful sendout-safety design;
  deliberately not exposed yet

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
