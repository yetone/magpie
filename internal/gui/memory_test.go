package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/memory"
)

// The Memory tab's Select acts on the memories picked at once
// (RealZeroP27 on X): /api/memory/status keeps the ones to review and
// answers with the tab as it is now; deleting several goes through
// /api/memory/remove. A request naming none, or a status that isn't
// one, is refused and changes nothing.
func TestMemoryStatusRouteActsOnSome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	memoryRoutes(mux)
	post := func(path, body string) (int, memoryView) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		var v memoryView
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
				t.Fatalf("%s: %s", path, rec.Body)
			}
		}
		return rec.Code, v
	}
	var ids []string
	for _, text := range []string{"Prefers pnpm.", "Ships on Fridays.", "Answers in Chinese."} {
		kind, scope, st := memory.Preference, memory.Global, memory.Candidate
		m, _, err := memory.Save(memory.Input{Text: &text, Kind: &kind, Scope: &scope, Status: &st}, memory.FromInferred, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	statuses := func(v memoryView) map[string]string {
		out := map[string]string{}
		for _, m := range v.Memories {
			out[m.ID] = m.Status
		}
		return out
	}

	code, v := post("/api/memory/status", `{"ids":["`+ids[0]+`","`+ids[1]+`"],"status":"active"}`)
	if code != 200 {
		t.Fatalf("keep: %d", code)
	}
	if s := statuses(v); s[ids[0]] != "active" || s[ids[1]] != "active" || s[ids[2]] != "candidate" {
		t.Fatalf("after keeping two: %v", s)
	}
	if code, _ := post("/api/memory/status", `{"ids":[],"status":"active"}`); code != 400 {
		t.Fatalf("none named: %d", code)
	}
	if code, _ := post("/api/memory/status", `{"ids":["`+ids[2]+`"],"status":"gone"}`); code != 400 {
		t.Fatalf("a bad status: %d", code)
	}
	if m, _ := memory.Get(ids[2]); m.Status != memory.Candidate {
		t.Fatalf("a refused request changed it: %s", m.Status)
	}

	code, v = post("/api/memory/remove", `{"ids":["`+ids[0]+`","`+ids[2]+`"]}`)
	if code != 200 {
		t.Fatalf("remove: %d", code)
	}
	if s := statuses(v); len(s) != 1 || s[ids[1]] != "active" {
		t.Fatalf("after removing two: %v", s)
	}
}
