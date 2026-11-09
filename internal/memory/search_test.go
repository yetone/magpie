package memory

import (
	"slices"
	"strings"
	"testing"
)

func TestWordsSplitsCJK(t *testing.T) {
	got := strings.Join(Words("用 Go 写测试, mote-run!"), "|")
	if got != "用|go|写|写测|测|测试|试|mote|run" {
		t.Fatalf("words %q", got)
	}
}

func TestSearch(t *testing.T) {
	fresh(t)
	save(t, "The user prefers tabs over spaces in Go", Preference, Global, "go")
	save(t, "Run heavy commands on mote, not on this machine", Rule, Global, "infra")
	save(t, "测试在 mote 上跑", Rule, "project:github.com/yetone/magpie")
	save(t, "Spaces, not tabs, in YAML", Preference, Global)

	ids := func(hs []Hit) []string {
		out := []string{}
		for _, h := range hs {
			out = append(out, h.Text)
		}
		return out
	}
	hits, err := Search(Query{Text: "tabs go"})
	if err != nil || len(hits) == 0 || hits[0].Text != "The user prefers tabs over spaces in Go" {
		t.Fatalf("tabs go: %v, %v", ids(hits), err)
	}
	if hits, _ := Search(Query{Text: "测试"}); len(hits) != 1 || hits[0].Scope != "project:github.com/yetone/magpie" {
		t.Fatalf("测试: %v", ids(hits))
	}
	if hits, _ := Search(Query{Text: "mote", Scopes: []string{Global}}); len(hits) != 1 || !strings.HasPrefix(hits[0].Text, "Run heavy") {
		t.Fatalf("mote in global: %v", ids(hits))
	}
	if hits, _ := Search(Query{Text: "mote", Tags: []string{"#Infra"}}); len(hits) != 1 {
		t.Fatalf("mote tagged infra: %v", ids(hits))
	}
	if hits, _ := Search(Query{Text: "nothing like it"}); len(hits) != 0 {
		t.Fatalf("no match: %v", ids(hits))
	}
	// no text: by kind, preferences before rules
	all, _ := Search(Query{Kinds: []string{Rule, Preference}})
	if len(all) != 4 || all[0].Kind != Preference || all[3].Kind != Rule {
		t.Fatalf("listed %v", ids(all))
	}
	if got, _ := Search(Query{Limit: 1}); len(got) != 1 {
		t.Fatal("limit not kept")
	}
	// a search notes its hits as used
	m, _ := Get(hits[0].ID)
	if m.Uses != 1 || m.Used.IsZero() {
		t.Fatalf("hit not noted: uses %d", m.Uses)
	}
	if slices.ContainsFunc(all, func(h Hit) bool { return h.Score != 0 }) {
		t.Fatal("a listing scored")
	}
}
