package gui

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// /api/usage/traffic answers the gateway's bytes so far and its requests
// in flight, which the Requests read a moment apart for how fast traffic
// flows now; {} when this process serves no gateway, so the page shows no
// rate rather than a made-up zero.
func TestUsageTrafficSaysWhatTheGatewayCounted(t *testing.T) {
	h := builtinHome(t)
	get := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/usage/traffic", nil))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return strings.TrimSpace(rec.Body.String())
	}
	prev := served.Swap(nil)
	t.Cleanup(func() { served.Store(prev) })
	if b := get(); b != "{}" {
		t.Fatalf("no gateway served here: %s", b)
	}
	served.Store(gateway.New())
	if b := get(); b != `{"up":0,"down":0,"requests":0}` {
		t.Fatalf("a gateway served here: %s", b)
	}
}
