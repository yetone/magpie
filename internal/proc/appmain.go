package proc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// errAppMain is what a probe of an app's own program (a Mac bundle's, a
// Windows GUI program) ends with, unrun.
var errAppMain = errors.New("not run: it is an app's own program, which opens the app's window")

// appMain reports whether the program at path is the main executable of
// an app a user opens (…/X.app/Contents/MacOS/<CFBundleExecutable>, X.app
// in no other bundle), after links. Run, that opens the app: its Dock icon
// and window, whatever the arguments, and a GUI app given --version never
// answers it. An `opencode` on PATH that resolves to OpenCode.app's own
// program (a folder of the bundle on PATH, where a Mac's case-insensitive
// disk finds OpenCode as opencode, or a link to it) put OpenCode's window
// up each time magpie asked its version, at every start and after each
// update (saint carol on Discord). The bundle's other programs (OpenCode's
// opencode-cli, Kiro CLI.app's kiro-cli beside its kiro_cli_desktop) are
// CLIs and are run, and so is a bundle inside an app's own: ChatGPT.app's
// codex is CodexCLI.app's main executable. When Info.plist can't be read
// (a binary one, as Kiro CLI.app's is), the program named as its bundle
// is taken for the main one.
func appMain(path string) bool {
	if path == "" {
		return false
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	macos := filepath.Dir(path)
	contents := filepath.Dir(macos)
	bundle := filepath.Dir(contents)
	if !strings.EqualFold(filepath.Base(macos), "MacOS") || !strings.EqualFold(filepath.Base(contents), "Contents") ||
		!strings.HasSuffix(strings.ToLower(filepath.Base(bundle)), ".app") {
		return false
	}
	for up := filepath.Dir(bundle); up != filepath.Dir(up); up = filepath.Dir(up) {
		if strings.HasSuffix(strings.ToLower(filepath.Base(up)), ".app") {
			return false // a helper or a CLI another app ships
		}
	}
	exe := bundleExecutable(filepath.Join(contents, "Info.plist"))
	if exe == "" {
		exe = filepath.Base(bundle)
		exe = exe[:len(exe)-len(".app")]
	}
	return strings.EqualFold(exe, filepath.Base(path))
}

var bundleExeRe = lazyre.New(`<key>CFBundleExecutable</key>\s*<string>([^<]+)</string>`)

// bundleExecutable is the CFBundleExecutable an XML Info.plist names, ""
// when it can't be read (a binary plist too) or names none.
func bundleExecutable(plist string) string {
	b, err := os.ReadFile(plist)
	if err != nil {
		return ""
	}
	if m := bundleExeRe.FindSubmatch(b); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}
