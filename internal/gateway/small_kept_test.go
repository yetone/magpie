package gateway

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// manyEyes is a key of a router listing n models that see, the cheapest a
// small model of a big vendor; all of them enabled, or three. (All reads
// the providers' file each time, which is as long as what is enabled.)
func manyEyes(t *testing.T, n int, all bool) {
	t.Helper()
	fresh(t)
	var ms []catalog.Model
	var every []string
	for i := range n {
		id := fmt.Sprintf("vendor%d/model-%d", i, i)
		if i == n-1 {
			id = "google/gemini-3-flash"
		}
		ms = append(ms, catalog.Model{ID: id, Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: float64(n - i), Output: 1}})
		every = append(every, id)
	}
	ids := []string{ms[0].ID, ms[1].ID, "google/gemini-3-flash"}
	if all {
		ids = every
	}
	if err := provider.Save(provider.Provider{ID: "eyes", Name: "Eyes", Chat: "http://eyes.invalid/v1", Key: "k", Models: ids}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("eyes", "http://eyes.invalid/v1", ms); err != nil {
		t.Fatal(err)
	}
}

// AutoVision is asked for every agent's list of models (provider.Described,
// dsh's every 30s, the window's state), and built every provider's list
// again each time, twice, then asked whether each of its models was out of
// its allowance: 5.3 GB of a day's 36 GB of allocations. Asked again with
// nothing changed, it costs the same whatever the lists' length.
func TestAutoVisionDoesNotBuildListsAgain(t *testing.T) {
	// what each costs asked again, with n models; measured against the
	// same call with 4, as a fixed count differs by OS and load (#5123:
	// searcherModel made 129 on Windows, 51 in a full Linux run)
	allocs := func(n int) (vision, searcher float64) {
		manyEyes(t, n, false)
		if got := AutoVision(); got != "eyes/google/gemini-3-flash" {
			t.Fatalf("AutoVision = %q of %d", got, n)
		}
		vision = testing.AllocsPerRun(20, func() { AutoVision() })
		// and the searcher's pick, built the same way
		p, err := provider.Find("eyes")
		if err != nil {
			t.Fatal(err)
		}
		searcherModel(*p)
		searcher = testing.AllocsPerRun(20, func() { searcherModel(*p) })
		return vision, searcher
	}
	few, fewS := allocs(4)
	many, manyS := allocs(400)
	t.Logf("allocations an AutoVision: %.0f with 4 models, %.0f with 400; searcherModel %.0f and %.0f", few, many, fewS, manyS)
	if many > few+50 {
		t.Fatalf("AutoVision made %.0f allocations with 400 models, %.0f with 4: the lists are built again", many, few)
	}
	if manyS > fewS+50 {
		t.Fatalf("searcherModel made %.0f allocations with 400 models, %.0f with 4: the lists are built again", manyS, fewS)
	}
}

