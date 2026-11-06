package access

import (
	"strings"
	"testing"
	"time"
)

// A limit of some models (zola_xynb on X) keeps its models cleaned, never
// a routing group, and sits beside a limit of the whole key in the same
// window: two limits in one window are the same only for the same models.
func TestScopedLimitValidAndBeside(t *testing.T) {
	l, err := (&Limit{Period: "month", Cost: 20, Models: []string{" openai/* ", "OpenAI/*", "anthropic/claude-opus"}}).Valid()
	if err != nil || len(l.Models) != 2 || l.Models[0] != "openai/*" {
		t.Fatalf("cleaned as %+v %v", l, err)
	}
	for _, bad := range []string{"group/coding", "Auto/x", "gpt-5"} {
		if _, err := (&Limit{Period: "day", Tokens: 5, Models: []string{bad}}).Valid(); err == nil {
			t.Errorf("%s was taken", bad)
		}
	}
	many := make([]string, MaxLimitModels+1)
	for i := range many {
		many[i] = "p/m" + strings.Repeat("x", i)
	}
	if _, err := (&Limit{Period: "day", Tokens: 5, Models: many}).Valid(); err == nil {
		t.Error("more models than a limit counts were taken")
	}
	if (&Limit{Models: []string{"b/*", "A/x"}}).Scope() != (&Limit{Models: []string{"a/X", "b/*"}}).Scope() {
		t.Error("the same models in another order or case are another scope")
	}

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	Now = func() time.Time { return time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { Now = time.Now })
	if _, err := Update("add-key", Change{Name: "Shared"}); err != nil {
		t.Fatal(err)
	}
	keys, _ := List()
	id := keys[0].ID
	at := func(n int, l *Limit) error {
		_, err := Update("limit-key", Change{Key: id, Limit: l, Index: &n})
		return err
	}
	if err := at(0, &Limit{Period: "month", Cost: 100}); err != nil {
		t.Fatal(err)
	}
	if err := at(1, &Limit{Period: "month", Cost: 20, Models: []string{"openai/*"}}); err != nil {
		t.Fatal("a month of OpenAI beside a month of the key:", err)
	}
	if err := at(2, &Limit{Period: "month", Cost: 5, Models: []string{"OpenAI/*"}}); err == nil || !strings.Contains(err.Error(), "openai/*") {
		t.Fatalf("a second month of OpenAI: %v", err)
	}
	if err := at(2, &Limit{Period: "month", Cost: 5}); err == nil {
		t.Fatal("a second month of the whole key was taken")
	}
	keys, _ = List()
	ls := keys[0].Limits()
	if len(ls) != 2 || ls[0].Scoped() || !ls[1].Scoped() || ls[1].Models[0] != "openai/*" {
		t.Fatalf("kept as %+v", ls)
	}
}
