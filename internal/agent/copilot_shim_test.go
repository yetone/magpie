package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Tony-ooo's Windows has VS Code 1.143, which ships a copilot launcher of
// its own in bin\copilot-shim in its install folder and puts that on the
// user's PATH (#3269). The launcher runs only to ask for the CLI to be
// installed; it is not the CLI, and ~/.copilot doesn't exist. A copilot a
// user installed itself still is.
func TestCopilotLauncherFromAnEditorIsNotTheCLI(t *testing.T) {
	home := t.TempDir()
	name := "copilot"
	if runtime.GOOS == "windows" {
		name += ".cmd"
		t.Setenv("PATHEXT", ".CMD")
	}
	put := func(dir string) {
		t.Helper()
		writeAt(t, filepath.Join(dir, name), "vscode-copilot-launcher\n")
		if err := os.Chmod(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
	}
	// the shim in the install's bin, of the layout VS Code writes
	install := filepath.Join(t.TempDir(), "Microsoft VS Code")
	writeAt(t, filepath.Join(install, "resources", "app", "product.json"), `{"nameShort":"Visual Studio Code"}`)
	put(filepath.Join(install, "bin", "copilot-shim"))
	if copilot(home, filepath.Join(home, ".config")).Detected() {
		t.Error("VS Code's copilot launcher is taken for the Copilot CLI")
	}
	if bin, _ := copilot(home, filepath.Join(home, ".config")).cliBin(); bin != "" {
		t.Errorf("the CLI looked for is %s, the launcher", bin)
	}
	if bin := copilot(home, filepath.Join(home, ".config")).BinPath(); bin != "" {
		t.Errorf("the agent's program is %s, the launcher", bin)
	}
	// a Mac's editor keeps its launcher inside the app proper, whose
	// product.json is a level above bin
	app := filepath.Join(t.TempDir(), "Visual Studio Code.app", "Contents", "Resources", "app")
	writeAt(t, filepath.Join(app, "product.json"), `{"applicationName":"code"}`)
	put(filepath.Join(app, "bin", "copilot-shim"))
	if copilot(home, filepath.Join(home, ".config")).Detected() {
		t.Error("the editor app's copilot launcher is taken for the Copilot CLI")
	}
	// a launcher in the editor's bin, no shim folder of its own
	put(filepath.Join(install, "bin"))
	if copilot(home, filepath.Join(home, ".config")).Detected() {
		t.Error("a copilot launcher in the editor's bin is taken for the Copilot CLI")
	}
	// the CLI itself, where a user's installs put it, still is one
	cli := t.TempDir()
	writeAt(t, filepath.Join(cli, name), "")
	if err := os.Chmod(filepath.Join(cli, name), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", cli)
	if !copilot(home, filepath.Join(home, ".config")).Detected() {
		t.Error("the copilot command on PATH no longer finds the Copilot CLI")
	}
	if bin, _ := copilot(home, filepath.Join(home, ".config")).cliBin(); bin == "" {
		t.Error("the copilot command on PATH no longer stands for the CLI")
	}
	// a product.json further up than an editor keeps one, as a folder of
	// the user's may hold (a checkout of VS Code's own source), says
	// nothing of the CLI: npm's global bin three folders below it
	far := t.TempDir()
	writeAt(t, filepath.Join(far, "product.json"), `{"nameShort":"Code - OSS"}`)
	npm := filepath.Join(far, "tools", "npm-global", "lib", "bin")
	writeAt(t, filepath.Join(npm, name), "")
	if err := os.Chmod(filepath.Join(npm, name), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", npm)
	if !copilot(home, filepath.Join(home, ".config")).Detected() {
		t.Error("a product.json far above the CLI's folder makes the CLI an editor's launcher")
	}
	if runtime.GOOS == "windows" {
		return
	}
	// a link on PATH to the editor's launcher (a Linux package's
	// /usr/bin link) is the launcher it points to
	links := t.TempDir()
	if err := os.Symlink(filepath.Join(install, "bin", "copilot-shim", name), filepath.Join(links, name)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", links)
	if copilot(home, filepath.Join(home, ".config")).Detected() {
		t.Error("a link to the editor's launcher is taken for the Copilot CLI")
	}
}

// VS Code 1.139's versioned Windows layout (win32VersionedUpdate) keeps
// the app's payload in a folder named for the build's commit — the short
// hash, ten lowercase hex digits — with resources\app under it, and bin
// beside it: Tony-ooo's 1.144.0 has bin\copilot-shim at
// <install>, product.json only at <install>\2c33b50b16\resources\app
// (microsoft/vscode#343045), and every product.json the six places of
// 6f435677c look in is absent, so the launcher came back as the CLI
// (#3269). A folder of the user's beside it — one not named as a commit's
// short hash — is still nothing of an editor's.
func TestCopilotLauncherBesideTheBuildsHashFolderIsNotTheCLI(t *testing.T) {
	home := t.TempDir()
	name := "copilot"
	if runtime.GOOS == "windows" {
		name += ".cmd"
		t.Setenv("PATHEXT", ".CMD")
	}
	install := filepath.Join(t.TempDir(), "Microsoft VS Code")
	writeAt(t, filepath.Join(install, "2c33b50b16", "resources", "app", "product.json"), `{"nameShort":"Visual Studio Code"}`)
	shim := filepath.Join(install, "bin", "copilot-shim")
	writeAt(t, filepath.Join(shim, name), "vscode-copilot-launcher\n")
	if err := os.Chmod(filepath.Join(shim, name), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim)
	if copilot(home, filepath.Join(home, ".config")).Detected() {
		t.Error("VS Code 1.144's copilot launcher is taken for the Copilot CLI")
	}
	if bin, _ := copilot(home, filepath.Join(home, ".config")).cliBin(); bin != "" {
		t.Errorf("the CLI looked for is %s, the launcher", bin)
	}
	// a folder beside bin that no commit's short hash names, holding a
	// product.json of its own (a user's checkout dropped into the
	// install), is no editor's: with the build's hash folder gone, the
	// launcher reads as the CLI again, as it did before
	writeAt(t, filepath.Join(install, "workshop", "resources", "app", "product.json"), `{"nameShort":"Visual Studio Code"}`)
	os.RemoveAll(filepath.Join(install, "2c33b50b16"))
	if !copilot(home, filepath.Join(home, ".config")).Detected() {
		t.Error("a folder beside bin that is no build's hash counts as an editor's install")
	}
}