// What AutoVision keeps is built again when what it was built of changes:
// a provider saved (its picks), a vendor's list fetched, an account signed
// in, the settings and the providers' file written by another magpie.
func TestAutoVisionSeesEachChange(t *testing.T) {
	routerUp(t, "text")
	if m := AutoVision(); m != "" {
		t.Fatalf("AutoVision = %q with no model that sees enabled", m)
	}

	// a provider saved: Haiku enabled
	p, err := provider.Find("router")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"text", "anthropic/claude-haiku-5.5", "google/gemini-3-flash"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if m := AutoVision(); m != "router/google/gemini-3-flash" {
		t.Fatalf("AutoVision = %q after Haiku and Gemini were enabled", m)
	}

	// the vendor's list fetched anew: Haiku the cheaper now
	if err := catalog.SaveLive("router", p.Chat, []catalog.Model{
		{ID: "text", ImageInput: imageInputBool(false), Price: &catalog.Price{Input: 0.1, Output: 0.1}},
		{ID: "anthropic/claude-haiku-5.5", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 0.1, Output: 0.2}},
		{ID: "google/gemini-3-flash", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 0.3, Output: 2.5}},
	}); err != nil {
		t.Fatal(err)
	}
	if m := AutoVision(); m != "router/anthropic/claude-haiku-5.5" {
		t.Fatalf("AutoVision = %q after the list priced Haiku the cheaper", m)
	}

	// the providers' file written by another magpie (magpie provider
	// edit in a terminal): Haiku taken out
	b, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	other := strings.Replace(string(b), `"anthropic/claude-haiku-5.5",`, "", 1)
	if other == string(b) {
		t.Fatalf("no Haiku pick in %s", b)
	}
	if err := os.WriteFile(provider.Path(), []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := AutoVision(); m != "router/google/gemini-3-flash" {
		t.Fatalf("AutoVision = %q after another magpie took Haiku out", m)
	}

	// a setting saved moves what it is kept by
	before := provider.StampNow()
	st := settings.Load()
	st.Searcher = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if provider.StampNow() == before {
		t.Fatal("a setting saved left the stamp where it was")
	}

	// an account signed in, in this same home: Codex's tier goes first
	if err := catalog.SaveLive("codex", provider.CodexBase, []catalog.Model{{ID: "gpt-6.1-sol", Images: true, ImageInput: imageInputBool(true)}}); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	claims := func(m map[string]any) string {
		return "h." + base64.RawURLEncoding.EncodeToString(mustJSON(m)) + ".s"
	}
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      claims(map[string]any{"email": "me@example.com"}),
		"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": "acct-1"}),
		"refresh_token": "r-acct-1", "account_id": "acct-1"}}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts() // the 30s accounts are looked for again in
	t.Cleanup(provider.ForgetAccounts)
	if m := AutoVision(); m != "codex/gpt-6.1-sol" {
		t.Fatalf("AutoVision = %q after Codex was signed in", m)
	}
}

// A model out of its allowance is passed over as soon as it is, though
// the lists are kept: what is out is asked each time.
func TestAutoVisionKeptListsAskAllowanceEachTime(t *testing.T) {
	codexSignedIn(t)
	used := map[string]float64{"me@example.com": 20}
	capUsage(t, used)
	if err := catalog.SaveLive("codex", provider.CodexBase, []catalog.Model{{ID: "gpt-6.1-sol", Images: true, ImageInput: imageInputBool(true)}}); err != nil {
		t.Fatal(err)
	}
	if got, ready := autoVision(); got != "codex/gpt-6.1-sol" || !ready {
		t.Fatalf("autoVision = %q %v", got, ready)
	}
	used["me@example.com"] = 100
	if got, ready := autoVision(); got != "codex/gpt-6.1-sol" || ready {
		t.Fatalf("autoVision = %q %v with the only account used up", got, ready)
	}
}

// A relay's model IDs can carry a namespace colon, cn:deepseek-v4.1-flash
// and the like (#4359). The pool the small models are picked from leaves
// IDs with a colon out, as magpie's own effort-suffixed ones have one;
// the user's answer about a model named in full is about one of the
// vendor's models, not one of magpie's shapes: their "it takes images"
// keeps it in the pool, for AutoVision to pick, and their "it takes
// none" keeps it out. Without an answer of theirs, it stays out as it
// was.
func TestAutoVisionPicksANamespacedIDTheUserSaidSees(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://relay.invalid/v1", Key: "k",
		Models: []string{"cn:deepseek-v4.1-flash", "cn:glm-5v-turbo"}}); err != nil {
		t.Fatal(err)
	}
	// the vendor's list, as a relay's is, says nothing of images
	if err := catalog.SaveLive("relay", "http://relay.invalid/v1", []catalog.Model{
		{ID: "cn:deepseek-v4.1-flash", Price: &catalog.Price{Input: 0.1, Output: 0.1}},
		{ID: "cn:glm-5v-turbo", Price: &catalog.Price{Input: 0.2, Output: 0.2}},
	}); err != nil {
		t.Fatal(err)
	}
	if m := AutoVision(); m != "" {
		t.Fatalf("AutoVision = %q though no model of the relay is known to see and the user said nothing", m)
	}

	// the user marks the relay's models as taking images (#4359's
	// settings.json): the one with flash in its ID is picked
	st := settings.Load()
	st.ModelImages = map[string]bool{"relay/cn:deepseek-v4.1-flash": true, "relay/cn:glm-5v-turbo": true}
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if m := AutoVision(); m != "relay/cn:deepseek-v4.1-flash" {
		t.Fatalf("AutoVision = %q, want relay/cn:deepseek-v4.1-flash, the model the user said sees", m)
	}

	// the user's "takes no images" keeps it out again
	st = settings.Load()
	st.ModelImages = map[string]bool{"relay/cn:deepseek-v4.1-flash": false}
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if m := AutoVision(); m != "" {
		t.Fatalf("AutoVision = %q, a model the user said takes no images", m)
	}
}

