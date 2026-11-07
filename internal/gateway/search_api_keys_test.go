package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A search API given several keys (#1477) goes on to the next when one is
// out of credit (Tavily's 432), and the next search doesn't start from the
// keys refused, which rest (search_pool_test.go). A failure that isn't about the key (a 500) doesn't spend
// the other keys.
func TestSearchAPIKeysTakeTurns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		asked = append(asked, key)
		mu.Unlock()
		switch key {
		case "tvly-spent":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(432)
			io.WriteString(w, `{"detail":{"error":"This request exceeds your plan's set usage limit. Please upgrade your plan or contact support@tavily.com"}}`)
		case "tvly-down":
			http.Error(w, `{"detail":{"error":"down"}}`, 500)
		case "tvly-good":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"query":"x","results":[{"title":"Go downloads","url":"https://go.dev/dl/","content":"go1.27.1","score":0.9}]}`)
		default:
			http.Error(w, `{"detail":{"error":"Unauthorized: missing or invalid API key."}}`, 401)
		}
	}))
	defer srv.Close()
	s := New()
	search := func(keys string) (string, error) {
		t.Helper()
		if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "tavily", Key: keys, URL: srv.URL}); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		asked = nil
		mu.Unlock()
		said, _, err := s.apiSearch(context.Background(), "latest go")
		return said, err
	}
	took := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(asked) }

	forgetSearchKeys()
	if said, err := search("tvly-spent, tvly-bad\ntvly-good"); err != nil || !strings.Contains(said, "go1.27.1") {
		t.Fatalf("spent, bad, good: %q, %v", said, err)
	}
	if got := took(); !slices.Equal(got, []string{"tvly-spent", "tvly-bad", "tvly-good"}) {
		t.Errorf("asked with %v, want each key in turn", got)
	}
	if _, err := search("tvly-spent,tvly-bad,tvly-good"); err != nil {
		t.Fatal(err)
	}
	if got := took(); !slices.Equal(got, []string{"tvly-good"}) {
		t.Errorf("the next search asked with %v, want the key that answered, the others resting", got)
	}

	forgetSearchKeys()
	if _, err := search("tvly-down,tvly-good"); err == nil {
		t.Error("a 500 on the first key went on to the next")
	}
	if got := took(); !slices.Equal(got, []string{"tvly-down"}) {
		t.Errorf("asked with %v after a 500, want the first key alone", got)
	}

	forgetSearchKeys()
	_, err := search("tvly-spent,tvly-bad")
	if err == nil || !strings.Contains(err.Error(), "key 1 (") || !strings.Contains(err.Error(), "usage limit") || !strings.Contains(err.Error(), "key 2 (") {
		t.Errorf("every key refused: %v", err)
	}

	if as := provider.StoredSearchAPIs(); len(as) != 1 || as[0].Key != "tvly-spent,tvly-bad" || as[0].MaskedKey() != "tvly…pent, ••••••••" {
		t.Errorf("stored %+v, shown %q", as, as[0].MaskedKey())
	}
}

// A key switched off in its list (辰林鸭~ on Discord) is never asked, and
// each key refused says why it rests, as the list shows it: Tavily's 433
// out of credit, a 429 rate limited for as long as its Retry-After, a 401
// turned away. The key that answered rests not at all.
func TestSearchKeyOffAndWhyItRests(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		asked = append(asked, key)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch key {
		case "tvly-limited":
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			io.WriteString(w, `{"detail":{"error":"Your request has been blocked due to excessive requests."}}`)
		case "tvly-paygo":
			w.WriteHeader(433)
			io.WriteString(w, `{"detail":{"error":"This request exceeds the pay-as-you-go limit."}}`)
		case "tvly-good":
			io.WriteString(w, `{"query":"x","results":[{"title":"Go","url":"https://go.dev/dl/","content":"go1.27.1","score":0.9}]}`)
		default:
			w.WriteHeader(401)
			io.WriteString(w, `{"detail":{"error":"Unauthorized: missing or invalid API key."}}`)
		}
	}))
	defer srv.Close()
	forgetSearchKeys()
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "tavily", Key: "tvly-off,tvly-limited,tvly-paygo,tvly-bad,tvly-good", URL: srv.URL, Pick: provider.SearchPickOrder}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SwitchSearchKey("tavily", provider.SearchKeyRef("tvly-off"), false); err != nil {
		t.Fatal(err)
	}
	said, _, err := New().apiSearch(context.Background(), "latest go")
	if err != nil || !strings.Contains(said, "go1.27.1") {
		t.Fatalf("%q, %v", said, err)
	}
	mu.Lock()
	got := slices.Clone(asked)
	mu.Unlock()
	if !slices.Equal(got, []string{"tvly-limited", "tvly-paygo", "tvly-bad", "tvly-good"}) {
		t.Fatalf("asked with %v", got)
	}
	for key, want := range map[string]string{"tvly-limited": "rate limited", "tvly-paygo": "out of credit", "tvly-bad": "turned away", "tvly-good": "", "tvly-off": ""} {
		why, until := SearchKeyRest("tavily", key)
		if why != want || (want == "") != until.IsZero() {
			t.Errorf("%s rests %q until %v, want %q", key, why, until, want)
		}
	}
	if _, until := SearchKeyRest("tavily", "tvly-limited"); time.Until(until) < 90*time.Second || time.Until(until) > 125*time.Second {
		t.Errorf("a 429 with Retry-After 120 rests until %v", until)
	}
}
