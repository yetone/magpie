package edit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EachTOMLTable gives what TOMLTables and GetTOMLTable give, table by
// table, without a map or a stat for each: Grok Build's model list reads
// every [model."<id>"] table of its config on each look.
func TestEachTOMLTableAgreesWithGetTOMLTable(t *testing.T) {
	var b strings.Builder
	b.WriteString("top = 1\n\n[[array]]\nname = \"skipped\"\n\n[plain]\nname = \"p\"\nlist = [1, 2]\n\n")
	for i := range 200 {
		fmt.Fprintf(&b, "[model.\"m-%d\"]\nname = \"Model %d\"\nbase_url = 'https://h%d.example/v1' # own\n\n", i, i, i)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	names, err := TOMLTables(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := EachTOMLTable(path, func(tb TOMLTable) {
		got = append(got, tb.Name())
		want, _ := GetTOMLTable(path, tb.Name())
		for _, k := range []string{"name", "base_url", "list", "missing"} {
			if v := tb.Scalar(k); v != want[k] {
				t.Errorf("%s.%s: %q, GetTOMLTable has %q", tb.Name(), k, v, want[k])
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != strings.Join(names, "|") {
		t.Fatalf("tables %v, TOMLTables lists %v", got, names)
	}
	seen := 0
	if n := testing.AllocsPerRun(10, func() {
		seen = 0
		EachTOMLTable(path, func(tb TOMLTable) {
			if tb.Scalar("base_url") != "" {
				seen++
			}
		})
	}); n > 5 {
		t.Errorf("a look at 201 tables: %.0f allocations, want a few", n)
	}
	if seen != 200 {
		t.Errorf("tables with a base_url: %d", seen)
	}
	if err := EachTOMLTable(filepath.Join(t.TempDir(), "none.toml"), func(TOMLTable) { t.Error("a missing file has tables") }); err != nil {
		t.Errorf("missing file: %v", err)
	}
}
