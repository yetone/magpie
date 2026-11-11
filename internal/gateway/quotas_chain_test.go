package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// yuyong_id on Discord: magpie1 holds a Codex (ChatGPT) sign-in, magpie2
// uses magpie1 as a Remote magpie, and magpie3 uses magpie2. magpie2
// showed magpie1's Codex card; magpie3 showed none of it, as magpie2
// told another magpie only the cards of its own accounts. Every kind of
// card magpie1 tells comes through to magpie3 now, named after both
// magpies; a refresh pressed at magpie3 reaches magpie1; a card that
// comes round to a magpie it went through is left out.
func TestRemoteChainPassesOnCards(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	// magpie1, as its gateway answers (CachedCards): its own cards, each
	// with the way it came by, magpie1 alone
	var mu sync.Mutex
	var refreshes []string
	m1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-magpie-m1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET " + provider.RemoteCardsPath:
		case "POST " + provider.RemoteRefreshPath:
			mu.Lock()
			refreshes = append(refreshes, r.URL.Query().Get("provider")+"|"+r.URL.Query().Get("user"))
			mu.Unlock()
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"object":"list","data":[
{"provider":"codex","name":"Codex","icon":"codex-color","plan":"plus","user":"a@x.com","kind":"subscription","via":["m1"],
 "windows":[{"name":"5h","used":37,"resetSecs":3600},{"name":"Week","used":12,"resetSecs":86400}],"resets":{"count":1}},
{"provider":"antigravity","name":"Antigravity","icon":"antigravity","user":"g@x.com","kind":"subscription","via":["m1"],
 "windows":[{"name":"Gemini","used":5}]},
{"provider":"zhipu","name":"GLM Coding","kind":"plan","via":["m1"],"windows":[{"name":"5h","used":10}]},
{"provider":"deepseek","name":"DeepSeek","kind":"balance","balance":"¥12.00","via":["m1"],"windows":[]}]}`)
	}))
	defer m1.Close()

	// magpie2: this process's gateway, magpie1 its Remote magpie
	gw := httptest.NewServer(lanGuard(New().Handler()))
	defer gw.Close()
	if err := provider.Save(provider.Provider{ID: "m1", Name: "M1", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-m1", Chat: m1.URL}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// magpie2's Usage page reads magpie1's cards, as reported
	var m2Codex bool
	for _, q := range provider.RemoteCards(ctx) {
		m2Codex = m2Codex || q.Provider == "m1/codex" && q.User == "a@x.com"
	}
	if !m2Codex {
		t.Fatal("magpie2 doesn't show magpie1's Codex card")
	}
	m2Self := catalog.Self()
	get := func(path string) []byte {
		t.Helper()
		res, err := http.Get(gw.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, res.StatusCode, b)
		}
		return b
	}
	// what magpie2 tells magpie3, through its real handler
	told := get(provider.RemoteCardsPath)

	// magpie3: another magpie, with magpie2 as its Remote magpie, told
	// what magpie2's gateway told
	defer catalog.SetSelfForTest("m3")()
	m2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method+" "+r.URL.Path != "GET "+provider.RemoteCardsPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(told)
	}))
	defer m2.Close()
	// one process plays both magpies; magpie3 knows magpie2 alone, as the
	// same account by two paths would be shown once
	m1Saved := provider.Provider{ID: "m1", Name: "M1", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-m1", Chat: m1.URL}
	if err := provider.Delete("m1"); err != nil {
		t.Fatal(err)
	}
	provider.ForgetRemoteCardsForTest()
	if err := provider.Save(provider.Provider{ID: "m2", Name: "M2", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-m2", Chat: m2.URL}); err != nil {
		t.Fatal(err)
	}
	byID := map[string]provider.SubscriptionQuota{}
	for _, q := range remoteOnly(provider.RemoteCards(ctx)) {
		byID[q.Provider] = q
	}
	c, ok := byID["m2/m1/codex"]
	if !ok || c.Name != "Codex · M1 · M2" || c.From != "M2" || c.User != "a@x.com" || c.Kind != "subscription" ||
		len(c.Windows) != 2 || c.Windows[0].Used != 37 || c.Windows[1].Used != 12 {
		t.Fatalf("magpie1's Codex card at magpie3: %+v (all: %+v)", c, byID)
	}
	// a reset is spent on the magpie next door only (UseRemoteCodexReset)
	if c.Resets != nil {
		t.Errorf("a reset two magpies away is offered: %+v", c.Resets)
	}
	if !slices.Equal(c.Via, []string{"m1", m2Self}) {
		t.Errorf("the Codex card came by %v, want [m1 %s]", c.Via, m2Self)
	}
	// every kind passes on alike
	for id, kind := range map[string]string{"m2/m1/antigravity": "subscription", "m2/m1/zhipu": "plan", "m2/m1/deepseek": "balance"} {
		if q, ok := byID[id]; !ok || q.Kind != kind {
			t.Errorf("%s at magpie3: %+v", id, q)
		}
	}
	if q := byID["m2/m1/deepseek"]; q.Balance != "¥12.00" {
		t.Errorf("magpie1's DeepSeek balance at magpie3: %+v", q)
	}
	// magpie2's own stand-in for magpie1, and its key's limit there, are
	// its own business with magpie1
	var cards []provider.SubscriptionQuota
	json.Unmarshal(told, &struct {
		Data *[]provider.SubscriptionQuota `json:"data"`
	}{&cards})
	for _, q := range cards {
		if q.Provider == "m1" || q.GatewayKey != nil {
			t.Errorf("magpie2 told magpie3 its own card of magpie1: %+v", q)
		}
	}

	if err := provider.Delete("m2"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(m1Saved); err != nil {
		t.Fatal(err)
	}
	// a refresh pressed at magpie3 is read again at magpie1, by magpie2
	res, err := http.Post(gw.URL+provider.RemoteRefreshPath+"?provider=m1/codex&user=a@x.com", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	mu.Lock()
	got := slices.Clone(refreshes)
	mu.Unlock()
	if res.StatusCode != 200 || !slices.Equal(got, []string{"codex|a@x.com"}) {
		t.Errorf("magpie3's refresh of m1/codex: %d, magpie1 asked %v", res.StatusCode, got)
	}

	// a card that went through this magpie before is left out: magpie3
	// told a card that came m3 → m2 shows nothing of it
	told = []byte(`{"object":"list","data":[{"provider":"m2/codex","name":"Codex · M2","from":"M2","kind":"subscription","user":"loop@x.com","via":["m3","m2x"],"windows":[]}]}`)
	provider.ForgetRemoteCardsForTest()
	for _, q := range provider.RemoteCards(ctx) {
		if q.User == "loop@x.com" {
			t.Errorf("a card that came back round: %+v", q)
		}
	}
}

// magpie2's status line (GET /v1/magpie/quotas?kept=1, KeptQuotaReport)
// is told its Remote magpie's cards as last read, as its Usage page shows
// them: a magpie whose Codex is another's had none on its status line.
func TestKeptReportHasRemoteCards(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	m1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"list","data":[{"provider":"codex","name":"Codex","user":"a@x.com","kind":"subscription","via":["m1"],"windows":[{"name":"5h","used":37}]}]}`)
	}))
	defer m1.Close()
	if err := provider.Save(provider.Provider{ID: "m1", Name: "M1", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-m1", Chat: m1.URL}); err != nil {
		t.Fatal(err)
	}
	provider.RemoteCards(context.Background())
	r := httptest.NewRequest("GET", "/v1/magpie/quotas?kept=1", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	lanGuard(New().Handler()).ServeHTTP(w, r)
	var got struct {
		Data []provider.Quota `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	for _, q := range got.Data {
		if q.Provider == "m1/codex" && q.From == "M1" && len(q.Windows) == 1 && q.Windows[0].Used == 37 {
			return
		}
	}
	t.Fatalf("kept report: %d %s", w.Code, w.Body)
}
