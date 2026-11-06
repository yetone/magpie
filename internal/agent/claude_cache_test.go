package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestClaudeCacheSet (Linka on Discord): Claude Code's prompt cache picked
// in its settings goes where the Usage card writes it, the user's other
// settings kept; Claude Code's default takes the key out; a value the card
// stashed is forgotten, so its Undo never puts an older one back over the
// pick; a variable Claude Code reads first leaves the file alone.
func TestClaudeCacheSet(t *testing.T) {
	home := tuneSandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	flat := func() string { return strings.Join(strings.Fields(tuneFile(t, path)), "") }
	tuneWrite(t, path, `{"theme": "dark", "promptCacheTtl": "1h"}`)
	if s, _ := TuneRead(home, "claude", TuneCache, ""); s.Value != "1h" || s.Source != "settings" {
		t.Fatalf("read %+v; want the user's own 1h", s)
	}
	if err := ClaudeCacheSet(home, TuneCache, "5m"); err != nil {
		t.Fatal(err)
	}
	if got := flat(); got != `{"theme":"dark","promptCacheTtl":"5m"}` {
		t.Fatalf("after 5m: %s", got)
	}
	if err := ClaudeCacheSet(home, TuneSubCache, "1h"); err != nil {
		t.Fatal(err)
	}
	if s, _ := TuneRead(home, "claude", TuneSubCache, ""); s.Value != "1h" {
		t.Fatalf("subagents %+v; want 1h", s)
	}
	for _, knob := range []string{TuneCache, TuneSubCache} {
		if err := ClaudeCacheSet(home, knob, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := flat(); got != `{"theme":"dark"}` {
		t.Fatalf("Claude Code's default left a key: %s", got)
	}
	if err := ClaudeCacheSet(home, TuneCache, ""); err != nil {
		t.Fatalf("default again: %v", err)
	}

	// the card put 1h in over the user's 5m; picking 5m in the settings is
	// theirs, and the card's Undo has nothing left to put back
	tuneWrite(t, path, `{"promptCacheTtl": "5m"}`)
	if err := TuneApply(home, "claude", TuneCache, "", "1h"); err != nil {
		t.Fatal(err)
	}
	if err := ClaudeCacheSet(home, TuneCache, "1h"); err != nil {
		t.Fatal(err)
	}
	if s, _ := TuneRead(home, "claude", TuneCache, ""); s.Ours {
		t.Fatalf("read %+v; the pick is the user's, not the card's", s)
	}
	if err := TuneUndo(home, "claude", TuneCache, ""); err == nil {
		t.Fatal("the card could still undo the user's pick")
	}
	if got := flat(); got != `{"promptCacheTtl":"1h"}` {
		t.Fatalf("after Undo: %s", got)
	}

	for _, bad := range [][2]string{{TuneCache, "2h"}, {TuneCompact, "5m"}, {"cache", "5m"}} {
		if err := ClaudeCacheSet(home, bad[0], bad[1]); err == nil {
			t.Errorf("took %s=%q", bad[0], bad[1])
		}
	}

	// a variable Claude Code reads before the setting: nothing written
	tuneWrite(t, path, `{"env": {"CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL": "1h"}}`)
	if err := ClaudeCacheSet(home, TuneSubCache, "5m"); err == nil {
		t.Fatal("set under CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL")
	}
	if err := ClaudeCacheSet(home, TuneSubCache, ""); err == nil {
		t.Fatal("cleared under CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL")
	}
	t.Setenv("FORCE_PROMPT_CACHING_5M", "1")
	if err := ClaudeCacheSet(home, TuneCache, "1h"); err == nil {
		t.Fatal("set under FORCE_PROMPT_CACHING_5M")
	}
	if got := flat(); got != `{"env":{"CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL":"1h"}}` {
		t.Fatalf("wrote under a variable: %s", got)
	}
}
