package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/testenv"
)

// fakeRTK is an rtk on PATH that acts as rtk 0.50's installer does for
// Claude Code, OpenCode and Cursor: installing OpenCode or Cursor gives it to
// Claude Code as well. Its uninstaller isn't there: magpie doesn't use it.
const fakeRTK = `#!/bin/sh
c="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
claude() { [ -d "$c" ] && echo '{"hooks":{"PreToolUse":[{"hooks":[{"command":"rtk hook claude"}]}]}}' > "$c/settings.json"; }
case "$1" in
--version) echo "rtk 9.9.9"; exit 0 ;;
gain) echo '{"summary":{"total_commands":3,"total_input":100,"total_saved":60,"avg_savings_pct":60}}'; exit 0 ;;
esac
shift 2
case "$*" in
"--auto-patch") claude ;;
"--opencode --auto-patch") claude; /bin/mkdir -p "$XDG_CONFIG_HOME/opencode/plugins"; echo x > "$XDG_CONFIG_HOME/opencode/plugins/rtk.ts" ;;
"--agent cursor --auto-patch") claude; echo '{"hooks":{"preToolUse":[{"command":"rtk hook cursor"}]}}' > "$HOME/.cursor/hooks.json" ;;
*) echo "unknown: $*" >&2; exit 2 ;;
esac
`

func TestRTK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake rtk is a shell script")
	}
	h := sandbox(t)
	bin := filepath.Join(h, "bin")
	testenv.Program(t, filepath.Join(bin, "rtk"), fakeRTK)
	t.Setenv("PATH", bin)
	state := func() map[string]bool {
		m := map[string]bool{}
		for _, a := range ReadRTK().Agents {
			m[a.ID] = a.On
		}
		return m
	}
	want := func(claude, opencode, cursor bool) {
		t.Helper()
		s := state()
		if s["claude"] != claude || s["opencode"] != opencode || s["cursor"] != cursor {
			t.Fatalf("claude, opencode, cursor = %v, %v, %v; want %v, %v, %v", s["claude"], s["opencode"], s["cursor"], claude, opencode, cursor)
		}
	}
	set := func(id string, on bool) {
		t.Helper()
		if _, err := SetRTK(id, on); err != nil {
			t.Fatal(err)
		}
	}

	v := ReadRTK()
	if v.Version != "9.9.9" || v.Gain == nil || v.Gain.Saved != 60 {
		t.Fatalf("version %q, gain %+v", v.Version, v.Gain)
	}
	want(false, false, false)
	// OpenCode's and Cursor's installers leave Claude Code as it was
	set("opencode", true)
	set("cursor", true)
	want(false, true, true)
	set("claude", true)
	want(true, true, true)
	// taking it out of Claude Code leaves the other two as they are
	set("claude", false)
	want(false, true, true)
	set("claude", true)
	set("opencode", false)
	want(true, false, true)
	set("cursor", false)
	want(true, false, false)

	if _, err := SetRTK("goose", true); err == nil {
		t.Fatal("goose has no rtk hook, yet it was switched on")
	}
	t.Setenv("PATH", "")
	if v := ReadRTK(); v.Path != "" {
		t.Fatalf("rtk found at %s with nothing on PATH", v.Path)
	}
	if _, err := SetRTK("opencode", true); err == nil {
		t.Fatal("switched on without rtk installed")
	}
	// the hook of an rtk since removed can still be taken out
	set("claude", false)
	want(false, false, false)
}

