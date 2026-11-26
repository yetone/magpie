package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// kimiK31Config is Kimi Code signed in to a Kimi Code membership, with its
// k3.1-pro beside the model it starts on, as its own /model writes them.
const kimiK31Config = kimiOwnConfig + `
[models."kimi-code/k3.1-pro"]
provider = "managed:kimi-code"
model = "k3.1-pro"
max_context_size = 262144
capabilities = ["thinking", "always_thinking", "tool_use"]
`

// Picking one of Kimi Code's own models as the lead, on the Agents page,
// leaves a connected Kimi Code connected (yingxianyu on X: picking
// kimi-code/k3.1-pro turned its switch off by itself). Kimi Code's /model
// lists its own models beside magpie's, so the pick changes the model its
// sessions start on and nothing else: magpie's provider and models stay,
// a subagent on one of them keeps it, and the switch reads on until the
// user turns it off, which then takes magpie out and leaves their pick.
func TestKimiOwnLeadKeepsItConnected(t *testing.T) {
	home, path := kimiSubagentHome(t, kimiK31Config)
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	a := kimi(home)
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() {
		t.Fatalf("not connected:\n%s", read())
	}
	if err := a.Pick("subagent_model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}

	if err := a.Pick("model", "kimi-code/k3.1-pro"); err != nil {
		t.Fatal(err)
	}
	a = kimi(home)
	if !a.Wired() {
		t.Fatalf("picking Kimi Code's own k3.1-pro switched it off:\n%s", read())
	}
	if v := a.Values()["model"]; v != "kimi-code/k3.1-pro" {
		t.Fatalf("lead %q, want kimi-code/k3.1-pro:\n%s", v, read())
	}
	if p, _ := edit.GetTOMLTable(path, "providers.magpie"); p == nil {
		t.Fatalf("magpie's provider went with the pick:\n%s", read())
	}
	if m, _ := edit.GetTOMLTable(path, `models."magpie/think/deep"`); m == nil {
		t.Fatalf("magpie's models went with the pick:\n%s", read())
	}
	if v := a.Values()["subagent_model"]; v != "magpie/think/deep" {
		t.Fatalf("subagents %q, want magpie/think/deep as they were:\n%s", v, read())
	}
	kimiSubagentsValid(t, path)
	if d := a.Drift(); d != nil {
		t.Fatalf("drift on the user's own pick: %+v", d)
	}
	// a pick in Kimi Code's own /model reads the same
	if err := edit.SetTOMLTop(path, edit.KV{Path: "default_model", Value: "kimi-code/kimi-for-coding"}); err != nil {
		t.Fatal(err)
	}
	if a = kimi(home); !a.Wired() || a.Drift() != nil {
		t.Fatalf("a pick in Kimi Code's /model read as off (wired %v, drift %+v)", a.Wired(), a.Drift())
	}
	if err := a.Pick("model", "kimi-code/k3.1-pro"); err != nil {
		t.Fatal(err)
	}
	// and back on one of magpie's
	if err := a.Pick("model", "magpie/think/wide"); err != nil {
		t.Fatal(err)
	}
	if a = kimi(home); !a.Wired() || a.Values()["model"] != "magpie/think/wide" {
		t.Fatalf("back on magpie's: %v\n%s", a.Values(), read())
	}
	if err := a.Pick("model", "kimi-code/k3.1-pro"); err != nil {
		t.Fatal(err)
	}

	// the switch turned off: magpie out, the lead the user picked stays
	if err := kimi(home).Disconnect(); err != nil {
		t.Fatal(err)
	}
	a = kimi(home)
	if a.Wired() || strings.Contains(read(), "magpie") {
		t.Fatalf("disconnect left magpie in:\n%s", read())
	}
	if v := a.Values()["model"]; v != "kimi-code/k3.1-pro" {
		t.Fatalf("disconnect moved the lead to %q:\n%s", v, read())
	}
	kimiSubagentsValid(t, path)
	// and on again
	if _, err := a.ConnectHow(); err != nil {
		t.Fatalf("on again: %v\n%s", err, read())
	}
	if a = kimi(home); !a.Wired() {
		t.Fatalf("on again, not connected:\n%s", read())
	}
	kimiSubagentsValid(t, path)
}

// One of Kimi Code's own models picked while magpie isn't connected
// doesn't connect it, and leaves a provider named magpie the user wrote
// themselves as it is: it is theirs (handWired).
func TestKimiOwnLeadOffMagpieTouchesNothingOfMagpie(t *testing.T) {
	hand := kimiK31Config + `
[providers.magpie]
type = "kimi"
base_url = "http://127.0.0.1:9/v1"
api_key = "mine"
`
	home, path := kimiSubagentHome(t, hand)
	a := kimi(home)
	if a.Wired() {
		t.Fatal("a hand-written magpie provider read as connected")
	}
	if err := a.Pick("model", "kimi-code/k3.1-pro"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if a = kimi(home); a.Wired() || a.Values()["model"] != "kimi-code/k3.1-pro" {
		t.Fatalf("wired %v, model %q:\n%s", a.Wired(), a.Values()["model"], b)
	}
	if p, _ := edit.GetTOMLTable(path, "providers.magpie"); p["api_key"] != "mine" {
		t.Fatalf("the user's own provider named magpie was changed:\n%s", b)
	}
}

// The same with the subagents left on the lead's model: nothing but
// magpie's provider says Kimi Code is connected then.
func TestKimiOwnLeadAloneKeepsItConnected(t *testing.T) {
	home, path := kimiSubagentHome(t, kimiK31Config)
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	a := kimi(home)
	if _, err := a.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	if err := a.Pick("model", "kimi-code/k3.1-pro"); err != nil {
		t.Fatal(err)
	}
	if a = kimi(home); !a.Wired() || a.Values()["subagent_model"] != "" {
		t.Fatalf("picking k3.1-pro switched it off (wired %v):\n%s", a.Wired(), read())
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if a = kimi(home); a.Wired() || strings.Contains(read(), "magpie") || a.Values()["model"] != "kimi-code/k3.1-pro" {
		t.Fatalf("disconnect:\n%s", read())
	}
}
