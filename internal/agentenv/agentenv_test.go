package agentenv

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every name in Vars is one magpie reads somewhere outside this package. A
// name it stopped reading, or one a sandbox invented, is a hole that every
// sandbox clearing this list believes is covered: internal/backup's cleared
// GEMINI_CLI_HOME and OPENCODE_CONFIG, which nothing reads, and not
// OPENCODE_CONFIG_DIR, which internal/agent does. This reads the sources
// rather than the agents' table, since the table is in internal/agent, which
// internal/sessions' tests cannot import; it looks for a quoted name, so a
// name only a comment mentions still counts, and a variable magpie reads
// through a name it builds at runtime would not.
func TestVarsAreRead(t *testing.T) {
	found := make(map[string]bool, len(Vars))
	err := filepath.WalkDir(filepath.Join("..", ".."), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "dist", "node_modules", "agentenv":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, v := range Vars {
			if !found[v] && strings.Contains(string(b), `"`+v+`"`) {
				found[v] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range Vars {
		if !found[v] {
			t.Errorf("%s is in Vars, but nothing outside this package reads it: magpie stopped reading it, or the name was never the one magpie reads", v)
		}
	}
}

// A name twice is a name a sandbox clears twice, which says the list was
// pasted together rather than kept.
func TestVarsAreApart(t *testing.T) {
	seen := make(map[string]bool, len(Vars))
	for _, v := range Vars {
		if seen[v] {
			t.Errorf("%s is in Vars twice", v)
		}
		seen[v] = true
	}
}
