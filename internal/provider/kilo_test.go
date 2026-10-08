package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// kiloList is the Kilo Gateway's /models as it answers (October 2026):
// OpenRouter's shape with isFree, an auto router priced at -1, a free
// model by its flag alone, ":free" ones, one that takes no tools and one
// that only draws.
const kiloList = `{"data":[
	{"id":"kilo-auto/efficient","name":"Auto Efficient","context_length":1000000,"pricing":{"prompt":"-1","completion":"-1"},"supported_parameters":["tools","reasoning"],"isFree":false},
	{"id":"kilo-auto/free","name":"Auto Free","context_length":256000,"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"],"isFree":true},
	{"id":"stealth/space-bunny-alpha","name":"Space Bunny Alpha","context_length":1000000,"top_provider":{"max_completion_tokens":65536},"architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools","reasoning"],"isFree":true},
	{"id":"qwen/qwen3.8-27b:free","name":"Qwen: Qwen3.8 27B (free)","context_length":262144,"supported_parameters":["tools"],"isFree":true},
	{"id":"nvidia/nemotron-3.5-content-safety:free","name":"NVIDIA: Nemotron 3.5 Content Safety (free)","context_length":128000,"supported_parameters":["max_tokens"],"isFree":true},
	{"id":"google/lyria-3-pro-preview","name":"Lyria","context_length":1048576,"architecture":{"output_modalities":["audio"]},"pricing":{"prompt":"0","completion":"0"},"isFree":false},
	{"id":"anthropic/claude-sonnet-5","name":"Anthropic: Claude Sonnet 5","context_length":1000000,"pricing":{"prompt":"0.000003","completion":"0.000015"},"supported_parameters":["tools"],"isFree":false}
]}`

// The Kilo Gateway serves its free models to anyone (by IP), and Kilo's
// clients list them from its /models, asked as the Kilo CLI: with no key
// the provider lists the free ones alone, asked with no Authorization;
// with a key every model, the free ones marked Free and priced at nothing
// (lml on Discord: the community plugin's free models from KiloCode).
func TestKiloModels(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var asked []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/openrouter/models" {
			http.NotFound(w, r)
			return
		}
		asked = append(asked, r)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(kiloList))
	}))
	defer srv.Close()
	p, err := FromPreset("kilo")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ready() {
		t.Fatal("a Kilo provider with no key isn't ready")
	}
	p.ID, p.Chat = "kilo-test", srv.URL+"/api/openrouter"
	ids := func(ms []catalog.Model) (all, free []string) {
		for _, m := range ms {
			all = append(all, m.ID)
			if m.Free {
				free = append(free, m.ID)
			}
		}
		return
	}

	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h := asked[0].Header
	for k, v := range map[string]string{"Authorization": "", "User-Agent": "Kilo-Code/" + KiloVersion,
		"X-KILOCODE-EDITORNAME": "Kilo CLI " + KiloVersion, "HTTP-Referer": "https://kilocode.ai", "X-Title": "Kilo Code"} {
		if h.Get(k) != v {
			t.Errorf("list asked with %s %q, want %q", k, h.Get(k), v)
		}
	}
	all, free := ids(ms)
	want := []string{"kilo-auto/free", "stealth/space-bunny-alpha", "qwen/qwen3.8-27b:free"}
	if !slices.Equal(all, want) || !slices.Equal(free, want) {
		t.Fatalf("no key: %v (free %v)", all, free)
	}
	if m := ms[1]; m.Context != 1000000 || m.Output != 65536 || !m.Images || !m.Reasoning || m.Name != "Space Bunny Alpha" {
		t.Fatalf("model: %+v", m)
	}
	// a free model by its flag alone costs nothing, not its maker's price
	if pr, ok := p.ListPrice("stealth/space-bunny-alpha"); !ok || !pr.Same(catalog.Price{}) {
		t.Fatalf("free price: %+v %v", pr, ok)
	}

	p.Key = "kilo_jwt"
	ms, err = p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := asked[1].Header.Get("Authorization"); got != "Bearer kilo_jwt" {
		t.Fatalf("with a key, asked with %q", got)
	}
	all, free = ids(ms)
	if !slices.Equal(all, []string{"kilo-auto/efficient", "kilo-auto/free", "stealth/space-bunny-alpha", "qwen/qwen3.8-27b:free", "anthropic/claude-sonnet-5"}) || !slices.Equal(free, want) {
		t.Fatalf("with a key: %v (free %v)", all, free)
	}

	// another provider's ":free" model isn't Kilo's to price
	o, _ := FromPreset("together")
	if o.kiloFreeModel("qwen/qwen3.8-27b:free") {
		t.Fatal("kiloFreeModel on another provider")
	}
}

