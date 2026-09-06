# Peachesbot integration

The machine-readable contract is [bot-api-specs-and-examples.json](bot-api-specs-and-examples.json). It describes the bot server, not local-dl's own music API. The matching source lives in the putio-go-server repository; update both copies when the contract changes.

Set `REMOTE_SERVER` to the bot's private HTTP address and set the same `BOT_SERVICE_TOKEN` in both containers. Use `Authorization: Bearer <token>` on every authenticated request (the spec marks legacy public routes explicitly). Never log queue URLs, tokens, or private tracker URLs. Existing `/catalogUpdate` and `/update` calls also require this bearer token.

## Validation and replacement workflow

1. Poll `POST /queue`. Download the file completely, verify its expected size, validate its media streams, and publish it durably before acknowledging completion with `/updateQueue`.
2. Resolve its history ID with `GET /api/v1/link-files/{file_id}`. A 404 means an older untracked download or an already-cleaned group.
3. After successful publication, `POST /api/v1/links/{id}/report` with `{"status":"validated","file_id":123}`. The attempt remains `queued` until every primary file validates. This does not clear history.
4. For confirmed invalid content, report `{"status":"bad","reason":"invalid_media","file_id":123}` instead. Reasons are `no_media`, `invalid_media`, `wrong_content`, and `malware`. Do not report partial transfers, network failures, storage errors, missing ffprobe, or probe timeouts as bad content. Supporting artwork/subtitles cannot blacklist a whole release.
5. Call `POST /api/v1/links/{bad_id}/retry` with `{}`. The bot performs the Prowlarr search and automatically submits the best matching eligible replacement to Put.io. local-dl must not choose or resubmit a raw torrent URL. For a direct download without a search, supply `{"query":"Example Movie 2025","media_type":"movie"}`. TV replacements require a specific episode in the query.
6. Retain the returned replacement ID. Polling `/queue` will deliver its files through the normal pipeline. If that replacement is also bad, report and retry its new ID. There are at most three automatic replacements per original group.
7. After confirming that the media is actually wanted and good, call `POST /api/v1/links/{good_id}/confirm`. All good and bad attempt history and group blacklists are removed together; media files are unchanged. A human can instead use `!link confirm <id>` in Discord.

### Safe retries

Persist reports before sending them. The implemented outbox defaults to `link-feedback.json` alongside `CATALOG_DB`; set `LINK_FEEDBACK_PATH` to override. Mount that directory persistently. It retries with the ingest loop. Content validation does not prove that a movie has the right plot or that an album contains the expected performance; those cases need human review or a separate content classifier.

A timed-out replacement request might already have submitted to Put.io. Repeat the **same bad attempt ID**; the server returns its existing replacement rather than submitting twice. A response with `uncertain` or `submitting` needs operator reconciliation with Put.io. Do not create a new request to bypass that protection. A 409 means another operation is active or the state is incompatible; read current history before retrying. A 400 explains missing query/type, no eligible candidate, or the replacement limit; correct that condition before retrying. Authentication failures require fixing configuration.

The automatic local-dl integration reports validated files but leaves confirmation to `!link confirm` or an explicit API caller. It emits no per-file Discord messages. The bot announces bad-state changes, accepted replacements, and confirmation cleanup once per state change. Discord outages do not roll back persistent API state.

## Commands and API operations

| Discord | API |
| --- | --- |
| `!links [after-id]` | `GET /api/v1/links?after=0&limit=50` |
| Read one attempt | `GET /api/v1/links/{id}` |
| Report a bad or validated download | `POST /api/v1/links/{id}/report` |
| `!link retry <bad-id>` | `POST /api/v1/links/{bad-id}/retry` |
| `!link confirm <good-id>` | Report validated, then `POST /api/v1/links/{good-id}/confirm` |

`link` fields are safe display values, not reusable private torrent URLs. The bot retains the original URL internally and keys magnet blacklists by infohash. Confirmation removes those records; only numeric confirmation receipts remain for seven days to support safe repeat calls. The API credential is a trusted downloader credential with access across groups. Ordinary Discord members only see their own requests; bot admins see server history.
