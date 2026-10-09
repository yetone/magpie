package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

// Exercise the actual HTTP handlers with files emitted by published Reasonix,
// rather than returning a precomputed session from a browser route mock.
func TestReasonix229NativeSessionRoutes(t *testing.T) {
	home := sandboxHome(t)
	root := filepath.Join(home, "reasonix-state")
	dir := filepath.Join(root, "projects", "fixture", "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REASONIX_STATE_HOME", root)
	for _, name := range []string{"resumed.jsonl", "resumed.jsonl.meta", "resumed.jsonl.telemetry.json", "resumed.wire.jsonl"} {
		b, err := os.ReadFile(filepath.Join("..", "sessions", "testdata", "reasonix-2.29.0", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	sessions.Reset()
	forgetStats()
	t.Cleanup(sessions.Reset)
	t.Cleanup(forgetStats)
	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	sessionManageRoutes(mux, folderOnly{})
	var id string
	for _, path := range []string{"/api/sessions", "/api/sessions/manage?agent=reasonix"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var out struct {
			Sessions []sessions.Session `json:"sessions"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		found := false
		for _, s := range out.Sessions {
			if s.Agent == "reasonix" {
				found = true
				id = s.ID
				if s.Title != "hello there" || s.Cwd != "/work/reasonix-229" || s.Input != 702 || s.Output != 168 || s.CacheRead != 3000 || s.UsageIncomplete {
					t.Fatalf("%s: %+v", path, s)
				}
			}
		}
		if !found {
			t.Fatalf("%s hides native Reasonix session", path)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/transcript?agent=reasonix&id="+id, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "resume follow up") || strings.Contains(w.Body.String(), "Current workspace:") {
		t.Fatalf("native transcript: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/stats", nil))
	var stats struct {
		sessions.Stats
		Agents map[string]string `json:"agents"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &stats) != nil {
		t.Fatalf("stats: %d %s", w.Code, w.Body)
	}
	found := false
	for _, d := range stats.Days {
		for _, u := range d.Usage {
			if u.Agent == "reasonix" {
				found = true
				if u.Input != 702 || u.Output != 168 || u.CacheRead != 3000 || u.Model != "fake/fake-model" {
					t.Fatalf("native stats usage: %+v", u)
				}
			}
		}
	}
	if !found || stats.Agents["reasonix"] == "" {
		t.Fatalf("native stats not discoverable: %+v", stats)
	}
}

func TestReasonixFramedNativeSessionRoutes(t *testing.T) {
	home := sandboxHome(t)
	root := filepath.Join(home, "reasonix-state")
	t.Setenv("REASONIX_STATE_HOME", root)
	dir := filepath.Join(root, "projects", "fixture", "sessions-v4", "producer-capture")
	var copyTree func(string, string)
	copyTree = func(src, dst string) {
		es, e := os.ReadDir(src)
		if e != nil {
			t.Fatal(e)
		}
		os.MkdirAll(dst, 0700)
		for _, x := range es {
			a, b := filepath.Join(src, x.Name()), filepath.Join(dst, x.Name())
			if x.IsDir() {
				copyTree(a, b)
			} else {
				v, e := os.ReadFile(a)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(b, v, 0600); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	source := filepath.Join("..", "sessions", "testdata", "reasonix-stores")
	copyTree(filepath.Join(source, "linear-v4"), dir)
	copyTree(filepath.Join(source, ".content-v1"), filepath.Join(filepath.Dir(dir), ".content-v1"))
	sessions.Reset()
	forgetStats()
	t.Cleanup(sessions.Reset)
	t.Cleanup(forgetStats)
	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	sessionManageRoutes(mux, folderOnly{})
	for _, path := range []string{"/api/sessions", "/api/sessions/manage?agent=reasonix"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var out struct {
			Sessions    []sessions.Session `json:"sessions"`
			Unsupported int                `json:"unsupported_reasonix"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("invalid sessions response")
		}
		found := false
		for _, s := range out.Sessions {
			if s.Agent == "reasonix" {
				found = true
				if s.Title != "Native producer capture" || !s.Transcript || !s.UsageIncomplete {
					t.Fatal("native summary contract missing")
				}
			}
		}
		if !found || out.Unsupported != 0 {
			t.Fatal("framed store is hidden or marked unsupported")
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/transcript?agent=reasonix&id=producer-capture", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "你好，已接入") || !strings.Contains(w.Body.String(), "test reasoning") {
		t.Fatal("native body is not accessible through HTTP handler")
	}
}
