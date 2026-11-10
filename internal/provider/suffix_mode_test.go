package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// Provider in model names has three ways (#92): on, as by default and as a
// settings.json from before has it, every name with its provider's after
// it, one the user gave too (#203); own, a name the user gave just as they
// wrote it, the vendor's with their provider's; off, none (#335), as an
// older plainNames: true still reads. Each change tells the agents; one to
// the way already set doesn't, and a way of no such name is refused.
func TestSuffixMode(t *testing.T) {
	prefsHome(t)
	touched := 0
	catalog.Changed = func() { touched++ }
	t.Cleanup(func() { catalog.Changed = nil })
	if err := SetModelName("a/sol", "My Sol"); err != nil {
		t.Fatal(err)
	}
	touched = 0
	codex := func() []string {
		var out []string
		for _, m := range CodexListed() {
			out = append(out, m.ID+"="+m.Name)
		}
		slices.Sort(out)
		return out
	}

	if SuffixMode() != SuffixOn {
		t.Fatalf("default %q", SuffixMode())
	}
	if got := codex(); !slices.Equal(got, []string{"a/sol=My Sol · A", "auto/sol=Sol · routing group", "b/sol=Sol · B"}) {
		t.Fatalf("on: %q", got)
	}

	if err := SetSuffixMode(SuffixOwn); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); SuffixMode() != SuffixOwn || s.PlainNames || !s.PlainOwnNames || touched != 1 {
		t.Fatalf("own: %q %+v, told %d", SuffixMode(), s, touched)
	}
	if got := codex(); !slices.Equal(got, []string{"a/sol=My Sol", "auto/sol=Sol · routing group", "b/sol=Sol · B"}) {
		t.Fatalf("own: %q", got)
	}
	if err := SetSuffixMode(SuffixOwn); err != nil || touched != 1 {
		t.Fatal(err, touched)
	}

	if err := SetSuffixMode(SuffixOff); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); SuffixMode() != SuffixOff || !s.PlainNames || s.PlainOwnNames || touched != 2 {
		t.Fatalf("off: %q %+v, told %d", SuffixMode(), s, touched)
	}
	// b's sol and the group found for it would both read "Sol": the group
	// keeps "· routing group", which tells them apart
	if got := codex(); !slices.Equal(got, []string{"a/sol=My Sol", "auto/sol=Sol · routing group", "b/sol=Sol"}) {
		t.Fatalf("off: %q", got)
	}

	// the two-way switch of before: off is plain, on is on again
	if err := SetPlainNames(false); err != nil {
		t.Fatal(err)
	}
	if SuffixMode() != SuffixOn || touched != 3 {
		t.Fatalf("plain names off: %q, told %d", SuffixMode(), touched)
	}
	// a settings.json of before, plainNames: true, reads as off
	s := settings.Load()
	s.PlainNames = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if SuffixMode() != SuffixOff {
		t.Fatalf("plainNames: true reads %q", SuffixMode())
	}
	if err := SetSuffixMode("sometimes"); err == nil {
		t.Fatal("took sometimes")
	}
}

// An agent's own Provider in model names (KillTry27355 on X) is over every
// agent's for that agent alone, by the id its lists are kept by: its WSL
// copy and a named profile read it too, and another agent reads every
// agent's. "" gives it back, and each change tells the agents.
func TestAgentSuffix(t *testing.T) {
	prefsHome(t)
	touched := 0
	catalog.Changed = func() { touched++ }
	t.Cleanup(func() { catalog.Changed = nil })
	if err := SetSuffixMode(SuffixOff); err != nil {
		t.Fatal(err)
	}
	touched = 0
	if err := SetAgentSuffix(" Pi ", SuffixOn); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); s.AgentSuffix["pi"] != SuffixOn || !s.PlainNames || touched != 1 {
		t.Fatalf("%+v, told %d", s, touched)
	}
	for agent, want := range map[string]string{"pi": SuffixOn, "PI": SuffixOn, "pi@wsl:Ubuntu": SuffixOn, "pi#work": SuffixOn, "codex": SuffixOff, "": SuffixOff} {
		if got := SuffixModeFor(agent); got != want {
			t.Errorf("%q reads %q, want %q", agent, got, want)
		}
	}
	if AgentSuffix("pi") != SuffixOn || AgentSuffix("codex") != "" {
		t.Fatalf("own %q %q", AgentSuffix("pi"), AgentSuffix("codex"))
	}
	es := []Entry{{ID: "relay/glm-5", Name: "GLM-5", Provider: Provider{ID: "relay", Name: "Relay"}}}
	if got := LabelsFor("pi", es); !slices.Equal(got, []string{"GLM-5 · Relay"}) {
		t.Fatalf("pi %q", got)
	}
	if got := LabelsFor("codex", es); !slices.Equal(got, []string{"GLM-5"}) {
		t.Fatalf("codex %q", got)
	}
	if err := SetAgentSuffix("pi", ""); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); len(s.AgentSuffix) != 0 || SuffixModeFor("pi") != SuffixOff || touched != 2 {
		t.Fatalf("back: %+v, told %d", s, touched)
	}
	if SetAgentSuffix("pi", "sometimes") == nil || SetAgentSuffix("", SuffixOn) == nil {
		t.Fatal("took a bad mode or no agent")
	}
}
