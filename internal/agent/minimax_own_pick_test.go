package agent

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// Picking one of MiniMax Code's own models, on the Agents page or in its
// own /model, leaves a connected MiniMax Code connected: its /model lists
// the user's models beside magpie's, so the pick moves the default and
// nothing else, and the switch reads on until the user turns it off, which
// takes magpie's entry out and leaves their pick (the sibling of
// yingxianyu's Kimi Code report on X).
func TestMiniMaxCodeOwnPickKeepsItConnected(t *testing.T) {
	home, path := miniMaxHome(t)
	a := miniMax(home)
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() {
		_, raw := readMiniMax(t, path)
		t.Fatalf("not connected:\n%s", raw)
	}
	if err := a.Pick("model", "custom_provider:work/deep-reasoner-1"); err != nil {
		t.Fatal(err)
	}
	a = miniMax(home)
	c, raw := readMiniMax(t, path)
	if !a.Wired() {
		t.Fatalf("picking one of the user's own switched it off:\n%s", raw)
	}
	if c.DefaultModel != "custom_provider:work/deep-reasoner-1" || c.CustomProvider["magpie"].Kind != "custom" || len(c.CustomProvider["magpie"].Models) == 0 {
		t.Fatalf("own pick:\n%s", raw)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift on the user's own pick: %+v", d)
	}
	// a pick in MiniMax Code's own /model reads the same
	if err := edit.SetYAML(path, edit.KV{Path: "defaultModel", Value: "minimax/MiniMax-M2.7"}); err != nil {
		t.Fatal(err)
	}
	if a = miniMax(home); !a.Wired() || a.Drift() != nil {
		t.Fatalf("a pick in MiniMax Code's /model read as off (wired %v, drift %+v)", a.Wired(), a.Drift())
	}
	if err := a.Pick("model", "custom_provider:work/deep-reasoner-1"); err != nil {
		t.Fatal(err)
	}

	// the switch turned off: magpie out, the pick stays
	if err := miniMax(home).Disconnect(); err != nil {
		t.Fatal(err)
	}
	a = miniMax(home)
	c, raw = readMiniMax(t, path)
	if a.Wired() || strings.Contains(raw, "magpie") {
		t.Fatalf("disconnect left magpie in:\n%s", raw)
	}
	if c.DefaultModel != "custom_provider:work/deep-reasoner-1" || c.CustomProvider["work"].Kind != "custom" {
		t.Fatalf("disconnect moved the user's pick:\n%s", raw)
	}
	// and on again
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	if a = miniMax(home); !a.Wired() {
		_, raw = readMiniMax(t, path)
		t.Fatalf("on again, not connected:\n%s", raw)
	}
}

// A custom provider the user named magpie themselves, magpie never
// connected, isn't read as connected, and a pick of their own leaves it.
func TestMiniMaxCodeOwnPickLeavesAHandMadeMagpie(t *testing.T) {
	home, path := miniMaxHome(t)
	_, raw := readMiniMax(t, path)
	hand := strings.Replace(raw, "custom_provider:\n", `custom_provider:
  magpie:
    name: magpie
    kind: custom
    enabled: true
    api: anthropic-messages
    options:
      apiKey: mine
      baseURL: http://127.0.0.1:9
`, 1)
	writeFile(t, path, hand)
	a := miniMax(home)
	if a.Wired() {
		t.Fatal("a hand-made magpie provider read as connected")
	}
	if err := a.Pick("model", "custom_provider:work/deep-reasoner-1"); err != nil {
		t.Fatal(err)
	}
	c, raw := readMiniMax(t, path)
	if a = miniMax(home); a.Wired() || c.DefaultModel != "custom_provider:work/deep-reasoner-1" || c.CustomProvider["magpie"].Options["apiKey"] != "mine" {
		t.Fatalf("wired %v:\n%s", a.Wired(), raw)
	}
}
