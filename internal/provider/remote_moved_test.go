package provider

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// a magpie on 127.0.0.1 that proves it holds key and lists for key
// alone, its list made by lister
func movedMagpie(t *testing.T, key, lister string, hits *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if r.URL.Path == RemoteProvePath {
			answerProof(w, r, key)
			return
		}
		if r.Header.Get("x-api-key") != key {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"send an enabled gateway key"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"own/m1","object":"model","magpie_via":["`+lister+`"]}]}`)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// answerProof answers a proof's ask as a magpie holding key does, at the
// address the connection came to.
func answerProof(w http.ResponseWriter, r *http.Request, key string) {
	var in struct {
		Nonce string `json:"nonce"`
		KeyID string `json:"key_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.KeyID != RemoteKeyID(key) {
		http.NotFound(w, r)
		return
	}
	at := r.Context().Value(http.LocalAddrContextKey).(net.Addr).String()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"proof": hex.EncodeToString(RemoteProof(key, in.Nonce, at))})
}

// heard is every request an impostor was sent, its headers as they came.
type heard struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (h *heard) all() []*http.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.reqs)
}

// impostor is anything on the network that takes the connection at the
// port and lists 200 for any request, key or none; its proof is what
// prove writes (none: no such route).
func impostor(t *testing.T, prove func(w http.ResponseWriter, r *http.Request)) (string, *heard) {
	t.Helper()
	got := &heard{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.mu.Lock()
		got.reqs = append(got.reqs, r.Clone(context.Background()))
		got.mu.Unlock()
		if r.URL.Path == RemoteProvePath && prove != nil {
			prove(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), got
}

// keyHeard: one of reqs carried key or the provider's own header anywhere
// in its headers or its address.
func keyHeard(reqs []*http.Request, key, header string) bool {
	for _, r := range reqs {
		if strings.Contains(r.URL.String(), key) || r.Header.Get(header) != "" {
			return true
		}
		for _, vs := range r.Header {
			for _, v := range vs {
				if strings.Contains(v, key) {
					return true
				}
			}
		}
	}
	return false
}

// nobody listens on port 1
const movedAway = "127.0.0.1:1"

func seekHere(t *testing.T, ats ...string) *atomic.Int32 {
	t.Helper()
	asked := new(atomic.Int32)
	t.Cleanup(SeekHere(nil))
	seekMu.Lock()
	seekAt = func(host, port string) []string { asked.Add(1); return ats }
	seekMu.Unlock()
	return asked
}

// A Remote magpie whose computer came back at another address
// (luckymanjc): its list, finding no one at the old one, is asked at the
// address that lists for its key and isn't this magpie, and the
// provider is saved there. A magpie that refuses the key, and this
// magpie itself, are passed over.
func TestRemoteMagpieFoundMoved(t *testing.T) {
	isolate(t)
	isolateFetchHome(t)
	other := movedMagpie(t, "someone-else", "c", nil)
	self := movedMagpie(t, "secret", catalog.Self(), nil)
	moved := movedMagpie(t, "secret", "a", nil)
	seekHere(t, movedAway, other, self, moved)
	id, err := Add(Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, Key: "secret", Chat: movedAway})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := Find(id)
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ms) != 1 || ms[0].ID != "own/m1" {
		t.Errorf("listed %v, want own/m1", ms)
	}
	p, _ = Find(id)
	if p.Chat != "http://"+moved+"/v1" || p.Anthropic != "http://"+moved {
		t.Errorf("saved at %q / %q, want %s", p.Chat, p.Anthropic, moved)
	}
}

// With no magpie of its key anywhere, nothing is saved and the list's
// error stands; a look is made once a minute at most.
func TestRemoteMagpieNotFoundStays(t *testing.T) {
	isolate(t)
	isolateFetchHome(t)
	other := movedMagpie(t, "someone-else", "c", nil)
	asked := seekHere(t, movedAway, other)
	id, err := Add(Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, Key: "secret", Chat: movedAway})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := Find(id)
	if _, err := p.Fetch(context.Background()); err == nil || !unconnected.MatchString(err.Error()) {
		t.Fatalf("fetch: %v, want the dial's error", err)
	}
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("second fetch answered")
	}
	if n := asked.Load(); n != 1 {
		t.Errorf("looked %d times in a minute, want 1", n)
	}
	if q, _ := Find(id); q.Chat != "http://"+movedAway+"/v1" {
		t.Errorf("saved at %q", q.Chat)
	}
}

