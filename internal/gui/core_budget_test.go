package gui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// What the window's core may weigh, in bytes of script without its
// whole-line comments: everything index.html loads that isn't a plugin's
// (assets/plugins/<id>/) and isn't the words (i18n.js). A page belongs in
// a plugin. Each page moved out lowers these to what is left, so a page
// can't quietly grow back into the core; a change that needs more raises
// them and says why in its commit. Comments aren't counted, so they are
// never what pays for a change (#5918); the history of each number is in
// `git log -L` of this file.
const (
	appScriptBudget  = 1_086_267 // app.js: 1,080,614 at 8016205e3, with the 5,653 of room it had
	coreScriptBudget = 1_268_961 // every core script at 8016205e3, the old measure 682 over its cap there (#5918); +367 for the model test on a chosen account (tmr11235), no page left to move it to; +151 for the provider menu's "Only on this device" item and its four translations; +458 for Settings › About's Report a problem row and its translations; +113 for withScheme, the one rule every typed address goes through (eachg0 on X), cut to the shortest form that keeps it whole; +10 for the kept whole's tooltip (Musikrv77852 on X), the past-cycle line reused for it, no page left to move it to; +1,112 for Codex's Open button and its Open Codex with menu (Yink88564391 on X), packed to the shortest form, no page left to move them to; +168 for Discover's long lists drawn in part (the owner, 11-13), after the rest of its hook moved into plugingrid.js, imported when Plugins first draws; +296 for the cap toggle and Smart sentences that say what they do and the Sign in on another computer row (Wing8Aqua on X), packed to the shortest form, no page left to move them to; +333 for In turn's figure in a provider's editor and its words naming the message that goes round (Wing8Aqua on X), the figure itself in rotfig.js, imported when In turn is first shown, the hook packed to the shortest form; +236 for a lone capped Codex account's row saying its turns go to the fallbacks when Codex is connected (not_4O4 on X), the two helpers inlined, no page left to move it to; +717 for omp's advisor (Awad_Yoo on X, #6408): its three rows moved into ompadvisor.js, imported when an omp's settings show, and what is left is the import, the role's " · off" and the Settings line's name, packed to the shortest form; +468 for a plugin's own file import (auth.import) in the import sheet, in the plugin's words (marchen_cc on X), packed to one statement; importSay is read as the Providers rows draw, so it has no lazy page to go to, and nothing else was moved out; +180 for Claude Magpie's own Claude Code folder (xMopf7RF on X, #6459): its Advanced fold in claudemagpie.js, imported when it first shows, and what is left is the import and the fold's place in the Separate app line, 216 bytes packed to the shortest form, less the 36 of room there was; +81 for the Use Claude Magpie button on Claude Desktop's row (xMopf7RF on X), drawn by claudemagpie.js too, what is left the one line that places it (#2710); +225 for the Hermes row's Other profiles lines (Norethcy on X), which 1d60b165c took out while each profile had a row and c96f33d65 left with no place at all, packed to the shortest form; the dead hermes# Default note went with it, and the Agents rows draw it, so there is no page to move it to; +397 for a key's own price rate (tnt12231 on X): its lines, check and placeholder in keyrates.js, imported when a provider with two or more keys opens, and what is left is the import, the draft's copy, Advanced opening for it, the save's one line and proxyLines drawing another kind of line; +316 for an image or video model's chip saying what it makes from its right-click (iceloon on X): the chat chip's Makes… now shared with it, and the media chip's Copy model ID left out, packed to the shortest form; the provider's editor draws it, so there is no page to move it to
)

// codeBytes is how many bytes of b are code: its blank lines, its lines
// that are only a // comment, and its /* */ blocks that start a line,
// aren't counted.
func codeBytes(b []byte) int {
	n, block := 0, false
	for _, l := range strings.SplitAfter(string(b), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case block:
			block = !strings.Contains(t, "*/")
		case t == "", strings.HasPrefix(t, "//"):
		case strings.HasPrefix(t, "/*") && (strings.HasSuffix(t, "*/") || !strings.Contains(t, "*/")):
			block = !strings.HasSuffix(t, "*/")
		default:
			n += len(l)
		}
	}
	return n
}

func TestCoreScriptBudget(t *testing.T) {
	page, err := os.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	total, n := 0, 0
	for _, m := range regexp.MustCompile(`<script src="([^"?]+)`).FindAllStringSubmatch(string(page), -1) {
		name := m[1]
		if strings.HasPrefix(name, "plugins/") || name == "i18n.js" {
			continue
		}
		b, err := os.ReadFile(filepath.Join("assets", name))
		if os.IsNotExist(err) {
			continue // made by the server as it serves the page (boot.js)
		}
		if err != nil {
			t.Fatal(err)
		}
		code := codeBytes(b)
		if name == "app.js" && code > appScriptBudget {
			t.Errorf("app.js is %d bytes of code, over its budget of %d: put the page's code in its plugin, or raise the budget and say why (comments aren't counted, so cutting them doesn't help)", code, appScriptBudget)
		}
		total += code
		n++
	}
	if n < 5 {
		t.Fatalf("found only %d core scripts in index.html", n)
	}
	if total > coreScriptBudget {
		t.Errorf("the core's scripts are %d bytes of code, over their budget of %d: put the page's code in its plugin, or raise the budget and say why (comments aren't counted, so cutting them doesn't help)", total, coreScriptBudget)
	}
}
