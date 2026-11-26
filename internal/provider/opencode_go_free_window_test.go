package provider

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// goFreeKey is an OpenCode Go key as the preset adds it.
var goFreeKey = Provider{ID: "go", Name: "OpenCode Go", Preset: "opencode-go", Key: "sk-go-a",
	Chat: OpenCodeGoChat, Responses: OpenCodeGoChat, Anthropic: OpenCodeGoAnthropic}

// goUsedUp has OpenCode Go's /usage answer the key's 5 hours used up, as
// Winnie7572's was, with the week and the month below theirs.
func goUsedUp(t *testing.T) {
	t.Helper()
	five := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	week := time.Now().Add(50 * time.Hour).UTC().Format(time.RFC3339)
	month := time.Now().Add(400 * time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Host")+r.URL.Path != "opencode.ai/zen/go/v1/usage" || r.Header.Get("Authorization") != "Bearer sk-go-a" {
			t.Errorf("asked %s%s", r.Header.Get("X-Host"), r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"usage":{"rolling":{"status":"rate-limited","percent":100,"resetsAt":"` + five + `"},
			"weekly":{"status":"ok","percent":40,"resetsAt":"` + week + `"},
			"monthly":{"status":"ok","percent":20,"resetsAt":"` + month + `"}}}`))
	}))
	t.Cleanup(srv.Close)
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
}

// goPrices writes models.dev's opencode-go as it prices Go's models: the
// free ones at nothing on every count, with no word that they are free.
func goPrices(t *testing.T) {
	t.Helper()
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"opencode-go":{"id":"opencode-go","models":{
		"glm-5.3":{"id":"glm-5.3","cost":{"input":1,"output":3.2,"cache_read":0.2}},
		"qwen3-embedding-8b":{"id":"qwen3-embedding-8b","cost":{"input":0.01,"output":0}},
		"hy4-flash-free":{"id":"hy4-flash-free","cost":{"input":0,"output":0,"cache_read":0}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// Winnie7572 on X, #7496: an OpenCode Go key's windows count the models
// that draw on them, not its free ones, which Go serves with every window
// used up. With the 5 hours at 100% the paid model is used up — ordered
// last, held at the key's cap, shown so on the Routing page — and the
// free one has all of it: nothing full, no cap reached. The key itself
// (model "") is still out. Free by Go's own list (Free), and by models.dev's
// price alone, as a list saved before Go's word was read has it.
func TestOpenCodeGoWindowsLeaveFreeModels(t *testing.T) {
	for _, c := range []struct {
		name string
		list []catalog.Model
	}{
		{"Go's word", []catalog.Model{{ID: "glm-5.3"}, {ID: "qwen3-embedding-8b"}, {ID: "hy4-flash-free", Free: true}}},
		{"models.dev's price", []catalog.Model{{ID: "glm-5.3"}, {ID: "qwen3-embedding-8b"}, {ID: "hy4-flash-free"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			keyLimitsHome(t)
			goUsedUp(t)
			goPrices(t)
			if err := catalog.SaveLive("go", OpenCodeGoChat, c.list); err != nil {
				t.Fatal(err)
			}
			if err := Save(goFreeKey); err != nil {
				t.Fatal(err)
			}
			a := waitAllowance(t, goFreeKey, func(a Allowance) bool { return len(a) == 3 })
			now := time.Now()
			for _, m := range []string{"glm-5.3", "GLM-5.3", "qwen3-embedding-8b", ""} {
				if used, _ := a.For(m, now); used != 100 {
					t.Errorf("%q: %v%% used, want 100", m, used)
				}
				if a.Full(m, 100, now).IsZero() {
					t.Errorf("%q: not used up", m)
				}
				if a.CapHeld(m, WindowCaps{All: 50}, now) == nil {
					t.Errorf("%q: not held at a cap of 50%%", m)
				}
			}
			for _, m := range []string{"hy4-flash-free", "HY4-Flash-Free"} {
				if used, renews := a.For(m, now); used != 0 || len(renews) != 0 {
					t.Errorf("%s: %v%% used, renews %v: its windows count it", m, used, renews)
				}
				if t0 := a.Full(m, 100, now); !t0.IsZero() {
					t.Errorf("%s: used up until %v", m, t0)
				}
				if h := a.CapHeld(m, WindowCaps{All: 50}, now); h != nil {
					t.Errorf("%s: held at the key's cap: %+v", m, h)
				}
			}
			p, err := Find("go")
			if err != nil {
				t.Fatal(err)
			}
			p.Key = goFreeKey.Key
			if !p.PaidBesideFree("glm-5.3") || p.PaidBesideFree("hy4-flash-free") {
				t.Errorf("PaidBesideFree: glm-5.3 %v, hy4-flash-free %v", p.PaidBesideFree("glm-5.3"), p.PaidBesideFree("hy4-flash-free"))
			}
			if !p.FreeBesidePaid("hy4-flash-free") || p.FreeBesidePaid("glm-5.3") || p.FreeBesidePaid("not-listed-free") {
				t.Errorf("FreeBesidePaid: hy4-flash-free %v, glm-5.3 %v", p.FreeBesidePaid("hy4-flash-free"), p.FreeBesidePaid("glm-5.3"))
			}
		})
	}
}

// A key at another vendor is read as before: its windows are the whole
// key's, a model priced at nothing among them, and a vendor's $0 price
// alone frees no model of its refusals.
func TestKeyWindowsElsewhereStayWhole(t *testing.T) {
	keyLimitsHome(t)
	goPrices(t)
	p := Provider{ID: "glm", Key: "k", Chat: "https://open.bigmodel.cn/api/coding/paas/v4"}
	if err := catalog.SaveLive("glm", p.Chat, []catalog.Model{{ID: "glm-5.3"}, {ID: "hy4-flash-free"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	ws := []QuotaWindow{{Name: "5 hours", Used: 100, Span: 5 * time.Hour}}
	if got := p.paidOnly(ws); got[0].matches != nil {
		t.Error("scoped at a vendor whose windows count every model")
	}
	if p.PaidBesideFree("glm-5.3") || p.FreeBesidePaid("hy4-flash-free") {
		t.Error("a model free by name or price elsewhere counted as one the vendor serves free")
	}
}

// xzhih on X, #7773: an OpenCode Go key at its usage cap was shown held
// as a whole, while the gateway, asking the cap per model, served its
// free models. KeyCapLeaves names them; at another vendor, whose windows
// count every model, a key at its cap leaves none.
func TestKeyCapLeavesGoFreeModels(t *testing.T) {
	keyLimitsHome(t)
	goPrices(t)
	if err := catalog.SaveLive("go", OpenCodeGoChat, []catalog.Model{{ID: "glm-5.3"}, {ID: "hy4-flash-free"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	resets := now.Add(time.Hour)
	ws := []QuotaWindow{{Name: "5 hours", Used: 80, Span: 5 * time.Hour, ResetsAt: &resets}}
	read := func(q Provider) (Allowance, bool) { return keyAllowanceOf(q, ws, now), true }
	p := goFreeKey
	p.AccountCaps = map[string]int{KeyID(p.Key): 50}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	ka, _ := p.KeyAccountOf(KeyID(p.Key))
	if p.KeyCapHeld(ka, "", now, read) == nil {
		t.Fatal("the key isn't held at its cap: this test is about one that is")
	}
	if got := p.KeyCapLeaves(ka, now, read); len(got) != 1 || got[0] != "hy4-flash-free" {
		t.Errorf("at the cap the Go key leaves %v, want [hy4-flash-free]", got)
	}
	// below the cap, nothing is held, so nothing is left
	ws[0].Used = 30
	if got := p.KeyCapLeaves(ka, now, read); got != nil {
		t.Errorf("below the cap: %v", got)
	}
	ws[0].Used = 80

	g := Provider{ID: "glm", Key: "k", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", AccountCaps: map[string]int{KeyID("k"): 50}}
	if err := catalog.SaveLive("glm", g.Chat, []catalog.Model{{ID: "glm-5.3"}, {ID: "hy4-flash-free"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(g); err != nil {
		t.Fatal(err)
	}
	gk, _ := g.KeyAccountOf(KeyID("k"))
	if g.KeyCapHeld(gk, "", now, read) == nil {
		t.Fatal("the other vendor's key isn't held at its cap")
	}
	if got := g.KeyCapLeaves(gk, now, read); got != nil {
		t.Errorf("another vendor's key at its cap leaves %v", got)
	}
}

// ModelsRestAlone is OpenCode's, with free models beside paid ones: an
// old whole-key rest or notice of such a key is the pre-v0.1.4818 rule's
// (#7773). Another vendor's key, a $0 model on its list too, isn't.
func TestModelsRestAlone(t *testing.T) {
	keyLimitsHome(t)
	goPrices(t)
	if err := catalog.SaveLive("go", OpenCodeGoChat, []catalog.Model{{ID: "glm-5.3"}, {ID: "hy4-flash-free"}}); err != nil {
		t.Fatal(err)
	}
	if !goFreeKey.ModelsRestAlone() {
		t.Error("an OpenCode Go key with a free model beside paid ones")
	}
	g := Provider{ID: "glm", Key: "k", Chat: "https://open.bigmodel.cn/api/coding/paas/v4"}
	if err := catalog.SaveLive("glm", g.Chat, []catalog.Model{{ID: "glm-5.3"}, {ID: "hy4-flash-free", Free: true}}); err != nil {
		t.Fatal(err)
	}
	if g.ModelsRestAlone() {
		t.Error("another vendor's key")
	}
}
