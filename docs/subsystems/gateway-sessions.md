# Gateway Session Aggregation

The Sessions API combines file-backed native sessions with a read-only
projection of gateway usage records. A gateway session identity is exactly
`AgentOf(record.Agent) + record.Session`. Client conversation requests without a
session header receive a unique `request-…` identity when recording is enabled.
The response returns `X-Magpie-Session`; clients can send that value on later
requests to continue the same recorded session. Listed browser origins can
read this header through CORS. When recording is disabled, unidentified
requests receive no generated identity or response header. Generated
`request-…` identities are excluded from usage and Sessions projections.
Without reuse, each unidentified recorded request is a separate session: prompt text,
caller keys and network addresses are never used to guess conversation ownership.
Older ledger records without `Session` remain excluded. The projection is
computed from the usage ledger and is not persisted. Window projections and
routing summaries reuse versioned log snapshots; appends and rewrites invalidate
them, and price/provider changes also invalidate priced projections. Single-session
lookups use the cached identity index. Native exclusions are applied per caller.

When the same identity is present in a native session, the native session wins
and the gateway projection is omitted. Gateway projections have no path,
resume command, terminal action, or native-file delete capability. Their
models, token totals, start/last timestamps and effective prices come from the
same usage records used by Usage and stats endpoints.
All-time statistics include gateway history older than native files. Calendar
activity and output totals use each session's actual request dates, including
intermediate dates; extending the range rebases existing native calendar indices.

Implementation: [`internal/usage/gateway_sessions.go`](../../internal/usage/gateway_sessions.go),
[`internal/sessions/external.go`](../../internal/sessions/external.go), and
[`internal/gui/sessions.go`](../../internal/gui/sessions.go).

## Local Gateway Conversation Recording

The Sessions page offers an explicit, default-off recording switch. Confirmation
discloses that prompts, replies and tool results can contain private files and
code. `Settings.GatewayConversations` is machine-local consent:
`settings.SetGatewayConversations` updates it under the settings file lock.
Other Settings saves retain the latest stored consent under that lock, so an
older snapshot cannot re-enable recording after it is stopped. `KeepOwn`
also prevents sync/restore from enabling it.
`POST /api/sessions/recording` changes this switch; `clear: true` disables it and
erases only locally recorded gateway content, leaving usage and native files.
The existing browser-mode authentication guard protects these APIs; this is an
administrator view, not per-caller-key access control.

[`recordConversation`](../../internal/gateway/conversations.go) wraps
`serveAgent` outside protocol translation and gateway middleware. It records
one client-facing exchange, not each retry. Both explicit and generated
identities are recorded, using the same `usage.AgentOf` identity as the usage
projection. Codex's direct `/backend-api/codex/responses` relay is captured too.
Internal helper calls, token counting, embeddings, model listings, and compaction
paths that do not pass through these entry points are not recorded.
Generated identities live in request context, preserving the original headers
used by routing and prompt-cache affinity. Automatically assigned identities
are neither written to the usage ledger nor forwarded to remote Magpie instances.
Reused `request-…` headers are also excluded from the ledger and retain OTel
agent-level deduplication when no native session header exists. The existing protocol
readers handle Chat, Anthropic, Responses and Gemini; streaming replies use the
existing decoders, with completed Responses items retaining custom tool calls.
Only content actually sent through the gateway is available. Images are shown
as placeholders; hidden reasoning, client-only operations and unsupported wire
items cannot be reconstructed. This is not a raw request archive or remote
session synchronization, and existing S3 archives are not imported.

[`SaveGatewayTurn`](../../internal/sessions/gateway.go) stores normalized,
scrubbed exchanges in one append-only `turns.jsonl` per
`gateway-conversations/<UTC date>/<identity hash>/` under
the Magpie config directory, using 0700 directories and 0600 files. Each line is
one bounded turn; legacy per-turn JSON files remain readable until they expire.
An incomplete final append is omitted from reads (marked truncated) and removed
before the next append; complete earlier lines remain available. No headers are
stored. Known secrets and secret-named JSON
fields are scrubbed, but arbitrary sensitive text is not guaranteed removable.
Response capture uses an 8 MiB temporary spool; input is bounded to 8 MiB too.
Normalized turns are bounded to 1 MiB on disk, individual parts to 64 KiB,
and each daily session file to 256 MiB. A full daily session file rejects further
recording that day. Hourly background cleanup removes expired buckets and the
oldest files above 256 MiB; this is a soft store limit between cleanup passes.
Saving a turn does not rescan the whole store, and background traversal and
sorting do not hold the save lock.
Capture, parsing or persistence failure does not change the model response;
filesystem failures are logged without conversation bodies.

Content older than seven days is excluded from reads. The serving gateway
cleans up expired UTC-day buckets at startup and hourly, including with recording
off; the daily layout means physical cleanup can lag the read cutoff by one day.
Disable-and-clear and writes share a lock and recheck the consent generation,
preventing an in-flight request from refilling a cleared store even if recording
is enabled again before that request completes.

`GET /api/sessions/transcript` still prefers a matching native session. Otherwise
it reads a known gateway session's last 128 retained exchanges. Generated
recording identities do not create rows in Sessions, including for agents whose
native files provide their own sessions. A full exact
previous-history prefix is omitted from the next request, while repeated new
messages are preserved. System/tool-definition context is separately collapsed
when unchanged. Compacted, edited or incremental contexts may repeat material;
they are never guessed into a single canonical history. The existing transcript
display bounds apply and truncation is marked. Empty/expired/missing content
has an explicit empty state and all gateway transcripts identify their source.
The page invalidates pending transcript reads on reload or clear. A completed
read updates the current open transcript after redraw, and an older response
cannot replace content fetched after clearing the store.

Verification: `TestGatewayConversation*`, `TestGatewaySessionHistoryAndCalendar`,
`TestGatewaySessionsNativeWinsBeforeLimits`,
`TestGatewayStatsRebaseNativeCalendar`, and `TestCORSKeyThroughTheServer`; browser
`gateway-conversations.test.cjs`, `sessions-talk.test.cjs`, and locale checks.
