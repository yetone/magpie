package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// While the gateway writes, the log's change time is nearly always under a
// second old, so each read verifies the snapshot by a fingerprint (#1357).
// The parse itself produces that fingerprint: a rebuild re-reads the file only
// to verify the prefix it continues and to check nothing was rewritten during
// the read, not a third time before the read. Hashing doesn't allocate, so
// the allocation tests don't see this; this counts the bytes re-read.
func TestUsageAppendReadHashesNoPreReadScan(t *testing.T) {
	pageHome(t)
	holdClock(t, time.Now())
	historyLog(t, 20000)
	now := logIndexNow
	logIndexNow = func() time.Time { return time.Now().Add(2 * logStampSettle) }
	Summarize(Today) // settled first, as a log the gateway hasn't touched for a while
	logIndexNow = now
	t.Cleanup(func() { logIndexNow = now })
	hash := logRecordHash
	var hashed int64
	logRecordHash = func(path string, n int64) string {
		hashed += n
		return hash(path, n)
	}
	t.Cleanup(func() { logRecordHash = hash })
	const cycles = 5
	start := time.Now()
	for range cycles {
		Append(Record{Agent: "codex", Provider: "relay", Model: "m", Input: 1, Status: 200})
		Summarize(Today)
	}
	took := time.Since(start)
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	perCycle := hashed / cycles
	t.Logf("log %d bytes: %d bytes hashed per append + read (%.2f× the log), %v per cycle", info.Size(), perCycle, float64(perCycle)/float64(info.Size()), took/cycles)
	if s := Summarize(Today); s.Calls != 1+cycles {
		t.Fatalf("calls today: %d", s.Calls)
	}
	// An unsettled append verifies its prefix and re-hashes after the read:
	// two passes. A third, before the read, is the scan the parse replaces.
	if perCycle > 2*info.Size()+info.Size()/10 {
		t.Fatalf("an append + read hashed %d bytes over a %d-byte log, more than the prefix check and the after-read check", perCycle, info.Size())
	}
}

// Another writer can be mid-line when a read stats the log. The fingerprint
// covers only the complete lines parsed, so the line's completion continues
// the snapshot rather than counting the half line as read.
func TestUsagePartialLineThenCompletion(t *testing.T) {
	for _, settle := range []bool{false, true} {
		t.Run(fmt.Sprint("settled=", settle), func(t *testing.T) {
			pageHome(t)
			now := holdClock(t, time.Now())
			if settle {
				settleLogClock(t)
			}
			historyLog(t, logBlockRows+3)
			line, err := json.Marshal(Record{Time: now, Agent: "codex", Provider: "relay", Model: "m", Session: "late", Input: 41, Status: 200})
			if err != nil {
				t.Fatal(err)
			}
			line = append(line, '\n')
			half := len(line) / 2
			write := func(b []byte) {
				t.Helper()
				f, err := os.OpenFile(Path(), os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.Write(b); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			write(line[:half])
			before := Summarize(All)
			if s := readLogSnapshot(); s.hash != "" && s.hash != recordHash(Path(), s.off) {
				t.Fatal("fingerprint does not cover the parsed bytes")
			}
			write(line[half:])
			got, want := Summarize(All), summarize(All, now, Load(time.Time{}))
			if got.Totals != want.Totals || got.Calls != before.Calls+1 {
				t.Fatalf("completed line: got %+v (%d calls, %d before) want %+v", got.Totals, got.Calls, before.Calls, want.Totals)
			}
			equalPage(t, QueryPage(All, Filter{}, 0, 100), pageFromLedger(All, Filter{}, 0, 100, LedgerOf(All, Filter{})))
		})
	}
}
