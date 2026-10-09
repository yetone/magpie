# Offline Reasonix historical usage recovery

Requires Python 3.11+, standard library only. This is a manual recovery preview,
not part of Magpie startup or its normal session reader. No API/network calls,
credentials, provider refresh, migrations, source writes or process control.
Use a stable private copy of the state root if a writer is active. Outputs contain
private session IDs, paths and usage: do not attach them to a public issue.

## Exact reconciliation (default)

```sh
python3 tools/reasonix-history/history.py --root "$HOME/.reasonix" --out /path/to/new-private-output
```

Reads `*.turns.jsonl` with owner metadata and `stats/*.jsonl`. A full token/model
vector, identical Unix millisecond and unique retained receipt are required.
Repeated ledger identities are deduplicated; conflicting identities, invalid or
estimated token receipts and ambiguous joins are rejected. Outputs retain source
path/line/hash, receipts, attribution and unmatched rows. Malformed JSONL rows are
skipped; therefore this is not a format repair or proof of complete input. Ledger
and daily statistics describe the same consumption: never sum them together.

## Explicit approximate recovery

```sh
python3 tools/reasonix-history/history.py --root "$HOME/.reasonix" --out /path/to/new-private-output --allow-estimates --activity-index /path/to/reviewed-activity-index.json
```

The activity index is an explicitly supplied adapter input, not inferred from
conversation titles. Its JSON shape is:

```json
{"owners":[{"id":"session-id","key":"reasonix:session-id","path":"/state/session.jsonl","cwd":"/workspace","start":"2026-01-01T00:00:00Z","last":"2026-01-01T01:00:00Z","models":["provider/model"],"native":false}],"activities":[{"session_id":"session-id","at":"2026-01-01T00:30:00Z","kind":"turn/start"}]}
```

Supply final session identities and reliable committed turn/tool/message times.
Canonical decoding remains in Magpie; this Python tool does not decode RX4F or
Bolt databases or automatically export canonical activity. Review the index's
coverage first. No candidates cause failure rather than invented session IDs.

The policy first uses verified exact receipts, then a unique matching ledger
within one second, then model compatibility and nearest activity. Legacy lifetime
metadata is a weaker fallback. Explicit single-target imported aliases are
respected. Stable ties and nearby alternatives remain in the audit output.
`measured/high/medium/low` are evidence grades, not calibrated probabilities.
Model normalization currently targets DeepSeek Flash/Pro; review other providers.
Assignments conserve original token counters and original currency quote amounts.
Completion already includes reasoning; prompt already includes cache hits.

## Review and import boundary

`estimated/attributed-records.json` contains every ownership decision and its
alternatives; `session-allocations.json` and `summary.json` provide per-session
and global reconciliation. Review low-confidence decisions before import.
`reasonix-history-attribution.json` is a schema-1 candidate for the **local BYOK
recovery overlay**, not an importer supported by upstream Magpie or this PR.
The tool does not install it. No upstream import command is claimed.

The local overlay replaces native receipt totals on covered dates, attaches once
per identity, preserves message/activity data and labels ownership as estimated.
Removing it restores native accounting after cache invalidation. This is a dated
snapshot, not continuous recovery: later records on covered dates require a new
preview. Keep the previous overlay privately before replacing it. Do not add it
to native/gateway usage or copy one machine's usage history as synchronized config.
Prices shown by Magpie use its existing pricing policy; original daily quotes are
separate evidence, not invoices. Durable future attribution requires the producer
to persist session/request identities with usage receipts.

```sh
python3 -m unittest discover -s tools/reasonix-history -p 'test_*.py'
```
