package library

import (
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
