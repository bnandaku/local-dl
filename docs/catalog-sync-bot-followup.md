# Bot follow-up: ordering full catalog replacements

The existing `POST /catalogUpdate` contract replaces the whole catalog without a generation check. A timed-out request can still commit after a retry and a subsequent newer snapshot. Client-side serialization cannot establish the ordering of an unacknowledged server handler.

Please implement durable ordering on the existing endpoint; no delta endpoint is needed. Proposed contract for the bot session to confirm:

- Scope one catalog producer to a stable source ID and authenticated principal. Persist a monotonically increasing snapshot sequence and SHA-256 for the accepted full snapshot.
- Carry source ID, sequence and SHA-256 of the uncompressed request bytes in headers (please document final names). Each captured local snapshot gets one sequence; retries send identical bytes and headers. Sequence is distinct from the local dirty generation because daily reconciliations also need ordering.
- Atomically compare sequence and commit the replacement catalog plus accepted sequence/hash in the same database transaction. Lower sequences must never replace newer data. Equal sequence plus matching hash is an idempotent success; equal sequence with different content is a conflict.
- Return source/sequence/hash in the verified JSON acknowledgement. Document stale/conflict errors. Advertise ordered catalog support in authenticated capabilities.
- Provide the current accepted sequence/hash through an authenticated read so a restarted/new local DB can initialize its next sequence above the server watermark. Reject competing/unauthorized producers. Do not allow legacy unordered requests to bypass fencing once ordered sync is enrolled.
- Tests: delay request N, accept retry N, accept N+1, then release the delayed N; N+1 must remain authoritative. Cover crash between replacement and watermark, identical replay, conflicting replay, new-client DB bootstrap and key rotation/source identity.

Until that contract is deployed and verified, local-dl fences newer snapshots after an unknown transport outcome. It retains/retries only the captured snapshot, even if a later duplicate receives HTTP 200. This is safe but cannot provide automatic forward progress after uncertainty; do not claim the legacy endpoint can guarantee ordering.

The rest of event-driven scheduling, durable dirty generations, daily reconciliation, root-outage checks, strict bearer/JSON validation and serialized retries is implemented locally. The bearer key is configured privately; no token belongs in the handoff.
