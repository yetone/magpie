package gui

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
)

// Renaming a profile through the page's own call moves its key and keeps the
// setup it held, and a name already taken is rejected without dropping either
// profile (#2721).
func TestStateProfileRename(t *testing.T) {
	home := sandboxHome(t)
	t.Setenv("CODEX_HOME", home+"/.codex")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	h := Handler(nil, nil)
	call := func(path, body string) (stateJSON, int) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		var s stateJSON
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
				t.Fatal(err)
			}
		}
		return s, rec.Code
	}
	names := func(s stateJSON) []string {
		var ns []string
		for _, p := range s.Profiles {
			ns = append(ns, p.Name)
		}
		return ns
	}
	has := func(ns []string, want string) bool {
		for _, n := range ns {
			if n == want {
				return true
			}
		}
		return false
	}

	if _, code := call("/api/profile/save", `{"name":"work"}`); code != 200 {
		t.Fatalf("save code %d", code)
	}
	if err := profile.Save("taken", profile.Profile{Fields: map[string]string{"codex.model": "relay/m1"}}); err != nil {
		t.Fatal(err)
	}
	// rename success: old name gone, new name present
	s, code := call("/api/profile/rename", `{"name":"work","to":"home"}`)
	if code != 200 {
		t.Fatalf("rename code %d", code)
	}
	if has(names(s), "work") {
		t.Errorf("old name still present: %v", names(s))
	}
	if !has(names(s), "home") {
		t.Errorf("new name missing: %v", names(s))
	}
	// content kept: the renamed profile keeps what work held
	ps, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ps["home"]; !ok {
		t.Errorf("renamed profile not written: %v", ps)
	}
	// rename onto a taken name is rejected, both names survive
	s, code = call("/api/profile/rename", `{"name":"home","to":"taken"}`)
	if code == 200 {
		t.Fatalf("rename onto taken should fail: %v", names(s))
	}
	ps, _ = profile.Load()
	if _, ok := ps["home"]; !ok {
		t.Errorf("rejected rename dropped the source: %v", ps)
	}
	if _, ok := ps["taken"]; !ok {
		t.Errorf("rejected rename dropped the taken one: %v", ps)
	}
}
