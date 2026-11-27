package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// codexStatusOf is what config.toml says of the footer: the flag, the
// server's command and args, and the provider picked.
func codexStatusOf(t *testing.T, config string) (flag any, command any, args any, picked any) {
	t.Helper()
	var doc map[string]any
	if err := toml.Unmarshal([]byte(config), &doc); err != nil {
		t.Fatalf("%v:\n%s", err, config)
	}
	features, _ := doc["features"].(map[string]any)
	servers, _ := doc["mcp_servers"].(map[string]any)
	srv, _ := servers[codexStatusServer].(map[string]any)
	tui, _ := doc["tui"].(map[string]any)
	return features[codexStatusFeature], srv["command"], srv["args"], tui["status_line_provider"]
}

// lyj1115 on X: the account's quota or the relay's balance under Codex's
// chat box, off unless turned on. On writes the feature flag, magpie's
// server by its full path and the provider; off takes the three out, and
// nothing else in the file moves.
func TestCodexStatusLineSwitch(t *testing.T) {
	codexReadersAre(t, "0.171.0-alpha.13")
	const before = "model = \"gpt-5.5\"\n\n[tui]\ntheme = \"dark\"\n"
	home, read := codexHome(t, "", before)
	claudeStatusAt(t, "/Applications/Magpie.app/Contents/MacOS/magpie")
	f := codex(home).Field("statusline")
	if f == nil {
		t.Fatal("Codex has no statusline field")
	}
	if f.Get() != "" {
		t.Fatalf("on before it was turned on: %q", f.Get())
	}
	if opts := f.Options(nil); len(opts) != 1 || opts[0].Value != "on" || !strings.Contains(opts[0].Note, "Codex") {
		t.Fatalf("options: %+v", opts)
	}
	if err := f.Set("on"); err != nil {
		t.Fatal(err)
	}
	flag, cmd, args, picked := codexStatusOf(t, read())
	p, _ := picked.(map[string]any)
	if flag != true || cmd != "/Applications/Magpie.app/Contents/MacOS/magpie" ||
		strings.Join(strs(args), " ") != "statusline --codex" || !strings.Contains(read(), "env_vars = ['MAGPIE_ADDR']") ||
		p["server"] != "quota_line" || p["tool"] != "statusline" ||
		!strings.Contains(read(), `theme = "dark"`) || !strings.Contains(read(), `model = "gpt-5.5"`) {
		t.Fatalf("on:\n%s", read())
	}
	if f.Get() != "on" {
		t.Fatalf("Get after on: %q", f.Get())
	}
	on := read()
	if err := f.Set("on"); err != nil || read() != on {
		t.Fatalf("on again: %v\n%s", err, read())
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := read(); strings.Contains(got, "quota_line") || strings.Contains(got, codexStatusFeature) ||
		strings.Contains(got, "status_line_provider") || !strings.Contains(got, `theme = "dark"`) {
		t.Fatalf("off:\n%s", got)
	}
	if err := f.Set("maybe"); err == nil {
		t.Fatal("a value neither on nor off was taken")
	}
}

func strs(v any) []string {
	xs, _ := v.([]any)
	var out []string
	for _, x := range xs {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// What is the user's is left as it is: a provider of their own (or a
// server named as magpie's) keeps the switch away and on is refused; a
// flag they set themselves stays when magpie's footer is turned off, and
// one they turned off keeps it off.
func TestCodexStatusLineLeavesTheUsers(t *testing.T) {
	codexReadersAre(t, "0.171.0-alpha.13")
	claudeStatusAt(t, "/usr/local/bin/magpie")
	for name, config := range map[string]string{
		"their provider": "[tui]\nstatus_line_provider = { server = \"mine\", tool = \"line\" }\n",
		"their server":   "[mcp_servers.quota_line]\ncommand = \"node\"\nargs = [\"q.js\"]\n",
		"flag off":       "[features]\ntui_extensions_statusline = false\n",
		"unreadable":     "[tui\n",
	} {
		t.Run(name, func(t *testing.T) {
			home, read := codexHome(t, "", config)
			f := codex(home).Field("statusline")
			if opts := f.Options(nil); len(opts) != 0 {
				t.Fatalf("offered beside the user's: %+v", opts)
			}
			if err := f.Set("on"); err == nil {
				t.Fatal("on was taken")
			}
			if read() != config {
				t.Fatalf("written:\n%s", read())
			}
			if err := f.Set(""); err != nil || read() != config {
				t.Fatalf("off: %v\n%s", err, read())
			}
		})
	}
	t.Run("their flag on", func(t *testing.T) {
		const config = "[features]\ntui_extensions_statusline = true\n"
		home, read := codexHome(t, "", config)
		f := codex(home).Field("statusline")
		if err := f.Set("on"); err != nil {
			t.Fatal(err)
		}
		if err := f.Set(""); err != nil {
			t.Fatal(err)
		}
		if flag, _, _, _ := codexStatusOf(t, read()); flag != true || strings.Contains(read(), "quota_line") {
			t.Fatalf("off took the user's flag, or left magpie's server:\n%s", read())
		}
	})
	t.Run("default provider", func(t *testing.T) {
		home, read := codexHome(t, "", "[tui]\nstatus_line_provider = \"default\"\n")
		f := codex(home).Field("statusline")
		if err := f.Set("on"); err != nil {
			t.Fatal(err)
		}
		if f.Get() != "on" {
			t.Fatalf("Codex's own default kept magpie's out:\n%s", read())
		}
	})
}

// 0.170.0 reads the keys and draws nothing: the switch is offered only
// where a Codex CLI here draws a provider's footer.
func TestCodexStatusLineNeedsACodexThatDraws(t *testing.T) {
	for _, vs := range [][]string{{"0.170.0"}, {""}, {"0.171.0-alpha.11"}, nil} {
		codexReadersAre(t, vs...)
		home, _ := codexHome(t, "", "")
		if opts := codex(home).Field("statusline").Options(nil); len(opts) != 0 {
			t.Fatalf("offered with Codex %q: %+v", vs, opts)
		}
	}
	codexReadersAre(t, "0.170.0", "0.171.0-alpha.12")
	home, _ := codexHome(t, "", "")
	if opts := codex(home).Field("statusline").Options(nil); len(opts) != 1 {
		t.Fatalf("not offered beside a Codex that draws it: %+v", opts)
	}
}

// Disconnect takes magpie's footer out with the rest of magpie.
func TestCodexStatusLineGoesWithUnwire(t *testing.T) {
	codexReadersAre(t, "0.171.0-alpha.13")
	claudeStatusAt(t, "/usr/local/bin/magpie")
	home, read := codexHome(t, "", "model = \"gpt-5.5\"\n")
	cx := codex(home)
	if err := cx.Field("statusline").Set("on"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Unwire(); err != nil {
		t.Fatal(err)
	}
	if got := read(); strings.Contains(got, "quota_line") || strings.Contains(got, codexStatusFeature) {
		t.Fatalf("left after Disconnect:\n%s", got)
	}
}

// A distro's Codex isn't offered the switch, running or stopped: the
// server would name this computer's magpie (only on this computer, as
// Claude Code's).
func TestWSLCodexNoStatusLine(t *testing.T) {
	codexReadersAre(t, "0.171.0-alpha.13")
	root, _ := claudeDistroHome(t, "")
	os.MkdirAll(filepath.Join(root, "home", "me", ".codex"), 0o755)
	d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"dir:.codex": true}, Gateway: "172.20.0.1"}
	k := wslKindOf("codex")
	for name, a := range map[string]*Agent{"running": wslAgent(k, d), "stopped": asleep(wslAgent(k, d), k, d)} {
		f := a.Field("statusline")
		if f == nil {
			t.Fatalf("%s distro: no statusline field", name)
		}
		if o := f.Options(nil); len(o) != 0 {
			t.Errorf("%s distro offers %v", name, o)
		}
	}
	if err := wslAgent(k, d).Field("statusline").Set("on"); err == nil {
		t.Error("on taken in a distro")
	}
}

// The status line's keys are decoded once per change of config.toml, not
// on every look: a real Codex config (~11k [projects] tables, 1.5 MB) was
// decoded whole on each window poll that asked for Codex's values (#2278).
// A change made outside magpie is still seen.
func TestCodexStatusLineReadOncePerChange(t *testing.T) {
	codexReadersAre(t, "0.171.0-alpha.13")
	home, _ := codexHome(t, "", "model = \"gpt-5.5\"\n")
	claudeStatusAt(t, "/Applications/Magpie.app/Contents/MacOS/magpie")
	n := 0
	codexStatusParsed = func() { n++ }
	t.Cleanup(func() { codexStatusParsed = nil })
	f := codex(home).Field("statusline")
	for range 5 {
		if f.Get() != "" {
			t.Fatalf("on: %q", f.Get())
		}
	}
	if n != 1 {
		t.Fatalf("decoded %d times for five looks at one file", n)
	}
	if err := f.Set("on"); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "on" {
		t.Fatalf("Get after on: %q", f.Get())
	}
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.WriteFile(path, []byte("model = \"gpt-5.5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "" {
		t.Fatalf("an edit outside magpie wasn't seen: %q", f.Get())
	}
}