// TestRTKRemove: switching an agent off takes out just what rtk's installer
// put in, as rtk 0.50 writes it, with no rtk to do it.
func TestRTKRemove(t *testing.T) {
	h := sandbox(t)
	hook := func(key, matcher, cmd string) string {
		quoted, _ := json.Marshal(cmd) // a Windows path has backslashes
		return `"hooks": {
    "` + key + `": [
      {
        "matcher": "` + matcher + `",
        "hooks": [
          {
            "type": "command",
            "command": ` + string(quoted) + `
          }
        ]
      }
    ]
  }`
	}
	claude := filepath.Join(h, ".claude")
	write(t, filepath.Join(claude, "settings.json"), `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Write",
        "hooks": [{"type": "command", "command": "lint"}]
      },
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "rtk hook claude"
          }
        ]
      }
    ]
  }
}
`)
	write(t, filepath.Join(claude, "CLAUDE.md"), "# mine\n\n@RTK.md\n")
	write(t, filepath.Join(claude, "RTK.md"), "# RTK\n")
	codex := filepath.Join(h, ".codex")
	write(t, filepath.Join(codex, "hooks.json"), "{\n  "+hook("PreToolUse", "Bash", "rtk hook codex")+"\n}\n")
	write(t, filepath.Join(codex, "AGENTS.md"), "@"+filepath.Join(codex, "RTK.md")+"\n")
	write(t, filepath.Join(codex, "RTK.md"), "# RTK\n")
	gemini := filepath.Join(h, ".gemini")
	sh := filepath.Join(gemini, "hooks", "rtk-hook-gemini.sh")
	write(t, filepath.Join(gemini, "settings.json"), "{\n  \"theme\": \"dark\",\n  "+hook("BeforeTool", "run_shell_command", sh)+"\n}\n")
	write(t, sh, "#!/bin/sh\n")
	write(t, filepath.Join(gemini, "hooks", ".rtk-hook.sha256"), "x\n")
	write(t, filepath.Join(gemini, "GEMINI.md"), "# g\n")
	write(t, filepath.Join(h, ".cursor", "hooks.json"), `{"version":1,"hooks":{"preToolUse":[{"command":"rtk hook cursor","matcher":"Shell"}]}}`)
	copilot := filepath.Join(h, ".copilot")
	write(t, filepath.Join(copilot, "hooks", "rtk-rewrite.json"), "{}")
	write(t, filepath.Join(copilot, "copilot-instructions.md"), "# c\n\n<!-- rtk-instructions v2 -->\nAlways prefix shell commands with rtk\n<!-- /rtk-instructions -->\n")
	write(t, filepath.Join(h, ".pi", "agent", "extensions", "rtk.ts"), "x")
	write(t, filepath.Join(h, ".config", "opencode", "plugins", "rtk.ts"), "x")

	ids := []string{"claude", "codex", "gemini", "cursor", "copilot", "pi", "opencode"}
	v := ReadRTK()
	if v.Path != "" {
		t.Fatalf("rtk found at %s", v.Path)
	}
	on := map[string]bool{}
	for _, a := range v.Agents {
		on[a.ID] = a.On
	}
	for _, id := range ids {
		if !on[id] {
			t.Fatalf("%s's hook isn't seen: %v", id, on)
		}
		if _, err := SetRTK(id, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range ReadRTK().Agents {
		if a.On {
			t.Fatalf("%s still has rtk", a.ID)
		}
	}
	jsonIs := func(p, want string) {
		t.Helper()
		var got, w any
		if err := json.Unmarshal([]byte(read(t, p)), &got); err != nil {
			t.Fatalf("%s: %v\n%s", p, err, read(t, p))
		}
		json.Unmarshal([]byte(want), &w)
		if !reflect.DeepEqual(got, w) {
			t.Fatalf("%s:\n%s\nwant %s", p, read(t, p), want)
		}
	}
	jsonIs(filepath.Join(claude, "settings.json"), `{"model":"opus","hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"lint"}]}]}}`)
	jsonIs(filepath.Join(codex, "hooks.json"), `{}`)
	jsonIs(filepath.Join(gemini, "settings.json"), `{"theme":"dark"}`)
	jsonIs(filepath.Join(h, ".cursor", "hooks.json"), `{"version":1}`)
	for p, want := range map[string]string{
		filepath.Join(claude, "CLAUDE.md"):                  "# mine\n",
		filepath.Join(copilot, "copilot-instructions.md"):   "# c\n",
		filepath.Join(gemini, "GEMINI.md"):                  "# g\n",
		filepath.Join(claude, "RTK.md"):                     "",
		filepath.Join(codex, "AGENTS.md"):                   "",
		filepath.Join(codex, "RTK.md"):                      "",
		sh:                                                  "",
		filepath.Join(copilot, "hooks", "rtk-rewrite.json"): "",
	} {
		if got := read(t, p); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
}

// TestRTKRemoveClaudeOtherEvents (#2879): rtk 1.0.1's installer writes its
// hook under hooks.PostToolUse too, when the rtk pipeline has after_tool
// rules (its src/hooks/init/claude.rs: "enabling rtk:posthook adds
// PostToolUse"), and its uninstaller takes its entries out of PreToolUse,
// PostToolUse and SessionStart alike. magpie says Claude Code has rtk by
// the file whole, so an entry under another event kept the row on: turning
// it off took only PreToolUse out, then said "rtk's hook is still in
// Claude". The user's own hooks under those events stay.
func TestRTKRemoveClaudeOtherEvents(t *testing.T) {
	h := sandbox(t)
	claude := filepath.Join(h, ".claude")
	write(t, filepath.Join(claude, "settings.json"), `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{"type": "command", "command": "rtk hook claude"}]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "Write",
        "hooks": [{"type": "command", "command": "lint"}]
      },
      {
        "matcher": "Bash",
        "hooks": [{"type": "command", "command": "rtk hook claude"}]
      }
    ],
    "SessionStart": [
      {
        "matcher": "startup",
        "hooks": [{"type": "command", "command": " greet"}]
      },
      {
        "matcher": "*",
        "hooks": [{"type": "command", "command": "/opt/homebrew/bin/rtk hook claude"}]
      }
    ]
  }
}
`)
	if _, err := SetRTK("claude", false); err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(claude, "settings.json"))), &s); err != nil {
		t.Fatal(err)
	}
	if s.Model != "opus" {
		t.Errorf("model: %q", s.Model)
	}
	for event, entries := range s.Hooks {
		for _, e := range entries {
			for _, h := range e.Hooks {
				if strings.Contains(h.Command, "rtk hook claude") {
					t.Errorf("%s holds %q (matcher %q)", event, h.Command, e.Matcher)
				}
			}
		}
	}
	if got := s.Hooks["PostToolUse"]; len(got) != 1 || got[0].Matcher != "Write" || len(got[0].Hooks) != 1 || got[0].Hooks[0].Command != "lint" {
		t.Errorf("the user's PostToolUse hook didn't stay: %+v", got)
	}
	if got := s.Hooks["SessionStart"]; len(got) != 1 || got[0].Matcher != "startup" || len(got[0].Hooks) != 1 || got[0].Hooks[0].Command != " greet" {
		t.Errorf("the user's SessionStart hook didn't stay: %+v", got)
	}
	if _, ok := s.Hooks["PreToolUse"]; ok {
		t.Errorf("PreToolUse stayed: %+v", s.Hooks["PreToolUse"])
	}
	for _, a := range ReadRTK().Agents {
		if a.ID == "claude" && a.On {
			t.Error("claude still reads as on")
		}
	}
}

