package gui

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// POST /api/pipelines/check takes only a pipeline magpie serves, and with
// no gateway of its own (another magpie serves it) says where to check.
func TestPipelineCheckRoute(t *testing.T) {
	onePortHome(t)
	prev := served.Swap(nil)
	t.Cleanup(func() { served.Store(prev) })
	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		handlePipelineCheck(w, httptest.NewRequest("POST", "/api/pipelines/check", strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	if code, body := post(`{"kind":"chat"}`); code < 400 || !strings.Contains(body, "no such pipeline") {
		t.Fatalf("an unknown kind: %d %s", code, body)
	}
	if code, body := post(`{"kind":"speech"}`); code < 400 || !strings.Contains(body, "Another magpie serves the gateway") {
		t.Fatalf("no gateway here: %d %s", code, body)
	}
	served.Store(gateway.New())
	code, body := post(`{"kind":"speech"}`)
	var c gateway.PipelineCheck
	if err := json.Unmarshal([]byte(body), &c); err != nil || code != 200 || c.Kind != "speech" || !c.Unset {
		t.Fatalf("a fresh home has no speech model: %d %s", code, body)
	}
}
