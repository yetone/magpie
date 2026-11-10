package gui

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
)

// genericIcon are the clients magpie has no logo for yet, drawn with the
// generic cube on purpose (#4487). Each leaves this list when its real
// logo, from the app or its vendor, is added.
var genericIcon = map[string]bool{"ante": true, "pencil": true, "openpencil": true, "newmax": true, "kirocrew": true, "raft": true}

// Every client a request can be recorded as is drawn by its own name and
// icon in the Routing page's request list, Usage and Sessions, which all
// look the request's client up in /api/state's clients (agent.Clients):
// one not there shows its raw id and the cube (jiabubu on Discord:
// kimi-code-cli and kimi-code-desktop). This holds the table to the ids
// the gateway records, and each entry's icon to a file app.js's icon()
// loads: a PNG only when pngIcons names it, else an SVG.
func TestEveryClientHasNameAndIcon(t *testing.T) {
	js, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`const pngIcons = new Set\(\[([^\]]*)\]\)`).FindSubmatch(js)
	if m == nil {
		t.Fatal("app.js's pngIcons isn't found")
	}
	png := map[string]bool{}
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1) {
		png[string(q[1])] = true
	}
	exists := func(f string) bool { _, err := os.Stat("assets/icons/" + f); return err == nil }

	clients := map[string]bool{}
	for _, a := range agent.Clients() {
		clients[a.ID] = true
		if strings.TrimSpace(a.Name) == "" {
			t.Errorf("%s has no name", a.ID)
		}
		switch {
		case a.Icon == "generic":
			if !genericIcon[a.ID] {
				t.Errorf("%s is drawn with the generic cube: give it its real logo", a.ID)
			}
		case a.Icon == "" || strings.HasPrefix(a.Icon, "file:") || strings.ContainsAny(a.Icon, ":/"):
			t.Errorf("%s's icon %q isn't one of magpie's", a.ID, a.Icon)
		case png[a.Icon]:
			if !exists(a.Icon + ".png") {
				t.Errorf("%s's icon: pngIcons names %s, with no icons/%[2]s.png", a.ID, a.Icon)
			}
		case !exists(a.Icon + ".svg"):
			if exists(a.Icon + ".png") {
				t.Errorf("%s's icon %s is a PNG app.js's pngIcons doesn't name, so it is drawn as the cube", a.ID, a.Icon)
			} else {
				t.Errorf("%s's icon %s has no file in icons/", a.ID, a.Icon)
			}
		}
	}
	for id := range genericIcon {
		if !clients[id] {
			t.Errorf("genericIcon names %s, which is no client", id)
		}
	}
	// the ids the gateway gives a request by more than its User-Agent
	// (gateway.agentOf), and those it names by one: each is a client
	for _, id := range []string{"claude-desktop", gateway.ScienceID, "cursor-local", "codex", "magpie", "kimi", "kimi-code-desktop"} {
		if !clients[id] {
			t.Errorf("the gateway records requests as %s, which isn't among the clients", id)
		}
	}
}
