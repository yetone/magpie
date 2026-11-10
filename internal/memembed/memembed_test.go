package memembed

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/memory"
	"github.com/yetone/magpie/internal/pii/guard"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// fake reads a text as the topics it names, in either language: what a
// real model does for a search across languages, without its 135 MB.
type fake struct{ calls atomic.Int32 }

var topics = [][]string{
	{"release", "ship", "上线", "发布"},
	{"tabs", "indent", "缩进"},
	{"port", "端口"},
}

func (f *fake) Embed(text string) []float32 {
	f.calls.Add(1)
	v := make([]float32, guard.EmbedDim)
	v[len(v)-1] = 0.5 // something in common, as real texts have
	for i, words := range topics {
		for _, w := range words {
			if strings.Contains(strings.ToLower(text), w) {
				v[i] = 1
			}
		}
	}
	var sq float64
	for _, x := range v {
		sq += float64(x) * float64(x)
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(sq))
	}
	return v
}

// sandbox points memembed and memory at a folder of the test's own, with
// the model's files on disk at their sizes (sparse) and a fake model.
func sandbox(t *testing.T, installed bool) *fake {
	t.Helper()
	d := t.TempDir()
	oldRoot, oldState, oldMem, oldLoad := Root, StatePath, memory.Path, loadModel
	Root = func() string { return filepath.Join(d, "model") }
	StatePath = func() string { return filepath.Join(d, "memory-semantic.json") }
	memory.Path = func() string { return filepath.Join(d, "memory.json") }
	f := &fake{}
	loadModel = func(string) (embedder, error) { return f, nil }
	t.Cleanup(func() {
		Root, StatePath, memory.Path, loadModel = oldRoot, oldState, oldMem, oldLoad
		unload()
		store.mu.Lock()
		store.v = nil
		store.mu.Unlock()
	})
	if installed {
		if err := os.MkdirAll(dir(), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, p := range files {
			path := filepath.Join(dir(), filepath.Base(p.path))
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(path, p.size); err != nil {
				t.Fatal(err)
			}
		}
		b := []byte(`{"on":true}`)
		if err := os.WriteFile(StatePath(), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func save(t *testing.T, text, status string) *memory.Memory {
	t.Helper()
	k, sc := memory.Fact, memory.Global
	m, _, err := memory.Save(memory.Input{Text: &text, Kind: &k, Scope: &sc, Status: &status}, memory.FromUser, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func ids(hs []memory.Hit) []string {
	out := []string{}
	for _, h := range hs {
		out = append(out, h.Text)
	}
	return out
}

// A search finds what means the same in other words and another language,
// beside what holds its words; switched off, by words alone.
func TestSearchFindsByMeaning(t *testing.T) {
	sandbox(t, true)
	save(t, "Releases go out on Tuesdays", memory.Active)
	save(t, "Use tabs, never spaces", memory.Active)
	save(t, "A busy port is someone else's", memory.Active)
	hits, err := memory.Search(memory.Query{Text: "什么时候上线"})
	if err != nil || len(hits) != 1 || hits[0].Text != "Releases go out on Tuesdays" {
		t.Fatalf("%v %v", ids(hits), err)
	}
	// found both ways ahead of found one way: "never" is in one, and
	// both mean 缩进
	save(t, "Indent Go files with tabs", memory.Active)
	hits, _ = memory.Search(memory.Query{Text: "never 缩进"})
	if got := ids(hits); len(got) != 2 || got[0] != "Use tabs, never spaces" || got[1] != "Indent Go files with tabs" {
		t.Fatalf("%v", got)
	}
	// one common word ranks as high as the nearest in meaning; the nearer
	// in meaning goes first, though the other is newer
	save(t, "Never delete images when freeing disk space", memory.Active)
	hits, _ = memory.Search(memory.Query{Text: "when do we release"})
	if got := ids(hits); len(got) != 2 || got[0] != "Releases go out on Tuesdays" {
		t.Fatalf("%v", got)
	}
	if err := Set(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Root()); !os.IsNotExist(err) {
		t.Fatalf("the model is still there: %v", err)
	}
	if hits, _ := memory.Search(memory.Query{Text: "什么时候上线"}); len(hits) != 0 {
		t.Fatalf("off, by meaning: %v", ids(hits))
	}
}

// Index reads what a search can return and has no vector for its text:
// once, again when its text changes, and the old text's vector goes. The
// vectors are kept on disk, and a file cut short is read as none.
func TestIndexReadsOnce(t *testing.T) {
	f := sandbox(t, true)
	a := save(t, "Releases go out on Tuesdays", memory.Active)
	save(t, "Use tabs", memory.Candidate)
	save(t, "Old port rule", memory.Archived)
	if n, err := Index(context.Background()); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if n, _ := Index(context.Background()); n != 0 || f.calls.Load() != 2 {
		t.Fatalf("read again: %d, %d calls", n, f.calls.Load())
	}
	text := "Releases go out on Wednesdays"
	if _, err := memory.Edit(a.ID, memory.Input{Text: &text}, true); err != nil {
		t.Fatal(err)
	}
	if n, _ := Index(context.Background()); n != 1 {
		t.Fatalf("edited: %d", n)
	}
	if st := Now(); st.Read != 2 || st.Of != 2 || !st.On || !st.Installed {
		t.Fatalf("%+v", st)
	}
	path := filepath.Join(dir(), "vectors.bin")
	if got := len(readVecs(path)); got != 2 {
		t.Fatalf("on disk: %d", got)
	}
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, b[:len(b)-1], 0o644); err != nil {
		t.Fatal(err)
	}
	if got := len(readVecs(path)); got != 0 {
		t.Fatalf("cut short: %d", got)
	}
}

// With the model not on disk, or a switch file that can't be read, a
// search is by words and nothing is loaded.
func TestNotThereIsWords(t *testing.T) {
	f := sandbox(t, false)
	save(t, "Releases go out on Tuesdays", memory.Active)
	if hits, _ := memory.Search(memory.Query{Text: "上线"}); len(hits) != 0 || f.calls.Load() != 0 {
		t.Fatalf("%v, %d calls", ids(hits), f.calls.Load())
	}
	if err := os.WriteFile(StatePath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if On() {
		t.Fatal("a broken switch file is on")
	}
}
