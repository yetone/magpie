package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/yetone/magpie/internal/provider"
)

// The Sessions page reads its ranges again every 10 s. While no file and
// no alias changed, an aliased range is the one made last time, not a new
// copy of every session's summary; a changed alias names them again.
func TestAliasedRangeMadeOncePerNames(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	dir := filepath.Join(claude, "projects", "-work-alias")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := aliasMsg("m1", "claude-opus-5-5", "2026-09-25T10:00:00Z", 1000) +
		aliasMsg("m2", "gpt-6-astra", "2026-09-25T10:01:00Z", 1000)
	if err := os.WriteFile(filepath.Join(dir, "44444444-2222-3333-4444-555555555555.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.SetUsageAlias("gpt-6-astra", "claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = provider.SetUsageAlias("gpt-6-astra", "")
		_, _ = provider.SetUsageAlias("claude-opus-5-5", "")
	})
	ranges := []int{0, 7}
	read := func() []Stats {
		t.Helper()
		out, _ := StatsAndNativeAll(ranges)
		if len(out[0].Sessions) == 0 || len(out[0].Days) == 0 {
			t.Fatalf("nothing read: %+v", out[0])
		}
		return out
	}
	models := func(st Stats) []string {
		for _, d := range st.Days {
			for _, u := range d.Usage {
				if u.Cwd == "/work/alias" {
					return append([]string(nil), u.Model)
				}
			}
		}
		return nil
	}
	same := func(a, b Stats) bool {
		return unsafe.SliceData(a.Sessions) == unsafe.SliceData(b.Sessions) && unsafe.SliceData(a.Days) == unsafe.SliceData(b.Days)
	}
	first := read()
	if m := models(first[0]); len(m) != 1 || m[0] != "claude-opus-5-5" {
		t.Fatalf("aliased: %v", m)
	}
	again := read()
	for i := range ranges {
		if !same(first[i], again[i]) {
			t.Fatalf("range %d named again with nothing changed", ranges[i])
		}
	}

	// another name: made again, and it says what the names say now
	if _, err := provider.SetUsageAlias("gpt-6-astra", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.SetUsageAlias("claude-opus-5-5", "gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	renamed := read()
	if same(renamed[0], first[0]) {
		t.Fatal("the old names' range given after the names changed")
	}
	var seen []string
	for _, d := range renamed[0].Days {
		for _, u := range d.Usage {
			if u.Cwd == "/work/alias" {
				seen = append(seen, u.Model)
			}
		}
	}
	if len(seen) != 1 || seen[0] != "gpt-6-astra" {
		t.Fatalf("renamed: %v", seen)
	}

	// a file changed: its range is read again and named again
	if err := os.WriteFile(filepath.Join(dir, "44444444-2222-3333-4444-555555555555.jsonl"), []byte(body+aliasMsg("m3", "claude-opus-5-5", "2026-09-25T10:02:00Z", 1000)), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "44444444-2222-3333-4444-555555555555.jsonl"), later, later); err != nil {
		t.Fatal(err)
	}
	grown := read()
	var in int
	for _, d := range grown[0].Days {
		for _, u := range d.Usage {
			if u.Cwd == "/work/alias" {
				in += u.Input
			}
		}
	}
	if in != 3000 {
		t.Fatalf("after the file grew: %d input tokens, want 3000", in)
	}
}
