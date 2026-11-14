package davsync

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/library"
)

// A library that holds only Claude Code's plugins is the user's, not an
// unset part a joining computer's library would be taken over by
// (#5642), and adding a plugin changes the library's hash.
func TestClaudePluginsInLibraryPart(t *testing.T) {
	empty := func() backup.Bundle {
		return backup.Bundle{Library: &library.Bundle{MCP: []*library.Server{}, Skills: []*library.CarriedSkill{}}}
	}
	with := empty()
	with.Library.ClaudePlugins = &library.ClaudePlugins{Enabled: []string{"frontend-design@claude-plugins-official"}}
	if unset(with, "library") {
		t.Error("a library with Claude Code's plugins said unset")
	}
	if hashes(with)["library"] == hashes(empty())["library"] {
		t.Error("Claude Code's plugins didn't change the library's hash")
	}
}

// The sync's preview names each marketplace and plugin it would add.
func TestClaudePluginsInPreview(t *testing.T) {
	at := func(cp *library.ClaudePlugins) backup.Bundle {
		return backup.Bundle{Library: &library.Bundle{MCP: []*library.Server{}, Skills: []*library.CarriedSkill{}, ClaudePlugins: cp}}
	}
	to := &library.ClaudePlugins{
		Marketplaces: map[string]json.RawMessage{"claude-code-plugins": json.RawMessage(`{"source":{"source":"github","repo":"anthropics/claude-code"}}`)},
		Enabled:      []string{"commit-commands@claude-code-plugins"},
	}
	got := diffItems(itemsOf(at(nil), "library"), itemsOf(at(to), "library"))
	want := []Item{
		{Kind: "claudeMarketplace", Name: "claude-code-plugins", Change: "added"},
		{Kind: "claudePlugin", Name: "commit-commands@claude-code-plugins", Change: "added"},
	}
	slices.SortFunc(got, func(a, b Item) int {
		if a.Kind < b.Kind {
			return -1
		}
		if a.Kind > b.Kind {
			return 1
		}
		return 0
	})
	if !slices.Equal(got, want) {
		t.Errorf("preview: %+v", got)
	}
	if got := diffItems(itemsOf(at(to), "library"), itemsOf(at(to), "library")); len(got) != 0 {
		t.Errorf("the same plugins: %+v", got)
	}
}
