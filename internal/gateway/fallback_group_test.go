package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// fallbackToGroup saves a provider "plan" whose key kp answers 429 (out of
// quota) and falls back to the routing group "backup" of members, served
// by a (key ka, model x) and b (key kb, model x).
func fallbackToGroup(t *testing.T, g provider.Group) (plan, a, b *keyed) {
	t.Helper()
	fresh(t)
	plan, a, b = &keyed{fail: map[string]int{"kp": 429}}, &keyed{}, &keyed{}
	serveOn(t, "a", "ka", []string{"x"}, a)
	serveOn(t, "b", "kb", []string{"x"}, b)
	up := httptest.NewServer(plan)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "kp", Models: []string{"m"}, Chat: up.URL + "/v1", Fallback: []string{"group/backup"}}); err != nil {
		t.Fatal(err)
	}
	g.ID, g.Name = "backup", "Backup"
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	return plan, a, b
}

// A provider's fallback can be a routing group, not only a model
// (01huadalang on Discord): with the primary out of quota, the request
// goes through the group, its members in the group's own order.
func TestFallbackToGroupServedByMember(t *testing.T) {
	plan, a, b := fallbackToGroup(t, provider.Group{Members: []string{"a/x", "b/x"}, Routing: provider.Ordered})
	a.fail = map[string]int{"ka": 429}
	code, body := postAs(t, New(), "", `{"model":"plan/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from kb") {
		t.Fatalf("plan/m out of quota, falling back to group/backup: %d %s", code, body)
	}
	if len(plan.tried) != 1 || len(a.tried) != 1 || len(b.tried) != 1 {
		t.Fatalf("each tried once, the group's order: plan %v, a %v, b %v", plan.tried, a.tried, b.tried)
	}
}

// The group's own order holds: In order with b first sends to b, a never
// asked once b answers.
func TestFallbackGroupKeepsItsOrder(t *testing.T) {
	_, a, b := fallbackToGroup(t, provider.Group{Members: []string{"b/x", "a/x"}, Routing: provider.Ordered})
	code, body := postAs(t, New(), "", `{"model":"plan/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from kb") || len(a.tried) != 0 || len(b.tried) != 1 {
		t.Fatalf("group b, a: %d %s, a %v, b %v", code, body, a.tried, b.tried)
	}
}

// A group holding the provider's own model is no loop: the model is tried
// once, as the primary, and the group's other members after it.
func TestFallbackGroupWithThePrimaryTriesItOnce(t *testing.T) {
	plan, a, b := fallbackToGroup(t, provider.Group{Members: []string{"plan/m", "a/x", "b/x"}, Routing: provider.Ordered})
	a.fail = map[string]int{"ka": 500}
	b.fail = map[string]int{"kb": 500}
	if code, _ := postAs(t, New(), "", `{"model":"plan/m","messages":[{"role":"user","content":"hi"}]}`); code == 200 {
		t.Fatalf("every one failing answered 200")
	}
	// every one failing, the request is asked again as many times as
	// magpie's patience has it; each round asks each model once
	if len(plan.tried) == 0 || len(a.tried) != len(plan.tried) || len(b.tried) != len(plan.tried) {
		t.Fatalf("each tried once a round: plan %v, a %v, b %v", plan.tried, a.tried, b.tried)
	}
}

// A model named both as a fallback and in a fallback group is tried once,
// where it was named first; so is a model two fallback groups share.
func TestFallbackGroupSharedModelTriedOnce(t *testing.T) {
	plan, a, b := fallbackToGroup(t, provider.Group{Members: []string{"a/x", "b/x"}, Routing: provider.Ordered})
	if err := provider.SaveGroup(provider.Group{ID: "more", Name: "More", Members: []string{"b/x", "a/x"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("plan")
	if err != nil {
		t.Fatal(err)
	}
	p.Fallback = []string{"a/x", "group/backup", "group/more"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	a.fail = map[string]int{"ka": 500}
	b.fail = map[string]int{"kb": 500}
	postAs(t, New(), "", `{"model":"plan/m","messages":[{"role":"user","content":"hi"}]}`)
	// every one failing, the request is asked again as many times as
	// magpie's patience has it; each round asks each model once
	if len(plan.tried) == 0 || len(a.tried) != len(plan.tried) || len(b.tried) != len(plan.tried) {
		t.Fatalf("each tried once a round: plan %v, a %v, b %v", plan.tried, a.tried, b.tried)
	}
}

// A member the group's pause rule holds now is out of the group as a
// fallback too, as it is for a request to the group.
func TestFallbackGroupPauseHolds(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.Local) // a Tuesday, in peak
	ruleClock = func() time.Time { return now }
	t.Cleanup(func() { ruleClock = time.Now })
	_, a, b := fallbackToGroup(t, provider.Group{Members: []string{"a/x", "b/x"}, Routing: provider.Ordered,
		Rules: []provider.Rule{{Use: "a/x", Pause: true, Time: peak}}})
	code, body := postAs(t, New(), "", `{"model":"plan/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from kb") || len(a.tried) != 0 {
		t.Fatalf("a/x paused: %d %s, a %v, b %v", code, body, a.tried, b.tried)
	}
}

// A fallback group that is gone is passed over, as a model none serves is.
func TestFallbackGroupGoneIsSkipped(t *testing.T) {
	plan, a, b := fallbackToGroup(t, provider.Group{Members: []string{"a/x"}, Routing: provider.Ordered})
	p, err := provider.Find("plan")
	if err != nil {
		t.Fatal(err)
	}
	p.Fallback = []string{"group/nothing", "b/x"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	code, body := postAs(t, New(), "", `{"model":"plan/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from kb") || len(a.tried) != 0 || len(plan.tried) != 1 {
		t.Fatalf("group/nothing then b/x: %d %s, a %v, b %v", code, body, a.tried, b.tried)
	}
}

// A provider set to go unmasked stays masked when a routing group among
// its fallbacks may send the request to a model that isn't: the group's
// members are its fallbacks as much as a model named alone is.
func TestFallbackGroupKeepsMasking(t *testing.T) {
	fresh(t)
	for _, p := range []provider.Provider{
		{ID: "near", Name: "Near", Key: "k1", Models: []string{"m"}, Chat: "http://127.0.0.1:1/v1", Unredacted: true, Fallback: []string{"group/backup"}},
		{ID: "near2", Name: "Near2", Key: "k2", Models: []string{"m"}, Chat: "http://127.0.0.1:2/v1", Unredacted: true},
		{ID: "far", Name: "Far", Key: "k3", Models: []string{"m"}, Chat: "https://api.example.com/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "backup", Name: "Backup", Members: []string{"near2/m"}}); err != nil {
		t.Fatal(err)
	}
	near, err := provider.Find("near")
	if err != nil {
		t.Fatal(err)
	}
	if !unredactedRoute(*near, "", false, nil) {
		t.Fatal("a group of unmasked local models masked the request")
	}
	if err := provider.SaveGroup(provider.Group{ID: "backup", Name: "Backup", Members: []string{"near2/m", "far/m"}}); err != nil {
		t.Fatal(err)
	}
	if unredactedRoute(*near, "", false, nil) {
		t.Fatal("a fallback group with a remote member left the request unmasked")
	}
}
