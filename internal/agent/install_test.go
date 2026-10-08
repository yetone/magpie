package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// The install commands are the vendors' own (#727): the installer their
// docs give first, then npm's package, the one the update uses; per OS.
func TestInstallCommands(t *testing.T) {
	cmds := func(id, goos string) []string {
		var out []string
		for _, c := range installCommands(id, goos, true) {
			out = append(out, c.Via+": "+c.Command)
		}
		return out
	}
	for _, c := range []struct {
		id, goos string
		want     []string
	}{
		{"claude", "darwin", []string{"script: curl -fsSL https://claude.ai/install.sh | bash", "npm: npm install -g @anthropic-ai/claude-code"}},
		{"claude", "windows", []string{"powershell: irm https://claude.ai/install.ps1 | iex", "npm: npm install -g @anthropic-ai/claude-code"}},
		{"codex", "darwin", []string{"script: curl -fsSL https://chatgpt.com/codex/install.sh | sh", "brew: brew install --cask codex", "npm: npm install -g @openai/codex"}},
		{"codex", "linux", []string{"script: curl -fsSL https://chatgpt.com/codex/install.sh | sh", "npm: npm install -g @openai/codex"}},
		{"gemini", "linux", []string{"brew: brew install gemini-cli", "npm: npm install -g @google/gemini-cli"}},
		{"gemini", "windows", []string{"npm: npm install -g @google/gemini-cli"}},
		{"opencode", "darwin", []string{"script: curl -fsSL https://opencode.ai/install | bash", "npm: npm install -g opencode-ai"}},
		{"opencode", "windows", []string{"npm: npm install -g opencode-ai"}},
		{"pi", "linux", []string{"npm: npm install -g @earendil-works/pi-coding-agent"}},
		{"cursor", "darwin", nil}, // no command magpie knows
	} {
		if got := cmds(c.id, c.goos); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s on %s: %q, want %q", c.id, c.goos, got, c.want)
		}
	}
}

// Only the agents not on this machine are offered, and not WSL's twins.
func TestInstallsOnlyMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	here := &Agent{ID: "codex", Name: "Codex", Path: filepath.Join(home, "codex.toml"), Dir: home}
	gone := &Agent{ID: "gemini", Name: "Gemini CLI", Icon: "gemini", Path: filepath.Join(home, "nope", "settings.json"), Bin: "gemini"}
	wsl := &Agent{ID: "claude@wsl:Ubuntu", Name: "Claude Code", WSL: "Ubuntu", Path: filepath.Join(home, "nope", "x")}
	none := &Agent{ID: "cursor", Name: "Cursor", Path: filepath.Join(home, "nope", "y")}
	got := installsOf([]*Agent{here, gone, wsl, none}, "darwin", true)
	if len(got) != 1 || got[0].ID != "gemini" || got[0].Name != "Gemini CLI" || got[0].Icon != "gemini" || len(got[0].Commands) != 2 {
		t.Fatalf("installs: %+v", got)
	}
	if got := installsOf(nil, "linux", true); got == nil {
		t.Error("none is [] for the page, not null")
	}
}

