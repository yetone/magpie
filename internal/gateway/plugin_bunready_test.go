package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A plugin's provider whose Bun is still downloading, the download
// stalled, fails at once and says why, and a routing group goes on to its
// next member, as for any other member that can't answer (#6735): neither
// waits on the download.
func TestPluginWhoseBunIsNotReadyFailsOver(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)

	up := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"from plugin"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := provider.StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SubmitSignInCallback(st.ID, "good"); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	other := &keyed{}
	serveOn(t, "other", "ko", []string{"m"}, other)
	if err := provider.SaveGroup(provider.Group{Name: "Mixed", Members: []string{"fakeco/fake-1", "other/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}

	// now no Bun on hand: magpie's own is to be downloaded, from a release
	// that never answers
	stalled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stalled:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(stalled) })
	t.Cleanup(plugin.BunReleaseForTest(srv.URL, 300*time.Millisecond))
	t.Setenv("MAGPIE_BUN", "")
	plugin.Restart()

	s := New()
	ask := func(model string) (int, string) {
		t.Helper()
		type reply struct {
			code int
			body string
		}
		got := make(chan reply, 1)
		go func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hello"}]}`))
			s.Handler().ServeHTTP(rec, req)
			got <- reply{rec.Code, rec.Body.String()}
		}()
		select {
		case r := <-got:
			return r.code, r.body
		case <-time.After(20 * time.Second):
			close(stalled)
			stalled = make(chan struct{})
			t.Fatalf("%s still unanswered after 20s while Bun downloads", model)
			return 0, ""
		}
	}
	if code, body := ask("group/mixed"); code != 200 || !strings.Contains(body, "from ko") {
		t.Fatalf("the group with its plugin member's Bun not ready: %d %s", code, body)
	}
	if code, body := ask("fakeco/fake-1"); code < 500 || !strings.Contains(body, "Bun runtime not ready") {
		t.Fatalf("the plugin's model with its Bun not ready: %d %s", code, body)
	}
}
