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
	// registered after fakeNPM, so it runs before the fake's Close, which
	// waits for every handler: a check below that fails doesn't leave the
	// slow one held and the package hung until -timeout (#1808)
	let := sync.OnceFunc(func() { close(release) })
	t.Cleanup(let)
	// The slow one is held until the test lets it go and each package is
	// waited on for an hour, so Info coming back at all is npmAll at work,
	// with no wall-clock bound a loaded machine can pass; the minute only
	// turns a wait into a FAIL. npmAll is the quick ones' time to answer,
	// room enough on a loaded machine.
	npmEach, npmAll = time.Hour, 5*time.Second
	answered := make(chan map[string]NPM, 1)
	go func() { answered <- Info(context.Background(), []string{"fast-one", "slow-one", "fast-two"}) }()
	var got map[string]NPM
	select {
	case got = <-answered:
	case <-time.After(time.Minute):
		t.Fatal("Info waited for the slow package")
	}
	if got["fast-one"].Version != "1.2.3" || got["fast-two"].Version != "1.2.3" {
		t.Fatalf("the quick ones: %+v", got)
	}
	if _, ok := got["slow-one"]; ok {
		t.Fatalf("the slow one before it answered: %+v", got["slow-one"])
	}
	let()
	deadline := time.Now().Add(30 * time.Second)
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

// yetone: 这里的 agent plugin 为什么没有 logo. The picture a package gives
// in package.json's magpie.icon comes with what npm says of it, so a
// listing the market gives no icon shows it; anything but an https URL or
// a data:image URI is left out.
func TestNPMInfoGivesIcon(t *testing.T) {
	icons := map[string]string{
		"agent-aider": "data:image/png;base64,iVBORw0KGgo=",
		"agent-web":   "https://aider.chat/icon.png",
		"agent-plain": "http://aider.chat/icon.png",
		"agent-js":    "javascript:alert(1)",
		"agent-none":  "",
	}
	fakeNPM(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/downloads/") {
			w.Write([]byte(`{"downloads":1}`))
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/latest")
		w.Write([]byte(`{"version":"0.1.1","magpie":{"agent":"./a.agent.js","icon":"` + icons[name] + `"}}`))
	})
	names := []string{}
	for n := range icons {
		names = append(names, n)
	}
	got := Info(context.Background(), names)
	want := map[string]string{"agent-aider": icons["agent-aider"], "agent-web": icons["agent-web"]}
	for _, n := range names {
		if got[n].Version != "0.1.1" || got[n].Icon != want[n] {
			t.Errorf("%s: %+v, want icon %q", n, got[n], want[n])
		}
	}
}

// The update check asks npm now, not what it said within the hour: a
// community fix published a few minutes after the page last asked reaches
// a magpie started since at its first look (ysm119 on Discord).
func TestUpdateCheckAsksNPMNow(t *testing.T) {
	var version atomic.Value
	version.Store("1.2.3")
	fakeNPM(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/downloads/") {
			w.Write([]byte(`{"downloads":1}`))
			return
		}
		w.Write([]byte(`{"version":"` + version.Load().(string) + `"}`))
	})
	ctx := context.Background()
	pkg := "@magpie-community/opencode-x-auth"
	if got := Info(ctx, []string{pkg})[pkg].Version; got != "1.2.3" {
		t.Fatalf("first ask: %q", got)
	}
	version.Store("1.2.4")
	if got := Info(ctx, []string{pkg})[pkg].Version; got != "1.2.3" {
		t.Fatalf("the page within the hour: %q, want what npm said last", got)
	}
	if got := latestOf(ctx, []string{pkg})[pkg]; got != "1.2.4" {
		t.Fatalf("the update check: %q, want npm's version now", got)
	}
}

// The owner, 11-27 (「这个加载特别慢」): a package npm answered for over an
// hour ago is what it said then, at once, while npm is asked again; its
// newer answer is kept for the next ask. Waiting on npm for it held
// Discover's week up to npmAll. The update check still waits for npm
// (TestUpdateCheckAsksNPMNow).
func TestNPMInfoOldAnswerAtOnce(t *testing.T) {
	release := make(chan struct{})
	var version atomic.Value
	version.Store("1.2.3")
	fakeNPM(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/downloads/") {
			w.Write([]byte(`{"downloads":1}`))
			return
		}
		if version.Load() != "1.2.3" {
			<-release
		}
		w.Write([]byte(`{"version":"` + version.Load().(string) + `"}`))
	})
	let := sync.OnceFunc(func() { close(release) })
	t.Cleanup(let)
	const pkg = "opencode-x-auth"
	if got := Info(context.Background(), []string{pkg})[pkg].Version; got != "1.2.3" {
		t.Fatalf("first ask: %q", got)
	}
	npmMu.Lock()
	e := npmCached()[pkg]
	e.At = time.Now().Add(-2 * time.Hour)
	npmCache[pkg] = e
	npmMu.Unlock()
	// npm is held until let, and each ask may take an hour: Info coming
	// back at all is it not waiting on npm
	version.Store("1.2.4")
	npmEach, npmAll = time.Hour, time.Hour
	answered := make(chan map[string]NPM, 1)
	go func() { answered <- Info(context.Background(), []string{pkg}) }()
	select {
	case got := <-answered:
		if got[pkg].Version != "1.2.3" {
			t.Fatalf("an hour on: %+v, want what npm said last", got[pkg])
		}
	case <-time.After(time.Minute):
		t.Fatal("Info waited on npm for a package it had an answer for")
	}
	let()
	deadline := time.Now().Add(30 * time.Second)
	for InfoCached([]string{pkg})[pkg].Version != "1.2.4" {
		if time.Now().After(deadline) {
			t.Fatal("npm's newer answer wasn't kept")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
