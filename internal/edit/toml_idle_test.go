package edit

import (
	"os"
	"path/filepath"
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
