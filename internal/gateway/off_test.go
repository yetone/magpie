package gateway

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider switched off (#163) is kept, but agents are given none of its
// models, no request, group or fallback goes to it, and one naming it is
// told it is switched off; switched on, all of it comes back.
func TestProviderOff(t *testing.T) {
	fresh(t)
	a, b := &keyed{}, &keyed{}
	serveOn(t, "a", "ka", []string{"m", "only-a"}, a)
	serveOn(t, "b", "kb", []string{"vendor/m", "only-b"}, b)
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	pa, _ := provider.Find("a")
	pa.Fallback = []string{"b/only-b"}
	if err := provider.Save(*pa); err != nil {
		t.Fatal(err)
	}
	catalog := func() string {
		var ids []string
		for _, e := range provider.Catalog() {
			if e.Provider.Account == nil { // signed-in agents the machine may have aside
				ids = append(ids, e.ID)
			}
		}
		return strings.Join(ids, " ")
	}
	const all = "group/mine group/auto-m a/m a/only-a b/vendor/m b/only-b"
	if got := catalog(); got != all {
		t.Fatalf("catalog: %s", got)
	}

	if err := provider.SetOff("b", true); err != nil {
		t.Fatal(err)
	}
	pb, err := provider.Find("b")
	if err != nil || !pb.Off || pb.Key != "kb" {
		t.Fatalf("switched off, b is kept with its key: %+v %v", pb, err)
	}
	if got := catalog(); got != "group/mine a/m a/only-a" {
		t.Fatalf("catalog with b off: %s", got)
	}
	if _, _, ok := provider.Resolve("b/vendor/m"); ok {
		t.Fatal("b/vendor/m resolves with b off")
	}
	if _, _, ok := provider.Resolve("only-b"); ok {
		t.Fatal("only-b resolves with b off")
	}
	if p, m, ok := provider.Resolve("group/mine"); !ok || p.ID != "a" || m != "only-a" {
		t.Fatalf("group with b off: %v %s %s", ok, p.ID, m)
	}
	if slices.ContainsFunc(provider.Served(), func(e provider.Entry) bool { return e.Provider.ID == "b" }) {
		t.Fatal("b's models served with b off")
	}

	s := New()
	code, body := postAs(t, s, "", `{"model":"b/vendor/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 404 || !strings.Contains(body, "switched off in Magpie") || len(b.tried) != 0 {
		t.Fatalf("b/vendor/m with b off: %d %s, b tried %v", code, body, b.tried)
	}
	code, body = postAs(t, s, "", `{"model":"only-b","messages":[{"role":"user","content":"hi"}]}`)
	if code != 404 || !strings.Contains(body, "switched off in Magpie") || len(b.tried) != 0 {
		t.Fatalf("only-b with b off: %d %s, b tried %v", code, body, b.tried)
	}
	code, body = postAs(t, s, "", `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from ka") || len(b.tried) != 0 {
		t.Fatalf("group/mine with b off: %d %s, b tried %v", code, body, b.tried)
	}
	// a's fallback is b's: with b off there is none to go to
	a.fail = map[string]int{"ka": 429}
	code, _ = postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 429 || len(b.tried) != 0 {
		t.Fatalf("a/m falling back to b off: %d, b tried %v", code, b.tried)
	}
	a.fail = nil

	if err := provider.SetOff("b", false); err != nil {
		t.Fatal(err)
	}
	if got := catalog(); got != all {
		t.Fatalf("catalog with b on again: %s", got)
	}
	code, body = postAs(t, s, "", `{"model":"b/vendor/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from kb") {
		t.Fatalf("b/vendor/m with b on again: %d %s", code, body)
	}
}
