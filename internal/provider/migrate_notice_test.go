package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every built-in a plugin serves once moved says so in its own code, so
// that a fix made there isn't taken for one that reaches the users moved
// onto the plugin (AGENTS.md): a file of it carries PLUGIN-SERVED with the
// plugin's package.
func TestMovedBuiltinsSayTheirPlugin(t *testing.T) {
	var notices []string
	for _, dir := range []string{".", "../gateway"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if i := strings.Index(string(b), "// PLUGIN-SERVED"); i >= 0 {
				notices = append(notices, string(b[i:min(len(b), i+1200)]))
			}
		}
	}
	for id, mv := range movers {
		said := false
		for _, n := range notices {
			// the notice's words run over lines: compare them unwrapped
			n = strings.Join(strings.Fields(strings.ReplaceAll(n, "//", "")), " ")
			if strings.Contains(n, `"`+id+`"`) && strings.Contains(n, mv.pkg) {
				said = true
				break
			}
		}
		if !said {
			t.Errorf("%s moves onto %s, but none of its files says so: add a PLUGIN-SERVED notice naming %q and the package (see AGENTS.md)", id, mv.pkg, id)
		}
	}
}
