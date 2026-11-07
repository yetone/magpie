package gateway

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func setGroupDefaults(t *testing.T, sink bool, firstToken, patience int) {
	t.Helper()
	if err := settings.SetGroupDefaults(sink, firstToken, patience); err != nil {
		t.Fatal(err)
	}
}

// A group that sets no first-token time of its own follows the default for
// every group (jiezhengj on Discord: set once, not in each group); one
// that picked Wait itself waits, whatever the default.
func TestGroupFirstTokenFollowsTheDefault(t *testing.T) {
	fresh(t)
	setGroupDefaults(t, false, 1, 0)
	a := &slowStart{wait: 10 * time.Second}
	b := &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	began := time.Now()
	code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
	if code != 200 || !strings.Contains(body, "from b") || a.n != 1 || b.n != 1 {
		t.Fatalf("default: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}

	// the group's own Wait wins over the default
	fresh(t)
	setGroupDefaults(t, false, 1, 0)
	a = &slowStart{wait: 2 * time.Second}
	b = &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, FirstToken: provider.FirstTokenWait}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/g")
	if g.FirstToken != provider.FirstTokenWait {
		t.Fatalf("Wait not kept: %d", g.FirstToken)
	}
	s = New()
	if code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "slow answer") || b.n != 0 {
		t.Fatalf("own wait: %d %s (b %d)", code, body, b.n)
	}
}

// A group that sets nothing for one rate limited with quota left follows
// the default for every group; its own Keeps its place (NoSink) or Goes to
// the back (Sink) wins.
func TestGroupSinkFollowsTheDefault(t *testing.T) {
	fresh(t)
	forgetSinks(t)
	s := &Server{}
	mem := func(id string) provider.Member {
		return provider.Member{ID: id + "/m", Path: []string{id + "/m"}, Provider: provider.Provider{ID: id, Chat: "http://127.0.0.1:1/v1", Key: "k-" + id}, Model: "m"}
	}
	ms := []provider.Member{mem("a"), mem("b"), mem("c")}
	ids := func(cs []candidate) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.p.ID)
		}
		return out
	}
	rate := []byte(`{"error":{"message":"Too many requests"}}`)
	cs, _ := s.planGroup(provider.Group{ID: "g", Routing: provider.Ordered}, ms, provider.Chat)
	s.restAfter(cs[0], 429, http.Header{}, rate)
	rested(cs[0].restKey())
	order := func(g provider.Group) []string {
		out, _ := s.planGroup(g, ms, provider.Chat)
		return ids(out)
	}
	back, kept := []string{"b", "c", "a"}, []string{"a", "b", "c"}
	if got := order(provider.Group{ID: "g", Routing: provider.Ordered}); !slices.Equal(got, kept) {
		t.Fatalf("no default: %v", got)
	}
	setGroupDefaults(t, true, 0, 0)
	if got := order(provider.Group{ID: "g", Routing: provider.Ordered}); !slices.Equal(got, back) {
		t.Fatalf("default to the back: %v", got)
	}
	if got := order(provider.Group{ID: "g", Routing: provider.Ordered, NoSink: true}); !slices.Equal(got, kept) {
		t.Fatalf("own keeps its place: %v", got)
	}
	setGroupDefaults(t, false, 0, 0)
	if got := order(provider.Group{ID: "g", Routing: provider.Ordered, Sink: true}); !slices.Equal(got, back) {
		t.Fatalf("own to the back: %v", got)
	}
}
