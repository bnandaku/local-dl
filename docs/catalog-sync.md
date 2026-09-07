# Catalog synchronization

Local-dl synchronizes the movie/TV catalog via authenticated, gzip-compressed full snapshots to the existing `POST /catalogUpdate` endpoint. Music playlists use their existing separate sync. Queue polling and recovery retain their existing cadence; successful five-minute queue polls no longer upload the catalog.

## Scheduling and durability

SQLite triggers commit the dirty generation and catalog audit history with additions, metadata edits, removals and moves. Merely updating last-seen does not cause a sync. Publication hooks catalog only atomically published, validated primary media. Repair operations update old/new catalog paths in one transaction.

One background worker batches events for 30 seconds, with a 120-second ceiling during continuous changes. Startup, explicit bot resync and authenticated `POST /catalog/scan` use that same worker. Once every 24 hours it reconciles the actual movie/TV roots and uploads a fresh full snapshot. The successful reconciliation time and next due time persist in SQLite; overdue restarts run one reconciliation, not a backlog of missed days.

A snapshot is read in one transaction and stored with its captured generation before transmission. Only HTTP 200 JSON with the expected success message and show/episode counts acknowledges it. New mutations and reconciliation requests arriving during upload remain pending. DNS, connection-refused and TLS failures before request delivery remain ordinary retries. Known HTTP failures retry the same captured bytes with jittered exponential backoff, starting at 30 seconds and capped at 30 minutes. Authentication failures pause for 30 minutes with an actionable status message. Routine refreshes are not sent to Discord.

**Legacy endpoint limitation:** an unacknowledged network request may still execute remotely. Without server-side ordering, a duplicate's success cannot prove the earlier handler has settled. Local-dl persists `uncertain_delivery`, retries only the same frozen snapshot, and refuses to advance to newer snapshots. Automatic progress after that condition requires the ordered server contract described in [catalog-sync-bot-followup.md](catalog-sync-bot-followup.md). Do not clear that state merely because a duplicate was acknowledged; no stale request may remain executing when newer data is allowed to advance.

## Storage safety

Reconciliation requires both configured movie and TV roots to be available, readable, distinct absolute directories. Root device/inode identity is persisted; replacement or disconnected mounts hold reconciliation. Scans do not skip filesystem errors or partially apply their results. Newly discovered/changed files undergo video validation. Successful validation fingerprints are cached separately from the legacy catalog, so old catalog rows are not automatically trusted and unchanged daily scans avoid repeated decoding. Hidden staging files, garbage, trailers, samples, wrong-library episodes and TV files lacking a complete show/season/episode identity are excluded.

On first enrollment, an empty root is refused unless the operator has verified that it is intentionally empty and sets `CATALOG_ALLOW_EMPTY_ROOTS=movie`, `tv`, or `movie,tv`. The setting does not override unavailable roots, identity changes, or a previously populated root unexpectedly appearing empty. Do not delete the catalog database to repair a mount problem. Restore the original mount first; intentional root replacement requires verifying the new volume before deliberately re-enrolling its identity.

Filesystem changes made outside local-dl are discovered at daily reconciliation or an explicit scan request. `scripts/audit_library.py --apply` also sends durable SQLite notifications for quarantines when given `--catalog-db`, `CATALOG_DB`, or an existing `ROOT/data/tvshows_catalog.db`. Host audit paths are mapped to `MOVIES_PATH`/`TVSHOW_PATH`/`MUSIC_PATH` (container defaults `/mnt/movies`, `/mnt/tvshows`, `/mnt/music`), so configure these if the service uses custom roots. Without a catalog DB, the offline auditor retains its previous standalone behavior and relies on later reconciliation.

Every quarantine move is journaled before catalog notification. If notification fails, replay without moving files again:

```sh
python3 scripts/audit_library.py --root /path/to/media \
  --catalog-db /path/to/catalog.db \
  --replay-quarantine /path/to/media/.local-dl-quarantine/RUN/manifest.json
```

An explicitly configured unavailable/unmigrated database prevents quarantine from starting. Quarantined copies are retained, and a reappeared source prevents deletion of its catalog record.

## Configuration and status

- `CATALOG_API_KEY`: private bearer API key for the bot's catalog endpoint; falls back to the existing `BOT_SERVICE_TOKEN` when unset. Never store real keys in this repository.
- `REMOTE_SERVER`: existing bot API base URL.
- `CATALOG_DB`, `MOVIES_PATH`, `TVSHOW_PATH`: existing database and managed roots. SQLite now also stores sync state, root identities and validation proofs.
- `CATALOG_ALLOW_EMPTY_ROOTS`: explicit first-enrollment exception for intentionally empty libraries, as described above.

Authenticated `GET /recovery/status` includes `catalog_sync`: generation/acknowledgement, last/next full reconciliation, retry time, errors and uncertainty flag. Private snapshot bytes and bearer credentials are never returned. Catalog scan/search/stat endpoints require the existing local `BOT_SERVICE_TOKEN` bearer credential. Reconfiguring an outgoing key does not change inbound local service authentication.

## Verification

Run `go test -race ./...`, `go vet ./...` and `python3 -m unittest discover -s scripts -p 'test_*.py'`. Install native unrar/ffmpeg tools (or use the runtime image) to include all existing media/recovery fixtures. Acceptance tests use actual SQLite, fake time and an HTTP bot to cover idle/daily behavior, transactional dirty state, debounce, moves, restart retries, in-flight mutations, root outages/enrollment, strict acknowledgements, auth holds and unknown-delivery fencing.

Deployment uses a privately configured catalog API key. The legacy ordering limitation remains a bot integration dependency; the local uncertainty fence preserves safety until that contract is available. A server-only change cannot stop the old deployed five-minute local-dl caller.
