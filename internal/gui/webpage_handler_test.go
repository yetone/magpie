package gui

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// webPageHandler is Handler for a test that serves magpie web's own page.
// Handler marks the process as magpie web (webPage) for good, which is
// right for the one page a real process serves but would carry into every
// later test of the package: one with a sandbox HOME and no agents would
// then be in gateway mode by itself, and read none of its own sessions
// (TestUsageRequestContent went red so). The mark ends with the test.
func webPageHandler(t *testing.T, w Windows, gw *gateway.Server) http.Handler {
	t.Helper()
	was := webPage.Load()
	t.Cleanup(func() {
		webPage.Store(was)
		ownWas.Store(0)
	})
	return Handler(w, gw)
}

// Every test that serves magpie web's page goes through webPageHandler, so
// none leaves the package in magpie web's gateway mode.
func TestWebPageHandlerEveryWhere(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	direct := regexp.MustCompile(`\bHandler\((askingHost\{)?webHost\{`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if direct.MatchString(line) && !strings.Contains(line, "webPageHandler(") {
				t.Errorf("%s:%d serves magpie web's page without webPageHandler: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
