package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A routing group renamed keeps its id, and a client the user typed the
// group's name into (ZCode's model field) asks for it by that name: the
// name is the group's, as its id is (MOMO on Discord: "magpie knows no
// model" after magpie group set <id> name=…). A model a provider serves
// by that id stays the provider's, and a name two groups share names
// neither.
func TestGroupAskedByItsName(t *testing.T) {
	fresh(t)
	a := &keyed{}
	serveOn(t, "a", "ka", []string{"deepseek-flash", "ds-pro"}, a)
	if err := provider.SaveGroup(provider.Group{ID: "fast-pool", Name: "DS Flash", Members: []string{"a/deepseek-flash"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func(model string) (int, string) {
		t.Helper()
		return postAs(t, s, "", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	}
	for _, id := range []string{"DS Flash", "ds-flash", "group/fast-pool", "fast-pool"} {
		code, body := ask(id)
		if code != 200 {
			t.Fatalf("%s: %d %s", id, code, body)
		}
		if r := s.trace.routes[len(s.trace.routes)-1]; r.Group == nil || r.Group.ID != "fast-pool" {
			t.Fatalf("%s went to %+v, not the group", id, r.Group)
		}
	}

	// a group named as a model a provider serves leaves the model the provider's
	if err := provider.SaveGroup(provider.Group{ID: "pro-pool", Name: "ds-pro", Members: []string{"a/deepseek-flash"}}); err != nil {
		t.Fatal(err)
	}
	if code, body := ask("ds-pro"); code != 200 || s.trace.routes[len(s.trace.routes)-1].Group != nil {
		t.Fatalf("ds-pro: %d %s, group %+v", code, body, s.trace.routes[len(s.trace.routes)-1].Group)
	}

	// two groups of one name: neither is meant
	if err := provider.SaveGroup(provider.Group{ID: "other-pool", Name: "DS Flash", Members: []string{"a/ds-pro"}}); err != nil {
		t.Fatal(err)
	}
	if code, body := ask("DS Flash"); code != 404 || !strings.Contains(body, "knows no model") {
		t.Fatalf("a name two groups share: %d %s", code, body)
	}
}
