package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	fetchPartnerIcon = func(_ context.Context, u string) (string, error) {
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
		 "iconUrl":"https://acme.example/logo.png","notes":{"en":"Fast relay","zh":"快速中转"},"langs":["zh"],
		 "decide":"https://api.acme.example/decide","noKey":true},
		{"id":"plain","name":"Plain","chat":"http://api.plain.example/v1"},
		{"id":"openrouter","name":"Not OpenRouter","chat":"https://evil.example/v1"},
		{"id":"Bad_Id","name":"Bad","chat":"https://bad.example/v1"},
		{"id":"noname","chat":"https://x.example/v1"},
		{"id":"later","name":"Later","chat":"https://later.example/v1","from":"` + future + `"},
		{"id":"gone","name":"Gone","chat":"https://gone.example/v1","until":"` + past + `"},
		{"id":"regioned","name":"Regioned","regions":[{"id":"cn","name":"China","chat":"https://cn.regioned.example/v1"}]},
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
	if pr := Preset("acme"); pr == nil || pr.Sponsored || pr.Chat != "https://api.acme.example/v1" {
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
	t.Setenv("MAGPIE_PARTNERS", srv.URL)

	Partners()
	for deadline := time.Now().Add(5 * time.Second); asked.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("Partners started no fetch")
		}
		time.Sleep(5 * time.Millisecond)
	}
	body.Store(`{"partners":[]}`)
	go func() { time.Sleep(50 * time.Millisecond); close(release) }()
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
	start := time.Now()
	for range 5 {
		Partners()
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Partners waited %v", d)
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
	start := time.Now()
	if ids := partnerIDs(PartnersNow(100 * time.Millisecond)); ids != "acme" {
		t.Fatalf("PartnersNow while the site hangs = %q, want the kept acme", ids)
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > 2*time.Second {
		t.Fatalf("PartnersNow waited %v, want its bound", d)
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
