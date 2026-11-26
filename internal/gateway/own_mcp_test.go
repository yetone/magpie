package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gwtool"
	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// ownMCPNames asks the gateway for magpie's own MCP servers, as the chat
// page plugin does, and gives each one's name and path.
func ownMCPNames(t *testing.T, s *Server) map[string]string {
	t.Helper()
	r := httptest.NewRequest("GET", OwnMCPPath, nil)
	r.RemoteAddr = "127.0.0.1:50000"
	w := httptest.NewRecorder()
	lanGuard(s.Handler()).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", OwnMCPPath, w.Code, w.Body)
	}
	var got struct {
		Servers []OwnMCP `json:"servers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Servers == nil {
		t.Fatalf("%s: %v %s", OwnMCPPath, err, w.Body)
	}
	out := map[string]string{}
	for _, m := range got.Servers {
		out[m.Name] = m.Path
	}
	return out
}

func toolsAre(t *testing.T, n int) {
	t.Helper()
	was := toolsOn
	toolsOn = func() (int, error) { return n, nil }
	t.Cleanup(func() { toolsOn = was })
}

// With nothing set up, only memory, sessions and artifacts, which need no
// model, are listed.
func TestOwnMCPNoneWhenNothingIsOn(t *testing.T) {
	fresh(t)
	toolsAre(t, 0)
	if got := ownMCPNames(t, New()); len(got) != 3 || got[memoryServerName] != MemoryMCPPath || got[sessionsServerName] != SessionsMCPPath || got[artifactsServerName] != ArtifactsMCPPath {
		t.Fatalf("listed %v", got)
	}
}

// Image recognition is listed while a model sees, and not with it Off in
// Settings (ferock on Discord: the chat page gave a model the Library's
// servers only).
func TestOwnMCPListsVisionWhileOn(t *testing.T) {
	s, _ := eyed(t)
	toolsAre(t, 0)
	if got := ownMCPNames(t, s); got["magpie-vision"] != VisionMCPPath {
		t.Fatalf("vision on: %v", got)
	}
	noVision(t)
	if got := ownMCPNames(t, s); got["magpie-vision"] != "" {
		t.Fatalf("vision off: %v", got)
	}
}

// Image generation is listed while a model draws, and not with it Off.
func TestOwnMCPListsImageWhileOn(t *testing.T) {
	s, _ := easeled(t)
	toolsAre(t, 0)
	if got := ownMCPNames(t, s); got[imageServerName] != ImageMCPPath {
		t.Fatalf("image on: %v", got)
	}
	st := settings.Load()
	st.ImageGen = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if got := ownMCPNames(t, s); got[imageServerName] != "" {
		t.Fatalf("image off: %v", got)
	}
}

// Web search is listed while something searches and plugins/search
// serves, and not with the plugin off.
func TestOwnMCPListsSearchWhileOn(t *testing.T) {
	fresh(t)
	toolsAre(t, 0)
	apis := newSearchAPIs(t)
	if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
		t.Fatal(err)
	}
	s := New()
	if got := ownMCPNames(t, s); got["magpie-web-search"] != SearchMCPPath {
		t.Fatalf("search on: %v", got)
	}
	stop := gwtool.Gate() // plugins/search off: nothing serves web_search
	defer stop()
	searchOn() // TestMain's
	defer func() { searchOn = gwtool.Serve(gwtool.WebSearch) }()
	if got := ownMCPNames(t, s); got["magpie-web-search"] != "" {
		t.Fatalf("plugins/search off: %v", got)
	}
}

// The plugin tools server is listed while plugins/tools serves and a
// plugin that is on gives a tool.
func TestOwnMCPListsPluginToolsWhileOn(t *testing.T) {
	fresh(t)
	s := New()
	toolsAre(t, 2)
	if got := ownMCPNames(t, s); got["magpie-tools"] != ToolsMCPPath {
		t.Fatalf("tools on: %v", got)
	}
	toolsAre(t, 0)
	if got := ownMCPNames(t, s); got["magpie-tools"] != "" {
		t.Fatalf("no plugin gives a tool: %v", got)
	}
	toolsAre(t, 2)
	stop := gwtool.Gate() // plugins/tools off
	defer stop()
	if got := ownMCPNames(t, s); got["magpie-tools"] != "" {
		t.Fatalf("plugins/tools off: %v", got)
	}
	defer gwtool.Serve(gwtool.PluginTools)()
	if got := ownMCPNames(t, s); got["magpie-tools"] != ToolsMCPPath {
		t.Fatalf("plugins/tools on: %v", got)
	}
}

// Each of magpie's own servers the Library's Discover lists says why it
// can't answer yet exactly while ownMCP leaves it out, and tells nothing
// set up from turned off in Settings (matrix0487 on Discord: all three in
// Discover, never a dead tool).
func TestOwnUnreadyWhileNotListed(t *testing.T) {
	fresh(t)
	toolsAre(t, 0)
	for _, n := range []string{"magpie-web-search", "magpie-vision", imageServerName} {
		if why := OwnUnready(n); !strings.Contains(why, "yet") {
			t.Errorf("%s with nothing set up: %q", n, why)
		}
	}
	if why := OwnUnready("magpie-browser"); why != "" {
		t.Errorf("magpie-browser: %q", why)
	}
	s, _ := eyed(t)
	if got := ownMCPNames(t, s); got["magpie-vision"] == "" || OwnUnready("magpie-vision") != "" {
		t.Fatalf("a model sees: listed %v, unready %q", got, OwnUnready("magpie-vision"))
	}
	noVision(t)
	if why := OwnUnready("magpie-vision"); !strings.Contains(why, "off in Settings") {
		t.Fatalf("vision off: %q", why)
	}
	st := settings.Load()
	st.ImageGen = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if why := OwnUnready(imageServerName); !strings.Contains(why, "off in Settings") {
		t.Fatalf("image generation off: %q", why)
	}
	stop := gwtool.Gate() // plugins/search off
	defer stop()
	searchOn()
	defer func() { searchOn = gwtool.Serve(gwtool.WebSearch) }()
	if why := OwnUnready("magpie-web-search"); !strings.Contains(why, "Plugins") {
		t.Fatalf("plugins/search off: %q", why)
	}
}

// plugins#873 (duhu2000): the chat page reads the Library's servers from
// library.json and called a server magpie is signed in to with no
// sign-in. OwnMCPPath tells it, with no credential, each server magpie is
// signed in to: its name and URL, to match the Library's entry by both,
// and the path on this gateway that carries magpie's sign-in, which
// answers. A key held to other servers is told of none of them.
func TestOwnMCPListsSignedInServers(t *testing.T) {
	fresh(t)
	toolsAre(t, 0)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	f := mcpauthtest.New(t)
	f.SignIn(t, "neon")
	token, _ := mcpauth.Token(t.Context(), "neon")
	h := lanGuard(New().Handler())
	list := func(hdr ...string) (string, []SignedInMCP) {
		t.Helper()
		r := httptest.NewRequest("GET", OwnMCPPath, nil)
		r.RemoteAddr = "127.0.0.1:50000"
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var got struct {
			SignedIn []SignedInMCP `json:"signedIn"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("%s: %d %s", OwnMCPPath, w.Code, w.Body)
		}
		return w.Body.String(), got.SignedIn
	}
	body, got := list()
	if len(got) != 1 || got[0] != (SignedInMCP{Name: "neon", URL: f.URL, Path: "/mcp/neon"}) {
		t.Fatalf("signed in: %+v", got)
	}
	if token == "" || strings.Contains(body, token) {
		t.Fatalf("the list carries the token (%q): %s", token, body)
	}

	// the path it names answers, with magpie's sign-in
	r := httptest.NewRequest("POST", got[0].Path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "127.0.0.1:3425"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || f.Seen.Get("Authorization") != "Bearer "+token {
		t.Fatalf("%s: %d %s, the server got %q", got[0].Path, w.Code, w.Body, f.Seen.Get("Authorization"))
	}

	other := keyHeldToMCP(t, "Other", "github")
	if _, got := list("Authorization", "Bearer "+other); len(got) != 0 {
		t.Fatalf("a key held to github is told of %+v", got)
	}
	neon := keyHeldToMCP(t, "Neon", "neon")
	if _, got := list("Authorization", "Bearer "+neon); len(got) != 1 || got[0].Name != "neon" {
		t.Fatalf("a key given neon: %+v", got)
	}
}
