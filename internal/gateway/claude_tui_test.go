package gateway

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/wslrun"
)

// A Claude run is Claude Code's interactive mode on macOS, Linux and
// Windows, for this machine's Claude Code, unless the user turned it off
// or the run needs what only -p takes.
func TestClaudeTUIChosen(t *testing.T) {
	was := settings.Load()
	t.Cleanup(func() { _ = settings.Save(was) })
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	here := claudeCLI{path: "/usr/local/bin/claude"}
	text := func(s string) []Message {
		return []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: s}}}}
	}
	on := runtime.GOOS == "darwin" || runtime.GOOS == "linux" || runtime.GOOS == "windows"
	cases := []struct {
		name   string
		req    *Request
		binary claudeCLI
		print  bool
		want   bool
		why    string // what is logged, on macOS, Linux and Windows
	}{
		{"a plain turn", &Request{Messages: text("hi")}, here, false, on, ""},
		{"turned off", &Request{Messages: text("hi")}, here, true, false, "the interactive mode is turned off"},
		{"in WSL", &Request{Messages: text("hi")}, claudeCLI{path: "claude", wsl: &wslrun.Tool{Distro: "Ubuntu", Home: "/home/me"}}, false, on, ""},
		{"in WSL, its home unknown", &Request{Messages: text("hi")}, claudeCLI{path: "claude", wsl: &wslrun.Tool{Distro: "Ubuntu"}}, false, on, ""},
		{"safeguards", &Request{Messages: text("hi"), Safeguards: json.RawMessage(`{"a":1}`)}, here, false, on, ""},
		{"client cache marks", &Request{Messages: text("hi"), clientCache: &clientCache{}}, here, false, on, ""},
		{"a schema", &Request{Messages: text("hi"), Format: &Format{Type: "json_schema", Schema: json.RawMessage(`{"type":"object"}`)}}, here, false, on, ""},
		{"an image", &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Image, MediaType: "image/png", Data: "AA=="}}}}}, here, false, on, ""},
		{"an image by its URL", &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Image, URL: "https://example.com/a.png"}}}}}, here, false, false, "an image known by its URL"},
		{"a tool's image by its URL", &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: ToolResult, Images: []Part{{Kind: Image, URL: "https://example.com/a.png"}}}}}}}, here, false, false, "an image known by its URL"},
		{"a long first message", &Request{Messages: text(strings.Repeat("the codebase as read so far. ", 40000))}, here, false, on, ""},
	}
	for _, c := range cases {
		st := settings.Load()
		st.ClaudePrintMode = c.print
		if err := settings.Save(st); err != nil {
			t.Fatal(err)
		}
		if got, _ := claudeTUI(c.req, c.binary); got != c.want {
			t.Errorf("%s: claudeTUI = %v, want %v", c.name, got, c.want)
		}
		if got := claudePrintWhy(c.req, c.binary); on && got != c.why {
			t.Errorf("%s: -p because %q, want %q", c.name, got, c.why)
		}
	}
}

// The helper's line saying why it became -p is logged; the rest of Claude
// Code's stderr isn't.
func TestClaudeTUIFallbackLogged(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	w := &lockedWriter{run: &subscriptionRun{}}
	_, _ = w.Write([]byte("warming up\nclaude-tui: Claude Code ended before it showed its prompt (exit status 1): B; this run is -p\n"))
	_, _ = w.Write([]byte("Error: something else\n"))
	got := logs.String()
	if !strings.Contains(got, "claude: Claude Code ended before it showed its prompt (exit status 1): B; this run is -p") || strings.Contains(got, "warming up") || strings.Contains(got, "something else") {
		t.Fatalf("logged %q", got)
	}
	if !strings.Contains(w.run.stderr.String(), "something else") {
		t.Fatalf("stderr kept %q", w.run.stderr.String())
	}
}

// The helper's lines, why it ended and Claude Code's screen row by row,
// go to the log without the user's folders or secrets, as a one-off run's
// do (logOneOff): the screen is the user's own, a folder and a token on it.
func TestClaudeTUIScreenLoggedSafe(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	token := "sk-ant-oat01-" + strings.Repeat("Q7x", 20)
	w := &lockedWriter{run: &subscriptionRun{}}
	_, _ = w.Write([]byte("claude-tui: Claude Code did not take the message: ⎿ Error · Please run /login\n" +
		"claude-tui: screen| C:\\Users\\fremont\\work\n" +
		"claude-tui: screen| ❯ use " + token + "\n"))
	got := logs.String()
	for _, want := range []string{"claude: Claude Code did not take the message: ⎿ Error · Please run /login", "claude: screen| ~\\work", "claude: screen| ❯ use "} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"fremont", strings.Repeat("Q7x", 20)} {
		if strings.Contains(got, gone) {
			t.Errorf("the log has %q:\n%s", gone, got)
		}
	}
}

