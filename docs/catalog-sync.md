# Catalog synchronization

Local-dl synchronizes the movie/TV catalog via authenticated, gzip-compressed full snapshots to the existing `POST /catalogUpdate` endpoint. Music playlists use their existing separate sync. Queue polling and recovery retain their existing cadence; successful five-minute queue polls no longer upload the catalog.

## Scheduling and durability

SQLite triggers commit the dirty generation and catalog audit history with additions, metadata edits, removals and moves. Merely updating last-seen does not cause a sync. Publication hooks catalog only atomically published, validated primary media. Repair operations update old/new catalog paths in one transaction.

One background worker batches events for 30 seconds, with a 120-second ceiling during continuous changes. Startup, explicit bot resync and authenticated `POST /catalog/scan` use that same worker. Once every 24 hours it reconciles the actual movie/TV roots and uploads a fresh full snapshot. The successful reconciliation time and next due time persist in SQLite; overdue restarts run one reconciliation, not a backlog of missed days.

A snapshot is read in one transaction and stored with its captured generation before transmission. Only HTTP 200 JSON with the expected success message and show/episode counts acknowledges it. New mutations and reconciliation requests arriving during upload remain pending. DNS, connection-refused and TLS failures before request delivery remain ordinary retries. Known HTTP failures retry the same captured bytes with jittered exponential backoff, starting at 30 seconds and capped at 30 minutes. Authentication failures pause for 30 minutes with an actionable status message. Routine refreshes are not sent to Discord.

## Ordered delivery

Set `CATALOG_SOURCE_ID=peaches-unraid` to enroll the dedicated local-dl key with the bot's `ordered_catalog_v1` contract. Local-dl verifies capabilities and reads `/api/v1/catalog/watermark?source_id=...` before assigning an ordered snapshot. It persists the stable source, monotonically increasing sequence, exact JSON bytes and SHA-256 before POST. The sequence is separate from catalog dirty generation and advances for daily reconciliations too. New databases bootstrap above the server watermark; existing pending snapshots retain their original identity across restarts.

The POST carries `X-Catalog-Source`, `X-Catalog-Sequence`, and `X-Catalog-SHA256`. Acknowledgement requires the matching source/sequence/hash, a boolean replay marker, and the existing success message/count checks. Lost responses and transient failures retry identical bytes and headers; a matching replay safely clears uncertainty. Mutations arriving during upload stay dirty.

A stale-sequence response causes a watermark read and schedules a fresh full reconciliation above that watermark, without falsely acknowledging the obsolete generation. Conflicting identities/content, invalid payloads and oversized snapshots set a durable operator hold. Automatic resyncs and restarts do not clear it. After repairing the cause, authenticated `POST /catalog/scan` resumes the saved snapshot and queues reconciliation. Authentication failures retain the 30-minute retry hold.

The source remains persisted even if the environment variable is removed; there is no silent downgrade. Restore the original source and rotate the existing key ID for credential changes. Creating a new key is a different producer and cannot take ownership. Legacy mode remains available only for databases never enrolled locally; its uncertainty fence remains until ordered enrollment receives a verified acknowledgement.

**Rollback:** after the server enrolls, it rejects all legacy unordered writers. A rollback to an older local-dl image preserves media/queue operations but cannot resume catalog synchronization. Restore an ordered-capable image and its durable database for full recovery; do not reset the server watermark or delete the local outbox. Keep pre-deployment SQLite backups and the previous container.

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

- `CATALOG_SOURCE_ID`: stable ordered producer ID, configured as `peaches-unraid` on Unraid.
- `CATALOG_API_KEY`: private bearer API key for the bot's catalog endpoint; required for ordered mode. Legacy mode can fall back to the existing `BOT_SERVICE_TOKEN` when unset. Never store real keys in this repository.
- `REMOTE_SERVER`: existing bot API base URL.
- `CATALOG_DB`, `MOVIES_PATH`, `TVSHOW_PATH`: existing database and managed roots. SQLite now also stores sync state, root identities and validation proofs. Configure `CATALOG_DB=/data/tvshows_catalog.db` without surrounding whitespace and mount `/data` persistently; recovery/archive state also derives its directory from this setting. Catalog initialization trims accidental surrounding whitespace. Before correcting an old deployment, recover its catalog and recovery/archive state from the old container into the mounted directory; retain backups.
- `CATALOG_ALLOW_EMPTY_ROOTS`: explicit first-enrollment exception for intentionally empty libraries, as described above.

Authenticated `GET /recovery/status` includes `catalog_sync`: generation/acknowledgement, last/next full reconciliation, retry time, errors, uncertainty flag, producer identity, assigned/pending sequences, pending hash and operator hold. Private snapshot bytes and bearer credentials are never returned. Catalog scan/search/stat endpoints require the existing local `BOT_SERVICE_TOKEN` bearer credential. Reconfiguring an outgoing key does not change inbound local service authentication.

## Verification

Run `go test -race ./...`, `go vet ./...` and `python3 -m unittest discover -s scripts -p 'test_*.py'`. Install native unrar/ffmpeg tools (or use the runtime image) to include all existing media/recovery fixtures. Acceptance tests use actual SQLite, fake time and an HTTP bot to cover idle/daily behavior, transactional dirty state, debounce, moves, restart retries, in-flight mutations, root outages/enrollment, strict acknowledgements, auth holds and unknown-delivery fencing.

Deployment uses a privately configured catalog API key. The ordered integration uses the deployed bot contract to recover safely from ambiguous delivery. A server-only change cannot stop the old deployed five-minute local-dl caller.
