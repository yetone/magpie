package sessions

import (
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// agyTimesConv is the conversation of testdata/agy-times.
const agyTimesConv = "85a28ef2-6195-4538-be65-666a8fa5f289"

// putAgyTimes writes testdata/agy-times into agy's data folder under a
// sandbox HOME: the transcript and the steps rows agy 1.5.9 wrote on this
// Mac for `agy -p "say hi"` against a fake Gemini endpoint that answered
// the request 2.01s after it came with its first chunk and ended the
// stream 4.03s after it came. Step 1 is the model's: created_at 19.939s,
// viewable_at 21.954s, finished_generating_at 23.971s past the minute.
func putAgyTimes(t *testing.T) {
	t.Helper()
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "home", ".gemini", "antigravity-cli")
	logs := filepath.Join(dir, "brain", agyTimesConv, ".system_generated", "logs")
	conv := filepath.Join(dir, "conversations")
	for _, d := range []string{logs, conv} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	transcript, err := os.ReadFile(filepath.Join("testdata", "agy-times", "transcript_full.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "transcript_full.jsonl"), transcript, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(conv, agyTimesConv+".db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(antigravitySchema); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "agy-times", "steps.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		idx, h, _ := strings.Cut(line, "\t")
		b, err := hex.DecodeString(strings.TrimSpace(h))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO steps (idx, step_type, metadata) VALUES (?, 15, ?)", idx, b); err != nil {
			t.Fatal(err)
		}
	}
	Reset()
}

// guyng35955 on X: Usage → Requests had no time to the first token, no
// duration and so no speed for agy's own calls. Each carries what its
// step's row says.
func TestAgyCallsCarryTheirTimes(t *testing.T) {
	putAgyTimes(t)
	got := agyCallsNow()
	if len(got) != 1 {
		t.Fatalf("want 1 call, got %d: %+v", len(got), got)
	}
	if c := got[0]; c.TTFT != 2014 || c.Millis != 4032 || c.Tokens != (Tokens{Input: 1200, Output: 30}) {
		t.Errorf("ttft %dms, %dms, %+v; want 2014ms, 4032ms, 1200 in 30 out", c.TTFT, c.Millis, c.Tokens)
	}
}

// A step whose call hasn't finished, or whose times are out of order, has
// none.
func TestStepTimesUnknown(t *testing.T) {
	ts := func(field uint64, sec int64) []byte {
		in := appendVarintField(nil, 1, uint64(sec))
		return appendBytesField(nil, field, in)
	}
	for name, b := range map[string][]byte{
		"no end":           append(ts(1, 100), ts(6, 101)...),
		"end before start": append(append(ts(1, 100), ts(6, 101)...), ts(7, 99)...),
		"no start":         append(ts(6, 101), ts(7, 102)...),
	} {
		if got := stepTimesOf(b); got != (stepTimes{}) {
			t.Errorf("%s: %+v, want none", name, got)
		}
	}
	// a first content after the end is not known; the duration is
	b := append(append(ts(1, 100), ts(6, 103)...), ts(7, 102)...)
	if got := stepTimesOf(b); got != (stepTimes{millis: 2000}) {
		t.Errorf("first after end: %+v, want 2000ms and no ttft", got)
	}
}

func appendVarintField(b []byte, field, v uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(b, field<<3), v)
}

func appendBytesField(b []byte, field uint64, d []byte) []byte {
	b = binary.AppendUvarint(b, field<<3|2)
	return append(binary.AppendUvarint(b, uint64(len(d))), d...)
}
