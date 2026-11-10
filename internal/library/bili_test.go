package library

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/testenv"
)

// fakeBili stands in for bili 0.1.213's plugin installer and remover, for
// Pi and Claude Code, as they were seen writing in a sandbox HOME: each
// write leaves <file>.bili-bak (the file before, taken again whenever it
// changed since bili's last write) and <file>.bili-last; Pi's remove leaves
// "packages": [], and Claude Code's deletes ANTHROPIC_BASE_URL outright
// and puts back the DISABLE_AUTO_COMPACT of a backup taken after it.
type fakeBili struct {
	t     *testing.T
	h     string
	on    map[string]bool
	runs  []string
	last  map[string]string // each file as bili last wrote it
	proxy string
}

func newFakeBili(t *testing.T, h string) *fakeBili {
	f := &fakeBili{t: t, h: h, on: map[string]bool{}, last: map[string]string{}, proxy: "http://127.0.0.1:18787/bili/"}
	path, run := biliPath, biliRun
	biliPath = func() string { return filepath.Join(h, ".local", "bin", "bili") }
	biliRun = func(_ string, _ time.Duration, args ...string) (string, error) { return f.run(args...) }
	t.Cleanup(func() { biliPath, biliRun = path, run })
	return f
}

func (f *fakeBili) settings(id string) string {
	if id == "pi" {
		return filepath.Join(f.h, ".pi", "agent", "settings.json")
	}
	return filepath.Join(f.h, ".claude", "settings.json")
}

func (f *fakeBili) obj(p string) map[string]any {
	m := map[string]any{}
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &m); err != nil {
			f.t.Fatalf("bili reads %s: %v", p, err)
		}
	}
	return m
}

// write is bili's backupBeforeWrite and its write
func (f *fakeBili) write(p string, m map[string]any) {
	cur, err := os.ReadFile(p)
	if err == nil && string(cur) != f.last[p] {
		write(f.t, p+".bili-bak", string(cur))
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	s := string(b) + "\n"
	write(f.t, p, s)
	write(f.t, p+".bili-last", "sha256")
	f.last[p] = s
}

func (f *fakeBili) run(args ...string) (string, error) {
	f.runs = append(f.runs, strings.Join(args, " "))
	switch strings.Join(args, " ") {
	case "--version":
		return "0.1.213 (133f3525c)", nil
	case "plugin list":
		out := "agent     status          version   updates via\n"
		for _, id := range []string{"pi", "omp", "opencode", "claude", "codex"} {
			st := "not installed"
			if f.on[id] {
				st = "installed (0.1.213)"
			}
			out += id + "  " + st + "  -  npm\n"
		}
		return out, nil
	}
	if len(args) != 3 || args[0] != "plugin" {
		return "", errors.New("unknown: " + strings.Join(args, " "))
	}
	id, p := args[2], f.settings(args[2])
	m := f.obj(p)
	switch args[1] + " " + id {
	case "install pi":
		m["packages"] = append(anySlice(m["packages"]), "npm:billion-context")
	case "remove pi":
		m["packages"] = slices.DeleteFunc(anySlice(m["packages"]), func(v any) bool { return v == "npm:billion-context" })
	case "install claude":
		env, _ := m["env"].(map[string]any)
		if env == nil {
			env = map[string]any{}
		}
		up, _ := env["ANTHROPIC_BASE_URL"].(string)
		if up == "" {
			up = "https://api.anthropic.com"
		}
		env["ANTHROPIC_BASE_URL"] = f.proxy + up
		// bili's applyClaudeManagedBlock: a value other than its own "1"
		// is the user's, left as it is
		if v, has := env["DISABLE_AUTO_COMPACT"]; !has || v == "1" {
			env["DISABLE_AUTO_COMPACT"] = "1"
		}
		m["env"] = env
		m["hooks"] = map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "node claude-native-bootstrap.js"}}}}}
		write(f.t, filepath.Join(f.h, ".claude", "commands", "acp-cache.md"), "acp cache\n")
	case "remove claude":
		bak := map[string]any{}
		f.write(p, m) // the backup taken again, when magpie wrote since
		_ = json.Unmarshal([]byte(read(f.t, p+".bili-bak")), &bak)
		env, _ := m["env"].(map[string]any)
		delete(env, "ANTHROPIC_BASE_URL")
		if env["DISABLE_AUTO_COMPACT"] == "1" {
			delete(env, "DISABLE_AUTO_COMPACT")
		}
		if be, ok := bak["env"].(map[string]any); ok && be["DISABLE_AUTO_COMPACT"] != nil {
			env["DISABLE_AUTO_COMPACT"] = be["DISABLE_AUTO_COMPACT"]
		}
		delete(m, "hooks")
		os.Remove(filepath.Join(f.h, ".claude", "commands", "acp-cache.md"))
	default:
		return "", errors.New("no lane " + id)
	}
	f.write(p, m)
	f.on[id] = args[1] == "install"
	return "done", nil
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func biliOnFor(t *testing.T) map[string]bool {
	t.Helper()
	m := map[string]bool{}
	for _, a := range ReadBili().Agents {
		m[a.ID] = a.On
	}
	return m
}

