package provider

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
)

// sarosaka1023 on X: providers.json edited by hand while magpie runs, with
// a slip in it, was read from its last good generation, and the next save
// of magpie's wrote that, with the save's change, over the edit. The edit
// is the user's: the save is refused, saying why, the file left as it was,
// magpie runs on the last good copy, and the edit, fixed, is what the
// next save keeps.
func TestBrokenHandEditNotWrittenOver(t *testing.T) {
	providerFileHome(t)
	lastgood.Reset()
	t.Cleanup(lastgood.Reset)
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: strings.ToUpper(id), Chat: "https://" + id + ".example.invalid/v1", Key: "sk-" + id, Models: []string{"m"}}); err != nil {
			t.Fatal(err)
		}
	}
	// the edit: a's name changed, a provider added, and a comma left over
	edit := []byte(`{"providers":[
  {"id":"a","name":"Mine","chat":"https://a.example.invalid/v1","key":"sk-a","models":["m"]},
  {"id":"hand","name":"Hand","chat":"https://hand.example.invalid/v1","key":"sk-hand","models":["m"]},
]}
`)
	if err := os.WriteFile(Path(), edit, 0o600); err != nil {
		t.Fatal(err)
	}
	err := Save(Provider{ID: "c", Name: "C", Chat: "https://c.example.invalid/v1", Key: "sk-c", Models: []string{"m"}})
	if err == nil || !strings.Contains(err.Error(), Path()) || !strings.Contains(err.Error(), "left it unchanged") {
		t.Errorf("a save over a broken edit: %v", err)
	}
	if got, _ := os.ReadFile(Path()); !bytes.Equal(got, edit) {
		t.Fatalf("the edit was written over:\n%s", got)
	}
	// what runs meanwhile is the last good copy, and the user is told
	if _, err := Find("a"); err != nil {
		t.Errorf("the last good copy isn't used: %v", err)
	}
	if n := lastgood.Recovered(); len(n) != 1 || n[0].File != "providers.json" {
		t.Errorf("not told: %+v", n)
	}
	// fixed, the edit is read and kept by the next save
	fixed := bytes.Replace(edit, []byte("},\n]"), []byte("}\n]"), 1)
	if err := os.WriteFile(Path(), fixed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "c", Name: "C", Chat: "https://c.example.invalid/v1", Key: "sk-c", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range f.Providers {
		ids = append(ids, p.ID+"="+p.Name)
	}
	if got := strings.Join(ids, ","); got != "a=Mine,hand=Hand,c=C" {
		t.Errorf("after the fixed edit and a save: %s", got)
	}
}

// A file a crash left empty or all zero (#1505) is no edit: with a last
// good generation it is still saved over, kept aside first.
func TestCrashedFileStillSavedOver(t *testing.T) {
	for name, junk := range map[string][]byte{"zeroed": make([]byte, 300), "empty": nil, "blank": []byte("\n  \n")} {
		t.Run(name, func(t *testing.T) {
			providerFileHome(t)
			lastgood.Reset()
			t.Cleanup(lastgood.Reset)
			for _, id := range []string{"a", "b"} {
				if err := Save(Provider{ID: id, Chat: "https://" + id + ".example.invalid/v1", Key: "sk-" + id}); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(Path(), junk, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := Save(Provider{ID: "c", Chat: "https://c.example.invalid/v1", Key: "sk-c"}); err != nil {
				t.Fatalf("a save over a crashed file with a backup: %v", err)
			}
			if _, err := Find("c"); err != nil {
				t.Error(err)
			}
		})
	}
}

// A valid edit made by hand while magpie runs is read at once and kept
// by magpie's next save.
func TestHandEditReadAndKept(t *testing.T) {
	providerFileHome(t)
	if err := Save(Provider{ID: "a", Name: "A", Chat: "https://a.example.invalid/v1", Key: "sk-a", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	edit := `{"providers":[{"id":"a","name":"Mine","chat":"https://a.example.invalid/v1","key":"sk-a","models":["m","m2"]},{"id":"hand","name":"Hand","chat":"https://hand.example.invalid/v1","key":"sk-hand","models":["m"]}]}`
	if err := os.WriteFile(Path(), []byte(edit), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("hand"); err != nil || p.Name != "Hand" {
		t.Fatalf("the edit isn't read: %v", err)
	}
	if err := Save(Provider{ID: "c", Name: "C", Chat: "https://c.example.invalid/v1", Key: "sk-c", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{"a": "Mine", "hand": "Hand", "c": "C"} {
		if p, err := Find(id); err != nil || p.Name != name {
			t.Errorf("%s after a save: %+v %v", id, p, err)
		}
	}
	if p, _ := Find("a"); len(p.Models) != 2 {
		t.Errorf("a's models after a save: %v", p.Models)
	}
}

// An edit made by hand reaches the agents' own model lists, as a save in
// the window does (catalog.Changed); magpie's own save is told once, by
// the save, and a broken edit tells nothing.
func TestFollowEditsTellsAgents(t *testing.T) {
	providerFileHome(t)
	if err := Save(Provider{ID: "a", Chat: "https://a.example.invalid/v1", Key: "sk-a", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	path := Path()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Keep both size and mtime fixed, including after filememo has settled.
	mtime := time.Now().Add(-time.Hour)
	write := func(b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	write(original)
	_ = storedFile() // cache the original at the fixed mtime

	var told atomic.Int64
	was := catalog.Changed
	catalog.Changed = func() { told.Add(1) }
	t.Cleanup(func() { catalog.Changed = was })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready, step, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	run := FollowEdits(ctx, func() time.Duration {
		select {
		case ready <- struct{}{}:
		case <-ctx.Done():
			return time.Hour
		}
		select {
		case <-step:
		case <-ctx.Done():
		}
		return time.Hour
	})
	waitReady := func() {
		t.Helper()
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("watcher did not reach its next wait")
		}
	}
	check := func(edit string, want int64) {
		t.Helper()
		filememo.Forget() // wake the same channel that Watch wakes
		select {
		case step <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("watcher did not accept the wake")
		}
		waitReady()
		if n := told.Load(); n != want {
			t.Fatalf("after %s: told %d times, want %d", edit, n, want)
		}
	}

	// The first edit lands after setup but before the watcher runs.
	changed := bytes.Replace(original, []byte(`"m"`), []byte(`"n"`), 1)
	write(changed)
	go func() { defer close(done); run() }()
	t.Cleanup(func() { cancel(); <-done })
	waitReady()
	check("the edit before startup", 1)
	got, err := Find("a")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Models, ",") != "n" {
		t.Fatalf("the changed model list was not read: %v", got.Models)
	}

	broken := bytes.Clone(changed)
	broken[0] = '!'
	write(broken)
	check("the broken edit", 1)
	write(original)
	check("the repaired edit at the same mtime and size", 2)

	if err := Save(Provider{ID: "b", Chat: "https://b.example.invalid/v1", Key: "sk-b"}); err != nil {
		t.Fatal(err)
	}
	check("magpie's own save", 3)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(changed)
	check("the edit after magpie's save", 4)
	write(saved)
	check("the edit restoring bytes from magpie's previous save", 5)
}
