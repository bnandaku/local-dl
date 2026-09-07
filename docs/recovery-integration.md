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

## Remaining integration work

**RAR extraction is not wired into local-dl's download queue yet.** The updated bot spec now includes `archive_sets_v1` and archive output receipts, and the live bot advertises archive sets and completed-transfer callbacks. The earlier complete-volume and public-callback contract gaps are addressed in that API revision; local archive orchestration still needs implementation and an end-to-end test.

The live bot now returns authenticated JSON recovery contract 2 and advertises `video_codec_policy` with `video_excluded_codecs: ["dolby_vision"]`. This matches local-dl's default. Local-dl `277e3ff` is deployed on Unraid; the recovery monitor completed without errors and codec-policy recovery is enabled. See [video-validation.md](video-validation.md) for validation scope and configuration.


The historical quarantine review is private under ignored `reports/`. Do not bootstrap acquisitions from preliminary inventory counts: use canonical identity, primary media evidence and valid on-disk files; retain ambiguous entries for identification. No source URL should be invented for legacy items.

## Verification

Run `go test -race ./...` and `go vet ./...`. Install `unrar`, or set `UNRAR_TEST_TOOL` to its executable, to exercise the permanent synthetic fixtures in `testdata/rar/`. Fixture payloads intentionally are not valid media; these tests prove extraction byte accuracy, not the later media-validation step. The legacy-named fixture contains RAR5 data with `.rar/.r00` naming; it proves naming behavior, not RAR4 encoding compatibility.

Automatic archive recovery remains pending local queue integration and a successful live integration check. Codec replacement also requires the advertised matching bot search policy.