// TestBiliOnOffExact: switched on then off, an agent's files are as they
// were byte for byte — bili's remove leaves Pi "packages": [] and its
// .bili-bak and .bili-last beside each file — and Claude Code's command
// file is gone with it.
func TestBiliOnOffExact(t *testing.T) {
	h := sandbox(t)
	f := newFakeBili(t, h)
	pi := filepath.Join(h, ".pi", "agent", "settings.json")
	cc := filepath.Join(h, ".claude", "settings.json")
	piWas := "{\n    \"defaultModel\": \"magpie/gpt-5\",   \"theme\": \"dark\"\n}\n"
	ccWas := "{\"model\":\"opus\",\n \"env\": {\"ANTHROPIC_BASE_URL\": \"http://127.0.0.1:3425\", \"ANTHROPIC_AUTH_TOKEN\": \"magpie\"}}"
	write(t, pi, piWas)
	write(t, cc, ccWas)

	v := ReadBili()
	if v.Version != "0.1.213" || v.ListErr != "" {
		t.Fatalf("version %q, list error %q", v.Version, v.ListErr)
	}
	var got []string
	for _, a := range v.Agents {
		got = append(got, a.ID)
	}
	// codex is here and bili has a lane for it, an MCP server only: not offered
	if !slices.Contains(got, "pi") || !slices.Contains(got, "claude") || !slices.Contains(got, "opencode") || slices.Contains(got, "codex") || slices.Contains(got, "gemini") {
		t.Fatalf("offered %v", got)
	}
	for _, id := range []string{"pi", "claude"} {
		if _, err := SetBili(id, true); err != nil {
			t.Fatal(err)
		}
	}
	if on := biliOnFor(t); !on["pi"] || !on["claude"] || on["opencode"] {
		t.Fatalf("on: %v", on)
	}
	if !strings.Contains(read(t, cc), "/bili/http://127.0.0.1:3425") {
		t.Fatalf("Claude Code isn't sent through bili:\n%s", read(t, cc))
	}
	for _, id := range []string{"pi", "claude"} {
		v, err := SetBili(id, false)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(v.Restart, []string{id}) || v.Backup == "" {
			t.Fatalf("restart %v, backup %q", v.Restart, v.Backup)
		}
	}
	if got := read(t, pi); got != piWas {
		t.Fatalf("Pi's settings.json:\n%s\nwant\n%s", got, piWas)
	}
	if got := read(t, cc); got != ccWas {
		t.Fatalf("Claude Code's settings.json:\n%s\nwant\n%s", got, ccWas)
	}
	for _, p := range []string{pi + ".bili-bak", pi + ".bili-last", cc + ".bili-bak", cc + ".bili-last", filepath.Join(h, ".claude", "commands", "acp-cache.md")} {
		if exists(p) {
			t.Fatalf("%s left behind", p)
		}
	}
	if on := biliOnFor(t); on["pi"] || on["claude"] {
		t.Fatalf("on: %v", on)
	}
	if want := []string{"plugin install pi", "plugin install claude", "plugin remove pi", "plugin remove claude"}; !slices.Equal(filter(f.runs, "plugin install", "plugin remove"), want) {
		t.Fatalf("ran %v, want %v", f.runs, want)
	}
}

func filter(runs []string, prefixes ...string) []string {
	var out []string
	for _, r := range runs {
		for _, p := range prefixes {
			if strings.HasPrefix(r, p) {
				out = append(out, r)
			}
		}
	}
	return out
}

