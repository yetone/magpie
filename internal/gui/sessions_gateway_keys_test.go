package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// linkaww on Discord, a magpie in Docker in gateway mode: Usage › Sessions
// was all but empty. Its list is the sessions whose keys the stats name
// (st.keys), and those were only the sessions read from this computer's
// files: one that came through the gateway alone, which in gateway mode
// is every one, was counted in the totals and never listed.
func TestGatewayModeStatsNameTheGatewaySessions(t *testing.T) {
	sandboxHome(t)
	sessions.Reset()
	forgetStats()
	t.Cleanup(func() {
		webPage.Store(false)
		ownWas.Store(0)
		sessions.Reset()
		forgetStats()
	})
	const remote = "8c1f0e7a-1111-4222-8333-444455556666"
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	usage.Append(usage.Record{Time: at, Agent: "claude", Session: remote, Provider: "fake", Model: "m", Input: 50, Output: 5, Status: 200})

	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	gatewayModeRoutes(mux)
	ok := func(method, path, body string) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	keys := func(days string) []string {
		t.Helper()
		var st struct{ Keys []string }
		if err := json.Unmarshal(ok("GET", "/api/sessions/stats?days="+days, ""), &st); err != nil {
			t.Fatal(err)
		}
		return st.Keys
	}
	for _, mode := range []string{"off", "on"} {
		if mode == "on" {
			webPage.Store(true)
			ok("POST", "/api/settings/gateway-mode", `{"mode":"on"}`)
		}
		for _, days := range []string{"0", "7", "1"} {
			if k := keys(days); !slices.Contains(k, "claude:"+remote) {
				t.Fatalf("gateway mode %s, days=%s: keys %v leave out the gateway's session", mode, days, k)
			}
		}
	}
}
