package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// A plugin account's model list is asked through the account's own proxy,
// as a built-in account's is fetched through its: each of two accounts
// behind its own proxy is told the list its proxy carried.
func TestPluginModelListThroughAccountProxy(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	fresh(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	// each proxy answers the vendor's list with a model of its own
	proxyOf := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Host != "models.invalid" {
				http.Error(w, "not the list: "+r.URL.String(), 400)
				return
			}
			fmt.Fprint(w, name)
		}))
	}
	pa, pb := proxyOf("via-a"), proxyOf("via-b")
	defer pa.Close()
	defer pb.Close()
	t.Setenv("FAKE_BASE", "http://vendor.invalid/v1")
	t.Setenv("FAKE_MODELS", "http://models.invalid/list")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, team := range []string{"a", "b"} {
		st, err := provider.StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": team})
		if err != nil {
			t.Fatal(err)
		}
		if err := provider.SubmitSignInCallback(st.ID, "good"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("fakeco")
	if err != nil {
		t.Fatal(err)
	}
	p.AccountProxies = map[string]string{"a@fake": pa.URL, "b@fake": pb.URL}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	ps, err := plugin.Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, pp := range ps {
		if pp.ID == "fakeco" {
			for _, a := range pp.Accounts {
				got[a.AccountID] = a.Models
			}
		}
	}
	for who, want := range map[string]string{"a@fake": "fake-via-a", "b@fake": "fake-via-b"} {
		if !slices.Contains(got[who], want) {
			t.Fatalf("%s's models %v, want %s among them (all: %v)", who, got[who], want, got)
		}
	}
	if slices.Contains(got["a@fake"], "fake-via-b") || slices.Contains(got["b@fake"], "fake-via-a") {
		t.Fatalf("an account's list came through the other's proxy: %v", got)
	}
}
