package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// claudeLaunchHome is a sandbox HOME with a relay's models, the user's own
// Claude Code settings.json, and the launch file in a folder of the
// test's.
func claudeLaunchHome(t *testing.T, user string) (home, userPath, launch string) {
	t.Helper()
	home, userPath = claudeDottedHome(t, provider.Provider{ID: "relay", Name: "Relay", Anthropic: "https://example.test", Key: "k",
		Models: []string{"claude-opus-4-6", "claude-sonnet-4-5", "claude-haiku-4-5", "glm-4.6"}}, user)
	dir := filepath.Join(t.TempDir(), "claude-launch")
	was := claudeLaunchDir
	claudeLaunchDir = func() string { return dir }
	t.Cleanup(func() { claudeLaunchDir = was })
	return home, userPath, filepath.Join(dir, "settings.json")
}

// claude-magpie (baranwang on X): `claude` on the user's own account,
// `claude-magpie` through magpie, nothing switched back and forth. The
// launch file carries what a connect writes into settings.json (the
// gateway, its key, a model of magpie's and the tiers), and blanks what
// would take a request past magpie; the user's settings.json is left byte
// for byte, and the Agents page's Claude Code (Wired, Check) still reads
// that file alone.
func TestClaudeLaunchFileIsWiredAndTheUsersIsNot(t *testing.T) {
	user := `{"model": "opus", "env": {"ANTHROPIC_API_KEY": "sk-ant-api03-USER", "CLAUDE_CODE_USE_BEDROCK": "1"}}`
	home, userPath, launch := claudeLaunchHome(t, user)

	path, err := ClaudeLaunch(home)
	if err != nil {
		t.Fatal(err)
	}
	if path != launch {
		t.Fatalf("launch file at %s, want %s", path, launch)
	}
	env := func(k string) (string, bool) { return edit.GetJSON(launch, "env."+k) }
	if v, _ := env("ANTHROPIC_BASE_URL"); v != gateway.URL() {
		t.Fatalf("launch file's base URL %q, want %q:\n%s", v, gateway.URL(), readFile(launch))
	}
	if v, _ := env("ANTHROPIC_AUTH_TOKEN"); v != gateway.Token {
		t.Fatalf("launch file's key %q, want %q", v, gateway.Token)
	}
	m, _ := edit.GetJSON(launch, "model")
	if !isMagpie(strings.TrimSuffix(m, "[1m]")) {
		t.Fatalf("launch file's model %q isn't one of magpie's:\n%s", m, readFile(launch))
	}
	if v, _ := env("ANTHROPIC_DEFAULT_HAIKU_MODEL"); !isMagpie(v) {
		t.Fatalf("launch file's haiku tier %q isn't one of magpie's:\n%s", v, readFile(launch))
	}
	for _, k := range claudeLaunchBlanks() {
		if v, has := env(k); !has || v != "" {
			t.Errorf("launch file's %s is %q (set %v), want blank over the user's", k, v, has)
		}
	}
	if got := readFile(userPath); got != user {
		t.Fatalf("the user's settings.json was written:\n%s", got)
	}
	c := claude(home)
	if c.Wired() {
		t.Fatal("the Agents page's Claude Code reads as wired from the launch file")
	}
	if d := c.Drift(); d != nil {
		t.Fatalf("the user's own Claude Code drifts after a launch: %+v", *d)
	}
	for _, a := range All() {
		if a.ID == ClaudeLaunchID {
			t.Fatal("the launch file is listed among the agents")
		}
	}
	// a stand-in for a Claude model a claude-magpie session names, from
	// the launch file while the user's own file is off magpie
	if s := StandIn("claude", "claude-opus-4-6-20260101"); s == "" {
		t.Error("no stand-in for a claude-magpie session's model")
	}

	// a second start keeps the model the file has
	if err := claudeLaunch(home).Apply("model", "relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaudeLaunch(home); err != nil {
		t.Fatal(err)
	}
	if got := claudeLaunch(home).Field("model").Get(); got != "relay/glm-4.6" {
		t.Fatalf("a second start set the model to %q, want the one picked", got)
	}
	if got := readFile(userPath); got != user {
		t.Fatalf("the user's settings.json was written on a second start:\n%s", got)
	}
}

