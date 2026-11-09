package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gwtool"
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

// With nothing set up, only memory, which needs no model, is listed.
func TestOwnMCPNoneWhenNothingIsOn(t *testing.T) {
	fresh(t)
	toolsAre(t, 0)
	if got := ownMCPNames(t, New()); len(got) != 1 || got[memoryServerName] != MemoryMCPPath {
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
