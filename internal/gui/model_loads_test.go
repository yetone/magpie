package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// /api/model-loads says how busy each plugin model is, keyed by the ref
// an agent's model picker gives the row, lowercased, so the Agents page
// shows the provider editor's figure by the model (axelburks on Discord).
// A model the user lists by another id is under that id; a stale load,
// and a model never reported, say nothing.
func TestModelLoadsByRef(t *testing.T) {
	sandboxHome(t)
	plugin.ForgetLoads()
	t.Cleanup(plugin.ForgetLoads)
	s := settings.Load()
	s.ModelIDs = map[string]string{"acme/Fast": "big", "acme/grok": "grok-4.7@256k"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	plugin.NoteLoadsForTest("acme", map[string]float64{"big": 87.4, "grok-4.7": 60, "GLM-5": 130}, now)
	plugin.NoteLoadsForTest("acme", map[string]float64{"old": 40}, now.Add(-plugin.LoadFresh-time.Minute))
	mux := http.NewServeMux()
	speedsAPI(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/model-loads", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got map[string]float64
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]float64{"acme/fast": 87.4, "acme/grok": 60, "acme/glm-5": 130} {
		if v, ok := got[ref]; !ok || v != want {
			t.Errorf("%s = %v (%v); want %v", ref, v, ok, want)
		}
	}
	for _, ref := range []string{"acme/old", "acme/quiet"} {
		if v, ok := got[ref]; ok {
			t.Errorf("%s says %v; want nothing", ref, v)
		}
	}
}

// With no plugin reporting, the answer is an empty object, not null: the
// page reads it as a map.
func TestModelLoadsNoneReported(t *testing.T) {
	sandboxHome(t)
	plugin.ForgetLoads()
	mux := http.NewServeMux()
	speedsAPI(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/model-loads", nil))
	if b := w.Body.String(); w.Code != 200 || (b != "{}" && b != "{}\n") {
		t.Fatalf("%d %q; want {}", w.Code, b)
	}
}