// openCodeRTK is an rtk on PATH that says it is version and, for
// rtk init -g --opencode, writes the plugin that version writes: up to 0.51
// one for OpenCode 1 only (hooks/opencode/rtk.ts at v0.51.0), from 0.52 one
// default object with OpenCode 2's id and setup() (rtk-ai/rtk#4187, the
// file at v1.0.1).
func openCodeRTK(version string) string {
	plugin := `export const RtkOpenCodePlugin: Plugin = async ({ $ }) => {`
	if !older(version, 0, 52) {
		plugin = `const RtkOpenCodePlugin = {
  id: "rtk",
  async setup(ctx: any) {},
  async server() {},
}
export default RtkOpenCodePlugin`
	}
	return `#!/bin/sh
case "$1" in
--version) echo "rtk ` + version + `"; exit 0 ;;
esac
shift 2
case "$*" in
"--opencode --auto-patch") /bin/mkdir -p "$XDG_CONFIG_HOME/opencode/plugins"; /bin/cat > "$XDG_CONFIG_HOME/opencode/plugins/rtk.ts" <<'PLUGIN'
` + plugin + `
PLUGIN
;;
*) echo "unknown: $*" >&2; exit 2 ;;
esac
`
}

// TestRTKOpenCode2: OpenCode 2 refuses the OpenCode 1 plugin rtk wrote up
// to 0.51 (rtk-ai/rtk#4311), and loads the one rtk 0.52 on writes
// (rtk-ai/rtk#4187; #2295, witt-bit, on rtk 1.0.1). So with an OpenCode 2
// here an older rtk isn't run for OpenCode, and one given before can still
// be taken out; a newer rtk is, and its plugin is on. An old plugin left
// from before an rtk update counts as off once rtk can write the new one,
// and switching it on writes that over it. An OpenCode 1 gets rtk from
// either.
func TestRTKOpenCode2(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	h := sandbox(t)
	bin := filepath.Join(h, "bin")
	rtk := func(version string) {
		t.Helper()
		testenv.Program(t, filepath.Join(bin, "rtk"), openCodeRTK(version))
	}
	opencode := func(version string) {
		t.Helper()
		testenv.Program(t, filepath.Join(bin, "opencode"), "#!/bin/sh\necho "+version+"\n")
	}
	t.Setenv("PATH", bin)
	plugin := filepath.Join(h, ".config", "opencode", "plugins", "rtk.ts")
	row := func() RTKAgent {
		t.Helper()
		for _, a := range ReadRTK().Agents {
			if a.ID == "opencode" {
				return a
			}
		}
		t.Fatal("opencode isn't listed")
		return RTKAgent{}
	}

	// what each prints for --version
	rtk("0.51.0")
	opencode("opencode v2.0.18")
	if b := row().Blocked; !strings.Contains(b, "OpenCode 2.0.18") || !strings.Contains(b, "0.51.0") {
		t.Fatalf("blocked: %q", b)
	}
	_, err := SetRTK("opencode", true)
	if err == nil || !strings.Contains(err.Error(), "needs RTK 0.52 or newer") {
		t.Fatalf("rtk 0.51 switched on for OpenCode 2: %v", err)
	}
	if exists(plugin) {
		t.Fatal("rtk 0.51's plugin was written for OpenCode 2")
	}
	// one written before (by an older magpie, or rtk init by hand) is on,
	// and goes
	write(t, plugin, "export const RtkOpenCodePlugin: Plugin = async ({ $ }) => {\n")
	if !row().On {
		t.Fatal("an old plugin under OpenCode 2 and rtk 0.51 isn't listed as on, so it can't be taken out")
	}
	if _, err := SetRTK("opencode", false); err != nil {
		t.Fatal(err)
	}
	if exists(plugin) {
		t.Fatal("rtk's plugin is still there")
	}

	// rtk 1.0.1 writes a plugin OpenCode 2 loads
	rtk("1.0.1")
	if a := row(); a.Blocked != "" || a.On {
		t.Fatalf("rtk 1.0.1, OpenCode 2: blocked %q, on %v", a.Blocked, a.On)
	}
	if _, err := SetRTK("opencode", true); err != nil {
		t.Fatalf("rtk 1.0.1 not given to OpenCode 2: %v", err)
	}
	if !contains(plugin, "setup(") || !row().On {
		t.Fatal("OpenCode 2 didn't get rtk 1.0.1's plugin")
	}
	if _, err := SetRTK("opencode", false); err != nil || exists(plugin) {
		t.Fatalf("rtk 1.0.1's plugin not taken out: %v", err)
	}
	// rtk 0.52 is the first that writes it
	rtk("0.52.0")
	if b := row().Blocked; b != "" {
		t.Fatalf("rtk 0.52.0 blocked for OpenCode 2: %q", b)
	}

	// rtk updated over an old plugin: it's off until switched on, which
	// writes the new one
	rtk("1.0.1")
	write(t, plugin, "export const RtkOpenCodePlugin: Plugin = async ({ $ }) => {\n")
	if row().On {
		t.Fatal("OpenCode 1's plugin under OpenCode 2 is listed as on")
	}
	if _, err := SetRTK("opencode", true); err != nil {
		t.Fatal(err)
	}
	if !contains(plugin, "setup(") {
		t.Fatal("the old plugin wasn't written over")
	}
	if err := os.Remove(plugin); err != nil {
		t.Fatal(err)
	}

	// OpenCode 1 gets rtk from an old rtk and a new one
	for _, v := range []string{"0.51.0", "1.0.1"} {
		rtk(v)
		opencode("1.18.32")
		if b := row().Blocked; b != "" {
			t.Fatalf("OpenCode 1 blocked with rtk %s: %q", v, b)
		}
		if _, err := SetRTK("opencode", true); err != nil {
			t.Fatal(err)
		}
		if !exists(plugin) || !row().On {
			t.Fatalf("OpenCode 1 didn't get rtk %s's plugin", v)
		}
		if _, err := SetRTK("opencode", false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDropHermesPlugin(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	for in, want := range map[string]string{
		"model: x\nplugins:\n  enabled:\n    - rtk-rewrite\n":          "model: x\n",
		"model: x\nplugins:\n  enabled:\n    - a\n    - rtk-rewrite\n": "model: x\nplugins:\n  enabled:\n    - a\n",
	} {
		write(t, p, in)
		if err := dropHermesPlugin(p); err != nil {
			t.Fatal(err)
		}
		if got := read(t, p); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

// TestRTKCodexOld: rtk has a hook for Codex from 0.50 on; an older one's
// rtk init --codex only puts @RTK.md in AGENTS.md, so it isn't run for
// Codex, and the Library says to update it. A newer one is.
func TestRTKCodexOld(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake rtk is a shell script")
	}
	h := sandbox(t)
	codex := filepath.Join(h, ".codex")
	bin := filepath.Join(h, "bin")
	rtk := func(version, init string) {
		t.Helper()
		testenv.Program(t, filepath.Join(bin, "rtk"), "#!/bin/sh\ncase \"$1\" in\n--version) echo \"rtk "+version+"\"; exit 0 ;;\ngain) exit 1 ;;\nesac\n"+init+"\n")
	}
	t.Setenv("PATH", bin)
	write(t, filepath.Join(codex, "config.toml"), "")
	blocked := func() string {
		t.Helper()
		for _, a := range ReadRTK().Agents {
			if a.ID == "codex" {
				return a.Blocked
			}
		}
		t.Fatal("codex isn't listed")
		return ""
	}

	rtk("0.49.0", `echo "@$CODEX_HOME/RTK.md" > "$HOME/.codex/AGENTS.md"`)
	if b := blocked(); !strings.Contains(b, "0.49.0") {
		t.Fatalf("blocked: %q", b)
	}
	if _, err := SetRTK("codex", true); err == nil || !strings.Contains(err.Error(), "RTK 0.50 or newer") {
		t.Fatalf("switched on with rtk 0.49: %v", err)
	}
	if exists(filepath.Join(codex, "AGENTS.md")) {
		t.Fatal("rtk 0.49's installer was run")
	}

	rtk("0.50.0", `echo '{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"rtk hook codex"}]}]}}' > "$HOME/.codex/hooks.json"`)
	if b := blocked(); b != "" {
		t.Fatalf("rtk 0.50 blocked: %q", b)
	}
	if _, err := SetRTK("codex", true); err != nil {
		t.Fatal(err)
	}
	for v, want := range map[string]bool{"0.49.9": true, "0.28.2": true, "0.50.0": false, "0.51.0-rc.473": false, "1.0.0": false, "x": false} {
		if older(v, 0, 50) != want {
			t.Errorf("older(%q) = %v", v, !want)
		}
	}
}

// #741: the RTK page said "nothing saved yet" while rtk gain had records.
// rtk's stdout and stderr were read as one, so anything rtk warned about on
// stderr (an outdated hook, a console code page, a table it can't find) went
// in front of the JSON and none of it was read; stdout is now read alone,
// a line around the JSON object is passed over, and an rtk gain that fails
// or prints nothing readable is said instead of "nothing saved".
func TestRTKGainRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake rtk is a shell script")
	}
	h := sandbox(t)
	bin := filepath.Join(h, "bin")
	gain := func(body string) {
		t.Helper()
		testenv.Program(t, filepath.Join(bin, "rtk"), "#!/bin/sh\ncase \"$1\" in\n--version) echo 'rtk 0.51.0'; exit 0 ;;\ngain) "+body+" ;;\nesac\nexit 2\n")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin"+string(os.PathListSeparator)+"/usr/bin")
	summary := `{"summary":{"total_commands":42,"total_input":9000,"total_output":1000,"total_saved":8000,"avg_savings_pct":88.9,"total_time_ms":10,"avg_time_ms":0},"daily":[{"date":"2026-10-03","commands":42,"input_tokens":9000,"output_tokens":1000,"saved_tokens":8000,"savings_pct":88.9,"total_time_ms":10,"avg_time_ms":0}]}`

	gain(`echo '[rtk] warning: no decoder for console code page 936; non-UTF-8 output will be shown with replacement characters' >&2; echo '` + summary + `'; exit 0`)
	if v := ReadRTK(); v.Gain == nil || v.Gain.Commands != 42 || v.Gain.Saved != 8000 || len(v.Days) != 1 || v.GainErr != "" {
		t.Fatalf("stderr warning: gain %+v, days %+v, err %q", v.Gain, v.Days, v.GainErr)
	}
	gain(`echo '[rtk] notice: tee files moved'; echo '` + summary + `'; exit 0`)
	if v := ReadRTK(); v.Gain == nil || v.Gain.Commands != 42 {
		t.Fatalf("stdout notice: gain %+v, err %q", v.Gain, v.GainErr)
	}
	// no commands yet: nothing saved, and nothing wrong
	gain(`echo '{"summary":{"total_commands":0,"total_input":0,"total_saved":0,"avg_savings_pct":0}}'; exit 0`)
	if v := ReadRTK(); v.Gain != nil || v.GainErr != "" {
		t.Fatalf("empty: gain %+v, err %q", v.Gain, v.GainErr)
	}
	gain(`echo 'Error: Failed to initialize tracking database' >&2; echo 'Caused by: database is locked' >&2; exit 1`)
	if v := ReadRTK(); v.Gain != nil || v.GainErr != "rtk gain: Error: Failed to initialize tracking database Caused by: database is locked" {
		t.Fatalf("failed: gain %+v, err %q", v.Gain, v.GainErr)
	}
	gain(`echo 'No tracking data yet.'; exit 0`)
	if v := ReadRTK(); v.Gain != nil || v.GainErr != "rtk gain printed no summary magpie can read: No tracking data yet." {
		t.Fatalf("not JSON: gain %+v, err %q", v.Gain, v.GainErr)
	}
}

// What rtk saves in Codex's Windows sandbox isn't in rtk gain: elevated, its
// commands run as Codex's sandbox account, with that account's AppData;
// unelevated, they can't write the user's. Read from Codex's config.toml.
func TestCodexSandbox(t *testing.T) {
	h := sandbox(t)
	t.Setenv("CODEX_HOME", filepath.Join(h, ".codex"))
	for conf, want := range map[string]string{
		"":                                      "",
		"[windows]\nsandbox = \"elevated\"\n":   "elevated",
		"[windows]\nsandbox = \"unelevated\"\n": "unelevated",
		"[windows]\nsandbox = \"mxc\"\n":        "",
		"[features]\nelevated_windows_sandbox = true\n":                              "elevated",
		"[features]\nexperimental_windows_sandbox = true\n":                          "unelevated",
		"sandbox_mode = \"danger-full-access\"\n[windows]\nsandbox = \"elevated\"\n": "",
		"model = \"gpt-5\"\n": "",
	} {
		write(t, filepath.Join(h, ".codex", "config.toml"), conf)
		if got := codexSandbox(); got != want {
			t.Errorf("%q: %q, want %q", conf, got, want)
		}
	}
}

// TestRTKNoHookListed (Discord, lc: rtk 没有识别 deepseek harness): an
// agent RTK has no hook for, DeepSeek Harness, is listed all the same with
// why, not left out as if magpie hadn't seen it, and it can't be switched on.
func TestRTKNoHookListed(t *testing.T) {
	h := sandbox(t)
	bin := filepath.Join(h, "bin")
	testenv.Program(t, filepath.Join(bin, "dsh"), "#!/bin/sh\n")
	write(t, filepath.Join(bin, "dsh.exe"), "")
	var dsh *RTKAgent
	for _, a := range ReadRTK().Agents {
		if a.ID == "dsh" {
			dsh = &a
		}
	}
	if dsh == nil {
		t.Fatal("DeepSeek Harness isn't listed")
	}
	// what the page reads: noHook, and Blocked saying why
	if b, _ := json.Marshal(dsh); dsh.On || !strings.Contains(string(b), `"noHook":true`) || !strings.Contains(dsh.Blocked, "rewriting the shell command") {
		t.Fatalf("listed as %s", b)
	}
	if _, err := SetRTK("dsh", true); err == nil || !strings.Contains(err.Error(), "DeepSeek Harness") {
		t.Fatalf("switched on: %v", err)
	}
	if _, err := RTKTakes("dsh"); err == nil || !strings.Contains(err.Error(), "rewriting the shell command") {
		t.Fatalf("magpie library rtk on dsh: %v", err)
	}
}

// fakeRTKHermes acts as rtk 0.51's rtk init -g --agent hermes does
// (src/hooks/init/hermes.rs): its plugin and config go into $HERMES_HOME,
// else ~/.hermes, whatever the OS. It notes the HERMES_HOME it was given.
const fakeRTKHermes = `#!/bin/sh
case "$1" in
--version) echo "rtk 0.51.0"; exit 0 ;;
gain) echo '{"summary":{}}'; exit 0 ;;
esac
shift 2
[ "$*" = "--agent hermes --auto-patch" ] || { echo "unknown: $*" >&2; exit 2; }
h="${HERMES_HOME:-$HOME/.hermes}"
echo "$HERMES_HOME" > "$HOME/given"
/bin/mkdir -p "$h/plugins/rtk-rewrite"
echo "name: rtk-rewrite" > "$h/plugins/rtk-rewrite/plugin.yaml"
echo "x" > "$h/plugins/rtk-rewrite/__init__.py"
printf 'plugins:\n  enabled:\n    - rtk-rewrite\n' >> "$h/config.yaml"
echo "Config: $h/config.yaml"
echo "Hermes will now rewrite terminal commands through rtk. Restart Hermes."
echo "Test with: git status"
`

// TestRTKHermesHome (新v学员 on Discord, native Windows): Hermes's folder is
// %LOCALAPPDATA%\hermes there, and rtk's installer wrote its plugin into it,
// while magpie looked for the plugin in ~/.hermes: "rtk's installer didn't
// add its hook to Hermes Agent". The plugin is looked for, and taken out,
// in Hermes's own folder (appdir.HermesHome), and the installer is told
// that folder, since rtk itself knows only $HERMES_HOME and ~/.hermes.
func TestRTKHermesHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake rtk is a shell script")
	}
	h := sandbox(t)
	bin := filepath.Join(h, "bin")
	testenv.Program(t, filepath.Join(bin, "rtk"), fakeRTKHermes)
	holdCLI(t, h, "hermes") // the config alone is gone (#2254)
	t.Setenv("PATH", bin)
	for _, c := range []struct{ name, env string }{
		// the reporter's layout: Hermes's folder isn't ~/.hermes
		{"elsewhere", filepath.Join(h, "AppData", "Local", "hermes")},
		// Hermes's default here: rtk is still told it, which on Windows
		// is the one way it writes where Hermes reads
		{"default", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HERMES_HOME", c.env)
			dir := appdir.HermesHome()
			if c.env != "" && dir != c.env {
				t.Fatalf("Hermes's folder is %s, want %s", dir, c.env)
			}
			cfg := filepath.Join(dir, "config.yaml")
			write(t, cfg, "model:\n  default: x\n")
			if _, err := SetRTK("hermes", true); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(read(t, filepath.Join(h, "given"))); got != dir {
				t.Errorf("rtk was given HERMES_HOME=%q, want %q", got, dir)
			}
			if !exists(filepath.Join(dir, "plugins", "rtk-rewrite", "plugin.yaml")) {
				t.Fatalf("no plugin in %s", dir)
			}
			if c.env != "" && exists(filepath.Join(h, ".hermes")) {
				t.Error("~/.hermes was made beside Hermes's own folder")
			}
			on := false
			for _, a := range ReadRTK().Agents {
				if a.ID == "hermes" {
					on = a.On
				}
			}
			if !on {
				t.Fatal("Hermes isn't seen with rtk on")
			}
			if _, err := SetRTK("hermes", false); err != nil {
				t.Fatal(err)
			}
			if exists(filepath.Join(dir, "plugins", "rtk-rewrite")) {
				t.Error("rtk's plugin is still in Hermes's folder")
			}
			if got := read(t, cfg); got != "model:\n  default: x\n" {
				t.Errorf("config.yaml = %q", got)
			}
			os.RemoveAll(dir)
		})
	}
}

