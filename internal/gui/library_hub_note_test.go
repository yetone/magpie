package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// The name the Library gives this computer's MCP router (packinggoodsllc_07668 on Discord) is saved
// to this computer's settings and comes back in the page's view, with
// this computer's name for the field's placeholder; a save from the
// Settings page, which doesn't send it, leaves it as it is.
func TestHubNoteSaved(t *testing.T) {
	sandboxHome(t)
	h := Handler(nil, nil)
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	rec := post("/api/library/servers/hub-note", `{"note":"  Los Angeles  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v struct{ HubNote, HubName string }
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.HubNote != "Los Angeles" || settings.Load().RouterNote != "Los Angeles" {
		t.Fatalf("view %+v, saved %q", v, settings.Load().RouterNote)
	}
	if rec := post("/api/settings", `{"theme":"dark","lang":"en"}`); rec.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}
	if got := settings.Load().RouterNote; got != "Los Angeles" {
		t.Fatalf("the Settings page's save left %q", got)
	}
	if rec := post("/api/library/servers/hub-note", `{"note":""}`); rec.Code != http.StatusOK || settings.Load().RouterNote != "" {
		t.Fatalf("cleared: %d %q", rec.Code, settings.Load().RouterNote)
	}
}

// The router's switch for giving every agent its tools whole, not on
// demand (ArvinCharl on X), is this computer's setting, comes back in the
// page's view, and a save from the Settings page leaves it as it is.
func TestHubAllToolsSaved(t *testing.T) {
	sandboxHome(t)
	h := Handler(nil, nil)
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	rec := post("/api/library/servers/hub-tools", `{"on":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v struct{ HubAllTools bool }
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !v.HubAllTools || !settings.Load().RouterAllTools {
		t.Fatalf("view %+v, saved %v", v, settings.Load().RouterAllTools)
	}
	if rec := post("/api/settings", `{"theme":"dark","lang":"en"}`); rec.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}
	if !settings.Load().RouterAllTools {
		t.Fatal("the Settings page's save turned it off")
	}
	if rec := post("/api/library/servers/hub-tools", `{"on":false}`); rec.Code != http.StatusOK || settings.Load().RouterAllTools {
		t.Fatalf("off: %d %v", rec.Code, settings.Load().RouterAllTools)
	}
}

// The Tools on demand row says how many tools an agent is given whole and
// which agents search their tools themselves from the page's view, which
// carries the gateway's own (gateway.HubOnDemandOver,
// gateway.HubFindsItself), each agent by its name: its words named four of
// the six (11-18).
func TestHubFindsItselfInTheView(t *testing.T) {
	sandboxHome(t)
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/api/library", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v struct {
		HubFindsItself  []string
		HubOnDemandOver int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	ids := gateway.HubFindsItself()
	if v.HubOnDemandOver != gateway.HubOnDemandOver || len(v.HubFindsItself) != len(ids) {
		t.Fatalf("view says %d, %q; the gateway %d, %q", v.HubOnDemandOver, v.HubFindsItself, gateway.HubOnDemandOver, ids)
	}
	for i, id := range ids {
		if a, err := agent.Find(id); err == nil && a.ID == id && v.HubFindsItself[i] != a.Name {
			t.Fatalf("%s is said as %q, not %q", id, v.HubFindsItself[i], a.Name)
		}
		if v.HubFindsItself[i] == id && id != "omp" {
			t.Fatalf("%s is said by its id", id)
		}
	}
}

// The router's Code mode switch is this computer's setting, on by
// default, comes back in the page's view, and a save from the Settings
// page leaves it as it is.
func TestHubCodeSaved(t *testing.T) {
	sandboxHome(t)
	h := Handler(nil, nil)
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	view := func(rec *httptest.ResponseRecorder) bool {
		var v struct{ HubCode bool }
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v.HubCode
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/library", nil))
	if !view(rec) {
		t.Fatal("code mode isn't on by default")
	}
	rec = post("/api/library/servers/hub-code", `{"on":false}`)
	if rec.Code != http.StatusOK || view(rec) || !settings.Load().RouterNoCode {
		t.Fatalf("off: %d %s, saved %v", rec.Code, rec.Body, settings.Load().RouterNoCode)
	}
	if rec := post("/api/settings", `{"theme":"dark","lang":"en"}`); rec.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}
	if !settings.Load().RouterNoCode {
		t.Fatal("the Settings page's save turned it on")
	}
	if rec := post("/api/library/servers/hub-code", `{"on":true}`); rec.Code != http.StatusOK || !view(rec) || settings.Load().RouterNoCode {
		t.Fatalf("on: %d %v", rec.Code, settings.Load().RouterNoCode)
	}
}
