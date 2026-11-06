package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// a magpie on 127.0.0.1 that lists for key alone, its list made by lister
func movedMagpie(t *testing.T, key, lister string, hits *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
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
