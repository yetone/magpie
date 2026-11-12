package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// tmr11235 on X: "Test on account…" posted the pick as `account`, but the
// test action never read it, so the test went with routing's pick and the
// result named another key than the one chosen. The vendor here answers
// only sk-b: a test pinned to it is answered and says "second", and one
// pinned to sk-a is turned away.
func TestProviderTestIsPinnedToTheAccountPosted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-b" {
			rw.WriteHeader(http.StatusServiceUnavailable)
			_, _ = rw.Write([]byte(`{"error":{"message":"wrong key"}}`))
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Icon: "generic",
		Key: "sk-a", KeyName: "first", KeyProtocol: provider.Chat, Models: []string{"m"},
		Keys: []provider.KeyAccount{{Name: "second", Key: "sk-b", Protocol: provider.Chat}}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	ask := func(account string) provider.Result {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"id": "relay", "test": []string{"m"}, "account": account})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/test", strings.NewReader(string(body))))
		var out struct{ Results []provider.Result }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Results) != 1 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return out.Results[0]
	}
	p, _ := provider.Find("relay")
	picks := p.TestPicks()
	if len(picks) != 2 {
		t.Fatalf("picks %+v", picks)
	}
	if r := ask(picks[1].ID); !r.OK || r.Account != "second" {
		t.Fatalf("pinned to the second key: %+v", r)
	}
	if r := ask(picks[0].ID); r.OK || r.Account != "first" {
		t.Fatalf("pinned to the first key: %+v", r)
	}
}
