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

## Archive integration

The archive worker polls authenticated `/api/v1/archives` once per minute and persists accepted sets before processing. It downloads every declared volume into private staging, validates the complete set, extracts and declares all outputs, then validates primary media before publication. Audio uses the genre-based Music importer; video uses the existing ffprobe/decode policy. Video in a music request and episodic content in a movie request are rejected. See [archive-ingestion.md](archive-ingestion.md) for recovery and configuration.

The live bot now returns authenticated JSON recovery contract 2 and advertises `video_codec_policy` with `video_excluded_codecs: ["dolby_vision"]`. This matches local-dl's default. The previous deployed video-validation release was `277e3ff`; its recovery monitor completed without errors and codec-policy recovery was enabled. See [video-validation.md](video-validation.md) for validation scope and configuration.


The historical quarantine review is private under ignored `reports/`. Do not bootstrap acquisitions from preliminary inventory counts: use canonical identity, primary media evidence and valid on-disk files; retain ambiguous entries for identification. No source URL should be invented for legacy items.

## Verification

Run `go test -race ./...` and `go vet ./...`. Install `unrar`, or set `UNRAR_TEST_TOOL` to its executable, to exercise the permanent synthetic fixtures in `testdata/rar/`. The extractor fixtures test byte accuracy and rejection. `music-valid` and `video-valid` contain generated FLAC/video, cover art and support/garbage files; integration tests run the complete publication and cleanup path using real unrar, ffprobe and ffmpeg. The legacy-named fixture contains RAR5 data with `.rar/.r00` naming; it proves naming behavior, not RAR4 encoding compatibility.

Archive recovery uses the bot's current output-receipt API and five-attempt request workflow. Codec replacement requires the advertised matching bot search policy. Real Put.io transfer behavior is an operational test distinct from the synthetic end-to-end fixtures.
