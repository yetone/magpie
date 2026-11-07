package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider's Routing and Stays are picked in its editor and made with its
// Save (01huadalang on Discord: each click saved at once); a Save that
// doesn't carry them keeps them, where the Stays set on the Routing page
// used to be lost at the editor's next Save, which never sent it.
func TestProviderSaveRouting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(action, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body)
		}
	}
	is := func(routing, affinity string) {
		t.Helper()
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		if p.Routing != routing || p.Affinity != affinity {
			t.Fatalf("routing %q, affinity %q; want %q, %q", p.Routing, p.Affinity, routing, affinity)
		}
	}
	const form = `"id":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","models":["sol"]`
	post("save", `{`+form+`,"key":"k","new":true}`)
	is("", "")
	// the editor's picks, made with its Save
	post("save", `{`+form+`,"from":"relay","routing":"rotate","affinity":"session"}`)
	is(provider.Rotate, provider.AffinitySession)
	// a Save that doesn't carry them keeps them
	post("save", `{`+form+`,"from":"relay"}`)
	is(provider.Rotate, provider.AffinitySession)
	// set on their own, from the Routing page, and kept by the next Save
	post("route", `{"id":"relay","routing":"order"}`)
	post("affinity", `{"id":"relay","affinity":"turn"}`)
	is(provider.Ordered, provider.AffinityTurn)
	post("save", `{`+form+`,"from":"relay"}`)
	is(provider.Ordered, provider.AffinityTurn)
	// Smart and Auto picked are "" sent, not left out
	post("save", `{`+form+`,"from":"relay","routing":"","affinity":""}`)
	is("", "")
}

// Sink (01huadalang: one rate limited with quota left goes to the back)
// is picked in the editor and made with its Save, or set on its own from
// the Routing page, and a Save that doesn't carry it keeps it.
func TestProviderSaveSink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(action, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body)
		}
	}
	is := func(want bool) {
		t.Helper()
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		if p.Sink != want {
			t.Fatalf("sink %v, want %v", p.Sink, want)
		}
		if got := providerInfo(*p, nil).Sink; got != want {
			t.Fatalf("listed sink %v, want %v", got, want)
		}
	}
	const form = `"id":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","models":["sol"]`
	post("save", `{`+form+`,"key":"k","new":true}`)
	is(false)
	post("save", `{`+form+`,"from":"relay","routing":"order","sink":true}`)
	is(true)
	post("save", `{`+form+`,"from":"relay"}`)
	is(true)
	post("sink", `{"id":"relay","sink":false}`)
	is(false)
	post("sink", `{"id":"relay","sink":true}`)
	post("save", `{`+form+`,"from":"relay"}`)
	is(true)
}

// A decision-only preset's provider (TypeSafe Jev) has no chat URL, so its
// editor's Save is rebuilt from the preset: what the editor picks beside
// the key — Routing, Stays, Rate limited, Unlisted, each key's proxy — is
// kept, not put back to Smart (fflake33 on Discord: In order saved, and
// reopened as Smart). The body is the editor's, as app.js sends it; its
// decision API is a closed local port, so the list fetched after a new key
// reaches no network.
func TestDecidePresetSaveKeepsRouting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(action, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body)
		}
	}
	post("save", `{"id":"typesafe","name":"TypeSafe Jev","preset":"typesafe","key":"jev-key-1","decide":"http://127.0.0.1:1/v1","models":[],"headers":{},"new":true}`)
	if _, _, err := provider.AddKeys("typesafe", []string{"jev-key-2"}, ""); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("typesafe")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "" || p.Decide == "" || len(p.KeysOn()) != 2 {
		t.Fatalf("not a decision-only provider of two keys: %+v", p)
	}
	second := p.KeyList()[1].ID
	form := `"id":"typesafe","from":"typesafe","name":"TypeSafe Jev","preset":"typesafe","key":"","decide":"http://127.0.0.1:1/v1","models":[],"headers":{},"fallback":[],"noModels":false,"searches":false,"pinUpstream":false,"unredacted":false,"passUserAgent":true,"contexts":{},"proxy":"","maxConcurrency":0`
	post("save", `{`+form+`,"unlisted":true,"accountProxies":{"`+second+`":"http://127.0.0.1:7890"},"routing":"order","affinity":"session","sink":true}`)
	p, err = provider.Find("typesafe")
	if err != nil {
		t.Fatal(err)
	}
	if p.Routing != provider.Ordered || p.Affinity != provider.AffinitySession || !p.Sink || !p.Unlisted || p.AccountProxies[second] != "http://127.0.0.1:7890" {
		t.Fatalf("after Save: routing %q, affinity %q, sink %v, unlisted %v, proxies %v", p.Routing, p.Affinity, p.Sink, p.Unlisted, p.AccountProxies)
	}
	if got := providerInfo(*p, nil).Routing; got != provider.Ordered {
		t.Fatalf("the editor reopens on %q, want order", got)
	}
	if p.Key != "jev-key-1" || len(p.KeysOn()) != 2 {
		t.Fatalf("keys changed by the Save: %q, %d on", p.Key, len(p.KeysOn()))
	}
	// a Save that doesn't carry them keeps them; Smart picked is "" sent
	post("save", `{`+form+`}`)
	if p, _ = provider.Find("typesafe"); p.Routing != provider.Ordered || p.Affinity != provider.AffinitySession || !p.Sink {
		t.Fatalf("a Save without them: %q %q %v", p.Routing, p.Affinity, p.Sink)
	}
	post("save", `{`+form+`,"routing":"","affinity":"","sink":false}`)
	if p, _ = provider.Find("typesafe"); p.Routing != "" || p.Affinity != "" || p.Sink {
		t.Fatalf("Smart picked: %q %q %v", p.Routing, p.Affinity, p.Sink)
	}
}
