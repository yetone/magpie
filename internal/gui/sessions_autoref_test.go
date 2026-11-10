package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// The model menu takes stats' model names, while the list and overview
// filter their own models. All three must agree for old and new group ids.
func TestSessionsAPIFoundGroupNames(t *testing.T) {
	home := sandboxHome(t)
	proj := filepath.Join(home, ".claude", "projects", "-work-auto")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ds-a", "ds-b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-flash"}}); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().Add(-time.Minute)
	for i, model := range []string{"group/auto-deepseek-flash", "auto/deepseek-flash"} {
		id := fmt.Sprintf("abcdef0%d-abcd-abcd-abcd-abcdefabcdef", i+1)
		body := fmt.Sprintf(`{"type":"assistant","isSidechain":false,"message":{"model":%q,"id":%q,"role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":2}},"timestamp":%q,"cwd":"/work/auto","sessionId":%q}`+"\n", model, id, at.Format(time.RFC3339Nano), id)
		if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	usage.Append(usage.Record{Time: at, Agent: "pi", Session: "gateway-auto", Provider: "relay", Model: "group/auto-deepseek-flash", Input: 10, Output: 2, Status: 200})
	sessions.Reset()
	forgetStats()
	t.Cleanup(func() { sessions.Reset(); forgetStats() })
	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	get := func(path, tag string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("X-Magpie-Had", tag)
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w
	}
	first := get("/api/sessions", "")
	var list sessionsJSON
	if err := json.Unmarshal(first.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 3 {
		t.Fatalf("list has %d sessions", len(list.Sessions))
	}
	for _, s := range list.Sessions {
		if len(s.Models) != 1 || s.Models[0].Model != "auto/deepseek-flash" {
			t.Fatalf("list's group names: %+v", s.Models)
		}
	}
	var stats struct {
		Days []sessions.Day `json:"days"`
	}
	if err := json.Unmarshal(get("/api/sessions/stats?days=0", "").Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, d := range stats.Days {
		for _, u := range d.Usage {
			if u.Model != "auto/deepseek-flash" {
				t.Fatalf("duplicate model option: %q", u.Model)
			}
			total += u.Input
		}
	}
	if total != 30 {
		t.Fatalf("stats input %d, want 30", total)
	}
	for _, model := range []string{"auto/deepseek-flash", "group/auto-deepseek-flash"} {
		var o sessions.Overview
		if err := json.Unmarshal(get("/api/sessions/overview?days=0&model="+url.QueryEscape(model), "").Body.Bytes(), &o); err != nil {
			t.Fatal(err)
		}
		if o.Count != 3 {
			t.Fatalf("%s filter lost sessions: %+v", model, o)
		}
	}
	tag := first.Header().Get("X-Magpie-Tag")
	if w := get("/api/sessions", tag); w.Header().Get("X-Magpie-Same") != "1" {
		t.Fatal("unchanged names did not keep the list tag")
	}
	if err := provider.SetAutoGroups(false); err != nil {
		t.Fatal(err)
	}
	if w := get("/api/sessions", tag); w.Header().Get("X-Magpie-Same") == "1" {
		t.Fatal("group catalog changed but list kept old names")
	}
}
