package edit

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

// Once nothing has used a file's whole parse for tomlIdle it is let go,
// and what GetTOMLTop and GetTOMLTable answered stays: the Codex app's
// route, looked at every 3s, parses nothing again, while a key no one
// asked before parses the file again.
func TestTOMLParseLetGoWhenIdle(t *testing.T) {
	tomlKept.off = false
	t.Cleanup(func() { tomlKept.off = true })
	p := filepath.Join(t.TempDir(), "config.toml")
	old := time.Now().Add(-time.Hour)
	write := func(s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		old = old.Add(time.Second)
		os.Chtimes(p, old, old)
		filememo.Forget()
	}
	write("model_provider = \"magpie\"\nother = 2\n\n[model_providers.magpie]\nbase_url = \"http://x\"\n")
	if v, ok := GetTOMLTop(p, "model_provider"); v != "magpie" || !ok {
		t.Fatalf("model_provider = %q, %v", v, ok)
	}
	tb, _ := GetTOMLTable(p, "model_providers.magpie")
	if tb["base_url"] != "http://x" {
		t.Fatalf("table = %v", tb)
	}
	tb["base_url"] = "changed by a caller"
	if _, ok := GetTOMLTop(p, "absent"); ok {
		t.Fatal("absent key found")
	}
	tomlKept.last.Store(time.Now().Add(-2 * tomlIdle).UnixNano())
	tomlIdleCheck()

	n := tomlParses.Load()
	for range 3 {
		if v, _ := GetTOMLTop(p, "model_provider"); v != "magpie" {
			t.Fatalf("model_provider after the let-go = %q", v)
		}
		if _, ok := GetTOMLTop(p, "absent"); ok {
			t.Fatal("absent key found after the let-go")
		}
		if tb, _ := GetTOMLTable(p, "model_providers.magpie"); tb["base_url"] != "http://x" {
			t.Fatalf("table after the let-go = %v", tb)
		}
	}
	if got := tomlParses.Load() - n; got != 0 {
		t.Fatalf("answered keys parsed the file %d times", got)
	}
	if v, _ := GetTOMLTop(p, "other"); v != "2" || tomlParses.Load()-n != 1 {
		t.Fatalf("a new key = %q after %d parses; want the file parsed again once it was let go", v, tomlParses.Load()-n)
	}

	write("model_provider = \"openai\"\n")
	if v, _ := GetTOMLTop(p, "model_provider"); v != "openai" {
		t.Fatalf("after a change model_provider = %q", v)
	}
	if tb, err := GetTOMLTable(p, "model_providers.magpie"); tb != nil || err != nil {
		t.Fatalf("after a change the table = %v, %v", tb, err)
	}
}

// TOMLTables' list is kept as the other answers are: a poll that only
// lists a file's tables parses nothing once the whole parse is let go,
// and a change to the file is listed.
func TestTOMLTablesKeptWhenIdle(t *testing.T) {
	tomlKept.off = false
	t.Cleanup(func() { tomlKept.off = true })
	p := filepath.Join(t.TempDir(), "config.toml")
	old := time.Now().Add(-time.Hour)
	write := func(s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		old = old.Add(time.Second)
		os.Chtimes(p, old, old)
		filememo.Forget()
	}
	write("a = 1\n\n[projects.\"/x\"]\ntrust_level = \"trusted\"\n\n[[arr]]\nb = 1\n\n[model_providers.magpie]\nbase_url = \"http://x\"\n")
	want := []string{`projects."/x"`, "model_providers.magpie"}
	names, err := TOMLTables(p)
	if err != nil || !slices.Equal(names, want) {
		t.Fatalf("tables = %q, %v; want %q", names, err, want)
	}
	tomlKept.last.Store(time.Now().Add(-2 * tomlIdle).UnixNano())
	tomlIdleCheck()

	n := tomlParses.Load()
	for range 3 {
		if names, _ := TOMLTables(p); !slices.Equal(names, want) {
			t.Fatalf("tables after the let-go = %q", names)
		}
	}
	if got := tomlParses.Load() - n; got != 0 {
		t.Fatalf("listing the tables parsed the file %d times", got)
	}

	write("[other]\nc = 1\n")
	if names, _ := TOMLTables(p); !slices.Equal(names, []string{"other"}) {
		t.Fatalf("after a change the tables = %q", names)
	}
}

// A file's table names no one has listed for namesIdle are let go, and
// the next list parses the file again; a list a caller holds stays whole.
func TestTOMLNamesLetGoWhenIdle(t *testing.T) {
	tomlKept.off = false
	t.Cleanup(func() { tomlKept.off = true })
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("a = 1\n\n[projects.\"/x\"]\ntrust_level = \"trusted\"\n\n[model_providers.magpie]\nbase_url = \"http://x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(p, old, old)
	filememo.Forget()
	want := []string{`projects."/x"`, "model_providers.magpie"}
	held, err := TOMLTables(p)
	if err != nil || !slices.Equal(held, want) {
		t.Fatalf("tables = %q, %v", held, err)
	}
	if v, _ := GetTOMLTop(p, "a"); v != "1" {
		t.Fatalf("a = %q", v)
	}
	tomlKept.last.Store(time.Now().Add(-2 * tomlIdle).UnixNano())
	tomlIdleCheck()

	// listed within namesIdle: kept
	namesKept.last.Store(time.Now().Add(-namesIdle / 2).UnixNano())
	n := tomlParses.Load()
	if names, _ := TOMLTables(p); !slices.Equal(names, want) || tomlParses.Load() != n {
		t.Fatalf("tables %q after %d parses; want them kept", names, tomlParses.Load()-n)
	}

	namesKept.last.Store(time.Now().Add(-2 * namesIdle).UnixNano())
	namesIdleCheck()
	a := tomlAnswersOf(p)
	if _, ok := a.getNames(); ok {
		t.Fatal("the names were kept past namesIdle")
	}
	if !slices.Equal(held, want) {
		t.Fatalf("a caller's list = %q after the let-go", held)
	}
	if v, _ := GetTOMLTop(p, "a"); v != "1" || tomlParses.Load() != n {
		t.Fatalf("a = %q after %d parses; the other answers should stay", v, tomlParses.Load()-n)
	}
	if names, _ := TOMLTables(p); !slices.Equal(names, want) || tomlParses.Load() != n+1 {
		t.Fatalf("tables %q after %d parses; want one parse", names, tomlParses.Load()-n)
	}
	if names, _ := TOMLTables(p); !slices.Equal(names, want) || tomlParses.Load() != n+1 {
		t.Fatalf("listed again: %q after %d parses; want them kept again", names, tomlParses.Load()-n)
	}
}

func BenchmarkTOMLTablesCold(b *testing.B) {
	src := os.Getenv("MAGPIE_BENCH_TOML")
	if src == "" {
		b.Skip("MAGPIE_BENCH_TOML unset")
	}
	for b.Loop() {
		filememo.DropKind("toml file")
		filememo.DropKind("toml answers")
		if _, err := TOMLTables(src); err != nil {
			b.Fatal(err)
		}
	}
}
