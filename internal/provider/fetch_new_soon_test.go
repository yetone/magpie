package provider

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// An account whose list couldn't be had is asked again a minute on, and
// opening the panel asks without the page waiting (#422: after an update
// Kiro offered Auto alone until magpie was restarted by hand — its one try
// had failed, and it wasn't asked again for ten minutes, and then only by
// the Providers page).
func TestFetchNewRetriedSoon(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	wbTokens.Lock()
	wbTokens.m = map[string]wbCreds{}
	wbTokens.Unlock()
	newFetches.Lock()
	newFetches.m = map[string]time.Time{}
	newFetches.Unlock()
	newSoonAt.Store(0)
	t.Cleanup(func() {
		waitNewSoon()
		newFetches.Lock()
		newFetches.m = map[string]time.Time{}
		newFetches.Unlock()
		newSoonAt.Store(0)
		wbTokens.Lock()
		wbTokens.m = map[string]wbCreds{}
		wbTokens.Unlock()
		forgetAccountCaches()
	})

	var asked atomic.Int32
	var down atomic.Bool
	down.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			w.WriteHeader(404)
			return
		}
		asked.Add(1)
		if down.Load() {
			w.WriteHeader(502)
			return
		}
		w.Write([]byte(`{"code":0,"data":{"agents":[{"name":"cli","models":["hy4-preview","glm-5.3"]}],
			"models":[{"id":"hy4-preview","name":"Hy4 preview"},{"id":"glm-5.3","name":"GLM-5.3"}]}}`))
	}))
	defer srv.Close()
	old := wbEndpoint
	wbEndpoint = srv.URL
	defer func() { wbEndpoint = old }()
	future := time.Now().Add(24 * time.Hour).UnixMilli()
	writeFile(t, wbAuthFile(home), map[string]any{
		"account": map[string]any{"uid": "u1", "nickname": "旅行者"},
		"auth": map[string]any{"accessToken": "a", "refreshToken": "r",
			"expiresAt": future, "refreshExpiresAt": future, "domain": "www.codebuddy.cn"},
	})
	forgetAccountCaches()
	listed := func() bool {
		p, err := Find("workbuddy")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range p.Available() {
			if m.ID == "hy4-preview" {
				return true
			}
		}
		return false
	}

	// the try at start-up fails
	FetchNew(5 * time.Second)
	if n := asked.Load(); n != 1 {
		t.Fatalf("asked %d times at start-up", n)
	}
	down.Store(false)
	FetchNew(5 * time.Second)
	if n := asked.Load(); n != 1 {
		t.Fatalf("a failed list asked again at once: %d", n)
	}

	// opened again straight away: nothing more is started
	FetchNewSoon(5 * time.Second)
	newSoonAt.Store(0)
	waitNewSoon()
	if n := asked.Load(); n != 1 {
		t.Fatalf("a failed list asked again at once by the panel: %d", n)
	}

	// a minute and a bit later the panel is opened: asked again, without
	// the page waiting for it
	newFetches.Lock()
	newFetches.m["workbuddy"] = time.Now().Add(-61 * time.Second)
	newFetches.Unlock()
	FetchNewSoon(5 * time.Second)
	waitNewSoon()
	if !listed() || asked.Load() != 2 {
		t.Fatalf("not asked again a minute on: asked %d times, listed %v", asked.Load(), listed())
	}

	// and the panel opened again within newSoonEvery starts nothing
	if err := catalog.SaveLive("workbuddy", "", nil); err != nil {
		t.Fatal(err)
	}
	newFetches.Lock()
	newFetches.m = map[string]time.Time{}
	newFetches.Unlock()
	FetchNewSoon(5 * time.Second)
	waitNewSoon()
	if n := asked.Load(); n != 2 {
		t.Fatalf("the panel asked again within %v: %d", newSoonEvery, n)
	}
}

// waitNewSoon waits for a FetchNewSoon under way to end.
func waitNewSoon() {
	for deadline := time.Now().Add(5 * time.Second); fetchingNew.Load() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
}