// With no Node.js here, an npm command installs it first, in the same
// line (#727, Sun1090): nvm and its LTS Node on a Mac or Linux, winget's
// Node.js LTS on Windows; the vendors' own installers are as they were.
func TestInstallCommandsWithoutNode(t *testing.T) {
	nvm := `export NVM_DIR="$HOME/.nvm" && mkdir -p "$NVM_DIR" && ` +
		`t=$(mktemp) && curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/` + nvmVersion +
		`/install.sh -o "$t" && bash "$t" && . "$NVM_DIR/nvm.sh" && nvm install --lts && npm install -g `
	winget := "winget install -e --id OpenJS.NodeJS.LTS --accept-source-agreements --accept-package-agreements; $env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User'); npm.cmd install -g "
	for _, c := range []struct {
		id, goos string
		want     []InstallCmd
	}{
		{"pi", "darwin", []InstallCmd{{Via: "npm", Node: "nvm-mac", Command: nvm + "@earendil-works/pi-coding-agent"}}},
		{"pi", "linux", []InstallCmd{{Via: "npm", Node: "nvm", Command: nvm + "@earendil-works/pi-coding-agent"}}},
		{"pi", "windows", []InstallCmd{{Via: "npm", Node: "winget", Command: winget + "@earendil-works/pi-coding-agent"}}},
		{"claude", "linux", []InstallCmd{
			{Via: "script", Command: "curl -fsSL https://claude.ai/install.sh | bash"},
			{Via: "npm", Node: "nvm", Command: nvm + "@anthropic-ai/claude-code"},
		}},
	} {
		if got := installCommands(c.id, c.goos, false); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s on %s: %+v, want %+v", c.id, c.goos, got, c.want)
		}
	}
	// with Node here, npm's alone, and no Node said
	if got := installCommands("pi", "darwin", true); len(got) != 1 || got[0].Node != "" || got[0].Command != "npm install -g @earendil-works/pi-coding-agent" {
		t.Errorf("with node: %+v", got)
	}
	// installsOf passes it on
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	gone := &Agent{ID: "pi", Name: "Pi", Path: filepath.Join(home, "nope", "models.json"), Bin: "pi"}
	if got := installsOf([]*Agent{gone}, "linux", false); len(got) != 1 || got[0].Commands[0].Node != "nvm" {
		t.Errorf("installs without node: %+v", got)
	}
}

// npm is found in a folder of a user's tools (nvm's, Homebrew's), as
// npm.cmd on Windows; a folder without it, or a folder named npm, isn't it.
func TestNpmIn(t *testing.T) {
	empty, nvm, win, odd := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	testenv.Program(t, filepath.Join(nvm, "npm"), "#!/bin/sh\n")
	if err := os.WriteFile(filepath.Join(win, "npm.cmd"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(odd, "npm"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		dirs []string
		goos string
		want bool
	}{
		{nil, "darwin", false},
		{[]string{empty, odd, ""}, "linux", false},
		{[]string{empty, nvm}, "darwin", true},
		{[]string{nvm}, "windows", false},
		{[]string{empty, win}, "windows", true},
		{[]string{win}, "linux", false},
	} {
		if got := npmIn(c.dirs, c.goos); got != c.want {
			t.Errorf("npmIn(%q, %s) = %v, want %v", c.dirs, c.goos, got, c.want)
		}
	}
}

// The command without Node is one line every shell it's run in reads:
// bash and zsh on a Mac or Linux.
func TestInstallWithoutNodeParses(t *testing.T) {
	c := npmInstall("@earendil-works/pi-coding-agent", "linux", false).Command
	for _, sh := range []string{"bash", "zsh", "sh"} {
		p, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		if out, err := exec.Command(p, "-n", "-c", c).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v %s", sh, err, out)
		}
	}
}

// A download that fails stops the line where it failed: piped into a
// shell, the line's status was the shell's, so a 404 or a blocked host
// went on to source nvm.sh and told the user that file was missing. The
// command now downloads to a file, and its own failure ends the chain.
func TestInstallWithoutNodeStopsWhenTheDownloadFails(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl to test the download with")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer bad.Close()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("NVM_DIR", filepath.Join(home, ".nvm"))
	c := strings.Replace(npmInstall("@earendil-works/pi-coding-agent", "linux", false).Command,
		"https://raw.githubusercontent.com/nvm-sh/nvm/"+nvmVersion+"/install.sh", bad.URL+"/install.sh", 1)
	for _, sh := range []string{"bash", "zsh", "sh"} {
		p, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		out, err := exec.Command(p, "-c", c).CombinedOutput()
		if err == nil {
			t.Errorf("%s: the line went on past the failed download: %s", sh, out)
			continue
		}
		if strings.Contains(string(out), "nvm.sh") {
			t.Errorf("%s: the failed download is still told as a missing nvm.sh: %s", sh, out)
		}
	}
}