// TestBiliClaudeKeepsMagpiesRoute: magpie rewrote Claude Code's
// settings.json while bili was on (another model picked). bili's remove
// then takes ANTHROPIC_BASE_URL out, which would send Claude Code to
// Anthropic, and puts back the DISABLE_AUTO_COMPACT it added; switched off,
// Claude Code goes to magpie again, without that, with the model picked.
func TestBiliClaudeKeepsMagpiesRoute(t *testing.T) {
	h := sandbox(t)
	newFakeBili(t, h)
	cc := filepath.Join(h, ".claude", "settings.json")
	write(t, cc, `{"model":"opus","env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:3425","ANTHROPIC_AUTH_TOKEN":"magpie"},"permissions":{"allow":["Bash"]}}`)
	if _, err := SetBili("claude", true); err != nil {
		t.Fatal(err)
	}
	// magpie picks another model, keeping bili's address in front of it
	m := map[string]any{}
	if err := json.Unmarshal([]byte(read(t, cc)), &m); err != nil {
		t.Fatal(err)
	}
	m["model"] = "magpie/glm-5"
	m["env"].(map[string]any)["ANTHROPIC_MODEL"] = "magpie/glm-5"
	b, _ := json.MarshalIndent(m, "", "\t")
	write(t, cc, string(b))

	if _, err := SetBili("claude", false); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	if err := json.Unmarshal([]byte(read(t, cc)), &got); err != nil {
		t.Fatalf("%v:\n%s", err, read(t, cc))
	}
	env, _ := got["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:3425" {
		t.Fatalf("ANTHROPIC_BASE_URL = %v, want magpie's:\n%s", env["ANTHROPIC_BASE_URL"], read(t, cc))
	}
	if _, ok := env["DISABLE_AUTO_COMPACT"]; ok {
		t.Fatalf("DISABLE_AUTO_COMPACT left:\n%s", read(t, cc))
	}
	if _, ok := got["hooks"]; ok {
		t.Fatalf("hooks left:\n%s", read(t, cc))
	}
	if got["model"] != "magpie/glm-5" || env["ANTHROPIC_MODEL"] != "magpie/glm-5" || env["ANTHROPIC_AUTH_TOKEN"] != "magpie" || got["permissions"] == nil {
		t.Fatalf("magpie's and the user's own values lost:\n%s", read(t, cc))
	}
}

// TestBiliOffWithoutRecord: a plugin put in outside magpie, with no record
// of the file before it: bili's remove takes Claude Code's
// ANTHROPIC_BASE_URL out, and magpie puts back the address bili forwarded to.
func TestBiliOffWithoutRecord(t *testing.T) {
	h := sandbox(t)
	f := newFakeBili(t, h)
	cc := filepath.Join(h, ".claude", "settings.json")
	write(t, cc, `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:3425"}}`)
	if _, err := f.run("plugin", "install", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetBili("claude", false); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	if err := json.Unmarshal([]byte(read(t, cc)), &got); err != nil {
		t.Fatal(err)
	}
	env, _ := got["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:3425" {
		t.Fatalf("ANTHROPIC_BASE_URL not put back:\n%s", read(t, cc))
	}
	if _, ok := env["DISABLE_AUTO_COMPACT"]; ok || got["hooks"] != nil {
		t.Fatalf("bili's own values put back:\n%s", read(t, cc))
	}
}

// TestBiliNotInstalled: no bili, so no switch works, and the page has
// billion-context's own install command when npm is here.
func TestBiliNotInstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake npm is a shell script")
	}
	h := sandbox(t)
	bin := filepath.Join(h, "bin")
	v := ReadBili()
	if v.Path != "" || v.Install != "" {
		t.Fatalf("path %q, install %q, with no bili and no npm", v.Path, v.Install)
	}
	if _, err := SetBili("pi", true); err == nil {
		t.Fatal("switched on with no bili")
	}
	if _, err := InstallBili(); err == nil {
		t.Fatal("installed with no npm")
	}
	// npm's global install with ~/.local as the prefix puts bili there
	defer proc.Forget()
	testenv.Program(t, filepath.Join(bin, "npm"), `#!/bin/sh
[ "$1 $2 $3 $4" = "install -g billion-context --prefix=$HOME/.local" ] || { echo "unknown: $*" >&2; exit 2; }
/bin/mkdir -p "$HOME/.local/bin"
printf '#!/bin/sh\necho "0.1.213 (x)"\n' > "$HOME/.local/bin/bili"
/bin/chmod +x "$HOME/.local/bin/bili"
`)
	proc.Forget()
	if v := ReadBili(); v.Install != "npm install -g billion-context --prefix="+filepath.Join(h, ".local") {
		t.Fatalf("install %q", v.Install)
	}
	v, err := InstallBili()
	if err != nil {
		t.Fatal(err)
	}
	if v.Path != filepath.Join(h, ".local", "bin", "bili") || v.Version != "0.1.213" {
		t.Fatalf("path %q, version %q", v.Path, v.Version)
	}
}

// TestParseBiliList: bili plugin list's table, as bili 0.1.213 prints it.
func TestParseBiliList(t *testing.T) {
	out := `agent     status                                     version   updates via
pi        installed (0.1.213)                        0.1.213   npm (pi update)
claude    partially installed — rerun bili plugin install claude   -   -
opencode  not installed                              -         -
omp       error: config.yml unreadable               -         -`
	st := parseBiliList(out)
	if !biliOn(st["pi"]) || biliOn(st["claude"]) || biliOn(st["opencode"]) || st["opencode"] != "not installed" || st["omp"] != "error: config.yml unreadable" {
		t.Fatalf("%q", st)
	}
}

// TestBiliMergeJSON: what the install changed and nothing touched since
// goes back; a value changed since stays; one the remove changed beyond
// the install goes back as it was before the remove.
func TestBiliMergeJSON(t *testing.T) {
	s := func(v string) *string { return &v }
	before := s(`{"a":1,"keep":"x"}`)
	after := s(`{"a":1,"keep":"x","added":true,"plugins":["b"]}`)
	cur := s(`{"a":2,"keep":"x","added":true,"plugins":["b","mine"]}`)
	now := s(`{"a":2,"plugins":["mine"],"added":true}`)
	out, err := biliMergeJSON(before, after, cur, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	if err := json.Unmarshal([]byte(*out), &got); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	if string(b) != `{"a":2,"keep":"x","plugins":["mine"]}` {
		t.Fatalf("got %s", b)
	}
}

// TestBiliClaudeKeepsAutoCompact: Kling__Lab's "Prompt is too long". bili's
// install turns Claude Code's auto-compaction off, and a session bili
// doesn't compress (Claude Desktop's Code tab, past its proxy) then stops
// at the window for good. magpie puts it back on, takes that away again
// with the plugin, and leaves a value the user had before as theirs.
func TestBiliClaudeKeepsAutoCompact(t *testing.T) {
	compact := func(cc string) (any, bool) {
		m := map[string]any{}
		if err := json.Unmarshal([]byte(read(t, cc)), &m); err != nil {
			t.Fatalf("%v:\n%s", err, read(t, cc))
		}
		env, _ := m["env"].(map[string]any)
		v, has := env["DISABLE_AUTO_COMPACT"]
		return v, has
	}
	t.Run("on and off", func(t *testing.T) {
		h := sandbox(t)
		f := newFakeBili(t, h)
		cc := filepath.Join(h, ".claude", "settings.json")
		write(t, cc, `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:3425","ANTHROPIC_AUTH_TOKEN":"magpie"}}`)
		if _, err := SetBili("claude", true); err != nil {
			t.Fatal(err)
		}
		if v, _ := compact(cc); v != "0" {
			t.Fatalf("DISABLE_AUTO_COMPACT = %v after the install, want \"0\":\n%s", v, read(t, cc))
		}
		// bili's own install again (its SessionStart hook, a reinstall)
		// keeps it
		if _, err := f.run("plugin", "install", "claude"); err != nil {
			t.Fatal(err)
		}
		if v, _ := compact(cc); v != "0" {
			t.Fatalf("DISABLE_AUTO_COMPACT = %v after bili's reinstall:\n%s", v, read(t, cc))
		}
		if _, err := SetBili("claude", false); err != nil {
			t.Fatal(err)
		}
		if v, has := compact(cc); has {
			t.Fatalf("DISABLE_AUTO_COMPACT = %v left after the plugin went:\n%s", v, read(t, cc))
		}
	})
	t.Run("the user's own", func(t *testing.T) {
		h := sandbox(t)
		newFakeBili(t, h)
		cc := filepath.Join(h, ".claude", "settings.json")
		write(t, cc, `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:3425","DISABLE_AUTO_COMPACT":"1"}}`)
		if _, err := SetBili("claude", true); err != nil {
			t.Fatal(err)
		}
		if v, _ := compact(cc); v != "1" {
			t.Fatalf("the user's DISABLE_AUTO_COMPACT became %v:\n%s", v, read(t, cc))
		}
		if _, err := SetBili("claude", false); err != nil {
			t.Fatal(err)
		}
		if v, _ := compact(cc); v != "1" {
			t.Fatalf("the user's DISABLE_AUTO_COMPACT became %v after off:\n%s", v, read(t, cc))
		}
	})
	t.Run("installed before", func(t *testing.T) {
		// a plugin magpie installed before this: the record has bili's "1"
		h := sandbox(t)
		f := newFakeBili(t, h)
		cc := filepath.Join(h, ".claude", "settings.json")
		write(t, cc, `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:3425"}}`)
		before := read(t, cc)
		if _, err := f.run("plugin", "install", "claude"); err != nil {
			t.Fatal(err)
		}
		after := read(t, cc)
		if err := writeBiliState(map[string][]biliFile{"claude": {{Path: cc, Before: &before, After: &after}}}); err != nil {
			t.Fatal(err)
		}
		keepBiliCompact()
		if v, _ := compact(cc); v != "0" {
			t.Fatalf("DISABLE_AUTO_COMPACT = %v after magpie started, want \"0\":\n%s", v, read(t, cc))
		}
		if _, err := SetBili("claude", false); err != nil {
			t.Fatal(err)
		}
		if v, has := compact(cc); has {
			t.Fatalf("DISABLE_AUTO_COMPACT = %v left after the plugin went:\n%s", v, read(t, cc))
		}
		if got := read(t, cc); !sameJSON(got, before) {
			t.Fatalf("settings.json not as before:\n%s\nwant\n%s", got, before)
		}
	})
}

// TestBiliZedThroughPrefix: Zed has no plugin of bili's; it's offered,
// and switched on its magpie provider's api_url goes through bili's
// /bili/ prefix with bili start answering there, no plugin installed.
// Off, the provider is back on the gateway and magpie's bili start ends.
// While it's on, KeepLinked starts bili start again when it doesn't
// answer.
func TestBiliZedThroughPrefix(t *testing.T) {
	h := sandbox(t)
	f := newFakeBili(t, h)
	dir := filepath.Join(h, "zed-config")
	t.Setenv("MAGPIE_ZED_CONFIG_DIR", dir)
	zs := filepath.Join(dir, "settings.json")
	gw := gateway.URL() + "/v1"
	write(t, zs, `{"theme":"One Dark","language_models":{"openai_compatible":{"magpie":{"api_url":"`+gw+`","available_models":[]}}}}`)

	serving, served, stopped := false, 0, 0
	through := []string{}
	was := []any{zedThrough, biliServe, biliStop, biliServing}
	zedThrough = func(a *agent.Agent, prefix string) error {
		through = append(through, prefix)
		b, _ := os.ReadFile(a.Path)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		m["language_models"].(map[string]any)["openai_compatible"].(map[string]any)["magpie"].(map[string]any)["api_url"] = prefix + gw
		out, _ := json.Marshal(m)
		return os.WriteFile(a.Path, out, 0o600)
	}
	biliServe = func(string) error { served++; serving = true; return nil }
	biliStop = func() { stopped++; serving = false }
	biliServing = func() bool { return serving }
	t.Cleanup(func() {
		zedThrough = was[0].(func(*agent.Agent, string) error)
		biliServe = was[1].(func(string) error)
		biliStop = was[2].(func())
		biliServing = was[3].(func() bool)
	})

	if on, ok := biliOnFor(t)["zed"]; !ok || on {
		t.Fatalf("Zed offered %v, on %v", ok, on)
	}
	v, err := SetBili("zed", true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(v.Restart, []string{"zed"}) || v.Backup == "" {
		t.Fatalf("restart %v, backup %q", v.Restart, v.Backup)
	}
	if !strings.Contains(read(t, zs), `"api_url":"`+biliPrefix+gw+`"`) || served != 1 {
		t.Fatalf("served %d, settings:\n%s", served, read(t, zs))
	}
	if !biliOnFor(t)["zed"] {
		t.Fatal("Zed isn't shown on")
	}
	// magpie restarted, bili start isn't answering: it's started again
	serving = false
	keepBiliServe(context.Background())
	if served != 2 {
		t.Fatalf("bili start run %d times, want 2", served)
	}
	if _, err := SetBili("zed", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, zs), `"api_url":"`+gw+`"`) || stopped != 1 || biliOnFor(t)["zed"] {
		t.Fatalf("stopped %d, settings:\n%s", stopped, read(t, zs))
	}
	if !slices.Equal(through, []string{biliPrefix, ""}) {
		t.Fatalf("through %q", through)
	}
	// off, nothing starts it
	keepBiliServe(context.Background())
	if served != 2 || len(filter(f.runs, "plugin install", "plugin remove")) != 0 {
		t.Fatalf("served %d, ran %v", served, f.runs)
	}
}