// A remote that answers, however badly, is where it was: nobody looks.
func TestRemoteMagpieAnsweringIsNotLookedFor(t *testing.T) {
	isolate(t)
	isolateFetchHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	asked := seekHere(t, movedMagpie(t, "secret", "a", nil))
	id, err := Add(Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, Key: "secret", Chat: strings.TrimPrefix(srv.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := Find(id)
	p.Fetch(context.Background())
	if n := asked.Load(); n != 0 {
		t.Errorf("looked %d times for a remote that answered", n)
	}
}

// The addresses looked at: the old one's /24 at its port, itself left
// out; none for a name or a public address.
func TestLANNeighbours(t *testing.T) {
	got := lanNeighbours("192.168.77.20", "3425")
	for _, want := range []string{"192.168.77.1:3425", "192.168.77.21:3425", "192.168.77.254:3425"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, not := range []string{"192.168.77.20:3425", "192.168.77.0:3425", "192.168.77.255:3425"} {
		if slices.Contains(got, not) {
			t.Errorf("has %s", not)
		}
	}
	if len(got) > seekNets*254 {
		t.Errorf("%d addresses", len(got))
	}
	for _, host := range []string{"a-pc.local", "8.8.8.8", "100.101.102.103", "::1"} {
		if got := lanNeighbours(host, "3425"); got != nil {
			t.Errorf("%s: looked at %d addresses", host, len(got))
		}
	}
}

// The dial keeps what takes the connection.
func TestDialable(t *testing.T) {
	up := movedMagpie(t, "k", "a", nil)
	got := dialable(context.Background(), []string{movedAway, up})
	if !slices.Equal(got, []string{up}) {
		t.Errorf("dialable = %v, want [%s]", got, up)
	}
}

// Whatever takes the connection on the network B is on (a café's, a
// hotel's, or A switched off) is sent neither the provider's key nor its
// own headers before it proves it holds the key, and isn't saved: one
// that lists 200 for anything and has no proof, one whose proof is made
// under another key, and one that passes B's nonce on to the real magpie
// elsewhere and hands back that magpie's proof. Only the real magpie,
// which proves it, is then asked with the key, and saved.
func TestRemoteMagpieKeyGoesToNoNeighbour(t *testing.T) {
	isolate(t)
	isolateFetchHome(t)
	const key, header = "sk-magpie-key-secret-of-a", "X-Office-Proxy"
	real := movedMagpie(t, key, "a", nil)
	silent, silentHeard := impostor(t, nil)
	// a proof made the right way under a key of its own
	wrong, wrongHeard := impostor(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Nonce string `json:"nonce"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		at := r.Context().Value(http.LocalAddrContextKey).(net.Addr).String()
		io.WriteString(w, `{"proof":"`+hex.EncodeToString(RemoteProof("sk-magpie-key-a-guess", in.Nonce, at))+`"}`)
	})
	garbled, garbledHeard := impostor(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"proof":"`+strings.Repeat("ab", 32)+`"}`)
	})
	// a relay: B's ask, nonce and key id as they came, put to the real
	// magpie, and its answer handed back
	relay, relayHeard := impostor(t, func(w http.ResponseWriter, r *http.Request) {
		res, err := http.Post("http://"+real+RemoteProvePath, "application/json", r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
	})
	asked := seekHere(t, movedAway, silent, wrong, garbled, relay)
	id, err := Add(Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, Key: key, Chat: movedAway, Headers: map[string]string{header: "office-proxy-token"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := Find(id)
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("fetch answered with no magpie of the key about")
	}
	if asked.Load() != 1 {
		t.Fatalf("looked %d times", asked.Load())
	}
	for name, h := range map[string]*heard{"silent": silentHeard, "wrong": wrongHeard, "garbled": garbledHeard, "relay": relayHeard} {
		reqs := h.all()
		if len(reqs) == 0 {
			t.Errorf("%s: asked nothing (the proof was never asked)", name)
		}
		if keyHeard(reqs, key, header) {
			t.Errorf("%s: was sent the key or the provider's header", name)
		}
		for _, r := range reqs {
			if r.URL.Path != RemoteProvePath {
				t.Errorf("%s: asked %s %s before a proof", name, r.Method, r.URL.Path)
			}
		}
	}
	if q, _ := Find(id); q.Chat != "http://"+movedAway+"/v1" {
		t.Errorf("saved at %q, want the old address", q.Chat)
	}

	// the real magpie, after them all, proves it and is saved
	seekHere(t, movedAway, silent, wrong, garbled, relay, real)
	p, _ = Find(id)
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if q, _ := Find(id); q.Chat != "http://"+real+"/v1" {
		t.Errorf("saved at %q, want %s", q.Chat, real)
	}
}

// The proof: only key's holder, reached at the address asked, makes it.
func TestRemoteProof(t *testing.T) {
	a := RemoteProof("k1", "00ff", "10.0.0.5:3425")
	if !hmac.Equal(a, RemoteProof("k1", "00ff", "[::ffff:10.0.0.5]:3425")) {
		t.Error("an IPv4 address mapped into IPv6 is another address")
	}
	for _, b := range [][]byte{RemoteProof("k2", "00ff", "10.0.0.5:3425"), RemoteProof("k1", "00fe", "10.0.0.5:3425"), RemoteProof("k1", "00ff", "10.0.0.6:3425"), RemoteProof("k1", "00ff", "10.0.0.5:3426")} {
		if hmac.Equal(a, b) {
			t.Error("a proof for another key, nonce or address is the same")
		}
	}
}
