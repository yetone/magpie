package library

import (
	"slices"
	"strings"
	"testing"
)

// The market's counts of who uses each server (gugediyige666 on X: a
// ranking by installs): the day's report names a library server by its
// market id only when the market lists it from what ships with magpie and
// an agent is given it. A server of the user's own, one no agent has, and
// one that names no package are never named, nor anything of a server but
// its id.
func TestPublicServers(t *testing.T) {
	listed := readPopular([]byte(`[
		{"name":"io.github.big/one","packages":[{"registryType":"npm","identifier":"@big/one"}]},
		{"name":"io.github.big/again","packages":[{"registryType":"npm","identifier":"@big/one"}]},
		{"name":"io.github.far/away","remotes":[{"type":"streamable-http","url":"https://far.dev/mcp"}]},
		{"name":"io.github.no/agent","packages":[{"registryType":"npm","identifier":"no-agent"}]}
	]`))
	if len(listed) < 3 {
		t.Fatalf("the fixture lists %d servers", len(listed))
	}
	l := &Library{MCP: []*Server{
		{Name: "secret-name", Command: "npx", Args: []string{"-y", "@big/one@latest"}, Env: map[string]string{"TOKEN": "sk-1"}, Agents: []string{"claude"}},
		{Name: "twice", Command: "npx", Args: []string{"@big/one"}, Agents: []string{"codex"}},
		{Name: "far", Transport: "http", URL: "https://FAR.dev/mcp/", Agents: []string{"claude"}},
		{Name: "idle", Command: "npx", Args: []string{"no-agent"}},
		{Name: "mine", Transport: "http", URL: "https://intranet.corp/mcp?key=abc", Agents: []string{"claude"}},
		{Name: "bare", Command: "npx", Agents: []string{"claude"}},
	}}
	listed = append(listed, MarketServer{ID: "bare-one", server: Server{Command: "npx"}})
	got := l.publicServers(listed)
	if want := []string{"io.github.big/one", "io.github.far/away"}; !slices.Equal(got, want) {
		t.Fatalf("named %q, want %q", got, want)
	}
	for _, id := range got {
		for _, leak := range []string{"secret", "sk-1", "intranet", "abc", "twice"} {
			if strings.Contains(id, leak) {
				t.Errorf("%q carries %q", id, leak)
			}
		}
	}
}

// What ships with magpie: every featured and popular server's id is one
// the site takes (site/pluginstats.js's validServer), so none is dropped
// there or names more than a catalog id.
func TestPublicServerIDsAreCatalogIDs(t *testing.T) {
	ok := func(id string) bool {
		if id == "" || len(id) > 200 {
			return false
		}
		parts := strings.Split(id, "/")
		if len(parts) > 2 {
			return false
		}
		for _, p := range parts {
			if p == "" || !(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z' || p[0] >= '0' && p[0] <= '9') {
				return false
			}
			for _, c := range p {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
					return false
				}
			}
		}
		return true
	}
	for _, m := range append(slices.Clone(featured), popularServers()...) {
		if !ok(m.ID) {
			t.Errorf("market id %q is not one the site takes", m.ID)
		}
	}
}
