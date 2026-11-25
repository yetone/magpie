package gateway

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// evjohnicu on X: one computer's magpie reached at home by its LAN
// address, from outside through Tailscale or EasyTier. With its own
// address taking no connection (nothing listens on 127.0.0.1:1), a turn
// goes to its other address and is answered there, and nothing rests.
// In a group it isn't passed over as on another network either: its
// first address may be, while another takes the connection.
func TestRemoteMagpieAskedAtItsOtherAddress(t *testing.T) {
	fresh(t)
	var nets atomic.Value
	nets.Store("192.168.51.7/24")
	lanNetsForTest(t, &nets)
	// the reach check finds nothing at its first address
	reachDial = func(ctx context.Context, hostport string) error {
		if hostport == "127.0.0.1:1" {
			return &net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded}
		}
		return nil
	}
	var officeHits, homeHits atomic.Int32
	office := remoteMagpieAt(t, "office", &officeHits)
	home := remoteMagpieAt(t, "home", &homeHits)
	for _, p := range []provider.Provider{
		{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-office", Chat: "http://127.0.0.1:1", Addresses: []string{office.URL}, Models: []string{"m"}},
		{ID: "home", Name: "Home", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-home", Chat: home.URL, Models: []string{"m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"office/m", "home/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	// the group first, before a turn of its own has shown office answers
	if got := askLANGroup(t, h); !strings.Contains(got, "from office") {
		t.Fatalf("group: %s; want office, at its other address, asked first as its place says", got)
	}
	for range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"office/m","messages":[{"role":"user","content":"hi"}]}`)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from office") {
			t.Fatalf("office/m: %d %s", rec.Code, rec.Body)
		}
	}
	if officeHits.Load() != 3 || homeHits.Load() != 0 {
		t.Errorf("office asked %d times, home %d", officeHits.Load(), homeHits.Load())
	}
	if r := restingNow(t); len(r) > 0 {
		t.Errorf("rests: %v", r)
	}
}
