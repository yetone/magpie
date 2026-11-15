package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func uniqueHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func idsOf(ms []Model) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// #6582: an API whose /models names a model twice gave it two rows. The
// list read is each id once, in the order first listed, and what the first
// copy leaves unsaid comes from the copy after it.
func TestFetchedListNamesEachModelOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"gpt-5.6"},{"id":"glm-5.3"},{"id":"gpt-5.6","context_length":400000},{"id":"glm-5.3"}]}`)
	}))
	t.Cleanup(srv.Close)
	ms, err := FetchURL(context.Background(), srv.URL+"/v1/models", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(ms); len(got) != 2 || got[0] != "gpt-5.6" || got[1] != "glm-5.3" {
		t.Fatalf("ids = %v, want [gpt-5.6 glm-5.3]", got)
	}
	if ms[0].Context != 400000 {
		t.Errorf("gpt-5.6's window = %d, want the second copy's 400000", ms[0].Context)
	}
}

// Every list saved, whoever fetched it (an account's, Azure's, Kilo's),
// and every list saved before the fix, is read with each id once.
func TestSavedListNamesEachModelOnce(t *testing.T) {
	uniqueHome(t)
	dup := []Model{{ID: "a", Name: "a"}, {ID: "b", Name: "B"}, {ID: "a", Name: "Model A", Keys: []string{"k2"}}}
	if err := SaveLiveSides("saved", "https://x/v1", dup, map[string][]Model{"https://x/v1": dup}); err != nil {
		t.Fatal(err)
	}
	ms, _, ok := Live("saved")
	if !ok || len(ms) != 2 || ms[0].ID != "a" || ms[1].ID != "b" {
		t.Fatalf("saved list = %v", idsOf(ms))
	}
	if ms[0].Name != "Model A" || len(ms[0].Keys) != 1 {
		t.Errorf("a = %+v, want the second copy's name and key", ms[0])
	}
	sides, _, _ := LiveSplit("saved")
	if got := idsOf(sides["https://x/v1"]); len(got) != 2 {
		t.Errorf("side = %v, want each id once", got)
	}
	if n := len(LiveByID("saved", "a")); n != 1 {
		t.Errorf("LiveByID(a) = %d models, want 1", n)
	}

	// a file written before Unique, with the copies in it
	b, _ := json.Marshal(liveFile{Fetched: time.Now(), Base: "https://y/v1", Models: dup, Kinded: true})
	p := LivePath("old")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	ms, _, ok = Live("old")
	if !ok || len(ms) != 2 {
		t.Fatalf("old list = %v, want each id once", idsOf(ms))
	}
	if all, _ := LiveAll("old"); len(all) != 2 {
		t.Errorf("LiveAll = %v, want each id once", idsOf(all))
	}
}

// A list with no id twice comes back as it is, not copied.
func TestUniqueLeavesAListWithoutCopies(t *testing.T) {
	ms := []Model{{ID: "a"}, {ID: "b"}}
	if got := Unique(ms); &got[0] != &ms[0] || len(got) != 2 {
		t.Errorf("Unique copied a list with no copies: %v", idsOf(got))
	}
}
