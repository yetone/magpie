package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every agent is named in internal/library: a case of targetOf
// (targets.go) or a line of noLibrary (every_agent_test.go). A change that
// adds an agent runs this package, and a new agent named in neither left
// library's TestEveryAgentInLibrary red on main again and again (399010bef,
// 68cb92e8e, 7e8214d60, effdd1beb), each found after the push. That test
// still checks the target has something in it; this one only checks the
// agent was named at all. library imports agent, so this reads its files.
func TestEveryAgentNamedForTheLibrary(t *testing.T) {
	dir := filepath.Join("..", "library")
	targets, err := os.ReadFile(filepath.Join(dir, "targets.go"))
	if err != nil {
		t.Fatal(err)
	}
	list, err := os.ReadFile(filepath.Join(dir, "every_agent_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	var cases strings.Builder
	for _, l := range strings.Split(string(targets), "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "case ") {
			cases.WriteString(l + "\n")
		}
	}
	h := t.TempDir()
	n := 0
	for _, a := range onMachine(h, filepath.Join(h, ".config")) {
		if a.WSL != "" || a.Plugin != "" || strings.Contains(a.ID, "#") {
			continue // a distro's twin, a plugin's agent and another profile have their own
		}
		n++
		q := regexp.QuoteMeta(`"` + a.ID + `"`)
		inCase := regexp.MustCompile(q).MatchString(cases.String())
		listed := regexp.MustCompile(`(?m)^\s*` + q + `:`).Match(list)
		if !inCase && !listed {
			t.Errorf("%s (%s) isn't named in internal/library: give it a case in targetOf (targets.go), or a reason in noLibrary (every_agent_test.go)", a.ID, a.Name)
		}
	}
	if n < 50 {
		t.Fatalf("read %d agents", n)
	}
}
