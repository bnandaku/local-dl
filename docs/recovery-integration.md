# Recovery integration status

Local-dl owns downloading, validation, durable library publication and the local recovery outbox. The Discord bot owns acquisition reservations, link fingerprints, search filtering, request state, Put.io source cleanup and Discord event delivery.

The contract reference is `../putio-go-server/docs/api/bot-api-specs-and-examples.json` (OpenAPI version 2.0.0, recovery contract 2). The root-level path previously supplied does not exist.

## Implemented locally

- Verify the authenticated JSON capabilities response before recovery mutations. A legacy 200 attachment is not compatibility evidence.
- Persist all accepted media jobs before acknowledging their upstream queue claim. Retain the existing `music-queue.json` path for compatibility.
- Require a local SHA-256 publication receipt before reporting a file as validated/published. Report, scope source cleanup to that file, then attempt group confirmation. Keep pending receipts across failures.
- Treat only the structured v2 `not_found` error as evidence of untracked historical success. HTML errors cannot authorize legacy cleanup.
- Persist invalid/missing-download reports, discard the mapped failed file using a stable event ID, then ask the bot to retry the recovery request. The bot owns the five-total-candidate limit; uncertain submissions and operational holds never trigger blind submission.
- Poll terminal ERROR transfers. Report tracked failures; generic failed transfers can be scoped-discarded. Preserve authentication/quota/network/storage failures for repair. Report unidentified transfers and expose durable identification tasks at authenticated `GET /recovery/status`.
- Skip/discard explicit unwanted supporting leaves through the bot, retaining recognized media and RAR volumes. The bot records removal events for Discord delivery.
- Provide a RAR extractor with complete-set naming validation, isolated declared volumes, all-volume listing, integrity testing, private extraction, path/link/special-file rejection, bounded expansion and typed intrinsic/operational failures.

## Still blocked on the bot contract

**RAR extraction is not wired into the download queue yet.** The bot currently excludes archives and does not supply the complete volume manifest or an extracted-file publication contract. Do not claim automatic multipart RAR recovery is available.

The bot session needs to define and implement:

1. Queue a complete archive volume set with stable attempt and archive-set IDs; each volume needs a file ID, filename, exact size and URL. Define when the set is complete, including a torrent whose root itself is an archive.
2. Treat the archive set as required primary input, not optional garbage. Define extracted-media identity and publication receipts when output files have no Put.io file IDs. Specify required output validation and safe library routing before successful set cleanup.
3. Provide scoped, idempotent cleanup for every volume after the required extracted outputs are published. Define bad-archive reporting (corrupt, missing volumes in an authoritative complete set, password locked, unsafe entries, no media) and replacement transitions. Missing local tools, transport failures, disk exhaustion and timeouts remain operational holds.
4. Advertise this support through capabilities and document request/response bodies and errors. Test direct archives, modern and legacy multipart sets, bad sets, restart/replay and one failed output among multiple extracted media files.
5. Verify actual Put.io transfer completion before accepting public `/dl` callbacks; reject an active/downloading transfer even when transfer/file IDs match.
6. Deploy the advertised API with the same `BOT_SERVICE_TOKEN` as local-dl. The live capabilities check on 2026-09-06 still returned HTTP 200 text/plain, so end-to-end recovery has not been validated or enabled on that deployment.

The historical quarantine review is private under ignored `reports/`. Do not bootstrap acquisitions from preliminary inventory counts: use canonical identity, primary media evidence and valid on-disk files; retain ambiguous entries for identification. No source URL should be invented for legacy items.

## Verification

Run `go test -race ./...` and `go vet ./...`. Install `unrar`, or set `UNRAR_TEST_TOOL` to its executable, to exercise the permanent synthetic fixtures in `testdata/rar/`. Fixture payloads intentionally are not valid media; these tests prove extraction byte accuracy, not the later media-validation step. The legacy-named fixture contains RAR5 data with `.rar/.r00` naming; it proves naming behavior, not RAR4 encoding compatibility.

Local-dl deployment and the automatic archive recovery claim remain pending the bot changes and a successful live integration check.
