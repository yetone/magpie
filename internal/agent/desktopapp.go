package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
)

// An agent that is a desktop app as well as a CLI has the app's version shown
// beside the CLI's (#1334, laacmilan: with Codex's CLI and its app both
// installed, the Agents page showed only the CLI's version). Both read the
// same settings, so one row stands for both.

// desktopApp is the macOS bundle an agent's desktop app is known by: its
// bundle id, and the names it has been installed under. OpenAI's Codex app
// is ChatGPT.app (com.openai.codex) since it took ChatGPT's name; the bundle
// id tells it from the ChatGPT chat app (com.openai.chat) of the same name.
// win is its executable under %LOCALAPPDATA%\Programs, where a per-user
// installer puts it on Windows, "" for one magpie doesn't know there.
type desktopApp struct {
	id    string
	names []string
	win   string
}

var desktopApps = map[string]desktopApp{
	"codex": {id: "com.openai.codex", names: []string{"ChatGPT.app", "Codex.app"}},
	// DeepSeek Harness Desktop (0.2.0-rc.2's DeepSeek Harness.app,
	// com.deepseek.dsh) reads ~/.dsh as the CLI does — its own profile,
	// profiles/desktop, beside the CLI's — and needs no CLI: its terminal
	// command is optional (star on Discord: 未找到 CLI with the desktop app
	// in use). On Windows its installer (electron-builder's per-user NSIS,
	// productName DeepSeek Harness) puts it under %LOCALAPPDATA%\Programs.
	"dsh": {id: "com.deepseek.dsh", names: []string{"DeepSeek Harness.app"}, win: filepath.Join("DeepSeek Harness", "DeepSeek Harness.exe")},
}

// appFolders are where apps are installed; a var so tests can point it
// elsewhere: /Applications and ~/Applications on macOS, %LOCALAPPDATA%\Programs
// on Windows (the Codex app there is a Store package magpie doesn't read yet).
var appFolders = func() []string {
	switch runtime.GOOS {
	case "darwin":
		dirs := []string{"/Applications"}
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(home, "Applications"))
		}
		return dirs
	case "windows":
		if local := appdir.Getenv("LOCALAPPDATA"); local != "" {
			return []string{filepath.Join(local, "Programs")}
		}
	}
	return nil
}

var (
	plistIDRe      = regexp.MustCompile(`<key>CFBundleIdentifier</key>\s*<string>([^<]+)</string>`)
	plistVersionRe = regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)
)

// AppVersion is the version of the agent's desktop app installed here, ""
// for an agent without one magpie knows, one not installed, or an Info.plist
// that doesn't say (a binary one isn't read).
func (a *Agent) AppVersion() string {
	app, ok := desktopApps[a.ID]
	if !ok || a.WSL != "" {
		return ""
	}
	for _, dir := range appFolders() {
		for _, name := range app.names {
			b, err := os.ReadFile(filepath.Join(dir, name, "Contents", "Info.plist"))
			if err != nil {
				continue
			}
			id, v := plistIDRe.FindSubmatch(b), plistVersionRe.FindSubmatch(b)
			if id == nil || v == nil || strings.TrimSpace(string(id[1])) != app.id {
				continue
			}
			return strings.TrimSpace(string(v[1]))
		}
	}
	return ""
}

// HasApp reports whether the agent's desktop app is installed here: its
// bundle on macOS (AppVersion), its executable on Windows.
func (a *Agent) HasApp() bool {
	if a.AppVersion() != "" {
		return true
	}
	app, ok := desktopApps[a.ID]
	if !ok || a.WSL != "" || app.win == "" {
		return false
	}
	for _, dir := range appFolders() {
		if isFile(filepath.Join(dir, app.win)) {
			return true
		}
	}
	return false
}
