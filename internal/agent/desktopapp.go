package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// An agent that is a desktop app as well as a CLI has the app's version shown
// beside the CLI's (#1334, laacmilan: with Codex's CLI and its app both
// installed, the Agents page showed only the CLI's version). Both read the
// same settings, so one row stands for both.

// desktopApp is the macOS bundle an agent's desktop app is known by: its
// bundle id, and the names it has been installed under. OpenAI's Codex app
// is ChatGPT.app (com.openai.codex) since it took ChatGPT's name; the bundle
// id tells it from the ChatGPT chat app (com.openai.chat) of the same name.
type desktopApp struct {
	id    string
	names []string
}

var desktopApps = map[string]desktopApp{
	"codex": {id: "com.openai.codex", names: []string{"ChatGPT.app", "Codex.app"}},
}

// appFolders are where apps are installed; a var so tests can point it
// elsewhere. Only macOS's are known: the Codex app on Windows is a Store
// package magpie doesn't read yet.
var appFolders = func() []string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	dirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	return dirs
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
