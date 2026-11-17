#!/usr/bin/env bash
# pkg-shards.sh I N: reads import paths on stdin and prints the ones that go
# in share I (1..N) of N, so N runners can test them at once. macOS CI ran
# every package but the gateway in one step on a 3-core runner: ~2250s of
# package time took ~58 minutes of wall time, and the job reached its cap
# with no test failing (#6903).
#
# Each package goes to the share with the least time so far, the slowest
# first (longest processing time first). The seconds below are macOS CI's
# for the slow packages (run 44024695993, 11-17); any other package counts
# as 5s. A stale number only makes the shares less even: every package
# still lands in exactly one share, since every share computes the same
# split.
set -euo pipefail
i=$1 n=$2
awk -v want="$i" -v n="$n" '
BEGIN {
	split("library 338 gui 261 usage 261 plugin 239 agent 217 provider 204 claudetui 180 davsync 108 sessions 89 cli 76 redact 57 update 34", w, " ")
	for (k = 1; k in w; k += 2) secs["github.com/yetone/magpie/internal/" w[k]] = w[k + 1]
}
NF { p[++m] = $1; t[m] = ($1 in secs) ? secs[$1] : 5 }
END {
	if (want < 1 || want > n) { print "share " want " of " n "?" > "/dev/stderr"; exit 2 }
	# slowest first; ties by name, so every share sorts the same way
	for (a = 2; a <= m; a++) for (b = a; b > 1 && (t[b] > t[b-1] || (t[b] == t[b-1] && p[b] < p[b-1])); b--) {
		x = p[b]; p[b] = p[b-1]; p[b-1] = x; x = t[b]; t[b] = t[b-1]; t[b-1] = x
	}
	for (k = 1; k <= m; k++) {
		best = 1
		for (s = 2; s <= n; s++) if (load[s] < load[best]) best = s
		load[best] += t[k]
		if (best == want) print p[k]
	}
}'
