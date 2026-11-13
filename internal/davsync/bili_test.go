package davsync

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/library"
)

// A library that holds only billion-context.json is the user's, not an
// unset part a joining computer's library would be taken over by
// (#5642); one with no file hashes as before it was carried.
func TestBiliFileInLibraryPart(t *testing.T) {
	empty := func() backup.Bundle {
		return backup.Bundle{Library: &library.Bundle{MCP: []*library.Server{}, Skills: []*library.CarriedSkill{}}}
	}
	if !unset(empty(), "library") {
		t.Fatal("an empty library is unset")
	}
	text := `{"compress":{"nudgeGrowthTokens":80000}}`
	with := empty()
	with.Library.Bili = &text
	if unset(with, "library") {
		t.Error("a library with billion-context.json said unset")
	}
	if hashes(with)["library"] == hashes(empty())["library"] {
		t.Error("billion-context.json didn't change the library's hash")
	}
}

// The sync's preview names billion-context.json when it would change,
// though the file is one string and not a list of items.
func TestBiliFileInPreview(t *testing.T) {
	at := func(text string) backup.Bundle {
		b := backup.Bundle{Library: &library.Bundle{MCP: []*library.Server{}, Skills: []*library.CarriedSkill{}}}
		if text != "" {
			b.Library.Bili = &text
		}
		return b
	}
	file := []Item{{Kind: "file", Name: "billion-context.json"}}
	for _, c := range []struct {
		from, to, change string
	}{
		{"", `{"compress":{}}`, "added"},
		{`{"compress":{}}`, `{"compress":{"requireNudge":true}}`, "changed"},
		{`{"compress":{}}`, "", "removed"},
	} {
		got := diffItems(itemsOf(at(c.from), "library"), itemsOf(at(c.to), "library"))
		want := []Item{{Kind: file[0].Kind, Name: file[0].Name, Change: c.change}}
		if !slices.Equal(got, want) {
			t.Errorf("%q to %q: %+v", c.from, c.to, got)
		}
	}
	if got := diffItems(itemsOf(at(`{}`), "library"), itemsOf(at(`{}`), "library")); len(got) != 0 {
		t.Errorf("the same file: %+v", got)
	}
}