// A page of the gateway's list is not the list: the gateway's shape is
// OpenRouter's, which pages with has_more and last_id, and a reply read as
// the whole one drops every model of a later page from the user's picks
// (#904). The fetch says so instead, and the picks are left alone.
func TestKiloModelsRefuseAPageOfTheList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var paged atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/openrouter/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if paged.Load() {
			w.Write([]byte(`{"data":[` +
				`{"id":"kilo-auto/free","name":"Auto Free","context_length":256000,"supported_parameters":["tools"],"isFree":true}` +
				`],"has_more":true,"first_id":"kilo-auto/free","last_id":"kilo-auto/free"}`))
			return
		}
		w.Write([]byte(kiloList))
	}))
	defer srv.Close()
	p, err := FromPreset("kilo")
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Chat, p.Key = "kilo-page", srv.URL+"/api/openrouter", "kilo_jwt"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	q, _ := Find("kilo-page")
	if _, err := q.Fetch(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	q, _ = Find("kilo-page")
	q.Models = []string{"kilo-auto/free", "stealth/space-bunny-alpha"}
	if err := Save(*q); err != nil {
		t.Fatal(err)
	}

	// the second list leaves out a model the first one had, because it is
	// a page: the fetch says so and takes nothing away
	paged.Store(true)
	if _, dropped, err := q.Refetch(context.Background()); err == nil {
		t.Fatal("a page of the list was read as the whole list")
	} else if len(dropped) != 0 {
		t.Errorf("dropped %v, want none", dropped)
	}
	q, _ = Find("kilo-page")
	if !slices.Equal(q.Models, []string{"kilo-auto/free", "stealth/space-bunny-alpha"}) {
		t.Errorf("picks: %v, want the two that were picked", q.Models)
	}
}

// The gateway's shape is OpenRouter's, which pages with has_more and
// last_id, so a list that comes twenty at a time is followed to the end of
// it and not refused: the models of the page after are the gateway's as
// much as the first twenty's, and the picks made of any of them keep.
func TestKiloModelsFollowThePagesOfTheList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var mu sync.Mutex
	var after []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/openrouter/models" {
			http.NotFound(w, r)
			return
		}
		a := r.URL.Query().Get("after_id")
		mu.Lock()
		after = append(after, a)
		mu.Unlock()
		start := 0
		if a != "" {
			n, err := strconv.Atoi(strings.TrimPrefix(a, "vendor/model-"))
			if err != nil {
				http.Error(w, "no such id: "+a, http.StatusBadRequest)
				return
			}
			start = n // after vendor/model-20 the list goes on with 21
		}
		var b strings.Builder
		b.WriteString(`{"data":[`)
		for i := start + 1; i <= min(start+20, 25); i++ {
			if i > start+1 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"id":"vendor/model-%d","name":"Model %d","context_length":1000000,"supported_parameters":["tools"]}`, i, i)
		}
		more := start+20 < 25
		last := ""
		if more {
			last = fmt.Sprintf("vendor/model-%d", start+20)
		}
		fmt.Fprintf(&b, `],"has_more":%t,"first_id":"vendor/model-%d","last_id":%q}`, more, start+1, last)
		w.Write([]byte(b.String()))
	}))
	defer srv.Close()
	p, err := FromPreset("kilo")
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Chat, p.Key = "kilo-pages", srv.URL+"/api/openrouter", "kilo_jwt"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	q, _ := Find("kilo-pages")
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch of a paged list: %v", err)
	}
	all := idsOf(ms)
	if len(all) != 25 {
		t.Fatalf("listed %d models, want all 25: %v", len(all), all)
	}
	if all[24] != "vendor/model-25" {
		t.Errorf("last model %q, want the one on the second page", all[24])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(after) != 2 || after[0] != "" || after[1] != "vendor/model-20" {
		t.Errorf("asked %q, want the first page and then the one after vendor/model-20", after)
	}
}

// The CLI's headers: the session as its task, and no Authorization at all
// when there is no key (a placeholder bearer is refused).
func TestKiloClient(t *testing.T) {
	h := http.Header{"Authorization": {"Bearer "}}
	KiloClient(h, "", "conversation-1")
	id := h.Get("X-KILOCODE-TASKID")
	if !openCodeIDRe.MatchString(id) || id[:4] != "ses_" || h.Get("x-session-affinity") != id || h.Get("X-Session-Id") != id ||
		h.Get("x-kilocode-mode") != "code" || len(h.Values("Authorization")) != 0 {
		t.Fatalf("headers: %v", h)
	}
	h2 := http.Header{}
	KiloClient(h2, "k", "conversation-1")
	if h2.Get("X-KILOCODE-TASKID") != id || h2.Get("Authorization") != "" {
		t.Fatalf("with a key: %v", h2)
	}
}
