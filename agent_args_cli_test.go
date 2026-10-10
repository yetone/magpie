package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// cliHome is groupsHome with every other folder an agent keeps files in
// (XDG_DATA_HOME, XDG_STATE_HOME, Windows' APPDATA) inside its home, so
// that homeFiles sees whatever a command writes, and with the gateway's
// address one nothing answers at. No agent runs: what a command prints
// doesn't gain "restart the Codex app" where Codex is open (and on Windows,
// where anything may be).
func cliHome(t *testing.T) {
	t.Helper()
	groupsHome(t)
	was := agent.Running
	agent.Running = func(...string) bool { return false }
	t.Cleanup(func() { agent.Running = was })
	home := os.Getenv("HOME")
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:1")
}

// browserFails puts failing stand-ins for the browser openers first on PATH:
// Cindy's link opened in the browser for any word after magpie cindy.
func browserFails(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	for _, name := range []string{"open", "xdg-open", "rundll32"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" {
			if err := os.WriteFile(filepath.Join(bin, name+".bat"), []byte("@exit /b 1\r\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// homeFiles is every folder and file in the test's home, config and cache
// folders, with each file's contents. Not magpie's cli-identity.json: who
// Cursor's and Devin's CLIs say is signed in, asked again behind a look a
// minute after the last answer and kept when it comes, which may be during
// a later command, or in a later test's home.
func homeFiles(t *testing.T) map[string]string {
	t.Helper()
	identities := filepath.Join(filepath.Dir(provider.Path()), "cli-identity.json")
	m := map[string]string{}
	for _, root := range []string{os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_CACHE_HOME")} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == identities {
				return nil
			}
			if d.IsDir() {
				m[p] = "(folder)"
				return nil
			}
			b, err := os.ReadFile(p)
			m[p] = string(b)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// filesChanged names what differs between two homeFiles, "" for nothing.
func filesChanged(before, after map[string]string) string {
	var out []string
	for p, b := range before {
		if a, ok := after[p]; !ok {
			out = append(out, "removed "+p)
		} else if a != b {
			out = append(out, "changed "+p+":\n"+a)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			out = append(out, "made "+p)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func writeHomeFile(t *testing.T, rel, s string) string {
	t.Helper()
	p := filepath.Join(os.Getenv("HOME"), rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// codexOnAdvanced is the reporter's machine: Codex's own config.toml, a
// routing group advanced, and Codex put on it with magpie codex
// group/advanced. It gives the config's path.
func codexOnAdvanced(t *testing.T) string {
	t.Helper()
	cliHome(t)
	path := writeHomeFile(t, filepath.Join(".codex", "config.toml"),
		"model = \"gpt-5.5\"\nmodel_reasoning_effort = \"high\"\n\n[projects.\"/Users/me/work\"]\ntrust_level = \"trusted\"\n")
	for _, args := range [][]string{{"group", "add", "advanced", "models=a/m,b/gpt-5.5"}, {"codex", "group/advanced"}} {
		if _, err := printed(t, func() error { return run(args) }); err != nil {
			t.Fatalf("magpie %s: %v", strings.Join(args, " "), err)
		}
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"group/advanced"`) || !strings.Contains(string(b), "trust_level") {
		t.Fatalf("Codex isn't on group/advanced:\n%s", b)
	}
	return path
}

// magpie codex --help printed "✓ Codex model --help" and wrote model =
// "--help" into ~/.codex/config.toml, magpie taken out with it (reported
// on 0.1.1082, Codex on group/advanced). --help, -h and help, wherever
// they come, now show how to set Codex, and any other word that starts
// with "-" is refused as a flag: neither writes a thing.
func TestAgentHelpWritesNothing(t *testing.T) {
	codexOnAdvanced(t)
	before := homeFiles(t)
	for _, args := range [][]string{{"codex", "--help"}, {"codex", "-h"}, {"codex", "help"}, {"codex", "Help"}, {"codex", "HELP"},
		{"codex", "model", "--help"}, {"codex", "effort", "-h"}, {"codex", "effort", "Help"}} {
		out, err := printed(t, func() error { return run(args) })
		if err != nil || !strings.HasPrefix(out, "usage:\n  magpie codex ") || !strings.Contains(out, "magpie codex <field> <value>") ||
			!strings.Contains(out, "fields: model, effort,") {
			t.Errorf("magpie %s: %v\n%s\nwant Codex's usage", strings.Join(args, " "), err, out)
		}
		if diff := filesChanged(before, homeFiles(t)); diff != "" {
			t.Fatalf("magpie %s wrote:\n%s", strings.Join(args, " "), diff)
		}
	}
	for _, args := range [][]string{{"codex", "-x"}, {"codex", "--model"}, {"codex", "-"}, {"codex", "effort", "-x"}} {
		flag := args[len(args)-1]
		out, err := printed(t, func() error { return run(args) })
		if err == nil || err.Error() != "unknown flag "+flag+" (magpie codex help)" || out != "" {
			t.Errorf("magpie %s: %v %q, want the flag refused", strings.Join(args, " "), err, out)
		}
		if diff := filesChanged(before, homeFiles(t)); diff != "" {
			t.Fatalf("magpie %s wrote:\n%s", strings.Join(args, " "), diff)
		}
	}
	// what tells a write: a value still goes in
	if _, err := printed(t, func() error { return run([]string{"codex", "effort", "low"}) }); err != nil {
		t.Fatal(err)
	}
	if filesChanged(before, homeFiles(t)) == "" {
		t.Fatal("magpie codex effort low wrote nothing")
	}
}

// Every agent takes its help and flags the same way, through any of its
// fields: they wrote "--help" as the model of most of them, turned magpie
// on for Claude Desktop, Cursor Private Inference, Pencil, T3 Code,
// WorkBuddy and ZCode, and opened Cindy's link in the browser. The usage
// names each field as it is typed and as it is shown (magpie gemini auth
// api-key sets its provider).
func TestEveryAgentHelpWritesNothing(t *testing.T) {
	cliHome(t)
	browserFails(t)
	writeHomeFile(t, filepath.Join(".claude", "settings.json"), "{\n  \"model\": \"opus\",\n  \"theme\": \"dark\"\n}\n")
	if _, err := printed(t, func() error { return run([]string{"version"}) }); err != nil {
		t.Fatal(err)
	}
	before := homeFiles(t)
	for _, a := range agent.All() {
		cases := [][]string{{a.ID, "--help"}, {a.ID, "-h"}, {a.ID, "help"}, {a.ID, "-x"}}
		for _, f := range a.Fields {
			cases = append(cases, []string{a.ID, f.Key, "--help"}, []string{a.ID, f.Key, "-x"})
		}
		for _, args := range cases {
			out, err := printed(t, func() error { return run(args) })
			if args[len(args)-1] == "-x" {
				if err == nil || !strings.HasPrefix(err.Error(), "unknown flag -x") {
					t.Errorf("magpie %s: %v %q, want the flag refused", strings.Join(args, " "), err, out)
				}
			} else if err != nil || !strings.HasPrefix(out, "usage:\n  magpie "+a.ID+" ") {
				t.Errorf("magpie %s: %v %q, want %s's usage", strings.Join(args, " "), err, out, a.Name)
			} else {
				for _, f := range a.Fields {
					name := f.Key
					if f.Label != f.Key {
						name += " (" + f.Label + ")"
					}
					if !strings.Contains(out, " "+name) {
						t.Errorf("magpie %s doesn't name %s:\n%s", strings.Join(args, " "), name, out)
					}
				}
				if a.Import != nil && !strings.Contains(out, "  magpie "+a.ID+" add  ") {
					t.Errorf("magpie %s doesn't say how to add magpie:\n%s", strings.Join(args, " "), out)
				}
			}
			if diff := filesChanged(before, homeFiles(t)); diff != "" {
				t.Errorf("magpie %s wrote:\n%s", strings.Join(args, " "), diff)
				before = homeFiles(t)
			}
		}
	}
}

// magpie codex model wrote model = "model" into Codex's config, magpie
// taken out with it, and magpie codex effort model = "effort" (reported
// with magpie codex --help, on 0.1.1082). A field's name alone, as typed
// or as shown, now says what the field is set to, and a field's name is
// refused as a value: magpie codex subagent effort ran the subagents on a
// model called effort. Neither writes a thing.
func TestAgentFieldNameShowsIt(t *testing.T) {
	codexOnAdvanced(t)
	before := homeFiles(t)
	for _, c := range []struct{ args, want string }{
		{"codex model", "Codex model group/advanced\n"},
		{"codex Model", "Codex model group/advanced\n"},
		{"codex effort", "Codex effort high\n"},
		{"codex EFFORT", "Codex effort high\n"},
		{"codex subagents", "Codex subagents default\n"},
		{"codex subagent_effort", "Codex subagent effort default\n"},
		{"codex default", ""},
	} {
		args := strings.Fields(c.args)
		if c.want == "" {
			// what tells a write: taking magpie out still does
			if _, err := printed(t, func() error { return run(args) }); err != nil || filesChanged(before, homeFiles(t)) == "" {
				t.Fatalf("magpie %s: %v, and wrote nothing", c.args, err)
			}
			continue
		}
		out, err := printed(t, func() error { return run(args) })
		if err != nil || out != c.want {
			t.Errorf("magpie %s: %v %q, want %q", c.args, err, out, c.want)
		}
		if diff := filesChanged(before, homeFiles(t)); diff != "" {
			t.Fatalf("magpie %s wrote:\n%s", c.args, diff)
		}
	}
	codexOnAdvanced(t)
	before = homeFiles(t)
	for _, c := range []struct{ args, want string }{
		{"codex subagent effort", `"subagent effort" is one field of Codex's: magpie codex subagent_effort shows it`},
		{"codex subagents Effort", `"subagent effort" is one field of Codex's: magpie codex subagent_effort shows it`},
		{"codex model effort", `"effort" is a field of Codex's, not a value for its model: magpie codex effort shows it`},
		{"codex model Effort", `"Effort" is a field of Codex's, not a value for its model: magpie codex effort shows it`},
		{"codex effort model", `"model" is a field of Codex's, not a value for its effort: magpie codex model shows it`},
		{"codex model sign-in", `"sign-in" is a field of Codex's, not a value for its model: magpie codex login shows it`},
	} {
		args := strings.Fields(c.args)
		out, err := printed(t, func() error { return run(args) })
		if err == nil || err.Error() != c.want || out != "" {
			t.Errorf("magpie %s: %v %q, want %s", c.args, err, out, c.want)
		}
		if diff := filesChanged(before, homeFiles(t)); diff != "" {
			t.Fatalf("magpie %s wrote:\n%s", c.args, diff)
		}
	}
	// a field's name in any case sets it too
	out, err := printed(t, func() error { return run([]string{"codex", "EFFORT", "low"}) })
	if err != nil || out != "✓ Codex effort low\n" || filesChanged(before, homeFiles(t)) == "" {
		t.Errorf("magpie codex EFFORT low: %v %q", err, out)
	}
}

// Every agent's fields go by their names the same way: alone, each shows
// its field; after any field, each is refused. They were written as most
// agents' model, and connected Claude Desktop, Cursor Private Inference,
// Pencil, T3 Code, WorkBuddy and ZCode to magpie. Claude Code's opus,
// sonnet, haiku and fable name its model as well as its tiers, and stay
// the model's (TestClaudeTierNamesSetTheModel).
func TestEveryAgentFieldNameWritesNothing(t *testing.T) {
	cliHome(t)
	writeHomeFile(t, filepath.Join(".claude", "settings.json"), "{\n  \"model\": \"opus\",\n  \"theme\": \"dark\"\n}\n")
	if _, err := printed(t, func() error { return run([]string{"version"}) }); err != nil {
		t.Fatal(err)
	}
	before := homeFiles(t)
	try := func(args []string, ok func(out string, err error) bool, want string) {
		t.Helper()
		out, err := printed(t, func() error { return run(args) })
		if !ok(out, err) {
			t.Errorf("magpie %s: %v %q, want %s", strings.Join(args, " "), err, out, want)
		}
		if diff := filesChanged(before, homeFiles(t)); diff != "" {
			t.Errorf("magpie %s wrote:\n%s", strings.Join(args, " "), diff)
			before = homeFiles(t)
		}
	}
	for _, a := range agent.All() {
		var names []string
		for _, f := range a.Fields {
			for _, n := range []string{f.Key, f.Label} {
				if !slices.Contains(names, n) && !slices.Contains(a.ModelAliases, n) {
					names = append(names, n)
				}
			}
		}
		for _, n := range names {
			f := a.Field(n)
			try([]string{a.ID, n}, func(out string, err error) bool {
				return err == nil && strings.HasPrefix(out, a.Name+" "+f.Label+" ") && strings.Count(out, "\n") == 1
			}, "its "+f.Label)
			refused := regexp.MustCompile(`^"[^"]+" is (a field|one field|a switch) of ` + regexp.QuoteMeta(a.Name) + `'s`)
			for _, g := range a.Fields {
				try([]string{a.ID, g.Key, n}, func(out string, err error) bool {
					return err != nil && refused.MatchString(err.Error()) && out == ""
				}, "it refused")
			}
		}
	}
}

// Claude Code's tiers are named as its model's aliases are: magpie claude
// opus sets its model to opus, as the docs have it, where another field's
// name alone shows that field; magpie claude help says so.
func TestClaudeTierNamesSetTheModel(t *testing.T) {
	cliHome(t)
	path := writeHomeFile(t, filepath.Join(".claude", "settings.json"), "{\n  \"model\": \"claude-opus-4-5\",\n  \"theme\": \"dark\"\n}\n")
	for _, args := range [][]string{{"claude", "opus"}, {"claude", "sonnet"}, {"claude", "haiku"}, {"claude", "fable"}, {"claude", "model", "opus"}} {
		want := args[len(args)-1]
		out, err := printed(t, func() error { return run(args) })
		if b, _ := os.ReadFile(path); err != nil || out != "✓ Claude Code model "+want+"\n" || !strings.Contains(string(b), `"model": "`+want+`"`) {
			t.Errorf("magpie %s: %v %q\n%s", strings.Join(args, " "), err, out, b)
		}
	}
	out, err := printed(t, func() error { return run([]string{"claude", "help"}) })
	if err != nil || !strings.Contains(out, "\n  opus, sonnet, haiku and fable alone name the model: magpie claude opus sets its model to opus\n") {
		t.Errorf("magpie claude help: %v\n%s", err, out)
	}
	// in another case too, where Claude Code takes its aliases as they are
	// spelled: the tier's field isn't shown for it
	out, err = printed(t, func() error { return run([]string{"claude", "Opus"}) })
	if err == nil || !strings.HasPrefix(err.Error(), `"Opus" is no model of Claude Code's`) || out != "" {
		t.Errorf("magpie claude Opus: %v %q, want it refused as a model", err, out)
	}
}

// Claude Code's ultracode is a switch: given as its model or its effort,
// the refusal says how to turn it on, as those fields' own errors did
// before a field's name was refused ahead of them.
func TestClaudeUltracodeNamedAsAValue(t *testing.T) {
	cliHome(t)
	path := writeHomeFile(t, filepath.Join(".claude", "settings.json"), "{\n  \"model\": \"opus\",\n  \"theme\": \"dark\"\n}\n")
	if _, err := printed(t, func() error { return run([]string{"version"}) }); err != nil {
		t.Fatal(err)
	}
	before := homeFiles(t)
	for _, args := range [][]string{{"claude", "model", "ultracode"}, {"claude", "effort", "ultracode"}, {"claude", "effort", "Ultracode"}} {
		out, err := printed(t, func() error { return run(args) })
		if err == nil || !strings.HasSuffix(err.Error(), ": magpie claude ultracode on") || out != "" {
			t.Errorf("magpie %s: %v %q, want it to say magpie claude ultracode on", strings.Join(args, " "), err, out)
		}
		if diff := filesChanged(before, homeFiles(t)); diff != "" {
			t.Fatalf("magpie %s wrote:\n%s", strings.Join(args, " "), diff)
		}
	}
	if _, err := printed(t, func() error { return run([]string{"claude", "ultracode", "on"}) }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"ultracode": true`) {
		t.Errorf("magpie claude ultracode on wrote:\n%s", b)
	}
}

// magpie codex DEFAULT wrote model = "DEFAULT" into Codex's config, magpie
// taken out with it, and magpie codex effort Default wrote
// model_reasoning_effort = "Default". default is magpie's word in any
// case, as help and a field's name are, and does what default does.
func TestAgentDefaultInAnyCase(t *testing.T) {
	path := codexOnAdvanced(t)
	for _, c := range []struct {
		args, want string
		gone       *regexp.Regexp
	}{
		{"codex effort Default", "✓ Codex effort default\n", regexp.MustCompile(`(?m)^model_reasoning_effort =`)},
		{"codex model DEFAULT", "✓ Codex model default\n", regexp.MustCompile(`(?m)^model =`)},
	} {
		out, err := printed(t, func() error { return run(strings.Fields(c.args)) })
		b, _ := os.ReadFile(path)
		if err != nil || out != c.want || c.gone.Match(b) {
			t.Errorf("magpie %s: %v %q, want %q\n%s", c.args, err, out, c.want, b)
		}
	}
	for _, word := range []string{"DEFAULT", "Default"} {
		path := codexOnAdvanced(t)
		out, err := printed(t, func() error { return run([]string{"codex", word}) })
		b, _ := os.ReadFile(path)
		if err != nil || out != "✓ Codex model gpt-5.5\n  disconnected from magpie, back to what it had before\n" ||
			!strings.Contains(string(b), "model = \"gpt-5.5\"\nmodel_reasoning_effort = \"high\"\n") || strings.Contains(string(b), "model_provider = ") {
			t.Errorf("magpie codex %s: %v %q, want Codex back on gpt-5.5\n%s", word, err, out, b)
		}
	}
}
