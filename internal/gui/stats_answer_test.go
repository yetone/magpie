package gui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

// statsAnswer writes the stats answer a day at a time, and the answer is
// byte for byte what the Encoder of the whole gave: the page's tag rests
// on those bytes. A field added to Stats shows here as a difference.
func TestStatsAnswerWritesWhatEncodeDid(t *testing.T) {
	// Active's hours are sessions' own type, so they come in as JSON does.
	var active sessions.Active
	if err := json.Unmarshal([]byte(`{"agent":"claude","cwd":"引号\"","seconds":61,"hours":[1,2]}`), &active); err != nil {
		t.Fatal(err)
	}
	day := func(date string) sessions.Day {
		return sessions.Day{Date: date,
			Usage:  []sessions.Usage{{Agent: "codex", Cwd: "/a<b>&c", Model: "gpt x", Tokens: sessions.Tokens{Input: 3, CacheWrite1h: 1}, Cost: 1e21, Priced: true}},
			Active: []sessions.Active{active}}
	}
	for _, c := range []struct {
		st     sessions.Stats
		agents map[string]string
	}{
		{sessions.Stats{}, nil},
		{sessions.Stats{From: "2026-10-01", To: "2026-10-30", Days: []sessions.Day{}, Keys: []string{}}, map[string]string{}},
		{sessions.Stats{From: "<", To: "&", Days: []sessions.Day{day("2026-10-29"), {Date: "2026-10-30"}, day("x")}, Keys: []string{"codex:1", "a<b"}, Sessions: []sessions.Summary{{Key: "hidden"}}}, map[string]string{"codex": "Codex", "z": "<Z>"}},
	} {
		var want bytes.Buffer
		if err := json.NewEncoder(&want).Encode(struct {
			sessions.Stats
			Agents map[string]string `json:"agents"`
		}{c.st, c.agents}); err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := statsAnswer(&got, c.st, c.agents); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("\n got %s\nwant %s", got.Bytes(), want.Bytes())
		}
	}
}

// writeSame holds no answer-sized buffer, and keeps none for the next:
// the stats' answers were 12.5 MB of a warm start's live heap (#2278).
// What it sends, and its tag, are the bytes write gave.
func TestSameAnswerNotHeldWhole(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 4<<20)
	write := func(w io.Writer) error {
		for i := 0; i < len(big); i += 64 << 10 {
			if _, err := w.Write(big[i : i+64<<10]); err != nil {
				return err
			}
		}
		return nil
	}
	w := httptest.NewRecorder()
	tag := writeSame(w, httptest.NewRequest("GET", "/x", nil), write) // warm
	sum := sha256.Sum256(big)
	if !bytes.Equal(w.Body.Bytes(), big) || tag != hex.EncodeToString(sum[:16]) {
		t.Fatalf("answer of %d bytes, tag %q: not what write gave", w.Body.Len(), tag)
	}
	var before, after runtime.MemStats
	w = httptest.NewRecorder()
	w.Body.Grow(len(big) + 1<<20) // the recorder's copy isn't magpie's
	runtime.ReadMemStats(&before)
	writeSame(w, httptest.NewRequest("GET", "/x", nil), write)
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > uint64(len(big)/4) {
		t.Fatalf("writeSame allocated %d bytes for a %d-byte answer", got, len(big))
	}
	same := httptest.NewRequest("GET", "/x", nil)
	same.Header.Set("X-Magpie-Had", tag)
	w = httptest.NewRecorder()
	writeSame(w, same, write)
	if w.Header().Get("X-Magpie-Same") != "1" || w.Body.Len() != 0 {
		t.Fatalf("asked with its own tag: same %q, %d bytes", w.Header().Get("X-Magpie-Same"), w.Body.Len())
	}
}

// writeJSONRows holds no answer-sized buffer: the list goes out a row at a
// time, and the whole answer was 25 MB of a warm start's live heap (#2278).
func TestJSONRowsNotHeldWhole(t *testing.T) {
	if raceOn {
		t.Skip("-race: sync.Pool drops what it holds, so allocations are not what a build allocates")
	}
	big := string(bytes.Repeat([]byte("x"), 64<<10))
	rows := make([]rowsBufRow, 64)
	for i := range rows {
		rows[i] = rowsBufRow{Name: big, N: float64(i)}
	}
	w := httptest.NewRecorder()
	writeJSONRows(w, httptest.NewRequest("GET", "/x", nil), "rows", rows, struct{}{}) // warm
	var before, after runtime.MemStats
	w = httptest.NewRecorder()
	w.Body.Grow(len(rows)*len(big) + 1<<20) // the recorder's copy isn't magpie's
	runtime.ReadMemStats(&before)
	writeJSONRows(w, httptest.NewRequest("GET", "/x", nil), "rows", rows, struct{}{})
	runtime.ReadMemStats(&after)
	answer := uint64(w.Body.Len())
	if got := after.TotalAlloc - before.TotalAlloc; got > answer/4 {
		t.Fatalf("writeJSONRows allocated %d bytes for a %d-byte answer", got, answer)
	}
}
