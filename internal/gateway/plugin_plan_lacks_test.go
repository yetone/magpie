package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// A plugin's account whose plan lacks the model is tried after the ones
// that list it, as a Codex account's is (a Free one behind a Plus): the
// plugin's first account, told only fake-1, isn't sent fake-resp.
func TestPluginAccountLackingTheModelGoesLast(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_RESPONSES", "1")
	t.Cleanup(plugin.Settle)
	var mu sync.Mutex
	var keys []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		keys = append(keys, r.Header.Get("Authorization"))
		mu.Unlock()
		done := `{"id":"r1","object":"response","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}],"usage":{"input_tokens":5,"output_tokens":1}}`
		if !strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, done)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`event: response.completed`+"\n"+`data: {"type":"response.completed","response":`+done+`}`))
	}))
	defer up.Close()
	t.Setenv("FAKE_BASE", up.URL+"/v1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	// "few" is told only fake-1
	for i, key := range []string{"few", "full"} {
		account := ""
		if i > 0 {
			account = plugin.NewAccount
		}
		if _, err := plugin.APIKey(ctx, "fakeco", 0, nil, key, account); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("fakeco")
	if err != nil || len(p.AlsoOn()) != 1 {
		t.Fatalf("Find(fakeco) = %+v, %v", p, err)
	}
	if p.Account.Lists("fake-resp") || !p.Account.Lists("fake-1") || !p.AlsoOn()[0].Account.Lists("fake-resp") {
		t.Fatal("the accounts' lists aren't what the plugin told")
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses",
		strings.NewReader(`{"model":"fakeco/fake-resp","stream":false,"input":"hi"}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 1 || keys[0] != "Bearer full" {
		t.Fatalf("the vendor was asked with %v", keys)
	}
}
