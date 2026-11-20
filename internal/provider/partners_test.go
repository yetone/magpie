package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// fetchPartnersNow fetches the list as the background refresh does, and
// waits for it. One fetch runs at a time, as Partners and PartnersNow
// keep it: a fetch Partners started is waited for first, so it can't
// store an older list over this one.
func fetchPartnersNow() {
	claimPartnerFetch()
	partnerFetches = true
	loadPartners()
	partnerMu.Unlock()
	refreshPartners()
}

func partnerFeedServer(t *testing.T, body *atomic.Value) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "magpie" {
			t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
		}
		_, _ = rw.Write([]byte(body.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func partnerIDs(ps []Partner) string {
	var ids []string
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, ",")
}

// The add sheet's Partners are what usemagpie.ai lists, checked: an entry
// that would send a key over http, take a built-in preset's id or set
// what only magpie's own presets may is left out, and one outside its
// window isn't listed.
func TestPartnersFromTheFeed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetPartners()
	t.Cleanup(resetPartners)
	was := fetchPartnerIcon
	t.Cleanup(func() { fetchPartnerIcon = was })
	var iconFetches atomic.Int32
	fetchPartnerIcon = func(_ context.Context, u string) (string, error) {
		iconFetches.Add(1)
		if u != "https://acme.example/logo.png" {
			t.Errorf("icon fetched from %q", u)
		}
		return StoreIcon([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	var body atomic.Value
	body.Store(`{"partners":[
		{"id":"acme","name":"Acme AI","chat":"https://api.acme.example/v1","keysUrl":"https://acme.example/keys?ref=magpie",
		 "website":"https://usemagpie.ai/go/acme/site","siteHost":"www.acme.example",
		 "iconUrl":"https://acme.example/logo.png","notes":{"en":"Fast relay","zh":"快速中转"},"langs":["zh"],
		 "decide":"https://api.acme.example/decide","noKey":true},
		{"id":"plain","name":"Plain","chat":"http://api.plain.example/v1"},
		{"id":"openrouter","name":"Not OpenRouter","chat":"https://evil.example/v1"},
		{"id":"Bad_Id","name":"Bad","chat":"https://bad.example/v1"},
		{"id":"noname","chat":"https://x.example/v1"},
		{"id":"later","name":"Later","chat":"https://later.example/v1","from":"` + future + `"},
		{"id":"gone","name":"Gone","chat":"https://gone.example/v1","until":"` + past + `"},
		{"id":"regioned","name":"Regioned","regions":[{"id":"cn","name":"China","chat":"https://cn.regioned.example/v1","website":"https://usemagpie.ai/go/regioned/site/cn","siteHost":"cn.regioned.example"}]},
		{"id":"acme","name":"Acme twice","chat":"https://twice.example/v1"}
	]}`)
	t.Setenv("MAGPIE_PARTNERS", partnerFeedServer(t, &body).URL)

	if got := Partners(); len(got) != 0 {
		t.Fatalf("listed before any list was fetched: %v", partnerIDs(got))
	}
	fetchPartnersNow()
	got := Partners()
	if ids := partnerIDs(got); ids != "acme,regioned" {
		t.Fatalf("partners = %s, want acme,regioned", ids)
	}
	a := got[0]
	if a.Kind != KindPartner || !a.Sponsored || a.Decide != "" || a.NoKey {
		t.Errorf("acme = kind %q sponsored %v decide %q noKey %v", a.Kind, a.Sponsored, a.Decide, a.NoKey)
	}
	if !strings.HasPrefix(a.Icon, "file:") || a.Note != "Fast relay" || a.Notes["zh"] != "快速中转" || len(a.Langs) != 1 {
		t.Errorf("acme = icon %q note %q notes %v langs %v", a.Icon, a.Note, a.Notes, a.Langs)
	}
	if got[1].Chat != "https://cn.regioned.example/v1" {
		t.Errorf("a partner with regions only is at %q, not its first region", got[1].Chat)
	}
	// the host a /go website sends the browser to, which the editor's link reads
	if a.SiteHost != "www.acme.example" || got[1].Regions[0].SiteHost != "cn.regioned.example" {
		t.Errorf("siteHost = %q, region's %q", a.SiteHost, got[1].Regions[0].SiteHost)
	}

	// fetched again, its icon from the same URL is the one kept
	fetchPartnersNow()
	if n := iconFetches.Load(); n != 1 {
		t.Errorf("icon fetched %d times over two fetches of one URL, want 1", n)
	}
	if got := Partners(); len(got) == 0 || got[0].Icon != a.Icon {
		t.Errorf("icon after a second fetch = %v, want the kept %q", got, a.Icon)
	}

	// a provider is added from it as from a preset
	p, err := FromPreset("acme")
	if err != nil || p.Chat != "https://api.acme.example/v1" || p.KeysURL != "https://acme.example/keys?ref=magpie" || p.Decide != "" {
		t.Fatalf("FromPreset(acme) = %+v, %v", p, err)
	}
	if pr := Preset("acme"); pr == nil || !pr.Sponsored {
		t.Fatalf("Preset(acme) = %+v", pr)
	}
	// a built-in preset is never a partner's
	if pr := Preset("openrouter"); pr == nil || pr.Kind == KindPartner || strings.Contains(pr.Chat, "evil") {
		t.Fatalf("Preset(openrouter) = %+v", pr)
	}

	// it leaves the list: no longer listed nor sponsored, but a provider
	// added from it still finds its preset, after a restart too
	body.Store(`{"partners":[]}`)
	fetchPartnersNow()
	if got := Partners(); len(got) != 0 {
		t.Fatalf("partners after the list emptied = %s", partnerIDs(got))
	}
	resetPartners()
	if pr := Preset("acme"); pr == nil || pr.Sponsored || pr.Chat != "https://api.acme.example/v1" || pr.SiteHost != "www.acme.example" {
		t.Fatalf("Preset(acme) after it left = %+v", pr)
	}

	// a failed fetch keeps the list held, from disk after a restart
	body.Store(`{"partners":[{"id":"acme","name":"Acme AI","chat":"https://api.acme.example/v1"}]}`)
	fetchPartnersNow()
	body.Store(`not json`)
	resetPartners()
	fetchPartnersNow()
	if ids := partnerIDs(Partners()); ids != "acme" {
		t.Fatalf("partners after a failed fetch = %q, want acme", ids)
	}
}

// A list fetched after the one Partners started behind the call is the
// list held, however late the earlier fetch answers.
func TestPartnersFetchedLastIsHeld(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetPartners()
	t.Cleanup(resetPartners)
	var body atomic.Value
	body.Store(`{"partners":[{"id":"acme","name":"Acme","chat":"https://a.example/v1"}]}`)
	var asked atomic.Int32
	release, served := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b := body.Load().(string)
		if asked.Add(1) == 1 {
			// the fetch Partners starts read the list before it changed,
			// and answers late
			<-release
			defer close(served)
		}
		_, _ = rw.Write([]byte(b))
	}))
	t.Cleanup(srv.Close)
	// runs before srv.Close, which waits for the held fetch: a check that
	// fails before the release below doesn't leave it held (#1808)
	let := sync.OnceFunc(func() { close(release) })
	t.Cleanup(let)
	t.Setenv("MAGPIE_PARTNERS", srv.URL)

	Partners()
	for deadline := time.Now().Add(5 * time.Second); asked.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("Partners started no fetch")
		}
		time.Sleep(5 * time.Millisecond)
	}
	body.Store(`{"partners":[]}`)
	go func() { time.Sleep(50 * time.Millisecond); let() }()
	fetchPartnersNow()
	<-served
	// the earlier fetch has answered; give it the time to store its list
	for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); {
		if got := Partners(); len(got) != 0 {
			t.Fatalf("partners after the list emptied = %s", partnerIDs(got))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := asked.Load(); n != 2 {
		t.Fatalf("asked %d times, want 2", n)
	}
}

// A process started fetches the list on its first ask, however fresh the
// list kept on disk: one fetched empty a minute before a partner was added
// held it back for six hours, restart or not.
func TestPartnersFetchedOnStart(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetPartners()
	t.Cleanup(resetPartners)
	kept, _ := json.Marshal(partnerCache{At: time.Now().Add(-time.Minute)})
	if err := os.MkdirAll(filepath.Dir(partnerCacheFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partnerCacheFile(), kept, 0o644); err != nil {
		t.Fatal(err)
	}
	var body atomic.Value
	body.Store(`{"partners":[{"id":"acme","name":"Acme","chat":"https://a.example/v1"}]}`)
	t.Setenv("MAGPIE_PARTNERS", partnerFeedServer(t, &body).URL)

	Partners()
	// the fetch it started has ended once a fetch can be claimed
	claimPartnerFetch()
	partnerMu.Unlock()
	if ids := partnerIDs(Partners()); ids != "acme" {
		t.Fatalf("partners after a start with a fresh empty list kept = %q, want acme", ids)
	}
}

// MAGPIE_PARTNERS=off lists none and asks nobody, whatever was kept.
func TestPartnersOff(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetPartners()
	t.Cleanup(resetPartners)
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_, _ = rw.Write([]byte(`{"partners":[{"id":"acme","name":"Acme","chat":"https://a.example/v1"}]}`))
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_PARTNERS", srv.URL)
	fetchPartnersNow()
	if _, err := os.Stat(partnerCacheFile()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_PARTNERS", "off")
	resetPartners()
	if got := Partners(); len(got) != 0 || Preset("acme") != nil {
		t.Fatalf("off listed %s", partnerIDs(got))
	}
	if asked.Load() != 1 {
		t.Fatalf("asked %d times", asked.Load())
	}
}

// Partners never blocks on the site, and asks it at most once at a time.
func TestPartnersDoesNotWait(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetPartners()
	t.Cleanup(resetPartners)
	release := make(chan struct{})
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		<-release
		_, _ = rw.Write([]byte(`{"partners":[{"id":"acme","name":"Acme","chat":"https://a.example/v1"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		// the fetch ends with the test
		close(release)
		for {
			partnerMu.Lock()
			busy := partnerFetches
			partnerMu.Unlock()
			if !busy {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	t.Setenv("MAGPIE_PARTNERS", srv.URL)
	// The site is held until the test ends, so Partners coming back at all is
	// Partners not waiting for it (#1617). The deadline only turns a wait
	// into a FAIL, and is short of the fetch's own 20s.
	looked := make(chan struct{})
	go func() {
		defer close(looked)
		for range 5 {
			Partners()
		}
	}()
	select {
	case <-looked:
	case <-time.After(10 * time.Second):
		t.Fatal("Partners waited for the held site")
	}
	deadline := time.Now().Add(5 * time.Second)
	for asked.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := asked.Load(); n != 1 {
		t.Fatalf("asked %d times, want 1", n)
	}
}

// PartnersNow, for a command that ends at once, has the list fetched before
// it answers, and doesn't wait past its bound.
func TestPartnersNow(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetPartners()
	t.Cleanup(resetPartners)
	var body atomic.Value
	body.Store(`{"partners":[{"id":"acme","name":"Acme","chat":"https://a.example/v1"}]}`)
	t.Setenv("MAGPIE_PARTNERS", partnerFeedServer(t, &body).URL)
	if ids := partnerIDs(PartnersNow(5 * time.Second)); ids != "acme" {
		t.Fatalf("PartnersNow = %q, want acme", ids)
	}

	resetPartners()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		close(release)
		for {
			partnerMu.Lock()
			busy := partnerFetches
			partnerMu.Unlock()
			if !busy {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	t.Setenv("MAGPIE_PARTNERS", srv.URL)
	// the list kept is due again
	partnerMu.Lock()
	loadPartners()
	partnerNext = time.Time{}
	partnerMu.Unlock()
	// The site is held, so PartnersNow coming back at all is its bound ending
	// the wait (#1617); the floor is that it did wait for the fetch, which a
	// busy machine only lengthens. The deadline only turns a wait past the
	// bound into a FAIL, and is short of the fetch's own 20s.
	type answer struct {
		ids  string
		took time.Duration
	}
	answered := make(chan answer, 1)
	go func() {
		start := time.Now()
		ids := partnerIDs(PartnersNow(100 * time.Millisecond))
		answered <- answer{ids, time.Since(start)}
	}()
	select {
	case a := <-answered:
		if a.ids != "acme" {
			t.Fatalf("PartnersNow while the site hangs = %q, want the kept acme", a.ids)
		}
		if a.took < 100*time.Millisecond {
			t.Fatalf("PartnersNow waited %v, not its 100ms bound", a.took)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PartnersNow waited past its bound for the held site")
	}
}

// A partner is new until the add sheet has shown it; that is kept on disk,
// and only for partners listed at some time.
func TestNoticePartners(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetPartners()
	t.Cleanup(resetPartners)
	var body atomic.Value
	body.Store(`{"partners":[{"id":"acme","name":"Acme","chat":"https://a.example/v1"},{"id":"beta","name":"Beta","chat":"https://b.example/v1"}]}`)
	t.Setenv("MAGPIE_PARTNERS", partnerFeedServer(t, &body).URL)
	fetchPartnersNow()
	if PartnerNoticed("acme") || PartnerNoticed("beta") {
		t.Fatal("noticed before shown")
	}
	NoticePartners("acme", "nobody")
	resetPartners()
	if !PartnerNoticed("acme") || PartnerNoticed("beta") || PartnerNoticed("nobody") {
		t.Fatalf("after a restart: acme %v beta %v nobody %v", PartnerNoticed("acme"), PartnerNoticed("beta"), PartnerNoticed("nobody"))
	}
	// a fetch keeps what was noticed
	body.Store(`{"partners":[{"id":"acme","name":"Acme","chat":"https://a.example/v1"},{"id":"gamma","name":"Gamma","chat":"https://g.example/v1"}]}`)
	fetchPartnersNow()
	if !PartnerNoticed("acme") || PartnerNoticed("gamma") {
		t.Fatalf("after a fetch: acme %v gamma %v", PartnerNoticed("acme"), PartnerNoticed("gamma"))
	}
}

// The magpie serving the gateway fetches the list when it starts and
// again when it is due, with no page asking: an app left open on another
// page still learns of a partner added within the hour.
func TestPartnersKeptFreshUnasked(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetPartners()
	t.Cleanup(resetPartners)
	was := partnerEvery
	partnerEvery = 50 * time.Millisecond
	t.Cleanup(func() { partnerEvery = was })
	var asked atomic.Int32
	var body atomic.Value
	body.Store(`{"partners":[]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_, _ = rw.Write([]byte(body.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MAGPIE_PARTNERS", srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); KeepPartnersFresh(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("%s: asked %d times", what, asked.Load())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("fetched on start", func() bool { return asked.Load() >= 1 })
	body.Store(`{"partners":[{"id":"acme","name":"Acme","chat":"https://a.example/v1"}]}`)
	n := asked.Load()
	waitFor("fetched again when due", func() bool { return asked.Load() > n })
	// the fetch counted may still be storing its list
	claimPartnerFetch()
	partnerMu.Unlock()
	waitFor("the partner added is held", func() bool {
		partnerMu.Lock()
		defer partnerMu.Unlock()
		return partnerState != nil && partnerIDs(partnerState.Partners) == "acme"
	})
}

// #3424: a partner's logo is no provider's icon until one is added from
// it, and a list fetched again uses the logo kept from the same URL as it
// is, so the file only ages. The prune after any save of providers.json
// took it once it was an hour old: the add sheet drew the partner with
// the generic icon, and the next fetch downloaded the logo again. It is
// kept while the partner is listed; a picture nothing points at still
// goes.
func TestPartnerLogoOutlivesAProvidersSave(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetPartners()
	t.Cleanup(resetPartners)
	was := fetchPartnerIcon
	t.Cleanup(func() { fetchPartnerIcon = was })
	var fetched atomic.Int32
	fetchPartnerIcon = func(context.Context, string) (string, error) {
		fetched.Add(1)
		return StoreIcon([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR acme's logo"))
	}
	var body atomic.Value
	body.Store(`{"partners":[{"id":"acme","name":"Acme AI","chat":"https://api.acme.example/v1","iconUrl":"https://acme.example/logo.png"}]}`)
	t.Setenv("MAGPIE_PARTNERS", partnerFeedServer(t, &body).URL)
	fetchPartnersNow()
	got := Partners()
	if len(got) != 1 || !strings.HasPrefix(got[0].Icon, iconPrefix) {
		t.Fatalf("partners = %+v", got)
	}
	logo := IconFile(strings.TrimPrefix(got[0].Icon, iconPrefix))
	stray, err := StoreIcon([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR nobody's"))
	if err != nil {
		t.Fatal(err)
	}
	strayFile := IconFile(strings.TrimPrefix(stray, iconPrefix))
	// kept from an earlier day, and the list fetched again (a start) uses
	// it as it is
	old := time.Now().Add(-2 * time.Hour)
	for _, f := range []string{logo, strayFile} {
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}
	fetchPartnersNow()

	pruneIcons(file{})
	if _, err := os.Stat(logo); err != nil {
		t.Errorf("the listed partner's logo was pruned: %v", err)
	}
	if _, err := os.Stat(strayFile); err == nil {
		t.Errorf("a picture nothing points at was kept")
	}
	fetchPartnersNow()
	if n := fetched.Load(); n != 1 {
		t.Errorf("the logo was downloaded %d times, want once", n)
	}
}

// partners.json says when the list was last tried and how that ended. An
// app that kept a list from 10-20 for three days left a file that
// couldn't tell a fetch failing every time from one that never came back,
// or from none asked (#3362). Now a failure keeps its error until a fetch
// gets the list, a fetch still out shows as a try after the list's time
// with no error, and a failure is logged once, however often it is
// tried again.
func TestPartnersKeepTheirLastTry(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetPartners()
	t.Cleanup(resetPartners)
	var logged bytes.Buffer
	was := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(was) })
	var fail, hold atomic.Bool
	fail.Store(true)
	var asked atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if hold.Load() {
			<-release
		}
		if fail.Load() {
			http.Error(rw, "down for maintenance", http.StatusServiceUnavailable)
			return
		}
		_, _ = rw.Write([]byte(`{"partners":[{"id":"acme","name":"Acme","chat":"https://a.example/v1"}]}`))
	}))
	t.Cleanup(srv.Close)
	// runs before srv.Close, which waits for a held fetch (#1808)
	let := sync.OnceFunc(func() { close(release) })
	t.Cleanup(let)
	t.Setenv("MAGPIE_PARTNERS", srv.URL)

	// what the file says, read as written, not through partnerCache
	type kept struct {
		at, tried time.Time
		err       string
		partners  string
	}
	read := func() kept {
		t.Helper()
		b, err := os.ReadFile(partnerCacheFile())
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		var k kept
		_ = json.Unmarshal(raw["at"], &k.at)
		_ = json.Unmarshal(raw["tried"], &k.tried)
		_ = json.Unmarshal(raw["error"], &k.err)
		var ps []Partner
		_ = json.Unmarshal(raw["partners"], &ps)
		k.partners = partnerIDs(ps)
		return k
	}

	start := time.Now()
	fetchPartnersNow()
	k := read()
	if k.tried.Before(start) || !strings.Contains(k.err, "503") || !k.at.IsZero() {
		t.Fatalf("after a failed fetch the file says tried %v, error %q, at %v; want a try since %v, the 503, no list", k.tried, k.err, k.at, start)
	}
	fetchPartnersNow()
	fetchPartnersNow()
	if n := strings.Count(logged.String(), "couldn't be fetched"); n != 1 {
		t.Fatalf("three failed fetches logged %d times, want once:\n%s", n, logged.String())
	}

	// a fetch still out: tried after the list's time, no error
	fail.Store(false)
	hold.Store(true)
	before, out := asked.Load(), time.Now()
	done := make(chan struct{})
	go func() { fetchPartnersNow(); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); asked.Load() == before; {
		if time.Now().After(deadline) {
			t.Fatal("the fetch never asked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	k = read()
	if k.tried.Before(out) || k.err != "" || !k.at.Before(k.tried) {
		t.Fatalf("while a fetch is out the file says tried %v, error %q, at %v; want a try since %v after the list's time, no error", k.tried, k.err, k.at, out)
	}

	let()
	<-done
	k = read()
	if k.err != "" || k.at.Before(k.tried) || k.partners != "acme" {
		t.Fatalf("after the fetch got the list the file says tried %v, error %q, at %v, partners %q", k.tried, k.err, k.at, k.partners)
	}
	if !strings.Contains(logged.String(), "fetched again") {
		t.Fatalf("the list fetched again after a failure wasn't logged:\n%s", logged.String())
	}
}

// A partners.json that doesn't parse is read as no list and no Seen, and
// every try now writes the file before it is sent (#3362). The first try,
// offline too, wrote over it: the presets of partners no longer listed,
// which providers added from them still use, were gone. It is kept aside
// first, as lastgood keeps every file read as empty when it doesn't parse.
func TestPartnersUnparsedFileKeptAside(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetPartners()
	t.Cleanup(resetPartners)
	bad := []byte(`{"at":"2026-10-20T15:40:31+08:00","seen":{"acme":{"id":"acme","name":"Acme AI","ch`)
	if err := os.MkdirAll(filepath.Dir(partnerCacheFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partnerCacheFile(), bad, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MAGPIE_PARTNERS", srv.URL)
	fetchPartnersNow()

	aside, _ := filepath.Glob(partnerCacheFile() + ".bad-*")
	if len(aside) != 1 {
		t.Fatalf("partners.json that didn't parse was written over with nothing kept aside (%v)", aside)
	}
	if b, err := os.ReadFile(aside[0]); err != nil || !bytes.Equal(b, bad) {
		t.Fatalf("kept aside %q (%v), want the file as it was", b, err)
	}
	partnerMu.Lock()
	defer partnerMu.Unlock()
	if partnerState.Error == "" {
		t.Fatalf("the failed try isn't recorded: %+v", partnerState)
	}
}

// A partner whose product is a plugin names its npm package and no
// endpoint: it is listed by PluginPartners, never by Partners nor as a
// preset, and an entry that isn't a package's name, sends to http, or
// has an endpoint as well is left out. The add sheet's partners are as
// they were, each list capped on its own, and the plugin's partner is
// counted as theirs are.
func TestPluginPartners(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("MAGPIE_NO_STATS", "")
	resetPartners()
	t.Cleanup(resetPartners)
	entries := []string{
		`{"id":"acme","name":"Acme AI","chat":"https://api.acme.example/v1"}`,
		`{"id":"notes","name":"Notes Sync","plugin":"@notes/magpie-sync","website":"https://usemagpie.ai/go/notes/site","siteHost":"notes.example",
		  "notes":{"en":"Your notes, in every agent","zh":"笔记进每个 agent"},"langs":["zh","en"],"keysUrl":"https://notes.example/keys","decide":"https://notes.example/d"}`,
		`{"id":"bare","name":"Bare","plugin":"bare-plugin"}`,
		`{"id":"badname","name":"Bad name","plugin":"Bad Name"}`,
		`{"id":"versioned","name":"Versioned","plugin":"some-plugin@1.2.3"}`,
		`{"id":"both","name":"Both","plugin":"both-plugin","chat":"https://api.both.example/v1"}`,
		`{"id":"regions","name":"Regions","plugin":"regions-plugin","regions":[{"id":"cn","name":"China","chat":"https://cn.example/v1"}]}`,
		`{"id":"plainsite","name":"Plain site","plugin":"plain-plugin","website":"http://plain.example"}`,
		`{"id":"openrouter","name":"Not OpenRouter","plugin":"openrouter-plugin"}`,
	}
	// thirteen more of each: each list stops at MaxPartners of its own
	for i := range 13 {
		entries = append(entries, fmt.Sprintf(`{"id":"pv%d","name":"P%d","chat":"https://p%d.example/v1"}`, i, i, i),
			fmt.Sprintf(`{"id":"pl%d","name":"L%d","plugin":"plugin-%d"}`, i, i, i))
	}
	var body atomic.Value
	body.Store(`{"partners":[` + strings.Join(entries, ",") + `]}`)
	t.Setenv("MAGPIE_PARTNERS", partnerFeedServer(t, &body).URL)
	fetchPartnersNow()

	pv, pl := Partners(), PluginPartners()
	if len(pv) != MaxPartners || pv[0].ID != "acme" || pv[1].ID != "pv0" {
		t.Fatalf("add sheet's partners = %s", partnerIDs(pv))
	}
	if len(pl) != MaxPartners || partnerIDs(pl[:3]) != "notes,bare,pl0" {
		t.Fatalf("plugin partners = %s", partnerIDs(pl))
	}
	n := pl[0]
	if n.Plugin != "@notes/magpie-sync" || n.Name != "Notes Sync" || n.Notes["zh"] != "笔记进每个 agent" || n.Note != "Your notes, in every agent" || len(n.Langs) != 2 || !n.Sponsored {
		t.Errorf("notes = %+v", n)
	}
	if n.Website != "https://usemagpie.ai/go/notes/site" || n.SiteHost != "notes.example" || n.Icon != "generic" {
		t.Errorf("notes site %q host %q icon %q", n.Website, n.SiteHost, n.Icon)
	}
	// nothing a preset carries: no key page, no decision API, no endpoint
	if n.KeysURL != "" || n.Decide != "" || n.Chat != "" {
		t.Errorf("notes kept preset fields: keys %q decide %q chat %q", n.KeysURL, n.Decide, n.Chat)
	}
	if pr := Preset("notes"); pr != nil {
		t.Errorf("a plugin's partner is a preset: %+v", pr)
	}
	if _, err := FromPreset("notes"); err == nil {
		t.Error("a provider was made from a plugin's partner")
	}
	if pr := Preset("acme"); pr == nil || !pr.Sponsored {
		t.Errorf("Preset(acme) = %+v", pr)
	}

	// counted as the add sheet's are: shown and opened as the page says,
	// added when its plugin is installed, and only then: not another
	// package, not a failed install, not one carried over from a backup
	CountPartner(PartnerShown, "notes", "badname")
	oldAdd := addPlugin
	t.Cleanup(func() { addPlugin = oldAdd })
	addPlugin = func(_ context.Context, spec string) (plugin.Entry, error) {
		if spec == "@notes/magpie-sync@9.9.9" {
			return plugin.Entry{}, errors.New("no such version")
		}
		return plugin.Entry{Spec: spec}, nil
	}
	ctx := context.Background()
	for _, spec := range []string{"@notes/magpie-sync", "other-plugin", "@notes/magpie-sync@9.9.9"} {
		_, _ = AddPlugin(ctx, spec)
	}
	if got, want := countsLine(PartnerCounts(time.Now().Add(24*time.Hour))), "notes added 1,notes shown 1"; got != want {
		t.Errorf("counts = %q, want %q", got, want)
	}
}
