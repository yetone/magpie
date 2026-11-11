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

// ljlwlcz on X: the first update from a magpie older than gateway.json
// finds no record, looked at 3425 and said "isn't running" while the
// service ran on 10100. Without a record it looks where such a magpie runs.
func TestUpdateFindsAMagpieThatWroteNoRecord(t *testing.T) {
	fakeServing(t, 4242) // sandbox HOME, no record, MAGPIE_ADDR unset
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"name":"magpie","version":"v0.1.3600"}`)
	}))
	defer ts.Close()
	oldProbe, oldCands := updateProbe, updateCandidates
	t.Cleanup(func() { updateProbe, updateCandidates = oldProbe, oldCands })
	updateProbe = func(u string) bool { return gateway.ServedAt(u).Running }
	updateCandidates = func() []string { return []string{"http://127.0.0.1:1", ts.URL} }

	if got := updateBase(""); got != ts.URL {
		t.Errorf("no record, the service on another port: %q, want %q", got, ts.URL)
	}
	if got := updateBase("http://127.0.0.1:7"); got != "http://127.0.0.1:7" {
		t.Errorf("--addr is the user's word: %q", got)
	}
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:9")
	if got := updateBase(""); got != gateway.URL() {
		t.Errorf("MAGPIE_ADDR is the user's word: %q", got)
	}
	// nothing answers anywhere: the address the command knew
	t.Setenv("MAGPIE_ADDR", "")
	updateCandidates = func() []string { return []string{"http://127.0.0.1:1"} }
	if got := updateBase(""); got != gateway.ClientURL() {
		t.Errorf("nothing running: %q", got)
	}
}

// --force reaches the running magpie, and its answer is told plainly: a
// forced restart, a bounded wait with its minutes, and an old magpie that
// ignored --force.
func TestUpdateForceAndTheBoundedWaitAreSaid(t *testing.T) {
	var reply gateway.RestartReply
	var body string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		json.NewEncoder(w).Encode(reply)
	}))
	defer ts.Close()

	reply = gateway.RestartReply{State: "restarting", Version: "v0.1.1", Forced: true}
	if got := restartAsk(ts.URL, "v0.1.2", true); !strings.Contains(got, "whatever it has in flight") || !strings.Contains(body, `"force":true`) {
		t.Errorf("forced: %q (asked %s)", got, body)
	}
	reply = gateway.RestartReply{State: "waiting", Version: "v0.1.1", Busy: &gateway.Busy{Requests: 3}, Drain: 600}
	got := restartAsk(ts.URL, "v0.1.2", false)
	if !strings.Contains(got, "3 request(s)") || !strings.Contains(got, "10 min at the latest") || !strings.Contains(got, "--force") || !strings.Contains(body, `"force":false`) {
		t.Errorf("bounded wait: %q (asked %s)", got, body)
	}
	reply = gateway.RestartReply{State: "waiting", Version: "v0.1.1", Busy: &gateway.Busy{Requests: 3}}
	if got := restartAsk(ts.URL, "v0.1.2", true); !strings.Contains(got, "too old to know --force") {
		t.Errorf("an old magpie that ignores --force: %q", got)
	}
	rest, _, _, force, err := updateFlagsForce([]string{"update", "--force"})
	if err != nil || !force || len(rest) != 1 {
		t.Errorf("--force parsed as %v %v %v", rest, force, err)
	}
}
