package gateway

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A Remote magpie whose computer came back at another address
// (luckymanjc on Discord: A's router gives it a new IP each boot): the
// request that finds no one at the old one is answered from the address
// that lists for the provider's key, and the provider is saved there.
func TestRemoteMagpieMovedIsFound(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"sol","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"sol"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	// the key B holds is one of A's gateway keys, which A proves it holds
	_, secrets := newCaller(t, "B")
	h := New().Handler()
	remote := httptest.NewServer(h)
	t.Cleanup(remote.Close)
	at := strings.TrimPrefix(remote.URL, "http://")
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: secrets[0], Chat: at})
	if err != nil {
		t.Fatal(err)
	}
	office, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A's old address now answers nobody (nothing listens on port 1)
	office, _ = provider.Find(id)
	office.Chat, office.Responses, office.Anthropic, office.Decide = "127.0.0.1:1", "", "", ""
	if err := provider.Save(*office); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.SeekHere([]string{"127.0.0.1:1", at}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"office/fake/sol","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	office, _ = provider.Find(id)
	if office.Chat != remote.URL+"/v1" {
		t.Errorf("saved at %q, want %s/v1", office.Chat, remote.URL)
	}
}

// A magpie proves it holds a gateway key to another computer that asks
// with no key (provider.RemoteProvePath), while it is shared: the HMAC of
// the asker's nonce and the address it was reached at, under the key the
// id names. A key it hasn't, or has turned off, gets 404; a web page and
// any ask while it isn't shared get nothing; the key never goes back.
func TestRemoteProveAnswersOnlyTheKeysHolder(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	resetAuthThrottle()
	t.Cleanup(resetAuthThrottle)
	keys, secrets := newCaller(t, "B")
	h := lanGuard(New().Handler())
	const nonce = "00112233445566778899aabbccddeeff"
	here := &net.TCPAddr{IP: net.ParseIP("192.168.1.2"), Port: 3425}
	ask := func(keyID string, hdr ...string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"nonce": nonce, "key_id": keyID})
		r := httptest.NewRequest("POST", provider.RemoteProvePath, strings.NewReader(string(body)))
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, net.Addr(here)))
		r.RemoteAddr, r.Host = fromThere, "192.168.1.2:3425"
		r.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := ask(provider.RemoteKeyID(secrets[0]))
	if w.Code != http.StatusOK {
		t.Fatalf("the key's holder: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), secrets[0]) {
		t.Fatal("the key went back")
	}
	var out struct {
		Proof string `json:"proof"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if want := hex.EncodeToString(provider.RemoteProof(secrets[0], nonce, "192.168.1.2:3425")); out.Proof != want {
		t.Errorf("proof %q, want %q", out.Proof, want)
	}
	if w := ask(provider.RemoteKeyID("sk-magpie-key-not-here")); w.Code != http.StatusNotFound {
		t.Errorf("a key it hasn't: %d %s", w.Code, w.Body)
	}
	for _, hdr := range [][]string{{"Origin", "http://192.168.1.2:3425"}, {"User-Agent", "Mozilla/5.0"}, {"Sec-Fetch-Mode", "cors"}} {
		if w := ask(provider.RemoteKeyID(secrets[0]), hdr...); w.Code == http.StatusOK {
			t.Errorf("a browser (%s): %d", hdr[0], w.Code)
		}
	}
	if _, err := access.Update("off-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if w := ask(provider.RemoteKeyID(secrets[0])); w.Code != http.StatusNotFound {
		t.Errorf("a key turned off: %d %s", w.Code, w.Body)
	}
	if _, err := access.Update("on-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.LAN = false
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if w := ask(provider.RemoteKeyID(secrets[0])); w.Code != http.StatusForbidden {
		t.Errorf("not shared: %d %s", w.Code, w.Body)
	}
}
