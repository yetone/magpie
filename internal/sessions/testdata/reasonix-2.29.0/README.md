# Reasonix 2.29.0 native fixture

Captured 2026-10-07 from the published `@reasonix/cli-darwin-arm64@2.29.0`
using `reasonix serve` in an empty HOME/REASONIX_HOME, isolated workspace and
a loopback fake OpenAI backend. Two POST /submit prompts: `hello there`,
`follow up`. Each upstream reply was `hello back` with prompt=1234,
completion=56, cached=1000. Provider/model: fake/fake-model.

Files are native output, including the system message, wrapped user content
and raw_content, assistant lines with no createdAt, metadata without
workspace_root, the telemetry totals and real wire frames. No turn ledger.
Normalizations: sandbox workspace path → /work/reasonix-229; writer_id →
fixture-host. Other fields and timestamps are retained. content_digest is the
native pre-normalization digest; this adapter does not verify that field.

Telemetry expected: prompt=2468, completion=112, cache hit=2000,
uncached input=468, requests=2. Telemetry is not added to wire usage.
The .wire.meta.json truncation marker is added only by truncation tests;
the complete run did not write one.

`resumed.*` comes from a second real 2.29.0 serve process using --resume,
with the same sandbox state/workspace and one `resume follow up` prompt.
It retains three wire usage frames (seq restarts at 1), while telemetry
resets to prompt=1234/completion=56/cache-hit=1000/requests=1. Expected retained
session usage is uncached input=702/output=168/cache-read=3000, with three
prompts/replies. This is an actual process-resume capture, not concatenated
frames. The same path/writer normalizations apply.
