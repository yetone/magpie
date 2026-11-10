package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// TestTinyFishSearchAPI: TinyFish's Search API (Niapyaya on X) is asked
// as its reference says (docs.tinyfish.ai/search-api/reference): a GET
// at its root with the query and the key in X-API-Key. Its pages are its
// docs' example reply; its 401 is the real service's reply to a made-up
// key, its 402 its docs' Search example; each is a refusal the next key
// or API gets past, the key never in the error.
func TestTinyFishSearchAPI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ok, err := os.ReadFile("testdata/tinyfish/search.json")
	if err != nil {
		t.Fatal(err)
	}
	badKey, err := os.ReadFile("testdata/tinyfish/invalid_key_401.json")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var asked []string
	status, reply := 200, ok
	f := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path+" "+r.URL.RawQuery+" key="+r.Header.Get("X-API-Key")+" auth="+r.Header.Get("Authorization")+" body="+string(b))
		st, rep := status, reply
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		w.Write(rep)
	}))
	t.Cleanup(f.Close)
	s := New()
	const key = "sk-tinyfish-0123456789abcdef"
	a := provider.SearchAPI{Vendor: "tinyfish", Key: key, URL: f.URL}

	pages, err := s.askSearchAPI(context.Background(), a, "web automation tools")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].URL != "https://tinyfish.ai" || pages[0].Title != "TinyFish — AI Web Automation Platform" ||
		pages[0].Text != "Automate any website with natural language instructions..." ||
		pages[1].URL != "https://github.com/example/web-automation-tools" ||
		pages[1].Text != "A curated list of browser automation frameworks..." {
		t.Fatalf("pages %+v", pages)
	}
	if want := "GET / query=web+automation+tools key=" + key + " auth= body="; asked[0] != want {
		t.Fatalf("asked %q, want %q", asked[0], want)
	}

	// a news page carries its date
	mu.Lock()
	reply = []byte(`{"query":"q","results":[{"position":1,"site_name":"go.dev","title":"Go 1.27.1 is released","snippet":"Go 1.27.1 is out.","url":"https://go.dev/blog/go1.27.1","date":"2026-10-01","publisher":"Go"},{"position":2,"title":"no url"}],"total_results":2,"page":0}`)
	mu.Unlock()
	pages, err = s.askSearchAPI(context.Background(), a, "q")
	if err != nil || len(pages) != 1 || pages[0].Age != "2026-10-01" || pages[0].Text != "Go 1.27.1 is out." {
		t.Fatalf("pages %+v, %v", pages, err)
	}

	for _, c := range []struct {
		status int
		body   string
		said   string
		why    string
	}{
		{401, string(badKey), "Invalid or expired API key", "turned away"},
		{402, `{"error":{"code":"INSUFFICIENT_CREDITS","message":"Daily free Search allowance used and your wallet balance is empty. Add funds at https://agent.tinyfish.ai/wallet?utm_source=api&utm_medium=insufficient_funds&utm_campaign=search"}}`, "Daily free Search allowance used", "out of credit"},
		{429, `{"error":{"code":"RATE_LIMIT_EXCEEDED","message":"Rate limit exceeded ` + key + `"}}`, "Rate limit exceeded", "rate limited"},
	} {
		mu.Lock()
		status, reply = c.status, []byte(c.body)
		mu.Unlock()
		_, err := s.askSearchAPI(context.Background(), a, "q")
		var r searchRefused
		if !errors.As(err, &r) || r.status != c.status || !strings.Contains(err.Error(), c.said) || strings.Contains(err.Error(), key) {
			t.Fatalf("%d: %#v", c.status, err)
		}
		if _, why := searchRestFor("tinyfish", c.status, "", time.Now()); why != c.why {
			t.Errorf("%d rests as %q", c.status, why)
		}
	}
	if v, ok := provider.SearchVendorOf("tinyfish"); !ok || v.Base != "https://api.search.tinyfish.ai" || v.KeyOptional ||
		v.KeysURL != "https://agent.tinyfish.ai/api-keys" || v.Name != "TinyFish" {
		t.Errorf("vendor %+v", v)
	}
	// its own address: the query at the root
	if got := (provider.SearchAPI{Vendor: "tinyfish", Key: key}).Base(); got != "https://api.search.tinyfish.ai" {
		t.Errorf("base %q", got)
	}
}
