package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
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

// Magpie Browser is given to a keyed caller from elsewhere too, and its
// call goes under the key's session (browser.go), whatever session the
// caller names; never to one from elsewhere with no key, nor to a page.
func TestHubGivesAKeyItsBrowser(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	servers := []HubServer{{Name: "magpie-browser", HereOnly: true, Program: true, Keyed: true}}
	f := &fakeHub{on: true, tools: map[string][]HubTool{"": {{Server: "magpie-browser", Tool: "do", Spec: hubSpec("do")}}}, servers: map[string][]HubServer{"": servers}}
	var mu sync.Mutex
	var seen []string
	f.call = func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		mu.Lock()
		seen = append(seen, HubCallerOf(ctx).Session)
		mu.Unlock()
		return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil
	}
	withHub(t, f)
	h := lanGuard(New().Handler())
	keys, secrets := newCaller(t, "Host", "Narrow")
	if _, err := access.Update("models-key", access.Change{Key: keys[1].ID, Models: []string{"some/model"}}); err != nil {
		t.Fatal(err)
	}
	const there = "192.0.2.7:4000"
	call := toolsCall("magpie-browser"+hubSep+"do", `{}`)
	for i, auth := range [][]string{{"Authorization", "Bearer " + secrets[0], "Mcp-Session-Id", "abc"}, {"Authorization", "Bearer " + secrets[1]}} {
		m := readMCP(t, hubPostAt(h, HubMCPAll, there, call, auth...))
		if m.Error != nil || m.Result.IsError {
			t.Fatalf("key %d from there: %+v", i, m)
		}
	}
	if w := hubPostAt(h, HubMCPAll, here, call, "Origin", "http://localhost:5173"); w.Code != http.StatusForbidden {
		t.Fatalf("a page here: %d %s", w.Code, w.Body.String())
	}
	m := readMCP(t, hubPostAt(h, HubMCPAll, here, call, "Mcp-Session-Id", "abc"))
	if m.Error != nil || m.Result.IsError {
		t.Fatalf("here: %+v", m)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"key:" + keys[0].ID + "/hub--abc", "key:" + keys[1].ID + "/", "hub--abc"}
	if !slices.Equal(seen, want) {
		t.Fatalf("sessions %q, want %q", seen, want)
	}
	for _, v := range []HubVisitor{{Away: true}, {Away: true, Held: true}} {
		if v.Gives(servers[0]) {
			t.Errorf("%+v is given Magpie Browser", v)
		}
	}
	if (HubVisitor{Away: true, Key: "k"}).Gives(HubServer{Name: "magpie-image", HereOnly: true}) {
		t.Error("a key from elsewhere is given magpie's image server")
	}
}

// Alma's MCP client is Node's fetch: Sec-Fetch-Mode: cors, no Origin, no
// Sec-Fetch-Site, no browser agent. It is this computer's own agent and is
// given magpie's image server (the Alma report, chang_lochuan on X). A web
// page, whatever Origin a browser can send it with, still isn't, with no
// key.
func TestHubGivesAlmaItsHereOnlyServersNotAPage(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	servers := []HubServer{{Name: "magpie-image", HereOnly: true, Program: true}, {Name: "magpie-vision"}}
	var tools []HubTool
	for _, s := range servers {
		tools = append(tools, HubTool{Server: s.Name, Tool: "do", Spec: hubSpec("do")})
	}
	f := &fakeHub{on: true, tools: map[string][]HubTool{"alma": tools}, servers: map[string][]HubServer{"alma": servers}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	listed := func(host string, hdr ...string) (int, []string) {
		t.Helper()
		w := hubPostAt(h, HubMCPPath+"alma", here, toolsList, hdr...)
		if host != "" {
			r := httptest.NewRequest(http.MethodPost, HubMCPPath+"alma", strings.NewReader(toolsList))
			r.RemoteAddr, r.Host = here, host
			for i := 0; i+1 < len(hdr); i += 2 {
				r.Header.Set(hdr[i], hdr[i+1])
			}
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
		}
		if w.Code != http.StatusOK {
			return w.Code, nil
		}
		var out []string
		for _, x := range readMCP(t, w).Result.Tools {
			out = append(out, strings.TrimSuffix(x.Name, hubSep+"do"))
		}
		slices.Sort(out)
		return w.Code, out
	}
	if code, got := listed("", "Sec-Fetch-Mode", "cors", "User-Agent", "node"); code != 200 || !slices.Equal(got, []string{"magpie-image", "magpie-vision"}) {
		t.Errorf("Alma's fetch is given %d %v", code, got)
	}
	pages := [][]string{
		{"Origin", "null"},
		{"Origin", "null", "Sec-Fetch-Mode", "cors"},
		{"Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"},
		{"Origin", "http://localhost:5173", "Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "cors"},
		{"Origin", "http://evil.localhost:3425", "Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "cors"},
		{"Origin", "http://tauri.localhost", "Sec-Fetch-Mode", "cors"},
		{"Origin", "app://obsidian.md", "Sec-Fetch-Mode", "cors"},
		{"Origin", "chrome-extension://abcdefghijklmnopabcdefghijklmnop", "Sec-Fetch-Mode", "cors"},
		{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "no-cors"},
		{"Sec-Fetch-Site", "none", "Sec-Fetch-Mode", "navigate"},
		{"User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Safari/605.1.15"},
		{"User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Safari/605.1.15", "Sec-Fetch-Mode", "cors"},
	}
	for _, hdr := range pages {
		if _, got := listed("", hdr...); slices.Contains(got, "magpie-image") {
			t.Errorf("a page %v is given %v", hdr, got)
		}
		w := hubPostAt(h, HubMCPPath+"alma", here, toolsCall("magpie-image"+hubSep+"do", `{}`), hdr...)
		if w.Code == http.StatusOK {
			if m := readMCP(t, w); m.Error == nil && !m.Result.IsError {
				t.Errorf("a page %v ran magpie-image", hdr)
			}
		}
	}
	// a page rebound to 127.0.0.1 under its own name, with and without an Origin
	for _, hdr := range [][]string{{"Origin", "http://rebind.example:3425", "Sec-Fetch-Site", "same-origin"}, {"Sec-Fetch-Mode", "cors", "User-Agent", "Mozilla/5.0"}, {"Origin", "http://rebind.example:3425"}} {
		if _, got := listed("rebind.example:3425", hdr...); slices.Contains(got, "magpie-image") {
			t.Errorf("a rebound page %v is given %v", hdr, got)
		}
	}
	if got := f.ran(); len(got) != 0 {
		t.Fatalf("ran for a page: %q", got)
	}
}
