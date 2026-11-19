package tui

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// paul-tian's #6881: the routing group list said Fast for a member whose
// speed is set Ultrafast, while the group's editor said Ultrafast. The
// group view's member line says Ultrafast too: the tier is saved on the
// group (Group.Ultra), and a line that reads Fast alone mislabels it.
// ggbdpq's test, from the patch posted on #6881.
func TestGroupMemberUltraShown(t *testing.T) {
	h := home(t)
	fakeCodex(t, h)
	// the group of the report: a Codex GPT-6-Astra member set Ultrafast
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-6-astra"}}); err != nil {
		t.Fatal(err)
	}
	id := "codex/gpt-6-astra"
	if err := provider.SaveGroup(provider.Group{ID: "gpt-6-astra-ultrafast", Name: "GPT-6-Astra-Ultrafast", Members: []string{id}, Fast: []string{id}, Ultra: []string{id}}); err != nil {
		t.Fatal(err)
	}
	m := press(t, model{w: 120, h: 40}, "3", "enter")
	if m.mode != modeGroup || m.gsel != 0 {
		t.Fatalf("mode %d, picked %d", m.mode, m.gsel)
	}
	v := m.View()
	if !strings.Contains(v, "· Ultrafast") {
		t.Fatalf("the member set Ultrafast is not shown as Ultrafast:\n%s", v)
	}
	if strings.Contains(v, "· fast") {
		t.Fatalf("the member set Ultrafast is shown as fast:\n%s", v)
	}
}

// A member set fast alone keeps the fast word: Ultrafast takes its place
// only on one the group sends Ultrafast (Group.Ultra is a part of Fast).
func TestGroupMemberFastStillShown(t *testing.T) {
	home(t)
	if err := provider.Save(provider.Provider{ID: "an", Name: "Anthropic", Key: "k", Anthropic: "https://api.anthropic.com", Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	id := "an/claude-opus-5-5"
	if err := provider.SaveGroup(provider.Group{ID: "opus", Name: "Opus", Members: []string{id}, Fast: []string{id}}); err != nil {
		t.Fatal(err)
	}
	m := press(t, model{w: 120, h: 40}, "3", "enter")
	if m.mode != modeGroup || m.gsel != 0 {
		t.Fatalf("mode %d, picked %d", m.mode, m.gsel)
	}
	if v := m.View(); !strings.Contains(v, "· fast") || strings.Contains(v, "· Ultrafast") {
		t.Fatalf("the member set fast is not shown as fast:\n%s", v)
	}
}