// While magpie is shared at an address beyond loopback, the gateway takes
// its sharing key alone: the launch file carries that, and is set again
// when the gateway moves.
func TestClaudeLaunchFileTakesTheSharedKey(t *testing.T) {
	home, _, launch := claudeLaunchHome(t, `{}`)
	if _, err := ClaudeLaunch(home); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ADDR", "172.23.80.1:3425")
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { access.ConfigureLAN(false, false) })
	lan := access.LANSecret()
	if _, err := ClaudeLaunch(home); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(launch, "env.ANTHROPIC_BASE_URL"); v != "http://172.23.80.1:3425" {
		t.Fatalf("launch file's base URL %q after the gateway moved:\n%s", v, readFile(launch))
	}
	if v, _ := edit.GetJSON(launch, "env.ANTHROPIC_AUTH_TOKEN"); v != lan {
		t.Fatalf("launch file's key %q, want the LAN sharing key", v)
	}
}

// Claude Code saves a /model pick to the user's settings.json, never to a
// --settings file (2.1.318). A magpie model picked in a claude-magpie
// session goes to the launch file, and the user's file gets its model
// back, so a plain claude isn't left asking Anthropic for it; a Claude
// model of its own stays where Claude Code put it.
func TestClaudeLaunchAfterTakesBackAMagpiePick(t *testing.T) {
	home, userPath, launch := claudeLaunchHome(t, `{"model": "opus", "theme": "dark"}`)
	if _, err := ClaudeLaunch(home); err != nil {
		t.Fatal(err)
	}
	mark := ClaudeLaunchBefore(home)
	// what /model leaves: the launch file's spelling of a magpie model
	if err := edit.SetJSON(userPath, edit.KV{Path: "model", Value: "relay/glm-4.6"}); err != nil {
		t.Fatal(err)
	}
	if err := ClaudeLaunchAfter(home, mark); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(userPath, "model"); v != "opus" {
		t.Fatalf("the user's model is %q after the session, want opus back:\n%s", v, readFile(userPath))
	}
	if v, _ := edit.GetJSON(userPath, "theme"); v != "dark" {
		t.Fatalf("the user's theme is %q", v)
	}
	if got := claudeLaunch(home).Field("model").Get(); got != "relay/glm-4.6" {
		t.Fatalf("the launch file's model is %q, want the pick:\n%s", got, readFile(launch))
	}

	// Claude Code's own opus -> opus[1m] at a start (with the launch
	// file's opus tier on 1M) is taken back too, and the launch file keeps
	// its pick
	if err := edit.SetJSON(userPath, edit.KV{Path: "model", Value: "opus"}); err != nil {
		t.Fatal(err)
	}
	mark = ClaudeLaunchBefore(home)
	if err := edit.SetJSON(userPath, edit.KV{Path: "model", Value: "opus[1m]"}); err != nil {
		t.Fatal(err)
	}
	if err := ClaudeLaunchAfter(home, mark); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(userPath, "model"); v != "opus" {
		t.Fatalf("the user's model is %q after Claude Code's rewrite, want opus back", v)
	}
	if got := claudeLaunch(home).Field("model").Get(); got != "relay/glm-4.6" {
		t.Fatalf("the launch file's model is %q after the rewrite, want the pick kept", got)
	}

	// no model before: none after
	if err := edit.DelJSON(userPath, "model"); err != nil {
		t.Fatal(err)
	}
	mark = ClaudeLaunchBefore(home)
	if err := edit.SetJSON(userPath, edit.KV{Path: "model", Value: "relay/claude-sonnet-4-5"}); err != nil {
		t.Fatal(err)
	}
	if err := ClaudeLaunchAfter(home, mark); err != nil {
		t.Fatal(err)
	}
	if v, has := edit.GetJSON(userPath, "model"); has {
		t.Fatalf("the user's file has model %q, which it hadn't before", v)
	}

	// a Claude model of its own stays
	mark = ClaudeLaunchBefore(home)
	if err := edit.SetJSON(userPath, edit.KV{Path: "model", Value: "sonnet"}); err != nil {
		t.Fatal(err)
	}
	if err := ClaudeLaunchAfter(home, mark); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(userPath, "model"); v != "sonnet" {
		t.Fatalf("the user's own pick is %q, want sonnet left", v)
	}
	if _, err := os.Stat(launch); err != nil {
		t.Fatal(err)
	}
}
