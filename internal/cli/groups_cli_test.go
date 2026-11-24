package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// groupsHome is a machine of its own with two providers: a serves m and
// only-a, b serves vendor/m (so magpie finds group/auto-m).
func groupsHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // no agent signed in
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "") // a dsh profile is found through it, not through HOME
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "ka", Models: []string{"m", "only-a", "claude-opus-5-5"}, Chat: "http://127.0.0.1:1/v1"},
		{ID: "b", Name: "B", Key: "kb", Models: []string{"vendor/m", "gpt-5.5"}, Chat: "http://127.0.0.1:1/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

// storedGroups: the groups as the file keeps them.
// A group an agent is on carries Claude Code's [1m] mark in the agent's
// own settings, as a model's does: `magpie group` lists "← agent" beside a
// group by its id, so the mark has to come off the id before it is looked
// up. Claude Code on group/auto-gpt-5[1m] was on no row at all.
func TestGroupUsesTakesTheOneMMarkOff(t *testing.T) {
	groupsHome(t)
	if err := provider.SaveGroup(provider.Group{ID: "auto-gpt-5", Name: "GPT 5", Members: []string{"a/gpt-5"}}); err != nil {
		t.Fatal(err)
	}
	// Claude Code's settings.json, as the reporter's reads
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".claude/settings.json"),
		[]byte(`{"model":"group/auto-gpt-5[1m]"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	uses := groupUses()
	if got := uses["auto-gpt-5"]; len(got) != 1 || !strings.Contains(got[0], "Claude") {
		t.Errorf("the group's agents: %q, all uses %q", got, uses)
	}
	if _, marked := uses["auto-gpt-5[1m]"]; marked {
		t.Errorf("the marked id got its own row: %q", uses)
	}
}

// sameGroup compares two values of a field, one of them read back from the
// agent's own settings where the [1m] mark rides on the group's id: the
// marked one is the group magpie set, not another group. (internal/agent's
// own, tested there.)
func storedGroups(t *testing.T) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Groups []map[string]any `json:"groups"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f.Groups
}

func TestParseRoutingStays(t *testing.T) {
	for in, want := range map[string]string{"smart": "", "": "", "order": "order", "In-Order": "order", "rotate": "rotate", "usage": "usage", "least-used": "usage", "fast": "fast", "Fastest": "fast", "speed": "fast", "speed-first": "fast", "cheap": "cheap", "Cheapest": "cheap", "cheapest-first": "cheap", "price": "cheap", "expiring": "expiring", "free-expiry": "expiring", "Soonest-Free": "expiring", "fit": "fit", "small-first": "fit", "Small-Context-First": "fit"} {
		if got, err := parseRouting(in); err != nil || got != want {
			t.Errorf("routing %q: %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := parseRouting("priciest"); err == nil || !strings.Contains(err.Error(), "smart, order, rotate, usage") || !strings.Contains(err.Error(), "fast") || !strings.Contains(err.Error(), "<plugin>/<name>") {
		t.Errorf("unknown routing: %v", err)
	}
	// a gateway plugin's routing is taken as it is written, and named so
	for _, in := range []string{"latency/fastest", "magpie-fastest/byTTFT", "x.y/a_b-1"} {
		if got, err := parseRouting(" " + in + " "); err != nil || got != in || routingName(got) != in {
			t.Errorf("plugin routing %q: %q %v %q", in, got, err, routingName(got))
		}
	}
	for _, in := range []string{"/fastest", "latency/", "a/b/c", "a b/c"} {
		if _, err := parseRouting(in); err == nil {
			t.Errorf("routing %q taken", in)
		}
	}
	for in, want := range map[string]string{"auto": "", "session": "session", "turn": "turn", "OFF": "off"} {
		if got, err := parseStays(in); err != nil || got != want {
			t.Errorf("stays %q: %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := parseStays("forever"); err == nil || !strings.Contains(err.Error(), "auto, session, turn, off") {
		t.Errorf("unknown stays: %v", err)
	}
}

func TestApplyGroupPairs(t *testing.T) {
	ids := map[string]string{"a/m": "a/m", "m": "a/m", "b/x": "b/x", "b/y": "b/y"}
	resolve := func(s string) (string, error) {
		if id, ok := ids[s]; ok {
			return id, nil
		}
		return "", os.ErrNotExist
	}
	g := provider.Group{}
	if err := applyGroupPairs(&g, []string{"models=m,b/x a/m", "routing=order", "stays=session", "name=G one"}, resolve, true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(g.Members, " ") != "a/m b/x" || g.Routing != "order" || g.Affinity != "session" || g.Name != "G one" {
		t.Fatalf("%+v", g)
	}
	if err := applyGroupPairs(&g, []string{"models+=b/y,b/x", "models-=a/m"}, resolve, false); err != nil {
		t.Fatal(err)
	}
	if strings.Join(g.Members, " ") != "b/x b/y" {
		t.Fatalf("members: %v", g.Members)
	}
	if err := applyGroupPairs(&g, []string{"models-=x"}, resolve, false); err != nil || strings.Join(g.Members, " ") != "b/y" {
		t.Fatalf("drop by bare id: %v %v", err, g.Members)
	}
	for _, c := range []struct {
		v    string
		want int
	}{{"272k", 272000}, {"smallest", provider.ContextSmallest}, {"largest", 0}, {"1m", 1000000}, {"", 0}} {
		if err := applyGroupPairs(&g, []string{"context=" + c.v}, resolve, false); err != nil || g.Context != c.want {
			t.Errorf("context=%s: %d %v", c.v, g.Context, err)
		}
	}
	// compact=: where agents compact on the group (NovaStudiosss on X)
	for _, c := range []struct {
		v    string
		want int
	}{{"160k", 160000}, {"1m", 1000000}, {"off", 0}, {"", 0}} {
		if err := applyGroupPairs(&g, []string{"compact=" + c.v}, resolve, false); err != nil || g.Compact != c.want {
			t.Errorf("compact=%s: %d %v", c.v, g.Compact, err)
		}
	}
	if err := applyGroupPairs(&g, []string{"compact=lots"}, resolve, false); err == nil {
		t.Error("compact=lots was accepted")
	}
	// listed=no: its models offered through the group alone (#1783)
	if err := applyGroupPairs(&g, []string{"listed=no"}, resolve, false); err != nil || !g.Unlists {
		t.Fatalf("listed=no: %v %v", g.Unlists, err)
	}
	if err := applyGroupPairs(&g, []string{"listed=YES"}, resolve, false); err != nil || g.Unlists {
		t.Fatalf("listed=yes: %v %v", g.Unlists, err)
	}
	for _, bad := range [][]string{{"models"}, {"colour=red"}, {"id=x"}, {"models=nope"}, {"routing=priciest"}, {"models-=a/m"}, {"context=lots"}, {"listed=maybe"}} {
		h := g
		if err := applyGroupPairs(&h, bad, resolve, false); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}

// Adding, changing and removing groups writes what the Routing view does.
func TestGroupAddSetRemove(t *testing.T) {
	groupsHome(t)

	g, err := addGroup("Opus anywhere", []string{"models=a/claude-opus-5-5,gpt-5.5", "routing=order", "stays=turn"})
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != "opus-anywhere" || strings.Join(g.Members, " ") != "a/claude-opus-5-5 b/gpt-5.5" {
		t.Fatalf("%+v", g)
	}
	gs := storedGroups(t)
	if len(gs) != 1 || gs[0]["id"] != "opus-anywhere" || gs[0]["name"] != "Opus anywhere" || gs[0]["routing"] != "order" || gs[0]["affinity"] != "turn" {
		t.Fatalf("stored: %v", gs)
	}
	if _, _, ok := provider.Resolve("group/opus-anywhere"); !ok {
		t.Fatal("group not in the catalog")
	}

	// the same name again replaces that group, keeping its id
	if g2, err := addGroup("opus Anywhere", []string{"models=a/only-a"}); err != nil || g2.ID != "opus-anywhere" || strings.Join(g2.Members, " ") != "a/only-a" {
		t.Fatalf("second: %+v %v", g2, err)
	}
	if gs := storedGroups(t); len(gs) != 1 {
		t.Fatalf("replaced into two: %v", gs)
	}
	if _, err := addGroup("Opus anywhere", []string{"models=a/claude-opus-5-5,gpt-5.5", "routing=order", "stays=turn"}); err != nil {
		t.Fatal(err)
	}
	if _, err := addGroup("x", []string{"models=a/m", "id=opus-anywhere"}); err == nil {
		t.Fatal("an id taken was taken again")
	}
	if _, err := addGroup("empty", nil); err == nil {
		t.Fatal("a group without models added")
	}
	_, err = addGroup("typo", []string{"models=a/claude-opus-5.5"})
	if err == nil || !strings.Contains(err.Error(), "a/claude-opus-5-5") {
		t.Fatalf("typo: %v", err)
	}
	if _, err := addGroup("amb", []string{"models=m"}); err != nil {
		// a/m and b/vendor/m are different ids; "m" is only a's model
		t.Fatalf("bare: %v", err)
	}
	// a group in a group, but never one it is in itself
	if _, err := addGroup("nested", []string{"models=group/opus-anywhere,a/only-a"}); err != nil {
		t.Fatalf("a group in a group: %v", err)
	}
	if _, err := setGroup("opus-anywhere", []string{"models+=group/nested"}); err == nil || !strings.Contains(err.Error(), "in itself") {
		t.Fatalf("a loop: %v", err)
	}
	if _, err := removeGroup("opus-anywhere"); err == nil || !strings.Contains(err.Error(), "take it out first") {
		t.Fatalf("rm while in a group: %v", err)
	}
	if _, err := removeGroup("nested"); err != nil {
		t.Fatal(err)
	}
	if _, err := addGroup("nowhere", []string{"models=group/nothing"}); err == nil || !strings.Contains(err.Error(), "no group") {
		t.Fatalf("no such group: %v", err)
	}

	// set: by name or id, a field at a time
	g, err = setGroup("group/opus-anywhere", []string{"routing=smart", "models+=a/only-a", "models-=b/gpt-5.5", "name=Opus"})
	if err != nil {
		t.Fatal(err)
	}
	if g.Routing != "" || g.Affinity != "turn" || strings.Join(g.Members, " ") != "a/claude-opus-5-5 a/only-a" || g.Name != "Opus" {
		t.Fatalf("set: %+v", g)
	}
	if _, err := setGroup("opus", []string{"stays=off"}); err != nil {
		t.Fatalf("by name: %v", err)
	}
	if _, err := setGroup("opus-anywhere", []string{"models-=a/claude-opus-5-5,a/only-a"}); err == nil {
		t.Fatal("emptied a group")
	}
	if _, err := setGroup("nope", []string{"stays=off"}); err == nil || !strings.Contains(err.Error(), "opus-anywhere") {
		t.Fatalf("unknown group: %v", err)
	}

	// one magpie found: changing it makes it the user's; removing it hides it
	if _, err := setGroup("auto-m", []string{"routing=rotate"}); err != nil {
		t.Fatal(err)
	}
	if g, _ := findGroup("auto-m"); g.Auto || g.Routing != "rotate" {
		t.Fatalf("auto after set: %+v", g)
	}
	if _, err := removeGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if g, _ := findGroup("auto-m"); !g.Hidden {
		t.Fatalf("auto after rm: %+v", g)
	}
	if _, err := setGroup("auto-m", []string{"routing=order"}); err == nil {
		t.Fatal("changed a removed group")
	}
	if g, err := restoreGroup("auto-m"); err != nil || g.Hidden {
		t.Fatalf("restore: %+v %v", g, err)
	}

	if _, err := addGroup("Second", []string{"models=a/only-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := removeGroup("second"); err != nil {
		t.Fatal(err)
	}
	if _, err := findGroup("second"); err == nil {
		t.Fatal("removed group still there")
	}
	if _, err := removeGroup("second"); err == nil {
		t.Fatal("removed twice")
	}
}

func TestCloseMatches(t *testing.T) {
	ids := []string{"claude/claude-opus-5-5", "copilot/claude-opus-5.5", "deepseek/deepseek-chat", "b/gpt-5.5"}
	got := closeMatches("claude-opus", ids, 5)
	if len(got) != 2 || got[0] != "claude/claude-opus-5-5" {
		t.Errorf("%v", got)
	}
	if got := closeMatches("x/deepseek-chta", ids, 5); len(got) != 1 || got[0] != "deepseek/deepseek-chat" {
		t.Errorf("%v", got)
	}
	if got := closeMatches("zzz", ids, 5); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

// magpie group set <id> id=<new>: a found group loses its auto- prefix,
// and its old id finds it under the new one, never as a second group.
func TestGroupSetID(t *testing.T) {
	groupsHome(t)
	if _, err := setGroup("auto-m", []string{"id=Not Slug!"}); err == nil {
		t.Fatal("a bad id taken")
	}
	g, err := setGroup("group/auto-m", []string{"id=m", "routing=order"})
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != "m" || g.Auto || g.Routing != "order" || len(g.Members) != 2 {
		t.Fatalf("%+v", g)
	}
	if _, _, ok := provider.Resolve("group/m"); !ok {
		t.Fatal("group/m not in the catalog")
	}
	// the old id still answers, with the renamed group (00af08e7b), never
	// a found auto-m of its own beside it
	if old, _, ok := provider.FindGroup("group/auto-m"); !ok || old.ID != "m" {
		t.Fatalf("group/auto-m: %v %+v", ok, old)
	}
	if _, err := addGroup("Other", []string{"models=a/only-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := setGroup("m", []string{"id=other"}); err == nil {
		t.Fatal("renamed onto another group")
	}
}

// magpie group set <id> name=… says the id stays, and what agents ask for,
// so a renamed group isn't taken for one with a new id (MOMO on Discord).
func TestGroupRenameSaysTheIDStays(t *testing.T) {
	groupsHome(t)
	if err := provider.SaveGroup(provider.Group{ID: "fast", Name: "Fast", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	out, err := stdoutOf(t, func() error { return groupCmd([]string{"group", "set", "fast", "name=DS Flash"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "its id stays fast") || !strings.Contains(out, "group/fast") {
		t.Fatalf("rename said %q; want that the id stays fast", out)
	}
}

// magpie group auto skip keeps providers out of the groups magpie finds
// (chenxiang0010 on Discord), refuses a name it has no provider of or a
// flag, and skip none lets them back in.
func TestGroupAutoSkip(t *testing.T) {
	groupsHome(t)
	found := func() bool {
		_, _, ok := provider.FindGroup("group/auto-m")
		return ok
	}
	if !found() {
		t.Fatal("no auto-m to begin with")
	}
	for _, bad := range [][]string{{"skip", "nope"}, {"skip", "--help"}} {
		if err := groupCmd(append([]string{"group", "auto"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if got := provider.AutoGroupSkip(); len(got) != 0 {
		t.Fatalf("a refused name was kept: %v", got)
	}
	if err := groupCmd([]string{"group", "auto", "skip", "B"}); err != nil {
		t.Fatal(err)
	}
	if got := provider.AutoGroupSkip(); len(got) != 1 || got[0] != "b" || found() {
		t.Fatalf("b left out: %v, auto-m still found: %v", got, found())
	}
	if err := groupCmd([]string{"group", "auto", "skip", "none"}); err != nil {
		t.Fatal(err)
	}
	if got := provider.AutoGroupSkip(); len(got) != 0 || !found() {
		t.Fatalf("let back in: %v %v", got, found())
	}
}

// A member saved in another case than its provider lists the model by is
// that model, served, in `magpie groups` and `magpie group <id>` as in the
// gateway: deepseek-agents/DeepSeek-V4-Pro read "not served" and the group
// "no member ready" (@CHENGJIAHEGOXIS on X).
func TestGroupsMemberInAnotherCaseIsServed(t *testing.T) {
	groupsHome(t)
	if err := provider.SaveGroup(provider.Group{ID: "ds", Name: "DS", Members: []string{"a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	// the reporter's spelling, as the file kept it
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie", "providers.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"a/only-a"`) {
		t.Fatalf("no member in %s:\n%s", path, b)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), `"a/only-a"`, `"a/Only-A"`)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := stdoutOf(t, groups)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "not served") || strings.Contains(out, "no member ready") {
		t.Errorf("magpie groups:\n%s", out)
	}
	out, err = stdoutOf(t, func() error { return groupCmd([]string{"group", "ds"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "only-a") || strings.Contains(out, "not served") {
		t.Errorf("magpie group ds:\n%s", out)
	}
}

// A group whose members are all groups magpie found is served when they
// are: the catalog lists a found group as auto/<model> while the members
// keep group/auto-<slug> (Will on Discord: `magpie groups` read each found
// group "not served" and the parent "no member ready", while `magpie
// group <id>` showed them served). A group in it that serves nothing still
// reads so.
func TestGroupsListServesFoundGroupMembers(t *testing.T) {
	groupsHome(t)
	if err := provider.Save(provider.Provider{ID: "c", Name: "C", Key: "kc", Models: []string{"x"}, Chat: "http://127.0.0.1:1/v1", Off: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := addGroup("Of found", []string{"models=group/auto-m"}); err != nil {
		t.Fatal(err)
	}
	// one saved by the found group's public id, as an agent is given it
	if err := provider.SaveGroup(provider.Group{ID: "of-public", Name: "Of public", Members: []string{"auto/m"}}); err != nil {
		t.Fatal(err)
	}
	// c is off, so its group serves nothing (addGroup refuses its model)
	if err := provider.SaveGroup(provider.Group{ID: "dead", Name: "Dead", Members: []string{"c/x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := addGroup("Of dead", []string{"models=group/dead"}); err != nil {
		t.Fatal(err)
	}
	out, err := stdoutOf(t, groups)
	if err != nil {
		t.Fatal(err)
	}
	row := func(name string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, name) {
				return l
			}
		}
		t.Fatalf("no row for %s in:\n%s", name, out)
		return ""
	}
	if l := row("Of found"); strings.Contains(l, "not served") || strings.Contains(l, "no member ready") {
		t.Errorf("a group of a found group: %s", l)
	}
	if l := row("Of public"); strings.Contains(l, "not served") || strings.Contains(l, "no member ready") {
		t.Errorf("a group of a found group by its public id: %s", l)
	}
	if l := row("Of dead"); !strings.Contains(l, "not served") || !strings.Contains(l, "no member ready") {
		t.Errorf("a group of a group that serves nothing: %s", l)
	}
	// magpie group <id> names the found group as a routing group
	for _, id := range []string{"of-found", "of-public"} {
		g, err := findGroup(id)
		if err != nil {
			t.Fatal(err)
		}
		shown, err := stdoutOf(t, func() error { return showGroup(g) })
		if err != nil || !strings.Contains(shown, "routing group") || strings.Contains(shown, "not served") {
			t.Errorf("magpie group %s: %v\n%s", id, err, shown)
		}
	}
}