// Grok App is listed on the RTK tab with the switch unable to turn on:
// rtk init has no Grok agent, so there is nothing to install.
func TestRTKNoHookGrokApp(t *testing.T) {
	h := sandbox(t)
	root := filepath.Join(h, "grok-app-data")
	t.Setenv("GROK_APP_HOME", root)
	write(t, filepath.Join(root, "agent-home", "config.toml"), "")
	var app *RTKAgent
	for _, a := range ReadRTK().Agents {
		if a.ID == "grok-app" {
			app = &a
		}
	}
	if app == nil {
		t.Fatal("Grok App isn't listed")
	}
	if b, _ := json.Marshal(app); app.On || !strings.Contains(string(b), `"noHook":true`) || !strings.Contains(app.Blocked, "no Grok or Grok App agent") {
		t.Fatalf("listed as %s", b)
	}
	if _, err := SetRTK("grok-app", true); err == nil || !strings.Contains(err.Error(), "Grok App") {
		t.Fatalf("switched on: %v", err)
	}
	if _, err := RTKTakes("grok-app"); err == nil || !strings.Contains(err.Error(), "no Grok or Grok App agent") {
		t.Fatalf("magpie library rtk on grok-app: %v", err)
	}
}

// Grok Build, which Grok App runs, has no rtk agent either (#1625's
// sibling): it is listed with the switch off, never switched on.
func TestRTKNoHookGrokBuild(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".grok", "config.toml"), "")
	var grok *RTKAgent
	for _, a := range ReadRTK().Agents {
		if a.ID == "grok" {
			grok = &a
		}
	}
	if grok == nil {
		t.Fatal("Grok Build isn't listed")
	}
	if b, _ := json.Marshal(grok); grok.On || !strings.Contains(string(b), `"noHook":true`) || !strings.Contains(grok.Blocked, "no Grok agent") {
		t.Fatalf("listed as %s", b)
	}
	if _, err := SetRTK("grok", true); err == nil || !strings.Contains(err.Error(), "Grok Build") {
		t.Fatalf("switched on: %v", err)
	}
	if _, err := RTKTakes("grok"); err == nil || !strings.Contains(err.Error(), "no Grok agent") {
		t.Fatalf("magpie library rtk on grok: %v", err)
	}
}

