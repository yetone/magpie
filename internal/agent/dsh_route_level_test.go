package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// dshStartEffort is the reasoningEffort of patch list f's start entry.
func dshStartEffort(t *testing.T, f string) string {
	t.Helper()
	_, items, err := dshLook(f)
	if err != nil {
		t.Fatal(err)
	}
	if i := dshFindLast(items, "agent-default-model"); i >= 0 {
		return dshConfig(items[i])["reasoningEffort"]
	}
	return ""
}

// dshSaveStartEffort writes e into f's start entry as dsh's own composer
// pick does: the entry keeps magpie's mark (#1392).
func dshSaveStartEffort(t *testing.T, f, e string) {
	t.Helper()
	head, items, err := dshRead(f)
	if err != nil {
		t.Fatal(err)
	}
	i := dshFindLast(items, "agent-default-model")
	if i < 0 {
		t.Fatalf("no start in %s", f)
	}
	n, err := dshNode(items[i])
	if err != nil {
		t.Fatal(err)
	}
	yamlSetKey(yamlMapping(n, "config"), "reasoningEffort", &yaml.Node{Kind: yaml.ScalarNode, Value: e})
	if items[i].lines, err = dshNodeLines(n); err != nil {
		t.Fatal(err)
	}
	if err := dshWrite(f, head, items); err != nil {
		t.Fatal(err)
	}
}

// A thinking level the user set on one of magpie's routes by hand
// (providers.magpie-responses.reasoning, ming_li52905 on X) is what dsh's
// sessions on that route start with: magpie's thinking default no longer
// names an effort over it, on a sync, a round, a switch or a set.
func TestDshRouteLevelOfTheUsersOwn(t *testing.T) {
	home, dir, web := dshProtocolHome(t)
	dshKeyFixture(dir)
	a := dsh(home)
	if err := a.Field("model").Set("magpie/resp/gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("effort").Set("medium"); err != nil {
		t.Fatal(err)
	}
	if e := dshStartEffort(t, web); e != "medium" {
		t.Fatalf("before the level: start effort %q, want medium", e)
	}
	// the user's own level, by hand, in the profile and in settings.yaml
	b, err := os.ReadFile(web)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), "      magpie-responses:\n", "      magpie-responses:\n        reasoning: high\n", 1)
	if s == string(b) {
		t.Fatalf("no magpie-responses route in the profile:\n%s", b)
	}
	if err := os.WriteFile(web, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(dir, "settings.yaml")
	sb, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	ss := strings.Replace(string(sb), "    magpie-responses:\n", "    magpie-responses:\n      reasoning: high\n", 1)
	if ss == string(sb) {
		t.Fatalf("no magpie-responses route in settings.yaml:\n%s", sb)
	}
	if err := os.WriteFile(settings, []byte(ss), 0o600); err != nil {
		t.Fatal(err)
	}
	level := func(step, want string) {
		t.Helper()
		if !strings.Contains(dshRouteLevelText(t, web), "reasoning: "+want) {
			t.Errorf("%s: profile's route level gone or not %s:\n%s", step, want, dshRead1(t, web))
		}
		if b, _ := os.ReadFile(settings); !strings.Contains(string(b), "reasoning: "+want) {
			t.Errorf("%s: settings.yaml's route level gone or not %s:\n%s", step, want, b)
		}
		if e := dshStartEffort(t, web); e != "" {
			t.Errorf("%s: start still names effort %q over the route's %s:\n%s", step, e, want, dshRead1(t, web))
		}
		if got := a.Field("effort").Get(); got != want {
			t.Errorf("%s: thinking reads %q, want the route's %s", step, got, want)
		}
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	level("sync", "high")
	before := dshRead1(t, web)
	dshWiredOnce()
	level("round", "high")
	if after := dshRead1(t, web); after != before {
		t.Errorf("a round after the sync rewrote the profile:\n%s\n----\n%s", before, after)
	}
	if err := a.Field("model").Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/resp/gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	level("switch back", "high")
	if err := a.Field("effort").Set("low"); err != nil {
		t.Fatal(err)
	}
	level("thinking low", "low")

	// an effort dsh saves itself into the start is the user's and stays
	dshSaveStartEffort(t, web, "medium")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	dshWiredOnce()
	if e := dshStartEffort(t, web); e != "medium" {
		t.Errorf("dsh's own saved effort went: %q, want medium:\n%s", e, dshRead1(t, web))
	}
}

// A route with no level of its own still starts on magpie's thinking.
func TestDshRouteWithoutLevelTakesMagpiesEffort(t *testing.T) {
	home, dir, web := dshProtocolHome(t)
	dshKeyFixture(dir)
	a := dsh(home)
	if err := a.Field("model").Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("effort").Set("max"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	dshWiredOnce()
	if e := dshStartEffort(t, web); e != "max" {
		t.Errorf("start effort %q, want magpie's max:\n%s", e, dshRead1(t, web))
	}
	if got := a.Field("effort").Get(); got != "max" {
		t.Errorf("thinking reads %q, want max", got)
	}
}

func dshRead1(t *testing.T, f string) string {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// dshRouteLevelText is magpie-responses' route in f's llm-pi-ai entry, as
// YAML.
func dshRouteLevelText(t *testing.T, f string) string {
	t.Helper()
	_, items, err := dshLook(f)
	if err != nil {
		t.Fatal(err)
	}
	r := dshRoutesIn(items)["magpie-responses"]
	if r == nil {
		return ""
	}
	out, err := yaml.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A level set in the home layer, whose llm-pi-ai entry dsh reads over each
// profile's, is the route's level for the profiles and the home layer's own
// start alike.
func TestDshRouteLevelInTheHomeLayer(t *testing.T) {
	home, dir, web := dshProtocolHome(t)
	dshKeyFixture(dir)
	a := dsh(home)
	if err := a.Field("model").Set("magpie/resp/gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("effort").Set("medium"); err != nil {
		t.Fatal(err)
	}
	hp := dshHomePatch(dir)
	s := strings.Replace(dshRead1(t, web), "      magpie-responses:\n", "      magpie-responses:\n        reasoning: high\n", 1)
	if err := os.WriteFile(hp, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	dshWiredOnce()
	for _, f := range []string{web, hp} {
		if e := dshStartEffort(t, f); e != "" {
			t.Errorf("%s: start still names effort %q over the home layer's high:\n%s", f, e, dshRead1(t, f))
		}
	}
	if !strings.Contains(dshRouteLevelText(t, hp), "reasoning: high") {
		t.Errorf("home layer's level gone:\n%s", dshRead1(t, hp))
	}
	if got := a.Field("effort").Get(); got != "high" {
		t.Errorf("thinking reads %q, want the home layer's high", got)
	}
	if err := a.Field("effort").Set("low"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dshRouteLevelText(t, hp), "reasoning: low") {
		t.Errorf("thinking low left the home layer's level:\n%s", dshRead1(t, hp))
	}
	for _, f := range []string{web, hp} {
		if e := dshStartEffort(t, f); e != "" {
			t.Errorf("%s: thinking low wrote effort %q:\n%s", f, e, dshRead1(t, f))
		}
	}
}
