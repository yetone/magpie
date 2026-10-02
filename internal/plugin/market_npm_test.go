package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeNPM answers as npm's registry and its downloads do, through the
// handler given, for one test.
func fakeNPM(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	reg, dl, atOnce, each, all := npmRegistry, npmDownloads, npmAtOnce, npmEach, npmAll
	npmRegistry, npmDownloads = srv.URL, srv.URL
	ReloadInfo()
	t.Cleanup(func() {
		npmRegistry, npmDownloads, npmAtOnce, npmEach, npmAll = reg, dl, atOnce, each, all
		ReloadInfo()
	})
}

func npmAnswer(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/downloads/") {
		w.Write([]byte(`{"downloads":1234}`))
		return
	}
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/latest")
	w.Write([]byte(`{"version":"1.2.3","description":"` + name + `","_npmUser":{"name":"me"}}`))
}

// What npm said is kept on disk: a magpie started again shows it at once,
// before asking, and when npm is down it is what the page shows.
func TestNPMInfoKeptOnDisk(t *testing.T) {
	up := atomic.Bool{}
	up.Store(true)
	fakeNPM(t, func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "down", 500)
			return
		}
		npmAnswer(w, r)
	})
	ctx := context.Background()
	got := Info(ctx, []string{"opencode-x-auth"})
	if n := got["opencode-x-auth"]; n.Version != "1.2.3" || n.Weekly != 1234 || n.Publisher != "me" {
		t.Fatalf("asked: %+v", got)
	}

	// started again: nothing in memory, the disk's copy there without asking
	up.Store(false)
	ReloadInfo()
	if n := InfoCached([]string{"opencode-x-auth", "never-asked"}); n["opencode-x-auth"].Version != "1.2.3" || len(n) != 1 {
		t.Fatalf("from disk: %+v", n)
	}
	// an hour on, npm down: what it said last
	npmMu.Lock()
	e := npmCached()["opencode-x-auth"]
	e.At = time.Now().Add(-2 * time.Hour)
	npmCache["opencode-x-auth"] = e
	npmMu.Unlock()
	if n := Info(ctx, []string{"opencode-x-auth"}); n["opencode-x-auth"].Version != "1.2.3" {
		t.Fatalf("npm down: %+v", n)
	}
}

// One package npm is slow to answer for doesn't hold the rest: Info
// answers by its deadline, the slow one as it was last (missing, never
// asked before), and its answer, once it comes, is kept for the next ask.
func TestNPMInfoDeadline(t *testing.T) {
	release := make(chan struct{})
	fakeNPM(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "slow-one") {
			<-release
		}
		npmAnswer(w, r)
	})
	npmAll = 300 * time.Millisecond
	start := time.Now()
	got := Info(context.Background(), []string{"fast-one", "slow-one", "fast-two"})
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("took %v: the slow package held it", took)
	}
	if got["fast-one"].Version != "1.2.3" || got["fast-two"].Version != "1.2.3" {
		t.Fatalf("the quick ones: %+v", got)
	}
	if _, ok := got["slow-one"]; ok {
		t.Fatalf("the slow one before it answered: %+v", got["slow-one"])
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for InfoCached([]string{"slow-one"})["slow-one"].Version == "" {
		if time.Now().After(deadline) {
			t.Fatal("the slow one's answer wasn't kept")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// npm is asked of a few packages at a time, not of all of them at once.
func TestNPMInfoAtOnce(t *testing.T) {
	var mu sync.Mutex
	now, most := 0, 0
	fakeNPM(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/downloads/") {
			npmAnswer(w, r)
			return
		}
		mu.Lock()
		now++
		most = max(most, now)
		mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		mu.Lock()
		now--
		mu.Unlock()
		npmAnswer(w, r)
	})
	npmAtOnce = 3
	names := []string{}
	for i := range 12 {
		names = append(names, "pkg-"+string(rune('a'+i)))
	}
	names = append(names, "not a name", "pkg-a")
	got := Info(context.Background(), names)
	if len(got) != 12 {
		t.Fatalf("%d answered, want 12: %+v", len(got), got)
	}
	if most > 3 || most == 0 {
		t.Fatalf("%d asked at once, want at most 3", most)
	}
}
