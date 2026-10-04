package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// storeSandbox gives the test its own magpie folders, without the Bun the
// other tests need: these only read and write plugins.json.
func storeSandbox(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	testenv.SetHome(t, dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeList puts plugins.json where magpie keeps it.
func writeList(t testing.TB, body string) {
	t.Helper()
	if err := os.WriteFile(listPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingIsEmptyNotGrown(t *testing.T) {
	storeSandbox(t)
	l := Load()
	if l.Plugins != nil || l.Config != nil {
		t.Fatalf("a missing plugins.json gave %+v, want the zero List", l)
	}
}

// TestLoadKeepsWhatAPartialFileGave: what a file that doesn't parse whole
// gives is what Load gives, as it always was — the error is no more read
// than it was, so a half-written file can't empty a list a magpie is using.
func TestLoadKeepsWhatAPartialFileGave(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"truncated", `{"plugins":[{"spec":"a"},{"spec":"b"`},
		{"a field of the wrong type", `{"plugins":[{"spec":"a"},{"spec":"b","off":"yes"}],"config":{"k":1}}`},
		{"empty", `{"plugins":[]}`},
		{"whole", `{"plugins":[{"spec":"a","options":{"k":1}}],"config":{"p":{"q":["x"]}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storeSandbox(t)
			writeList(t, tc.body)
			// what a plain parse of those bytes gives is what Load gives:
			// the error was never read, and a partly filled list stayed
			var want List
			_ = json.Unmarshal([]byte(tc.body), &want)
			if got := Load(); !reflect.DeepEqual(got, want) {
				t.Fatalf("Load = %+v, want the plain parse %+v", got, want)
			}
		})
	}
	t.Run("a plugin edited after a wrong type isn't saved away", func(t *testing.T) {
		storeSandbox(t)
		writeList(t, `{"plugins":[{"spec":"a"},{"spec":"b","off":"yes"}],"config":{"k":1}}`)
		if l := Load(); len(l.Plugins) != 2 || l.Plugins[0].Spec != "a" || l.Plugins[1].Spec != "b" || l.Plugins[1].Off {
			t.Fatalf("a wrong type gave %+v, want both plugins", l.Plugins)
		}
		if err := SetOff("a", true); err != nil {
			t.Fatal(err)
		}
		if l := Load(); len(l.Plugins) != 2 || !l.Plugins[0].Off || l.Plugins[1].Spec != "b" || l.Config["k"] != float64(1) {
			t.Fatalf("after turning one off: %+v", l)
		}
	})
}

// TestLoadCopyIsIndependent: what Load gives may be changed anywhere in it,
// and what magpie keeps is not.
func TestLoadCopyIsIndependent(t *testing.T) {
	storeSandbox(t)
	writeList(t, `{"plugins":[{"spec":"a","options":{"k":{"n":[1]}}}],"config":{"p":{"q":["x"]}}}`)
	l := Load()
	l.Plugins[0].Spec = "changed"
	l.Plugins[0].Off = true
	l.Plugins[0].Options["k"].(map[string]any)["n"].([]any)[0] = 2
	l.Plugins = append(l.Plugins, Entry{Spec: "added"})
	l.Config["p"].(map[string]any)["q"].([]any)[0] = "changed"
	l.Config["new"] = 1

	again := Load()
	if len(again.Plugins) != 1 || again.Plugins[0].Spec != "a" || again.Plugins[0].Off {
		t.Fatalf("the entries changed with a copy: %+v", again.Plugins)
	}
	if n := again.Plugins[0].Options["k"].(map[string]any)["n"].([]any)[0]; n != float64(1) {
		t.Fatalf("a nested option changed with a copy: %v", n)
	}
	if q := again.Config["p"].(map[string]any)["q"].([]any)[0]; q != "x" {
		t.Fatalf("a nested config value changed with a copy: %v", q)
	}
	if _, ok := again.Config["new"]; ok {
		t.Fatal("a new config key reached what magpie keeps")
	}
}

// TestListReusesTheParse: while the bytes are the same the parse is reused.
func TestListReusesTheParse(t *testing.T) {
	storeSandbox(t)
	writeList(t, `{"plugins":[{"spec":"a"},{"spec":"b"}]}`)
	a := list()
	b := list()
	if len(a.Plugins) == 0 || &a.Plugins[0] != &b.Plugins[0] {
		t.Fatal("the same bytes were parsed again")
	}
}

// TestLoadSeesAnEditThatKeepsSizeAndTime: an old file edited in place, or
// replaced by a rename, with its size and time put back, is read as it is
// now — a stamp would serve the parse before it.
func TestLoadSeesAnEditThatKeepsSizeAndTime(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	edit := func(t *testing.T, was, now string) {
		t.Helper()
		writeList(t, was)
		if err := os.Chtimes(listPath(), old, old); err != nil {
			t.Fatal(err)
		}
		if l := Load(); len(l.Plugins) != 1 || l.Plugins[0].Spec != specOf(was) {
			t.Fatalf("first read: %+v", l.Plugins)
		}
		writeList(t, now)
		if err := os.Chtimes(listPath(), old, old); err != nil {
			t.Fatal(err)
		}
		if l := Load(); len(l.Plugins) != 1 || l.Plugins[0].Spec != specOf(now) {
			t.Fatalf("an edit of one size kept the parse before it: %+v", l.Plugins)
		}
	}
	t.Run("in place", func(t *testing.T) {
		storeSandbox(t)
		edit(t, `{"plugins":[{"spec":"aaa"}]}`, `{"plugins":[{"spec":"bbb"}]}`)
	})
	t.Run("replaced by a rename", func(t *testing.T) {
		storeSandbox(t)
		writeList(t, `{"plugins":[{"spec":"aaa"}]}`)
		if err := os.Chtimes(listPath(), old, old); err != nil {
			t.Fatal(err)
		}
		if l := Load(); len(l.Plugins) != 1 || l.Plugins[0].Spec != "aaa" {
			t.Fatalf("first read: %+v", l.Plugins)
		}
		tmp := filepath.Join(filepath.Dir(listPath()), "plugins.json.tmp")
		if err := os.WriteFile(tmp, []byte(`{"plugins":[{"spec":"bbb"}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(tmp, old, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, listPath()); err != nil {
			t.Fatal(err)
		}
		if l := Load(); len(l.Plugins) != 1 || l.Plugins[0].Spec != "bbb" {
			t.Fatalf("a replaced file kept the parse before it: %+v", l.Plugins)
		}
	})
}

// specOf is the one plugin spec a test's list holds.
func specOf(body string) string {
	var l List
	_ = json.Unmarshal([]byte(body), &l)
	if len(l.Plugins) == 0 {
		return ""
	}
	return l.Plugins[0].Spec
}

// TestLoadDeleteRecreateAndReadError: gone, unreadable and back again.
func TestLoadDeleteRecreateAndReadError(t *testing.T) {
	storeSandbox(t)
	writeList(t, `{"plugins":[{"spec":"a"}]}`)
	if l := Load(); len(l.Plugins) != 1 {
		t.Fatalf("first read: %+v", l.Plugins)
	}
	if err := os.Remove(listPath()); err != nil {
		t.Fatal(err)
	}
	if l := Load(); l.Plugins != nil {
		t.Fatalf("a deleted file gave %+v, want the zero List", l.Plugins)
	}
	// a folder where the file goes: reading it fails
	if err := os.Mkdir(listPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if l := Load(); l.Plugins != nil {
		t.Fatalf("an unreadable file gave %+v, want the zero List", l.Plugins)
	}
	if err := os.Remove(listPath()); err != nil {
		t.Fatal(err)
	}
	writeList(t, `{"plugins":[{"spec":"back"}]}`)
	if l := Load(); len(l.Plugins) != 1 || l.Plugins[0].Spec != "back" {
		t.Fatalf("after it came back: %+v", l.Plugins)
	}
}

// TestLoadPerConfigDir: what one magpie folder keeps is not another's.
func TestLoadPerConfigDir(t *testing.T) {
	dir := storeSandbox(t)
	writeList(t, `{"plugins":[{"spec":"one"}]}`)
	if l := Load(); l.Plugins[0].Spec != "one" {
		t.Fatalf("first dir: %+v", l.Plugins)
	}
	other := filepath.Join(dir, "elsewhere")
	t.Setenv("XDG_CONFIG_HOME", other)
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	writeList(t, `{"plugins":[{"spec":"two"}]}`)
	if l := Load(); len(l.Plugins) != 1 || l.Plugins[0].Spec != "two" {
		t.Fatalf("second dir: %+v", l.Plugins)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	if l := Load(); len(l.Plugins) != 1 || l.Plugins[0].Spec != "one" {
		t.Fatalf("back to the first dir: %+v", l.Plugins)
	}
}

// TestLoadConcurrentWithSavesAndEdits: reading the list while another
// magpie's writes land (each of them a rename) never gives half a file, and
// add, off and remove work under it.
func TestLoadConcurrentWithSavesAndEdits(t *testing.T) {
	dir := storeSandbox(t)
	writeList(t, `{"plugins":[{"spec":"p0"}]}`)
	plug := filepath.Join(dir, "plug.mjs")
	if err := os.WriteFile(plug, []byte("// a plugin\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				plugins := Load().Plugins
				if len(plugins) < 1 || len(plugins) > 2 {
					t.Errorf("a read gave %d plugins", len(plugins))
					return
				}
				for _, e := range plugins {
					if e.Spec == "" {
						t.Errorf("a read gave an entry with no spec")
						return
					}
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := range 60 {
			_ = save(List{Plugins: []Entry{{Spec: fmt.Sprintf("p%d", i%2)}}})
			if _, err := Add(context.Background(), plug); err != nil {
				t.Errorf("add: %v", err)
				return
			}
			if err := SetOff(Name(plug), true); err != nil {
				t.Errorf("off: %v", err)
				return
			}
			if err := Remove(context.Background(), Name(plug)); err != nil {
				t.Errorf("remove: %v", err)
				return
			}
		}
	}()
	wg.Wait()
	if l := Load(); len(l.Plugins) != 1 || !strings.HasPrefix(l.Plugins[0].Spec, "p") {
		t.Fatalf("after the writers: %+v", l.Plugins)
	}
}

// BenchmarkCachedWarm is Cached with the providers already good, so nothing
// is refreshed in the background: what is measured is the per-call read.
func BenchmarkCachedWarm(b *testing.B) {
	storeSandbox(b)
	writeList(b, `{"plugins":[{"spec":"@magpie-community/opencode-zed-auth@latest","options":{"a":1}},{"spec":"@magpie-community/opencode-qoder-auth@latest","off":true}],"config":{"provider":{"zed":{"options":{"projectId":"p"}}}}}`)
	UseCached([]Provider{{ID: "zed", Spec: "@magpie-community/opencode-zed-auth@latest", Name: "Zed", SignedIn: true}})
	b.ReportAllocs()
	for b.Loop() {
		Cached()
	}
}
