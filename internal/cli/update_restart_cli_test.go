package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// magpie update tells the magpie running what it put in, and says what
// came of it: a restart, a wait, magpie serve, an older magpie (404), or
// none running.
func TestUpdateAsksTheRunningMagpieToRestart(t *testing.T) {
	var reply gateway.RestartReply
	var asked string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != gateway.RestartPath {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		asked = string(b)
		json.NewEncoder(w).Encode(reply)
	}))
	defer ts.Close()
	for _, c := range []struct {
		reply gateway.RestartReply
		want  string
	}{
		{gateway.RestartReply{State: "restarting", Version: "v0.1.1", Handover: true}, "restarts into v0.1.2 now, handing its gateway over"},
		{gateway.RestartReply{State: "waiting", Version: "v0.1.1", Busy: &gateway.Busy{Requests: 2}}, "once the 2 request(s)"},
		{gateway.RestartReply{State: "unsupported", Version: "v0.1.1"}, "magpie serve"},
		{gateway.RestartReply{State: "current", Version: "v0.1.1"}, "another copy"},
	} {
		reply = c.reply
		if got := restartRunning(ts.URL, "v0.1.2"); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q", c.reply.State, got)
		}
		if asked != `{"version":"v0.1.2","force":false}` {
			t.Errorf("asked %q", asked)
		}
	}
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	if got := restartRunning(old.URL, "v0.1.2"); !strings.Contains(got, "quit and reopen") {
		t.Errorf("an older magpie: %q", got)
	}
	old.Close()
	if got := restartRunning(old.URL, "v0.1.2"); !strings.Contains(got, "isn't running") {
		t.Errorf("none running: %q", got)
	}
}
