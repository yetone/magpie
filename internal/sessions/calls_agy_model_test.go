package sessions

import (
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// agyModelConv is the conversation of testdata/agy-db.
const agyModelConv = "7b4b53b1-0bb4-4391-b86e-680cea68e587"

// agyModelTranscript is a conversation's transcript: the user's step, the
// model's step parkavenue9639 matched to the generator on #3147 (step 1,
// 11519 in, 0 from the cache, 781 out), a tool's result, and a second
// model's step.
const agyModelTranscript = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-12T07:24:44Z","content":"<USER_REQUEST>\nlist the folder\n</USER_REQUEST>"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-12T07:24:50Z","input_tokens":11519,"cache_read_tokens":0,"output_tokens":781,"tool_calls":[{"name":"run_command","args":{}}]}
{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-12T07:25:02Z","input_tokens":900,"cache_read_tokens":100,"output_tokens":40,"content":"Done."}
`

// putAgyModelTranscript writes the transcript into agy's data folder under
// a sandbox HOME, and gives that folder.
func putAgyModelTranscript(t *testing.T) string {
	t.Helper()
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "home", ".gemini", "antigravity-cli")
	logs := filepath.Join(dir, "brain", agyModelConv, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "transcript_full.jsonl"), []byte(agyModelTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	Reset()
	return dir
}

// putAgyModelDB writes the conversation's database, in WAL mode as agy
// keeps it, in the layout agy 1.5.7 made on this Mac (the Antigravity
// app's, antigravitySchema), from testdata/agy-db:
//
//   - gen_metadata.tsv: generator 0, parkavenue9639's real agy 1.5.6 row
//     on #3147 (ids overwritten with x at the same length). Its field 1.19
//     is "gemini-4-argon", and its "last_step_index" "0" says it answered
//     step 1.
//   - steps.tsv: the two steps.metadata rows agy 1.5.7 wrote here (a
//     user's step, whose place names no generator, so 0; and a step placed
//     at generator 1), the second written as step 2.
//
// Generator 1 is made here: field 1.19 "gemini-3.8-flash-n", the
// deployment name the Antigravity app's rows carry (#2190), with a
// last_step_index that names no step of the transcript. Step 1 has no
// steps row, so its generator's own last_step_index names its model.
func putAgyModelDB(t *testing.T, dir string) {
	t.Helper()
	conv := filepath.Join(dir, "conversations")
	if err := os.MkdirAll(conv, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(conv, agyModelConv+".db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(antigravitySchema); err != nil {
		t.Fatal(err)
	}
	rows := func(name string) [][]byte {
		raw, err := os.ReadFile(filepath.Join("testdata", "agy-db", name))
		if err != nil {
			t.Fatal(err)
		}
		var out [][]byte
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			_, h, _ := strings.Cut(line, "\t")
			b, err := hex.DecodeString(strings.TrimSpace(h))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, b)
		}
		return out
	}
	gen0 := rows("gen_metadata.tsv")[0]
	steps := rows("steps.tsv")
	field := func(n uint64, b []byte) []byte {
		out := binary.AppendUvarint(nil, n<<3|2)
		return append(binary.AppendUvarint(out, uint64(len(b))), b...)
	}
	kv := append(field(1, []byte("last_step_index")), field(2, []byte("40"))...)
	gen1 := field(1, append(field(19, []byte("gemini-3.8-flash-n")), field(20, kv)...))
	for i, g := range [][]byte{gen0, gen1} {
		if _, err := db.Exec("INSERT INTO gen_metadata (idx, data, size) VALUES (?, ?, ?)", i, g, len(g)); err != nil {
			t.Fatal(err)
		}
	}
	for i, idx := range []int{0, 2} {
		if _, err := db.Exec("INSERT INTO steps (idx, step_type, metadata) VALUES (?, 15, ?)", idx, steps[i]); err != nil {
			t.Fatal(err)
		}
	}
}

func agyCallsNow() []Call {
	var got []Call
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "agy" {
			got = append(got, c)
		}
	}
	slices.SortFunc(got, func(a, b Call) int { return a.Time.Compare(b.Time) })
	return got
}

// DvnT662 on X (#3147, #4805): Usage → Requests showed agy's own calls
// with no model. Each names the model its conversation's database names
// for its step.
func TestAgyCallsNameTheirModel(t *testing.T) {
	dir := putAgyModelTranscript(t)
	putAgyModelDB(t, dir)
	got := agyCallsNow()
	want := []struct {
		model  string
		tokens Tokens
	}{
		{"gemini-4-argon", Tokens{Input: 11519, Output: 781}},
		{"gemini-3.8-flash", Tokens{Input: 900, CacheRead: 100, Output: 40}},
	}
	if len(got) != len(want) {
		t.Fatalf("want %d calls, got %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i].Model != w.model || got[i].Tokens != w.tokens {
			t.Errorf("call %d: %q %+v, want %q %+v", i, got[i].Model, got[i].Tokens, w.model, w.tokens)
		}
	}
}

// The database written after the transcript was read (agy writes its
// generator's row as the step ends): the calls, and the session's tokens,
// are read again under the model.
func TestAgyModelWrittenLater(t *testing.T) {
	inZone(t, 0)
	dir := putAgyModelTranscript(t)
	for _, c := range agyCallsNow() {
		if c.Model != "" {
			t.Fatalf("no database yet, but call names %q", c.Model)
		}
	}
	s := find(t, List(0), "agy", agyModelConv)
	if m := model(s, "agy"); m.Tokens != (Tokens{Input: 12419, CacheRead: 100, Output: 821}) {
		t.Fatalf("before the database: %+v", s.Models)
	}

	putAgyModelDB(t, dir)
	got := agyCallsNow()
	if len(got) != 2 || got[0].Model != "gemini-4-argon" || got[1].Model != "gemini-3.8-flash" {
		t.Fatalf("after the database: %+v", got)
	}
	s = find(t, List(0), "agy", agyModelConv)
	if m := model(s, "gemini-4-argon"); m.Tokens != (Tokens{Input: 11519, Output: 781}) {
		t.Fatalf("gemini-4-argon: %+v", s.Models)
	}
	if m := model(s, "gemini-3.8-flash"); m.Tokens != (Tokens{Input: 900, CacheRead: 100, Output: 40}) {
		t.Fatalf("gemini-3.8-flash: %+v", s.Models)
	}
	if m := model(s, "agy"); m.Tokens != (Tokens{}) {
		t.Fatalf("still counted under agy: %+v", s.Models)
	}
}

// A database that isn't one (agy_manage_test's stand-in) names no model,
// and the calls are read as before.
func TestAgyUnreadableDB(t *testing.T) {
	dir := putAgyModelTranscript(t)
	conv := filepath.Join(dir, "conversations")
	os.MkdirAll(conv, 0o755)
	if err := os.WriteFile(filepath.Join(conv, agyModelConv+".db"), []byte("SQLite format 3\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := agyCallsNow()
	if len(got) != 2 || got[0].Model != "" || got[1].Model != "" {
		t.Fatalf("%+v", got)
	}
}
