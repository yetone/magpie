package gui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// magpie web's page port serves /v1 too, and a rolling update's old pod
// kept the connections agents kept alive to it: once its gateway drains,
// each of the page port's replies closes its connection, as the gateway's
// own do, so the agent's next request dials the Service's next pod.
func TestPagePortClosesWhileDraining(t *testing.T) {
	prev := served.Load()
	t.Cleanup(func() { served.Store(prev) })
	gw := gateway.New()
	served.Store(gw)
	h := closesWhenDraining(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ask := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil))
		return rec.Header().Get("Connection")
	}
	if c := ask(); c != "" {
		t.Fatalf("before the drain: Connection %q", c)
	}
	gw.Drain(time.Minute)
	if c := ask(); c != "close" {
		t.Fatalf("draining: Connection %q, want close", c)
	}
}
