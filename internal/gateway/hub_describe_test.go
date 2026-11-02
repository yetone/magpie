package gateway

import (
	"fmt"
	"strings"
	"testing"
)

// However many servers and tools there are, the instructions stay within
// hubSayMax with the tail whole, and every server magpie knows anything
// of is named on a line of its own.
func TestHubAboutBounded(t *testing.T) {
	const tail = "The tools of every MCP server in magpie's Library, each named <server>__<tool>."
	for _, n := range []int{1, 3, 12, 40} {
		for _, tools := range []int{0, 2, 30, 200} {
			ss := make([]HubServer, n)
			for i := range ss {
				ss[i] = HubServer{Name: fmt.Sprintf("srv%02d", i), About: strings.Repeat("Reads and writes things for you. ", 6), Known: true}
				for j := range tools {
					ss[i].Tools = append(ss[i].Tools, HubToolNote{Name: fmt.Sprintf("tool_%03d", j), About: strings.Repeat("word ", 60)})
				}
			}
			say := hubAbout(ss, "This router: Los Angeles.", tail)
			if l := runeLen(say); l > hubSayMax {
				t.Errorf("%d servers × %d tools: %d characters", n, tools, l)
			}
			if !strings.HasSuffix(say, tail) {
				t.Errorf("%d servers × %d tools: the tail is cut:\n%s", n, tools, say)
			}
			if n <= 12 {
				for _, s := range ss {
					if !strings.Contains(say, "\n- "+s.Name) {
						t.Errorf("%d servers × %d tools: %s has no line:\n%s", n, tools, s.Name, say)
					}
				}
			}
		}
	}
}

// A server's line names its tools with the first clause of each; a
// router's "[name] " before a description is left out, and a server
// magpie knows nothing of has no line.
func TestHubServerLine(t *testing.T) {
	s := HubServer{Name: "la", Title: "Magpie MCP hub · Los Angeles", Tools: []HubToolNote{
		{Name: "fetch__fetch", About: "[Los Angeles] Fetches a URL, and more."},
		{Name: "x", About: ""},
	}}
	if got, want := hubServerLine(s, 1000), "- la (Magpie MCP hub · Los Angeles): Tools: fetch__fetch (Fetches a URL), x."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := hubServerLine(HubServer{Name: "bare"}, 1000); got != "" {
		t.Errorf("a server known by name only has a line: %q", got)
	}
	if got := hubServerLine(s, 30); runeLen(got) > 30 {
		t.Errorf("%q is over 30", got)
	}
}

// Another magpie's first line keeps each server's words in brackets, so
// its clause isn't cut inside them.
func TestHubClauseBrackets(t *testing.T) {
	for in, want := range map[string]string{
		"Tools of fetch (Fetch a page, as Markdown), time.": "Tools of fetch (Fetch a page, as Markdown)",
		"Search [docs: all of them]. Then more.":            "Search [docs: all of them]",
		"Plain words, then more":                            "Plain words",
	} {
		if got := hubClause(in); got != want {
			t.Errorf("hubClause(%q) = %q, want %q", in, got, want)
		}
	}
}

// Another magpie's router says "Tools of a (…), b." of itself; the first
// line names it with what follows, not "Tools of" twice.
func TestHubLineOfARouter(t *testing.T) {
	got := HubLine([]HubServer{{Name: "la", About: "Tools of notes (Notes the team keeps), time."}})
	if want := "Tools of la (notes (Notes the team keeps))."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
