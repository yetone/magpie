package provider

import (
	"slices"
	"strings"
	"testing"
)

// effortHome is a machine of its own with providers whose model ids have
// colons of their own: OpenRouter's :free, Ollama's :7b, Bedrock's :0,
// and one whose id ends in a level's name.
func effortHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []Provider{
		{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m", "n"}},
		{ID: "or", Name: "OR", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek/deepseek-r1:free", "odd:high"}},
		{ID: "ol", Name: "Ollama", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"qwen:7b"}},
		{ID: "bd", Name: "Bedrock", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"anthropic.claude-opus-4-v1:0"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

// A trailing :<level> is an effort; any other colon is the model's own, as
// is a level's name the model's own id ends in.
func TestMemberEffortParsing(t *testing.T) {
	effortHome(t)
	for in, want := range map[string][2]string{
		"a/m":                                  {"a/m", ""},
		"a/m:low":                              {"a/m", "low"},
		"a/m:XHigh":                            {"a/m", "xhigh"},
		"a/m:none":                             {"a/m", "none"},
		"a/m:max":                              {"a/m", "max"},
		"a/m:turbo":                            {"a/m:turbo", ""},
		"or/deepseek/deepseek-r1:free":         {"or/deepseek/deepseek-r1:free", ""},
		"or/deepseek/deepseek-r1:free:medium":  {"or/deepseek/deepseek-r1:free", "medium"},
		"deepseek/deepseek-r1:free":            {"deepseek/deepseek-r1:free", ""},
		"ol/qwen:7b":                           {"ol/qwen:7b", ""},
		"qwen:7b":                              {"qwen:7b", ""},
		"ol/qwen:7b:high":                      {"ol/qwen:7b", "high"},
		"bd/anthropic.claude-opus-4-v1:0":      {"bd/anthropic.claude-opus-4-v1:0", ""},
		"bd/anthropic.claude-opus-4-v1:0:high": {"bd/anthropic.claude-opus-4-v1:0", "high"},
		"or/odd:high":                          {"or/odd:high", ""}, // the model's own id
		"or/odd:high:low":                      {"or/odd:high", "low"},
		"group/fast:low":                       {"group/fast", "low"}, // for SaveGroup to refuse
		":low":                                 {":low", ""},
	} {
		m, e := MemberEffort(in)
		if m != want[0] || e != want[1] {
			t.Errorf("MemberEffort(%q) = %q %q, want %q %q", in, m, e, want[0], want[1])
		}
	}
	if WithMemberEffort("a/m", "") != "a/m" || WithMemberEffort("a/m", "low") != "a/m:low" {
		t.Error("WithMemberEffort")
	}
}

// Members at efforts are kept as typed, their effort lowercase; the same
// model at two efforts is two members, the same one twice is one; a
// group in the group takes none.
func TestSaveGroupMemberEfforts(t *testing.T) {
	effortHome(t)
	err := SaveGroup(Group{Name: "Fast", Members: []string{"a/m:LOW", "a/m:high", "a/m", "a/m:low", "ol/qwen:7b", "or/deepseek/deepseek-r1:free"},
		Rules: []Rule{{Use: "a/m:HIGH", Tokens: 10}}})
	if err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/fast")
	if !ok {
		t.Fatal("no group")
	}
	if want := []string{"a/m:low", "a/m:high", "a/m", "ol/qwen:7b", "or/deepseek/deepseek-r1:free"}; !slices.Equal(g.Members, want) {
		t.Fatalf("members %v, want %v", g.Members, want)
	}
	if g.Rules[0].Use != "a/m:high" {
		t.Fatalf("rule use %q", g.Rules[0].Use)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.ID+"="+m.Provider.ID+"|"+m.Model+"|"+m.Effort)
	}
	if s := strings.Join(got, " "); s != "a/m:low=a|m|low a/m:high=a|m|high a/m=a|m| ol/qwen:7b=ol|qwen:7b| or/deepseek/deepseek-r1:free=or|deepseek/deepseek-r1:free|" {
		t.Fatal(s)
	}
	// a stored group reads back the same (as a backup or a sync brings it)
	stored := slices.IndexFunc(Groups(), func(x Group) bool { return x.ID == "fast" })
	if stored < 0 || !slices.Equal(Groups()[stored].Members, g.Members) {
		t.Fatalf("stored: %+v", Groups())
	}
	if err := SaveGroup(Group{Name: "Top", Members: []string{"a/n", "group/fast:low"}}); err == nil || !strings.Contains(err.Error(), "is a group") {
		t.Fatalf("group at an effort: %v", err)
	}
}

// use= names a member at its effort, or without it when only one member
// is of that model.
func TestGroupMemberWithEffort(t *testing.T) {
	g := Group{ID: "fast", Members: []string{"a/m:low", "a/m:high", "b/n:medium"}}
	for in, want := range map[string]string{"a/m:high": "a/m:high", "m:low": "a/m:low", "b/n": "b/n:medium", "n": "b/n:medium", "A/M:HIGH": "a/m:high"} {
		if got, err := GroupMember(g, in); err != nil || got != want {
			t.Errorf("GroupMember(%q) = %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := GroupMember(g, "a/m"); err == nil || !strings.Contains(err.Error(), "name one") {
		t.Errorf("a/m at two efforts: %v", err)
	}
}

// The levels a group lists are those every member without an effort of
// its own has in common; members fixed at one don't narrow them. A group
// of fixed members alone lists the efforts they are fixed at.
func TestGroupLevelsWithFixedMembers(t *testing.T) {
	effortHome(t)
	if err := SetModelEfforts("a/m", []string{"low", "medium", "high"}); err != nil {
		t.Fatal(err)
	}
	if err := SetModelEfforts("a/n", []string{"medium", "high", "xhigh"}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		members []string
		want    []string
	}{
		"plain":   {[]string{"a/m", "a/n"}, []string{"medium", "high"}},
		"one":     {[]string{"a/m", "a/n:low"}, []string{"low", "medium", "high"}},
		"fixed":   {[]string{"a/m:xhigh", "a/n:low"}, []string{"low", "xhigh"}},
		"twice":   {[]string{"a/n:low", "a/n:high", "a/n"}, []string{"medium", "high", "xhigh"}},
		"no deal": {[]string{"a/m:low", "a/m:high"}, []string{"low", "high"}},
	} {
		if err := SaveGroup(Group{Name: "g-" + strings.ReplaceAll(name, " ", "-"), Members: c.members}); err != nil {
			t.Fatal(err)
		}
		id := GroupPrefix + "g-" + strings.ReplaceAll(name, " ", "-")
		i := slices.IndexFunc(Catalog(), func(e Entry) bool { return e.ID == id })
		if i < 0 {
			t.Fatalf("%s: not listed", name)
		}
		if got := Catalog()[i].Efforts; !slices.Equal(got, c.want) {
			t.Errorf("%s: levels %v, want %v", name, got, c.want)
		}
	}
}
