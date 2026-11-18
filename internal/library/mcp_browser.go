package library

// A server that drives a Chrome it starts itself, Chrome DevTools' or
// Playwright's, starts in magpie's container and lists its tools, and then
// every one of them fails: "Could not find Google Chrome executable for
// channel 'stable' at: /opt/google/chrome/chrome" (Passings_z on X). The
// image ships no browser, which would add some 300 MB for every user of
// it, so the check tells such a server as one with no browser to start
// (Why "nobrowser"), and the page says what works there instead: Magpie
// Browser, which drives the user's own Chrome through its extension, or
// the server's own option for a Chrome it can reach (Detail).

import (
	"os"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/gateway"
)

// browserNeed is a package that starts Chrome on its own: the option that
// points it at a running one instead, and the options that each say where
// its browser is, without dashes and in lower case, as yargs and
// commander take them in either spelling.
type browserNeed struct {
	remote    string
	elsewhere map[string]bool
}

var browserNeeds = map[string]browserNeed{
	// chrome-devtools-mcp's options: --browserUrl (-u), --wsEndpoint (-w),
	// --executablePath (-e), --channel, --autoConnect
	"chrome-devtools-mcp": {remote: "--browserUrl", elsewhere: map[string]bool{
		"browserurl": true, "u": true, "wsendpoint": true, "w": true, "executablepath": true, "e": true,
		"channel": true, "autoconnect": true}},
	// @playwright/mcp's: --cdp-endpoint, --executable-path, --browser
	// (its own Chromium, Firefox or WebKit), --extension
	"@playwright/mcp": {remote: "--cdp-endpoint", elsewhere: map[string]bool{
		"cdpendpoint": true, "executablepath": true, "browser": true, "extension": true}},
}

// Where both look for Chrome on Linux, and whether magpie runs in a
// container; vars for tests.
var (
	linuxChrome  = "/opt/google/chrome/chrome"
	runsInBox    = gateway.InContainer
	browserGOOS  = runtime.GOOS
	chromeExists = func(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }
)

// noBrowser is the option s's server takes for a Chrome it can reach, when
// s runs one that starts Chrome itself, in a container with none, and
// isn't pointed at a browser elsewhere; "" otherwise.
func noBrowser(s *Server) string {
	if s.Remote() || browserGOOS != "linux" {
		return ""
	}
	need, rest, ok := browserPackage(s)
	if !ok {
		return ""
	}
	for _, a := range rest {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		k, _, _ := strings.Cut(a, "=")
		if need.elsewhere[strings.ToLower(strings.ReplaceAll(k, "-", ""))] {
			return ""
		}
	}
	if chromeExists(linuxChrome) || !runsInBox() {
		return ""
	}
	return need.remote
}

// browserPackage is the package s runs by npx (or bunx, pnpx), when it is
// one that starts Chrome, and the words after it.
func browserPackage(s *Server) (browserNeed, []string, bool) {
	switch progName(s.Command) {
	case "npx", "bunx", "pnpx":
	default:
		return browserNeed{}, nil, false
	}
	for i := 0; i < len(s.Args); i++ {
		a := s.Args[i]
		if a == "-p" || a == "--package" {
			i++ // the package npx installs; the program is what follows
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		name := a
		if m := npmSpec.FindStringSubmatch(a); m != nil {
			name = m[1]
		}
		need, ok := browserNeeds[name]
		return need, s.Args[i+1:], ok
	}
	return browserNeed{}, nil, false
}
