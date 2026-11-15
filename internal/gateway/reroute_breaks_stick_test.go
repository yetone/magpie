package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #6708 (zzh11223311): a conversation picked by hand to A, its group then
// set to Use before expiry, which put B first, stayed on A at its next
// turns, kept there by a 2.8k-token cache. A routing the user just chose
// counts at the conversation's next turn, as a reorder of a provider's
// accounts does; a save that leaves the routing as it was, and Stays:
// Session, keep it where it is.
func TestReroutedGroupTakesTheConversationAtItsNextTurn(t *testing.T) {
	for _, c := range []struct {
		name, mode string
		reroute    bool
		want       string
	}{
		{"rerouted", "", true, "a a b b"},
		{"saved unrerouted keeps its cache", "", false, "a a a a"},
		{"session keeps it", provider.AffinitySession, true, "a a a a"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			up := &cacheWriter{wrote: 2800}
			srv := httptest.NewServer(up)
			t.Cleanup(srv.Close)
			for _, p := range []provider.Provider{
				{ID: "pa", Name: "PA", Key: "a", Models: []string{"m1"}, Anthropic: srv.URL + "/v1"},
				{ID: "pb", Name: "PB", Key: "b", Models: []string{"m1"}, Anthropic: srv.URL + "/v1"},
			} {
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			g := provider.Group{ID: "rr", Name: "RR", Members: []string{"pb/m1", "pa/m1"}, Routing: provider.Manual, Pick: "pa/m1", Affinity: c.mode}
			if err := provider.SaveGroup(g); err != nil {
				t.Fatal(err)
			}
			forgetRouting()
			s := New()
			t.Cleanup(s.subscription.abortAll)
			conv := `{"role":"user","content":"fix the bug"}`
			var got []string
			for i := range 4 {
				if i == 2 {
					// the user leaves Manual for Smart, or only renames it
					if c.reroute {
						g.Routing = ""
					} else {
						g.Name = "RR2"
					}
					if err := provider.SaveGroup(g); err != nil {
						t.Fatal(err)
					}
					forgetRouting()
				}
				body := `{"model":"group/rr","max_tokens":100,"messages":[` + conv + `]}`
				r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
				r.Header.Set("x-claude-code-session-id", "s-reroute")
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, r)
				if rec.Code != 200 {
					t.Fatalf("request %d: %d %s", i, rec.Code, rec.Body)
				}
				out := rec.Body.String()
				switch {
				case strings.Contains(out, "from a"):
					got = append(got, "a")
				case strings.Contains(out, "from b"):
					got = append(got, "b")
				default:
					t.Fatalf("request %d answered by nobody known: %s", i, out)
				}
				conv += `,{"role":"assistant","content":"done"},{"role":"user","content":"and more"}`
			}
			if g := strings.Join(got, " "); g != c.want {
				t.Fatalf("answered by %s, want %s", g, c.want)
			}
		})
	}
}

// The time a group's routing last changed is kept across saves that
// leave it, and set by one that changes it.
func TestGroupRoutedAtFollowsTheRouting(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "pa", Name: "PA", Key: "a", Models: []string{"m1"}, Anthropic: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	g := provider.Group{ID: "rt", Name: "RT", Members: []string{"pa/m1"}}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if at := provider.GroupRoutedAt("rt"); !at.IsZero() {
		t.Fatalf("a new group's routing changed at %v", at)
	}
	g.Routing = provider.Pace
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	at := provider.GroupRoutedAt("rt")
	if at.IsZero() {
		t.Fatal("a routing change isn't kept")
	}
	g.Note = "work"
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if again := provider.GroupRoutedAt("rt"); !again.Equal(at) {
		t.Fatalf("a note's save moved it from %v to %v", at, again)
	}
}
