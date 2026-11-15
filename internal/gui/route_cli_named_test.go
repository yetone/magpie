package gui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every /api route this package serves is named in internal/cli's route
// list (cliForRoute or notYet in settings_parity_test.go), checked here
// too: a GUI change runs this package, and a new route unnamed there left
// TestEveryRouteHasACommandOrIsNamed red on main again and again (#5946,
// #6162, #6677, #6724, #6786), each found by CI after the push. That
// test still says which command a route has; this one only says the
// route was named at all.
func TestEveryRouteIsNamedForTheCLI(t *testing.T) {
	list, err := os.ReadFile(filepath.Join("..", "cli", "settings_parity_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`HandleFunc\("([A-Z]+ /api/[^"]*)"`)
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			n++
			if !strings.Contains(string(list), `"`+m[1]+`":`) {
				t.Errorf("%s (%s) isn't named in internal/cli/settings_parity_test.go: add it to cliForRoute with its command, or to notYet with why there's none", m[1], f)
			}
		}
	}
	if n < 200 {
		t.Fatalf("read %d routes", n)
	}
}