// TestRTKGainRealDaily reads what rtk 1.0.1 itself printed for rtk gain
// --daily --format json (testdata/rtk-gain-daily.json, made by real rtk from
// its own history.db): every day comes through, oldest first, and the days
// add up to the summary, so the Library chart's bars and its range total are
// rtk's own numbers (jasonchia58 on X read the chart's scale as the total).
func TestRTKGainRealDaily(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "rtk-gain-daily.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, days, why := parseGain(string(raw))
	if g == nil || why != "" {
		t.Fatalf("gain %+v, %q", g, why)
	}
	if g.Commands != 632 || g.Input != 3160000 || g.Saved != 865000 {
		t.Fatalf("summary %+v", g)
	}
	if len(days) != 6 || days[0].Date != "2026-10-23" || days[5].Date != "2026-11-12" {
		t.Fatalf("days %+v", days)
	}
	var c int
	var in, saved int64
	for _, d := range days {
		c, in, saved = c+d.Commands, in+d.Input, saved+d.Saved
	}
	if c != g.Commands || in != g.Input || saved != g.Saved {
		t.Fatalf("days add up to %d commands, %d in, %d saved; summary %+v", c, in, saved, g)
	}
	if days[1].Input != 1205000 || days[1].Saved != 361500 {
		t.Fatalf("the day with most input: %+v", days[1])
	}
}