// A Claude Code in WSL is run by the helper through wsl.exe, its
// transcripts read from Windows: the distro's own folder through \\wsl$,
// or the account's config directory, a Windows folder. Where Windows
// can't read the distro's folder (no 9P redirector, as over ssh), or the
// probe didn't find its home, the helper reads the transcript inside the
// distro, never refusing the run; an account's folder is never asked
// about.
func TestClaudeTUIWSL(t *testing.T) {
	var asked []string
	readable := true
	old := wslStat
	wslStat = func(p string) (os.FileInfo, error) {
		asked = append(asked, p)
		if readable {
			return nil, nil
		}
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { wslStat = old })
	tool := wslrun.Tool{Distro: "Ubuntu", Path: "/home/me/.local/bin/claude", Home: "/home/me", Mount: "/mnt/"}
	homeless := tool
	homeless.Home = ""
	for _, tc := range []struct {
		name             string
		tool             wslrun.Tool
		readable         bool
		configDir        string
		projects, distro string
		asked            []string
	}{
		{"its own folder", tool, true, "", `\\wsl$\Ubuntu\home\me\.claude\projects`, "", []string{`\\wsl$\Ubuntu\home\me`}},
		{"an account's", tool, false, filepath.Join("acct", "1"), filepath.Join("acct", "1", "projects"), "", nil},
		{"its own folder, unread", tool, false, "", "", "Ubuntu", []string{`\\wsl$\Ubuntu\home\me`}},
		{"its home unknown", homeless, true, "", "", "Ubuntu", nil},
	} {
		asked, readable = nil, tc.readable
		c := claudeCLI{path: "claude", wsl: &tc.tool}
		var w claudetui.WSL
		if err := json.Unmarshal([]byte(c.tuiWSL(tc.configDir)), &w); err != nil {
			t.Fatal(err)
		}
		if w.Projects != tc.projects || w.Distro != tc.distro || w.Mount != "/mnt/" || !slices.Equal(asked, tc.asked) {
			t.Fatalf("%s: %+v, asked %q", tc.name, w, asked)
		}
		if len(w.Args) < 2 || w.Args[0] != "-d" || w.Args[1] != "Ubuntu" || w.Args[len(w.Args)-1] != tool.Path {
			t.Fatalf("%s: args %q", tc.name, w.Args)
		}
		if code, why := claudeMachineWhy(c); code == "wsl-home" {
			t.Fatalf("%s: every run refused: %s", tc.name, why)
		}
	}
}

// The provider page's codes for why every run is -p: the setting, none
// found; "" for the TUI, a WSL Claude Code whose home is unknown too.
func TestClaudeMachineWhy(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("no interactive mode on " + runtime.GOOS)
	}
	was := settings.Load()
	t.Cleanup(func() { _ = settings.Save(was) })
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	here := claudeCLI{path: "/usr/local/bin/claude"}
	for _, c := range []struct {
		name   string
		binary claudeCLI
		print  bool
		code   string
	}{
		{"here", here, false, ""},
		{"in WSL", claudeCLI{wsl: &wslrun.Tool{Distro: "Ubuntu", Home: "/home/me"}}, false, ""},
		{"turned off", here, true, "off"},
		{"in WSL, its home unknown", claudeCLI{wsl: &wslrun.Tool{Distro: "Ubuntu"}}, false, ""},
		{"none", claudeCLI{}, false, "missing"},
	} {
		st := settings.Load()
		st.ClaudePrintMode = c.print
		if err := settings.Save(st); err != nil {
			t.Fatal(err)
		}
		if code, why := claudeMachineWhy(c.binary); code != c.code || (code == "") != (why == "") {
			t.Errorf("%s: %q %q, want code %q", c.name, code, why, c.code)
		}
	}
	claudeTUIRuns = false
	if code, _ := claudeMachineWhy(here); code != "no-shim" {
		t.Errorf("no shim: %q", code)
	}
}
