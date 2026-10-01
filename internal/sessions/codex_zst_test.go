package sessions

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// The Codex app's "compress local chat history" packs older rollouts into
// rollout-….jsonl.zst (#448): they are read as the plain ones were, a
// rollout found in both forms counts once, and a session keeps its id and
// its usage when its file is packed.

// pack compresses a rollout to its .jsonl.zst, as the Codex app does, and
// takes the plain one away unless keep.
func pack(t *testing.T, path string, keep bool) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w, err := zstd.NewWriter(&out, zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatal(err)
	}
	w.Write(b)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Fatal("not a zstd frame")
	}
	fi, _ := os.Stat(path)
	z := path + ".zst"
	if err := os.WriteFile(z, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(z, fi.ModTime(), fi.ModTime())
	if !keep {
		os.Remove(path)
	}
	return z
}

func rollouts(t *testing.T, codex string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(filepath.Join(codex, "sessions"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			out = append(out, p)
		}
		return nil
	})
	if len(out) == 0 {
		t.Fatal("no rollouts")
	}
	return out
}

func codexOf(t *testing.T, ss []Session) Session {
	t.Helper()
	var found []Session
	for _, s := range ss {
		if s.Agent == "codex" {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one Codex session, got %d: %+v", len(found), found)
	}
	return found[0]
}

// same is a session as read, but for the file it was read from.
func same(s Session) Session {
	s.Path = strings.TrimSuffix(s.Path, ".zst")
	return s
}

func TestCodexPackedSessions(t *testing.T) {
	_, codex := setup(t)
	plain := codexOf(t, List(0))
	plainStats := StatsAt(0, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	files := rollouts(t, codex)

	// both forms side by side, as while the Codex app packs them: read once
	for _, p := range files {
		pack(t, p, true)
	}
	Reset()
	if both := codexOf(t, List(0)); !reflect.DeepEqual(both, plain) {
		t.Fatalf("both forms:\n got %+v\nwant %+v", both, plain)
	}

	// only the packed ones left: the same session, id, title and usage
	for _, p := range files {
		os.Remove(p)
	}
	packed := codexOf(t, List(0)) // read on from the kept parses of the plain ones
	if !reflect.DeepEqual(same(packed), plain) || !strings.HasSuffix(packed.Path, ".jsonl.zst") {
		t.Fatalf("packed:\n got %+v\nwant %+v", packed, plain)
	}
	if st := StatsAt(0, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)); !reflect.DeepEqual(st.Days, plainStats.Days) || len(st.Sessions) != len(plainStats.Sessions) {
		t.Fatalf("stats: got %+v, want %+v", st.Days, plainStats.Days)
	}
	Reset()
	if again := codexOf(t, List(0)); !reflect.DeepEqual(same(again), plain) {
		t.Fatalf("packed, read afresh:\n got %+v\nwant %+v", again, plain)
	}
	if got, ok := Find("codex", plain.ID); !ok || got.Tokens != plain.Tokens {
		t.Fatalf("find: %+v %v", got, ok)
	}
	m, ok := findManaged(ListAgent("codex"), plain.ID)
	if !ok || m.Files != len(files) || m.Messages == 0 {
		t.Fatalf("managed: %+v %v", m, ok)
	}
}

// A packed rollout's calls are the plain one's, each with its content; a
// call read before the file was packed still finds its content after.
func TestCodexPackedCalls(t *testing.T) {
	d := setupCalls(t)
	item := func(sec int, payload string) string {
		return `{"timestamp":"` + stamp(sec) + `","type":"response_item","payload":` + payload + `}`
	}
	lines := []string{`{"timestamp":"` + stamp(0) + `","type":"session_meta","payload":{"id":"t","session_id":"sess-x","cwd":"/work/z"}}`}
	lines = append(lines, cxTurnLines(1, "t1", "gpt-6-astra", "low")...)
	lines = append(lines,
		item(1, `{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files"}]}`),
		item(2, `{"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\"]}","call_id":"c1"}`),
		tokenCountLine(3, cxUse(1000, 0, 0, 50, 10), cxUse(1000, 0, 0, 50, 10)),
		item(4, `{"type":"function_call_output","call_id":"c1","output":"a.go\nb.go"}`),
		item(5, `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"There are two files."}]}`),
		tokenCountLine(6, cxUse(2500, 0, 0, 90, 10), cxUse(1500, 0, 0, 40, 0)),
	)
	path := filepath.Join(d.codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
	writeLines(t, path, lines...)
	plain := Calls(time.Time{})
	if len(plain) != 2 {
		t.Fatalf("%d calls", len(plain))
	}
	before, err := ContentOf(plain[0])
	if err != nil || len(before.Output) == 0 {
		t.Fatalf("content: %+v %v", before, err)
	}

	z := pack(t, path, true)
	if both := Calls(time.Time{}); !reflect.DeepEqual(both, plain) {
		t.Fatalf("both forms:\n got %+v\nwant %+v", both, plain)
	}
	os.Remove(path)
	// the call read from the plain file, its content from the packed one
	if c, err := ContentOf(plain[0]); err != nil || !reflect.DeepEqual(c, before) {
		t.Fatalf("content after packing: %+v %v", c, err)
	}
	got := Calls(time.Time{})
	if len(got) != len(plain) {
		t.Fatalf("packed: %d calls, want %d", len(got), len(plain))
	}
	for i := range got {
		if got[i].File != z {
			t.Fatalf("call %d read from %s", i, got[i].File)
		}
		g, p := got[i], plain[i]
		g.File, p.File = "", ""
		if !reflect.DeepEqual(g, p) {
			t.Fatalf("call %d:\n got %+v\nwant %+v", i, g, p)
		}
		c, err := ContentOf(got[i])
		if err != nil {
			t.Fatal(err)
		}
		if want, _ := ContentOf(plain[i]); !reflect.DeepEqual(c, want) {
			t.Fatalf("call %d's content:\n got %+v\nwant %+v", i, c, want)
		}
	}
	srcs := CallSources()
	if len(srcs) != 1 || srcs[0].Path != z {
		t.Fatalf("sources %+v", srcs)
	}
	if cs := ReadCallSource(srcs[0]); len(cs) != len(plain) {
		t.Fatalf("source calls %d", len(cs))
	}
}

// Deleting a session found in both forms moves both to the trash, so the
// packed one doesn't come back in its place; a restore puts both back.
func TestCodexPackedDelete(t *testing.T) {
	_, codex := manageSetup(t)
	files := rollouts(t, codex)
	for _, p := range files {
		z := pack(t, p, true)
		old := time.Now().Add(-24 * time.Hour)
		os.Chtimes(z, old, old)
	}
	if n := len(ListAgent("codex")); n != 1 {
		t.Fatalf("%d sessions", n)
	}
	tr, err := Delete("codex", mgCX)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Items) != 2*len(files) {
		t.Fatalf("moved %+v, want both forms of each", tr.Items)
	}
	if n := len(ListAgent("codex")); n != 0 {
		t.Fatalf("%d sessions left", n)
	}
	if _, err := Restore(tr.Key); err != nil {
		t.Fatal(err)
	}
	for _, p := range files {
		if _, err := os.Stat(p + ".zst"); err != nil {
			t.Fatal(err)
		}
	}
}
