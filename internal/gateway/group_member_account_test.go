package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A group's member on one account of its own (EastonXvii on X: Astra on
// account A, then Astra on B, then Sol on C) is sent on that account
// alone, and the group tries its members strictly in their order: the
// same model on two accounts is two members, never one, and a member's
// account refusing goes to the next member, never to the provider's
// other account under the same member.
func TestGroupMemberAccountsInOrder(t *testing.T) {
	relay, other := groupAccountsSetup(t)
	work, mine := provider.KeyID("sk-work"), provider.KeyID("sk-mine")
	// work first, though the provider's own first key is sk-mine
	if err := provider.SaveGroup(provider.Group{Name: "Chain", Routing: provider.Ordered,
		Members: []string{"relay/m#" + work, "relay/m#" + mine, "other/m"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := provider.FindGroup("group/chain")
	if !ok || len(g.Members) != 3 || len(ms) != 3 {
		t.Fatalf("saved as %v, %d members", g.Members, len(ms))
	}
	if ms[0].Account != "relay/"+work || ms[1].Account != "relay/"+mine || ms[2].Account != "" {
		t.Fatalf("members' accounts %q %q %q", ms[0].Account, ms[1].Account, ms[2].Account)
	}
	s := New()
	h := s.Handler()
	chat := func() *httptest.ResponseRecorder {
		return askGroup(h, "", "/v1/chat/completions", `{"model":"group/chain","messages":[{"role":"user","content":"hi"}]}`)
	}
	fresh := func() {
		noRests()
		sticks.Lock()
		sticks.m = map[string]stick{}
		sticks.Unlock()
		relay.reset()
		other.reset()
	}
	// all answering: the first member, on its own account
	fresh()
	if w := chat(); w.Code != 200 || !strings.Contains(w.Body.String(), "from sk-work") {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	if a := relay.asked(); !slices.Equal(a, []string{"sk-work"}) {
		t.Fatalf("first asked %v", a)
	}
	// the trace says the member's other account was left out for it
	r := s.trace.routes[len(s.trace.routes)-1]
	if !slices.ContainsFunc(r.Left, func(w Weighed) bool { return w.Held && w.HeldOwn && w.HeldBy == "Chain" }) {
		t.Fatalf("route left %+v", r.Left)
	}
	// A refusing: B, the second member, on the other account
	fresh()
	relay.refuse = map[string]bool{"sk-work": true}
	if w := chat(); w.Code != 200 || !strings.Contains(w.Body.String(), "from sk-mine") {
		t.Fatalf("second: %d %s", w.Code, w.Body.String())
	}
	if a := relay.asked(); !slices.Equal(a, []string{"sk-work", "sk-mine"}) {
		t.Fatalf("second asked %v", a)
	}
	if len(other.asked()) != 0 {
		t.Fatalf("third asked before the second: %v", other.asked())
	}
	// both refusing: the third member
	fresh()
	relay.refuse = map[string]bool{"sk-work": true, "sk-mine": true}
	if w := chat(); w.Code != 200 || !strings.Contains(w.Body.String(), "from sk-other") {
		t.Fatalf("third: %d %s", w.Code, w.Body.String())
	}
	if a := relay.asked(); !slices.Equal(a, []string{"sk-work", "sk-mine"}) {
		t.Fatalf("third, relay asked %v", a)
	}
	relay.refuse = nil
}

// A member on its own account never spills over to the provider's other
// accounts: alone in its group, its account refusing is the group's
// error, the other key unasked, on every route.
func TestGroupMemberAccountNeverSpills(t *testing.T) {
	relay, _ := groupAccountsSetup(t)
	if err := provider.SaveGroup(provider.Group{Name: "Pinned", Routing: provider.Ordered, Members: []string{"relay/m#" + provider.KeyID("sk-work")}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	for _, rt := range groupRoutes("group/pinned") {
		relay.reset()
		if w := askGroup(h, "", rt[0], rt[1]); w.Code != 200 {
			t.Fatalf("%s %s: %d %s", rt[0], rt[1], w.Code, w.Body.String())
		}
		for _, k := range relay.asked() {
			if k != "sk-work" {
				t.Fatalf("%s %s: spent %s", rt[0], rt[1], k)
			}
		}
	}
	relay.refuse = map[string]bool{"sk-work": true}
	t.Cleanup(func() { relay.refuse = nil })
	for i := range 2 {
		relay.reset()
		w := askGroup(h, "", "/v1/chat/completions", `{"model":"group/pinned","messages":[{"role":"user","content":"hi"}]}`)
		if w.Code < 400 {
			t.Fatalf("refused %d: %d %s", i, w.Code, w.Body.String())
		}
		if slices.Contains(relay.asked(), "sk-mine") {
			t.Fatalf("refused %d: spilled to the other key %v", i, relay.asked())
		}
	}
}

// A member's account is kept as its id though typed by the name it is
// shown by; one the provider has none such for, and one on a group, are
// refused; a group saved before (no account) is as it was; a shared or
// imported group carries none.
func TestGroupMemberAccountSaved(t *testing.T) {
	groupAccountsSetup(t)
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	p.Keys = []provider.KeyAccount{{Key: "sk-work", Name: "Company"}}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	work := provider.KeyID("sk-work")
	if err := provider.SaveGroup(provider.Group{Name: "Named", Members: []string{"relay/m#Company", "relay/m"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, _ := provider.FindGroup("group/named")
	if !slices.Equal(g.Members, []string{"relay/m#" + work, "relay/m"}) || len(ms) != 2 {
		t.Fatalf("kept as %v (%d)", g.Members, len(ms))
	}
	if err := provider.SaveGroup(provider.Group{Name: "Bad", Members: []string{"relay/m#nobody"}}); err == nil {
		t.Fatal("an account the provider lacks was saved")
	}
	if err := provider.SaveGroup(provider.Group{Name: "Bad", Members: []string{"group/named#" + work}}); err == nil {
		t.Fatal("a group on an account was saved")
	}
	// one the stored group has already, its key taken out since, stays,
	// matching nothing, rather than let loose on every account
	p.Keys = nil
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "named", Name: "Named", Members: g.Members}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := provider.FindGroup("group/named"); !slices.Equal(g.Members, []string{"relay/m#" + work, "relay/m"}) {
		t.Fatalf("a gone account's member saved again as %v", g.Members)
	}
	p.Keys = []provider.KeyAccount{{Key: "sk-work", Name: "Company"}}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Old", Members: []string{"relay/m", "other/m"}}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := provider.FindGroup("group/old"); !slices.Equal(g.Members, []string{"relay/m", "other/m"}) {
		t.Fatalf("a group without accounts saved as %v", g.Members)
	}
	text, err := provider.ShareGroup("named")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, work) {
		t.Fatalf("shared with a member's account: %s", text)
	}
	// one with a member's account in it (an older text, or by hand) is
	// imported without it
	text = strings.Replace(text, `"relay/m"`, `"relay/m#`+work+`"`, 1)
	if !strings.Contains(text, "#"+work) {
		t.Fatalf("no account put in %s", text)
	}
	imp, err := provider.ImportGroup(text)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range provider.Groups() {
		if g.ID == imp.ID && slices.ContainsFunc(g.Members, func(m string) bool { return strings.Contains(m, "#") }) {
			t.Fatalf("imported with an account %v", g.Members)
		}
	}
}

// A drawing to the group goes over its members as a chat does: the same
// model on two accounts is two members, each drawn on its own account
// alone, in the group's order, never spilling over to the provider's
// other key.
func TestGroupMemberAccountsDraw(t *testing.T) {
	fresh(t)
	t.Cleanup(noRests)
	var mu sync.Mutex
	var asked []string
	refuse := map[string]bool{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		asked = append(asked, key)
		no := refuse[key]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if no {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			return
		}
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"iVBORw0KGgo="}]}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "art", Name: "Art", Chat: up.URL + "/v1", Key: "sk-mine",
		Keys: []provider.KeyAccount{{Key: "sk-work"}}, Models: []string{"gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	work, mine := provider.KeyID("sk-work"), provider.KeyID("sk-mine")
	if err := provider.SaveGroup(provider.Group{Name: "Easel", Routing: provider.Ordered,
		Members: []string{"art/gpt-image-1#" + work, "art/gpt-image-1#" + mine}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Pinned", Routing: provider.Ordered,
		Members: []string{"art/gpt-image-1#" + work}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	draw := func(group string, no ...string) (int, []string) {
		mu.Lock()
		asked, refuse = nil, map[string]bool{}
		for _, k := range no {
			refuse[k] = true
		}
		mu.Unlock()
		noRests()
		code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"group/`+group+`","prompt":"a magpie"}`)
		mu.Lock()
		defer mu.Unlock()
		if code != 200 {
			t.Logf("%s: %d %s", group, code, raw)
		}
		return code, slices.Clone(asked)
	}
	if code, a := draw("easel"); code != 200 || !slices.Equal(a, []string{"sk-work"}) {
		t.Fatalf("first member: %d asked %v", code, a)
	}
	if code, a := draw("easel", "sk-work"); code != 200 || !slices.Equal(a, []string{"sk-work", "sk-mine"}) {
		t.Fatalf("second member: %d asked %v", code, a)
	}
	if code, a := draw("pinned", "sk-work"); code == 200 || slices.Contains(a, "sk-mine") {
		t.Fatalf("a member on its own account spilled over: %d asked %v", code, a)
	}
}
