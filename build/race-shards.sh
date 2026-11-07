#!/usr/bin/env bash
# race-shards.sh DIR BIN N [FLAGS]: runs the tests of BIN, a package's test
# binary (go test -c), from the package's DIR in N processes at once, and
# fails if any test process does. FLAGS go to every process. A package's
# tests run one at a time unless they call t.Parallel, which t.Setenv rules
# out, so the gateway's suite under the race detector keeps one core busy;
# N processes keep N busy. Each process isolates its own home (the
# package's TestMain).
#
# The tests go in chunks of about RACE_CHUNK (default 32), and each of the
# N workers takes the next chunk when it is done with one. A
# fixed split of the tests into N shares left one share with the slow
# tests: on macOS CI the gateway's three took 12, 16 and over 20 minutes
# (#5361, #5320), while chunks taken in turn end within a chunk of each
# other. Each chunk is a process of its own, ~1s of start-up under -race.
set -euo pipefail
pkg=$1 bin=$2 n=$3
shift 3
size=${RACE_CHUNK:-32}
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
(cd "$pkg" && "$bin" -test.list '.*') | grep -E '^(Test|Example|Fuzz)' | sort >"$dir/all"
total=$(wc -l <"$dir/all" | tr -d ' ')
# the Nth test goes in chunk N mod m: tests named alike are often slow
# alike (TestCodex..., TestCompaction...), and spread they don't make one
# chunk that ends long after the rest
m=$(((total + size - 1) / size))
awk -v m="$m" -v d="$dir" '{f = sprintf("%s/c.%04d", d, NR % m); print >> f; close(f)}' "$dir/all"
printf '%s\n' "$dir"/c.* >"$dir/chunks"
echo "$total tests in $(wc -l <"$dir/chunks" | tr -d ' ') chunks of $size, $n at a time"

# run CHUNK DIR BIN FLAGS...: one chunk's tests; its log and its status and
# seconds go beside it.
cat >"$dir/run" <<'EOF'
#!/usr/bin/env bash
c=$1 pkg=$2 bin=$3
shift 3
re="^($(paste -sd'|' "$c"))\$"
start=$(date +%s)
if (cd "$pkg" && "$bin" -test.run "$re" "$@") >"$c.log" 2>&1; then st=ok; else st=FAIL; fi
echo "$st $(($(date +%s) - start))" >"$c.st"
EOF
chmod +x "$dir/run"

start=$(date +%s)
# a chunk whose process didn't finish has no status, and fails below
xargs -P "$n" -I{} "$dir/run" {} "$pkg" "$bin" "$@" <"$dir/chunks" || true
fail=0
while read -r c; do
	st=FAIL secs=?
	if [ -f "$c.st" ]; then read -r st secs <"$c.st"; fi
	echo "::group::${c##*/} $st ($(wc -l <"$c" | tr -d ' ') tests, ${secs}s): $(head -1 "$c") .. $(tail -1 "$c")"
	cat "$c.log" 2>/dev/null || true
	echo "::endgroup::"
	if [ "$st" != ok ]; then
		fail=1
		grep -E '^(--- FAIL|FAIL|panic|WARNING: DATA RACE)' "$c.log" 2>/dev/null || echo "${c##*/}: no status, its process didn't finish"
	fi
done <"$dir/chunks"
echo "$total tests in $(($(date +%s) - start))s; the slowest chunks:"
while read -r c; do
	if [ -f "$c.st" ]; then echo "$(cut -d' ' -f2 "$c.st")s ${c##*/}: $(head -1 "$c") .. $(tail -1 "$c")"; fi
done <"$dir/chunks" | sort -rn | head -5 || true
exit $fail
