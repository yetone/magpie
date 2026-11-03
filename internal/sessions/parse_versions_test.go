package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The index an older magpie kept names no agent: its parses stand on the
// update, are named on the next read, and a later raise of Claude Code's
// parseVersions then reads Claude Code's files alone again, not Codex's
// (#2278: each raise of cacheVersion read a whole history again).
func TestParseVersionRaiseReadsThatAgentAlone(t *testing.T) {
	setup(t)
	unraised(t)
	var mu sync.Mutex
	read := map[string]int{}
	scanHook = func(p string, off int64) {
		mu.Lock()
		read[p]++
		mu.Unlock()
	}
	t.Cleanup(func() { scanHook = nil })
	agents := func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		n := map[string]int{}
		for p := range read {
			n[agentOf(p)]++
		}
		read = map[string]int{}
		return n
	}
	List(0)
	Saved()
	first := agents()
	if first["claude"] == 0 || first["codex"] == 0 {
		t.Fatalf("the fixture reads %v: it tests nothing", first)
	}

	// as an older magpie left it: no agent named, no versions
	b, err := os.ReadFile(CachePath())
	if err != nil {
		t.Fatal(err)
	}
	var c cacheFile
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	for _, s := range c.Files {
		if s != nil {
			if s.Agent == "" {
				t.Fatalf("a parse kept without its agent: %+v", s)
			}
			s.Agent = ""
		}
	}
	c.Parsers = nil
	if b, err = json.Marshal(c); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CachePath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
	Reset()
	List(0)
	Saved()
	if n := agents(); n["claude"]+n["codex"] != 0 {
		t.Fatalf("the update read %v again", n)
	}

	parseVersions = map[string]int{"claude": 1}
	Reset()
	List(0)
	Saved()
	if n := agents(); n["codex"] != 0 || n["claude"] != first["claude"] {
		t.Fatalf("Claude Code's version raised: read %v, want claude:%d alone", n, first["claude"])
	}
	// and kept at the new version: the next start reads nothing
	Reset()
	List(0)
	if n := agents(); n["claude"]+n["codex"] != 0 {
		t.Fatalf("the start after read %v again", n)
	}
}

// agentOf is the agent whose session folder holds p.
func agentOf(p string) string {
	switch {
	case strings.HasPrefix(p, os.Getenv("CLAUDE_CONFIG_DIR")+string(filepath.Separator)):
		return "claude"
	case strings.HasPrefix(p, os.Getenv("CODEX_HOME")+string(filepath.Separator)):
		return "codex"
	}
	return "other"
}
