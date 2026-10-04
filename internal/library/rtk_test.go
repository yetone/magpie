package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
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
	write(t, filepath.Join(bin, "rtk"), fakeRTK)
	if err := os.Chmod(filepath.Join(bin, "rtk"), 0o755); err != nil {
		t.Fatal(err)
	}
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

// TestRTKOpenCode2: rtk's OpenCode plugin is written for OpenCode 1, and
// OpenCode 2 refuses to load it (rtk-ai/rtk#4311), so with an OpenCode 2
// here rtk isn't given to OpenCode; one given before can still be taken out.
// An OpenCode 1 still gets it.
func TestRTKOpenCode2(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	h := sandbox(t)
	bin := filepath.Join(h, "bin")
	write(t, filepath.Join(bin, "rtk"), fakeRTK)
	opencode := func(version string) {
		t.Helper()
		write(t, filepath.Join(bin, "opencode"), "#!/bin/sh\necho "+version+"\n")
		for _, f := range []string{"rtk", "opencode"} {
			if err := os.Chmod(filepath.Join(bin, f), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("PATH", bin)
	plugin := filepath.Join(h, ".config", "opencode", "plugins", "rtk.ts")
	blocked := func() string {
		t.Helper()
		for _, a := range ReadRTK().Agents {
			if a.ID == "opencode" {
				return a.Blocked
			}
		}
		t.Fatal("opencode isn't listed")
		return ""
	}

	// what each prints for --version
	opencode("opencode v2.0.18")
	if b := blocked(); !strings.Contains(b, "OpenCode 2.0.18") {
		t.Fatalf("blocked: %q", b)
	}
	_, err := SetRTK("opencode", true)
	if err == nil || !strings.Contains(err.Error(), "doesn't support OpenCode 2") {
		t.Fatalf("switched on for OpenCode 2: %v", err)
	}
	if exists(plugin) {
		t.Fatal("rtk's plugin was written for OpenCode 2")
	}
	// one written before (by an older magpie, or rtk init by hand) goes
	write(t, plugin, "x")
	if _, err := SetRTK("opencode", false); err != nil {
		t.Fatal(err)
	}
	if exists(plugin) {
		t.Fatal("rtk's plugin is still there")
	}

	opencode("1.18.32")
	if b := blocked(); b != "" {
		t.Fatalf("OpenCode 1 blocked: %q", b)
	}
	if _, err := SetRTK("opencode", true); err != nil {
		t.Fatal(err)
	}
	if !exists(plugin) {
		t.Fatal("OpenCode 1 didn't get rtk's plugin")
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
		write(t, filepath.Join(bin, "rtk"), "#!/bin/sh\ncase \"$1\" in\n--version) echo \"rtk "+version+"\"; exit 0 ;;\ngain) exit 1 ;;\nesac\n"+init+"\n")
		if err := os.Chmod(filepath.Join(bin, "rtk"), 0o755); err != nil {
			t.Fatal(err)
		}
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
		write(t, filepath.Join(bin, "rtk"), "#!/bin/sh\ncase \"$1\" in\n--version) echo 'rtk 0.51.0'; exit 0 ;;\ngain) "+body+" ;;\nesac\nexit 2\n")
		os.Chmod(filepath.Join(bin, "rtk"), 0o755)
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
