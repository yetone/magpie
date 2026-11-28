package gui

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A switch of agy's sign-in refused while agy or the Antigravity app runs
// answers with a code and an i18n.js key naming what to quit, its pids
// in a slot, so the page says it in the reader's language (#1908); the
// English stays in error for the CLI and logs.
func TestFailSaysAgyRunning(t *testing.T) {
	for _, c := range []struct {
		name         string
		r            provider.AgyRunning
		said, pids   string
		pluginInPIDs string
	}{
		{"app", provider.AgyRunning{App: true}, "Quit the Antigravity app first", "", ""},
		{"agy", provider.AgyRunning{PIDs: []int{20, 50}}, "Quit agy (pid {pids}) first", "20, 50", ""},
		{"both", provider.AgyRunning{App: true, PIDs: []int{20}, Plugin: []int{60}}, "Quit the Antigravity app and agy (pid {pids})", "20, 60", "60"},
		{"plugin's", provider.AgyRunning{Plugin: []int{60}}, "The Antigravity CLI plugin's agy (pid {plugin})", "60", "60"},
		{"app and plugin's", provider.AgyRunning{App: true, Plugin: []int{60}}, "Quit the Antigravity app and agy (pid {pids})", "60", "60"},
	} {
		r := c.r
		rw := httptest.NewRecorder()
		fail(rw, &r)
		var out map[string]string
		if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if rw.Code != 400 || out["code"] != "agy_running" || !strings.HasPrefix(out["said"], c.said) || out["pids"] != c.pids || out["plugin"] != c.pluginInPIDs || !strings.HasPrefix(out["error"], "quit ") {
			t.Errorf("%s: fail answered %d %v", c.name, rw.Code, out)
		}
	}
	rw := httptest.NewRecorder()
	fail(rw, &provider.AgyUnlistedError{Err: errors.New("ps: not found")})
	var out map[string]string
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil || out["code"] != "agy_unlisted" || out["said"] == "" || !strings.Contains(out["error"], "ps: not found") {
		t.Fatalf("an unlisted refusal answered %v (%v)", out, err)
	}
}

// Every key a refused agy switch can be said in is in i18n.js in every
// language: a t() of a word it lacks shows the English.
func TestAgyRunningSaidIsTranslated(t *testing.T) {
	words, err := os.ReadFile("assets/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	saids := []string{agyUnlistedSaid}
	for _, r := range []provider.AgyRunning{{App: true}, {PIDs: []int{1}}, {App: true, PIDs: []int{1}}, {Plugin: []int{1}}} {
		saids = append(saids, agyRunningSaid(r))
	}
	for _, said := range saids {
		key, _ := json.Marshal(said)
		if n := strings.Count(string(words), string(key)+":"); n != 4 {
			t.Errorf("i18n.js has %q in %d languages, want zh, zh-TW, ja and de", said, n)
		}
	}
}
