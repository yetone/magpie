package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Richyisaflower on X: B moved above A in the pool, and the Codex
// conversation still went to A, kept there by its cache. The order the user
// just chose counts at the conversation's next turn (provider.NoteReordered
// in SetAccountOrder, Make first); a turn under way, and Stays: Session,
// keep it where it is.
func TestReorderedAccountsTakeTheConversationAtItsNextTurn(t *testing.T) {
	for _, c := range []struct {
		name, mode string
		reorder    bool
		want       string
	}{
		{"reordered", "", true, "k1 k2 k2"},
		{"not reordered keeps its cache", "", false, "k1 k1 k1"},
		{"session keeps it", provider.AffinitySession, true, "k1 k1 k1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			up := &cacheWriter{wrote: 3000}
			srv := httptest.NewServer(up)
			t.Cleanup(srv.Close)
			p := provider.Provider{ID: "ord", Name: "Ord", Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}},
				Models: []string{"m1"}, Anthropic: srv.URL + "/v1", Affinity: c.mode}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			forgetRouting()
			s := New()
			t.Cleanup(s.subscription.abortAll)
			conv := `{"role":"user","content":"fix the bug"}`
			var got []string
			for i := range 3 {
				if i == 1 && c.reorder {
					ids := p.KeyList()
					if err := provider.SetAccountOrder("ord", []string{ids[1].ID, ids[0].ID}); err != nil {
						t.Fatal(err)
					}
					forgetRouting()
				}
				body := `{"model":"ord/m1","max_tokens":100,"messages":[` + conv + `]}`
				r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
				r.Header.Set("x-claude-code-session-id", "s-reorder")
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, r)
				if rec.Code != 200 {
					t.Fatalf("request %d: %d %s", i, rec.Code, rec.Body)
				}
				out := rec.Body.String()
				switch {
				case strings.Contains(out, "from k1"):
					got = append(got, "k1")
				case strings.Contains(out, "from k2"):
					got = append(got, "k2")
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
