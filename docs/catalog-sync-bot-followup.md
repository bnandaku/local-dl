# Catalog ordering handoff completed

The bot supplied `ordered_catalog_v1` on the existing `/catalogUpdate` endpoint, and local-dl now implements the contract. The implementation remains entirely in local-dl; this session does not modify or deploy the bot.

- Source: `peaches-unraid`, persisted locally and bound on the bot to the dedicated integration key's stable ID.
- Headers: `X-Catalog-Source`, `X-Catalog-Sequence`, `X-Catalog-SHA256` (exact uncompressed JSON bytes).
- Bootstrap: authenticated `GET /api/v1/catalog/watermark?source_id=peaches-unraid`, allocating above both local sequence and server watermark.
- Retry: identical persisted payload and identity across timeouts, HTTP failures and restarts. Matching ordered acknowledgements release the old uncertainty fence.
- Stale sequence: reconcile watermark and schedule a new full scan; never claim obsolete data was acknowledged.
- Conflicting content/producer or invalid/oversized requests: durable operator hold. Authenticated manual scan can retry after repair; automatic resync cannot clear the hold.
- Key rotation must retain the existing key ID. A new source/key cannot silently replace the producer. Removing local configuration cannot downgrade a persisted ordered producer.

Regression tests cover lost response and restart replay, identity validation, source persistence, sequence advancement, watermark bootstrap/stale recovery, capability/auth gates, conflict holds, legacy outbox migration and uncertainty promotion. Existing transactional mutation, daily reconciliation and in-flight mutation tests remain in place.

See [catalog-sync.md](catalog-sync.md) for configuration, status and rollback behavior. No additional bot API changes are required for this integration.
