package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

// sessions/purge erases trashed sessions for good, the keys named or every
// one with all (#487); a key out of the trash is refused and nothing outside
// it goes.
func TestSessionsPurgeRoute(t *testing.T) {
	h := sandboxHome(t)
	mux := http.NewServeMux()
	sessionManageRoutes(mux, folderOnly{})
	trashed := func(key string) {
		dir := filepath.Join(sessions.TrashDir(), filepath.FromSlash(key))
		os.MkdirAll(filepath.Join(dir, "files"), 0o755)
		os.WriteFile(filepath.Join(dir, "files", "0-x.jsonl"), []byte("{}\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "session.json"), []byte(`{"agent":"claude","id":"x"}`), 0o644)
	}
	for _, k := range []string{"claude/a", "claude/b", "codex/c"} {
		trashed(k)
	}
	outside := filepath.Join(h, "keep")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "session.json"), []byte("{}"), 0o644)
	type result struct {
		Purged  []string `json:"purged"`
		Refused []struct {
			Key string `json:"key"`
		} `json:"refused"`
	}
	purge := func(body string) result {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/sessions/purge", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
		var r result
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	keys := func() []string {
		out := []string{}
		for _, x := range sessions.Trash() {
			out = append(out, x.Key)
		}
		slices.Sort(out)
		return out
	}

	r := purge(`{"keys":["claude/a","claude/../../keep","../keep","claude/"]}`)
	if !slices.Equal(r.Purged, []string{"claude/a"}) || len(r.Refused) != 3 {
		t.Fatalf("purged %+v", r)
	}
	if got := keys(); !slices.Equal(got, []string{"claude/b", "codex/c"}) {
		t.Fatalf("trash %v", got)
	}
	if _, err := os.Stat(filepath.Join(outside, "session.json")); err != nil {
		t.Fatal("a folder out of the trash was erased")
	}

	r = purge(`{"all":true}`)
	slices.Sort(r.Purged)
	if !slices.Equal(r.Purged, []string{"claude/b", "codex/c"}) || len(r.Refused) != 0 {
		t.Fatalf("purged %+v", r)
	}
	if got := keys(); len(got) != 0 {
		t.Fatalf("trash %v", got)
	}
	if _, err := os.Stat(filepath.Join(outside, "session.json")); err != nil {
		t.Fatal("a folder out of the trash was erased")
	}
	// nothing named, nothing erased
	if r := purge(`{}`); len(r.Purged) != 0 {
		t.Fatalf("purged %+v", r)
	}
}
