package library

import (
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

func valueOf(o ordered, k string) (any, bool) {
	for _, e := range o {
		if e.k == k {
			return e.v, true
		}
	}
	return nil, false
}

// generate_video waits up to ten minutes for its video; Codex gives a tool a
// minute and Goose five unless the entry says more, so magpie-image's entry
// does, and no other server's does.
func TestSelfServerIsGivenTimeToMakeAVideo(t *testing.T) {
	self := &Server{Name: selfServerName, Transport: "stdio", Command: "/opt/magpie", Args: []string{"mcp", "image"}}
	other := &Server{Name: "other", Transport: "stdio", Command: "x"}
	codex := &mcpFile{Format: fmtCodex}
	if v, ok := valueOf(codex.encode(self), "tool_timeout_sec"); !ok || v != 660 {
		t.Fatalf("Codex's entry: tool_timeout_sec = %v %v", v, ok)
	}
	if _, ok := valueOf(codex.encode(other), "tool_timeout_sec"); ok {
		t.Fatal("another server was given a tool timeout")
	}
	goose := &mcpFile{Format: fmtGoose}
	if v, _ := valueOf(goose.encode(self), "timeout"); v != 660 {
		t.Fatalf("Goose's entry: timeout = %v", v)
	}
	if v, _ := valueOf(goose.encode(other), "timeout"); v != 300 {
		t.Fatalf("Goose's default changed for another server: %v", v)
	}
	// what the user set is theirs
	for _, c := range []struct {
		f    *mcpFile
		key  string
		mine int64
	}{{codex, "tool_timeout_sec", 120}, {goose, "timeout", 900}} {
		got, ok := valueOf(c.f.merged(self, map[string]any{c.key: c.mine}), c.key)
		if !ok || got != c.mine {
			t.Errorf("%s: the user's %d became %v", c.key, c.mine, got)
		}
	}
}

// magpie-image is already installed for many users, as main wrote it: no
// tool_timeout_sec in Codex, the 300 every server had in Goose. Syncing after
// an upgrade gives those entries the timeout; one the user chose stays.
func TestSyncGivesAnInstalledSelfServerItsTimeout(t *testing.T) {
	h := sandbox(t)
	codex, goose := filepath.Join(h, ".codex", "config.toml"), filepath.Join(h, ".config", "goose", "config.yaml")
	s := Server{Name: selfServerName, Transport: "stdio", Command: "/opt/magpie", Args: []string{"mcp", "image"}, Agents: []string{"codex", "goose"}}
	ok(t)(SaveServer("", s))
	installed := func(codexExtra, gooseTimeout string) {
		write(t, codex, "[mcp_servers.magpie-image]\ncommand = \"/opt/magpie\"\nargs = [\"mcp\", \"image\"]\n"+codexExtra)
		write(t, goose, "extensions:\n  magpie-image:\n    enabled: true\n    name: magpie-image\n    type: stdio\n    cmd: /opt/magpie\n    args:\n      - mcp\n      - image\n    timeout: "+gooseTimeout+"\n")
	}
	want := func(what, file, key, val string) {
		t.Helper()
		if got := read(t, file); !regexp.MustCompile(`(?m)` + key + `[ :=]+` + val + `\s*$`).MatchString(got) {
			t.Errorf("%s: want %s %s in\n%s", what, key, val, got)
		}
	}
	installed("", "300")
	res, err := Sync()
	if err != nil || len(res.Problems) > 0 {
		t.Fatalf("%v %v", err, res)
	}
	if !slices.Contains(res.Changed, "codex") || !slices.Contains(res.Changed, "goose") {
		t.Fatalf("an installed entry was left as it was: changed %v", res.Changed)
	}
	want("Codex, written before", codex, "tool_timeout_sec", "660")
	want("Goose, still the old 300", goose, "timeout", "660")
	// the user's own values stay, and another sync changes nothing
	installed("tool_timeout_sec = 120\n", "900")
	if _, err := Sync(); err != nil {
		t.Fatal(err)
	}
	want("Codex, the user's", codex, "tool_timeout_sec", "120")
	want("Goose, the user's", goose, "timeout", "900")
	before := read(t, codex) + read(t, goose)
	if _, err := Sync(); err != nil || read(t, codex)+read(t, goose) != before {
		t.Fatalf("a second sync changed the files: %v", err)
	}
}
