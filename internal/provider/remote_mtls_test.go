package provider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/mtls"
)

// mtlsPeer is a magpie behind mTLS that answers what magpie asks of a
// Remote magpie besides its model calls: its cards, its quota history,
// a key's limit, its root and its model list's wait. It answers only a
// client that showed a certificate st issued, with the key.
func mtlsPeer(t *testing.T, st mtls.Store) *httptest.Server {
	t.Helper()
	cert, err := st.ServerCert([]string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := st.Pool()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "remote-key" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case RemoteCardsPath:
			json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []SubscriptionQuota{{Provider: "codex", Name: "Codex", Kind: "subscription", User: "a@x.com"}}})
		case "/v1/magpie/quotas/history":
			json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []QuotaHistory{{Provider: "codex"}}})
		case RemoteKeyLimitPath:
			io.WriteString(w, `{"limited":false,"key":"k"}`)
		case "/v1":
			io.WriteString(w, `{"name":"magpie","mcp_hub":true}`)
		case "/v1/models":
			w.Header().Set("ETag", `"l1"`)
			io.WriteString(w, `{"object":"list","data":[{"id":"gpt-6","object":"model"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// hhdaxia on X (0.1.4258): a Remote magpie behind mTLS, with the client
// certificate set on the provider, answered its model calls, but the
// Usage page's read of its cards went without the certificate and said
// the remote "isn't shared". Every request magpie makes of a Remote
// magpie goes through the provider's transport now: the cards, the
// quota history, a key's limit, the root (its MCP router, an update) and
// the model list's wait.
func TestRemoteMagpieOwnRequestsShowClientCert(t *testing.T) {
	azureHome(t)
	dialsOut(t)
	st := mtls.Store{Dir: t.TempDir()}
	srv := mtlsPeer(t, st)
	iss, err := st.Issue("office")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	dir, err := mtls.Export(iss, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, Key: "remote-key", Chat: srv.URL,
		ClientCert: dir + "/client.pem", ClientKey: dir + "/client-key.pem", ServerCA: dir + "/ca.pem"}); err != nil {
		t.Fatal(err)
	}
	ForgetRemoteCardsForTest()
	t.Cleanup(ForgetRemoteCardsForTest)
	p, err := Find("office")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := p.List(ctx); err != nil {
		t.Fatalf("model list (the path that always had it): %v", err)
	}
	cs := peerCards(RemoteCards(ctx))
	if len(cs) != 1 || cs[0].Error != "" || cs[0].Provider != "office/codex" {
		t.Errorf("cards: %+v", cs)
	}
	if hs := RemoteQuotaHistories(ctx, "7"); len(hs) != 1 {
		t.Errorf("quota history: %+v", hs)
	}
	if _, err := askRemoteKeyLimit(ctx, *p, p.Key); err != nil {
		t.Errorf("key limit: %v", err)
	}
	if _, err := remoteBody(ctx, *p, http.MethodGet, "/v1", RemoteHubWait); err != nil {
		t.Errorf("root: %v", err)
	}
	if tag, err := waitRemoteList(ctx, *p, ""); err != nil || tag != `"l1"` {
		t.Errorf("list wait: %q %v", tag, err)
	}
}

// A 403 is "isn't shared" only when the magpie there says so. A client
// certificate check's refusal — magpie's own, or a proxy's page in front
// of it — is said as it came, never as sharing to turn on.
func TestRemoteForbiddenSaysWhatRefused(t *testing.T) {
	for name, c := range map[string]struct {
		ctype, body string
		shared      bool
		has         string
	}{
		"not shared (quotas)": {"application/json", `{"error":{"message":"magpie's quotas are told to another machine only when magpie is shared on the local network (Settings → Network and sharing → Share on local network) and the request carries its API key"}}`, true, ""},
		"not shared (guard)":  {"text/plain", "magpie isn't shared on the local network\n", true, ""},
		"no client cert":      {"text/plain", "magpie asks other computers for a client certificate it issued (Settings → Network and sharing → Require a client certificate), and this request came without a good one\n", false, "client certificate"},
		"a proxy's page":      {"text/html", "<html><head><title>403 Forbidden</title></head><body><center><h1>403 Forbidden</h1></center><hr><center>nginx</center></body></html>", false, "client certificate"},
		"empty":               {"text/plain", "", false, "403"},
	} {
		t.Run(name, func(t *testing.T) {
			azureHome(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", c.ctype)
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, c.body)
			}))
			t.Cleanup(srv.Close)
			p := Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, Key: "k", Chat: srv.URL, Anthropic: srv.URL}
			for what, err := range map[string]error{
				"cards": func() error {
					_, err := remoteBody(context.Background(), p, http.MethodGet, RemoteCardsPath, remoteWait)
					return err
				}(),
				"key limit": func() error { _, err := askRemoteKeyLimit(context.Background(), p, "k"); return err }(),
			} {
				if err == nil {
					t.Fatalf("%s: no error", what)
				}
				if got := err.Error() == errRemoteNotShared; got != c.shared {
					t.Errorf("%s: %q, isn't shared = %v, want %v", what, err, got, c.shared)
				}
				if c.has != "" && !strings.Contains(err.Error(), c.has) {
					t.Errorf("%s: %q, want it to say %q", what, err, c.has)
				}
				if errors.Is(err, errKeyHeld) {
					t.Errorf("%s: held", what)
				}
			}
		})
	}
}