// Only the user's "it sees" lifts the shape guess in the small models'
// pool (#4359): their "takes no images" says nothing of the ID, so a
// colon ID they marked so stays out of the searcher's pool too, and an ID
// of magpie's own shape (a group's, auto's) stays out whatever they said.
func TestSmallCandsLiftOnlyWhatTheUserSaidSees(t *testing.T) {
	fresh(t)
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "http://relay.invalid/v1", Key: "k",
		Models: []string{provider.GroupPrefix + "g", provider.AutoPrefix + "a", "cn:text-only", "cn:sees", "cn:unsaid"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	st := settings.Load()
	st.ModelImages = map[string]bool{
		"relay/" + provider.GroupPrefix + "g": true, "relay/" + provider.AutoPrefix + "a": true,
		"relay/cn:text-only": false, "relay/cn:sees": true,
	}
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range smallCands(p, nil) {
		ids = append(ids, c.id)
	}
	if len(ids) != 1 || ids[0] != "cn:sees" {
		t.Fatalf("pool %v, want only cn:sees", ids)
	}
}

// rankSmall's order is pickSmall's pick of every subset, as pickSmall
// was before the ranking: the first of a subset is its pick.
func TestRankSmallIsPickSmall(t *testing.T) {
	pickSmall := func(ms []catalog.Model) string {
		if big := slices.DeleteFunc(slices.Clone(ms), func(m catalog.Model) bool {
			v, _, ok := strings.Cut(m.ID, "/")
			return ok && !slices.Contains([]string{"anthropic", "openai", "google", "x-ai"}, v)
		}); len(big) > 0 {
			ms = big
		}
		cost := func(m catalog.Model) float64 {
			if m.Price == nil {
				return 1e9
			}
			return m.Price.Input + m.Price.Output
		}
		cheapest := func(ms []catalog.Model) string {
			if len(ms) == 0 {
				return ""
			}
			return slices.MinFunc(ms, func(a, b catalog.Model) int { return cmp.Compare(cost(a), cost(b)) }).ID
		}
		small := slices.DeleteFunc(slices.Clone(ms), func(m catalog.Model) bool {
			l := strings.ReplaceAll(strings.ToLower(m.ID), "gemini", "")
			return !strings.Contains(l, "haiku") && !strings.Contains(l, "mini") && !strings.Contains(l, "flash") && !strings.Contains(l, "luna")
		})
		if id := cheapest(small); id != "" {
			return id
		}
		return cheapest(ms)
	}
	r := rand.New(rand.NewPCG(1, 2))
	vendors := []string{"", "anthropic/", "openai/", "google/", "x-ai/", "meta/", "qwen/"}
	names := []string{"claude-haiku-5", "gpt-6-mini", "gemini-3-flash", "gemini-3-pro", "gpt-6-luna", "big", "o9", "llama"}
	for range 2000 {
		var ms []catalog.Model
		for i := range r.IntN(8) + 1 {
			m := catalog.Model{ID: fmt.Sprintf("%s%s-%d", vendors[r.IntN(len(vendors))], names[r.IntN(len(names))], i)}
			if r.IntN(4) > 0 {
				m.Price = &catalog.Price{Input: float64(r.IntN(3)), Output: float64(r.IntN(3))}
			}
			ms = append(ms, m)
		}
		cs := rankSmall(ms)
		if cs[0].id != pickSmall(ms) {
			t.Fatalf("rankSmall's first %q, pickSmall %q of %v", cs[0].id, pickSmall(ms), ms)
		}
		// a subset: those not spent
		keep := map[string]bool{}
		var sub []catalog.Model
		for _, m := range ms {
			if r.IntN(2) == 0 {
				keep[m.ID] = true
				sub = append(sub, m)
			}
		}
		first := ""
		for _, c := range cs {
			if keep[c.id] {
				first = c.id
				break
			}
		}
		if first != pickSmall(sub) {
			t.Fatalf("rankSmall's first of a subset %q, pickSmall %q of %v", first, pickSmall(sub), sub)
		}
	}
}
