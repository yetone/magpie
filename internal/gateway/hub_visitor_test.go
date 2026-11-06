package gateway

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
)

// A caller on another computer, by any gateway key, is never given a
// server that acts as this computer's user (Magpie Browser, magpie's image
// server, another magpie's router), which /mcp/magpie/browser and the key's
// image models refuse it directly; one by a key held to some models,
// accounts or a spend limit isn't given a program run here either. Neither
// is listed, and neither runs when called by name.
func TestHubKeepsThisComputersOwnServersHere(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	servers := []HubServer{
		{Name: "magpie-browser", HereOnly: true, Program: true},
		{Name: "magpie-image", HereOnly: true, Program: true},
		{Name: "home-magpie", HereOnly: true, Bridge: true},
		{Name: "shell", Program: true},
		{Name: "docs"},
	}
	var tools []HubTool
	for _, s := range servers {
		tools = append(tools, HubTool{Server: s.Name, Tool: "do", Spec: hubSpec("do")})
	}
	f := &fakeHub{on: true, tools: map[string][]HubTool{"": tools}, servers: map[string][]HubServer{"": servers}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	keys, secrets := newCaller(t, "Full", "Narrow")
	if _, err := access.Update("models-key", access.Change{Key: keys[1].ID, Models: []string{"some/model"}}); err != nil {
		t.Fatal(err)
	}
	const there = "192.0.2.7:4000"
	listed := func(addr string, hdr ...string) []string {
		t.Helper()
		m := readMCP(t, hubPostAt(h, HubMCPAll, addr, toolsList, hdr...))
		var out []string
		for _, t := range m.Result.Tools {
			out = append(out, strings.TrimSuffix(t.Name, hubSep+"do"))
		}
		slices.Sort(out)
		return out
	}
	full := []string{"Authorization", "Bearer " + secrets[0]}
	narrow := []string{"Authorization", "Bearer " + secrets[1]}
	if got, want := listed(here), []string{"docs", "home-magpie", "magpie-browser", "magpie-image", "shell"}; !slices.Equal(got, want) {
		t.Errorf("this computer is given %v, want %v", got, want)
	}
	if got, want := listed(there, full...), []string{"docs", "shell"}; !slices.Equal(got, want) {
		t.Errorf("a full key from there is given %v, want %v", got, want)
	}
	if got, want := listed(there, narrow...), []string{"docs"}; !slices.Equal(got, want) {
		t.Errorf("a held key from there is given %v, want %v", got, want)
	}
	if got, want := listed(here, append([]string{"X-Forwarded-For", "203.0.113.9"}, full...)...), []string{"docs", "shell"}; !slices.Equal(got, want) {
		t.Errorf("through a proxy here, a full key is given %v, want %v", got, want)
	}
	for _, c := range []struct {
		name string
		auth []string
	}{
		{"magpie-browser", full}, {"magpie-image", full}, {"home-magpie", full},
		{"magpie-browser", narrow}, {"shell", narrow},
	} {
		m := readMCP(t, hubPostAt(h, HubMCPAll, there, toolsCall(c.name+hubSep+"do", `{"action":"tabs"}`), c.auth...))
		if m.Error == nil && !m.Result.IsError {
			t.Errorf("%s ran for %s from there: %+v", c.name, c.auth[1][:12], m)
		}
	}
	if got := f.ran(); len(got) != 0 {
		t.Fatalf("ran for a caller not given them: %q", got)
	}
	m := readMCP(t, hubPostAt(h, HubMCPAll, there, toolsCall("docs"+hubSep+"do", `{}`), narrow...))
	if m.Error != nil || m.Result.IsError {
		t.Fatalf("docs for the held key: %+v", m)
	}
	m = readMCP(t, hubPostAt(h, HubMCPAll, there, toolsCall("shell"+hubSep+"do", `{}`), full...))
	if m.Error != nil || m.Result.IsError {
		t.Fatalf("shell for the full key: %+v", m)
	}
}
