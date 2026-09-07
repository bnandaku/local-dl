# Video playback validation

New video downloads are checked before publication. ffprobe reads actual stream codecs, dimensions, default-track flags and Dolby Vision configuration; filenames are not trusted. Local-dl selects the default non-cover video stream and a permitted audio track, preferring a permitted default track. An excluded audio track does not reject a release when a permitted fallback exists.

The same `VIDEO_EXCLUDED_CODECS` setting should be configured in local-dl and the bot. Default: `dolby_vision`. Supported exclusions: `dolby_vision,av1,hevc,h264,vp9,mpeg4,truehd,dts,eac3`; use `none` to disable codec exclusions. Invalid settings hold processing. This is an explicit exclusion policy, not a universal Plex-device compatibility profile.

Permitted files then undergo software decode checks with ffmpeg: up to 15 seconds at the start and, for files longer than 45 seconds, the midpoint and near the end. Checks share a three-minute deadline and use two decoder threads per job. They must produce decoded video frames. This catches sampled decoding failures; it does not guarantee the entire file is intact or that a particular Plex client can Direct Play it. ffprobe alone only inspects metadata. See the [ffprobe documentation](https://ffmpeg.org/ffprobe.html) and [Plex playback explanation](https://support.plex.tv/articles/200250387-streaming-media-direct-play-and-direct-stream/).

## Recovery

- Explicit corrupt-input/decode diagnostics enter existing `bad/invalid_media` recovery.
- An excluded codec is saved in the durable feedback outbox with the observed codec and `bad/wrong_content`: the copy violates the configured content policy; it is not described as corrupt.
- Before reporting or discarding a codec-rejected source, the bot must advertise `video_codec_policy` and include that codec in `video_excluded_codecs`. Otherwise the source and pending feedback remain available, visible through authenticated `/recovery/status`.
- Once aligned, local-dl reports the policy mismatch, discards that mapped candidate, and requests another using existing v2 endpoints. The bot owns source-fingerprint exclusion, filtered searches, Discord events and the five-total-attempt limit.
- Missing tools/decoders, timeouts, I/O errors and ambiguous failures remain operational holds. They do not poison source history.

The bot's `unsupported_codec` reason is currently an operational hold and is deliberately not used for a configured codec-policy rejection. The observed codec remains in local feedback; the current ordinary report API accepts a reason enum, not codec details.

Existing library files are not automatically deleted or replaced by this change. Resumed publication receipts retain their existing behavior. Testing playback on the actual target devices is still necessary.
