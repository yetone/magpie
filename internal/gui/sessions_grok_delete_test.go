package gui

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

const (
	grokPageMain = "0190aaaa-0000-7000-8000-00000000000a"
	grokPageSub  = "0190aaaa-0000-7000-8000-00000000000b"
)

// #3003 asked for Grok Build's sessions to be deletable from the Sessions
// page's list: the page's own faces of it, /api/sessions/manage and
// /api/sessions/delete, offer Delete on a Grok Build session and take it to
// the trash, and Restore puts it back. Nothing of the user's own ~/.grok:
// the fixture reaches the page through the temporary home.
func TestGrokSessionDeleteOnThePage(t *testing.T) {
	h := sandboxHome(t)
	src := filepath.Join("..", "sessions", "testdata", "grok")
	dst := filepath.Join(h, ".grok")
	if err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	// nothing was written in the last minute, so the delete isn't refused
	// as active
	old := time.Now().Add(-24 * time.Hour)
	filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			os.Chtimes(p, old, old)
		}
		return nil
	})
	sessions.Reset()
	t.Cleanup(sessions.Reset)

	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	sessionManageRoutes(mux, folderOnly{})
	call := func(method, path string, body any, v any) int {
		t.Helper()
		var r *http.Request
		if body == nil {
			r = httptest.NewRequest(method, path, nil)
		} else {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r = httptest.NewRequest(method, path, bytes.NewReader(b))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if v != nil && w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
				t.Fatalf("%s %s: %v (%s)", method, path, err, w.Body)
			}
		}
		return w.Code
	}
	type managed struct {
		ID        string `json:"id"`
		Agent     string `json:"agent"`
		Title     string `json:"title"`
		Files     int    `json:"files"`
		Deletable bool   `json:"deletable"`
	}
	var page struct {
		Agent    string `json:"agent"`
		Sessions []managed
		Trash    []struct {
			Key   string `json:"key"`
			Agent string `json:"agent"`
			ID    string `json:"id"`
		} `json:"trash"`
	}
	if code := call("GET", "/api/sessions/manage?agent=grok", nil, &page); code != 200 {
		t.Fatalf("manage: %d", code)
	}
	var row managed
	for _, s := range page.Sessions {
		if s.ID == grokPageMain {
			row = s
		}
	}
	if b, err := json.Marshal(page.Sessions); err == nil {
		t.Logf("grok rows: %s", b)
	}
	if row.ID == "" || !row.Deletable || row.Files != 2 {
		t.Fatalf("the page's row: %+v", row)
	}

	var out struct {
		Deleted []string `json:"deleted"`
		Refused []struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		} `json:"refused"`
	}
	if code := call("POST", "/api/sessions/delete", map[string]any{"agent": "grok", "ids": []string{grokPageMain}}, &out); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if len(out.Deleted) != 1 || out.Deleted[0] != grokPageMain || len(out.Refused) != 0 {
		t.Fatalf("the page deleted %+v", out)
	}
	for _, id := range []string{grokPageMain, grokPageSub} {
		if _, err := os.Lstat(filepath.Join(h, ".grok", "sessions", "%2Fwork%2Fgrok", id)); err == nil {
			t.Fatalf("%s is still there", id)
		}
	}

	if code := call("GET", "/api/sessions/manage?agent=grok", nil, &page); code != 200 {
		t.Fatalf("manage after the delete: %d", code)
	}
	if b, err := json.Marshal(page.Trash); err == nil {
		t.Logf("trash: %s", b)
	}
	var key string
	for _, tr := range page.Trash {
		if tr.Agent == "grok" && tr.ID == grokPageMain {
			key = tr.Key
		}
	}
	if key == "" {
		t.Fatalf("the trash lists none of it: %+v", page.Trash)
	}
	for _, s := range page.Sessions {
		if s.ID == grokPageMain {
			t.Fatal("still listed after the delete")
		}
	}

	var back map[string]string
	if code := call("POST", "/api/sessions/restore", map[string]any{"key": key}, &back); code != 200 {
		t.Fatalf("restore: %d", code)
	}
	sessions.Reset()
	if code := call("GET", "/api/sessions/manage?agent=grok", nil, &page); code != 200 {
		t.Fatalf("manage after the restore: %d", code)
	}
	row = managed{}
	for _, s := range page.Sessions {
		if s.ID == grokPageMain {
			row = s
		}
	}
	if row.ID == "" || !row.Deletable || row.Title != "Parser port" {
		t.Fatalf("the restored row: %+v", row)
	}
	if _, err := os.Stat(filepath.Join(h, ".grok", "sessions", "%2Fwork%2Fgrok", grokPageSub, "updates.jsonl")); err != nil {
		t.Fatalf("its subagent's folder isn't back: %v", err)
	}
}
