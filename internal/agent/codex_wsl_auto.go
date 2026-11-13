package agent

import (
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// codexTwinAutoKey, after a twin's id, marks in the stash that magpie
// connected it once from Windows' Codex being connected (or found it
// connected or disconnected already): it is never done again, so a Disconnect
// or a change the user makes in the distro stands.
const codexTwinAutoKey = ".autoconnected"

// ConnectCodexTwins connects Codex in each running WSL distro when Codex on
// Windows is connected. The Codex app's work mode on a project whose run
// environment is WSL starts its app-server inside the distro, on the
// distro's ~/.codex, not Windows' (the app resolves CODEX_HOME there with
// `bash -lc 'printf %s "${CODEX_HOME:-$HOME/.codex}"'`): a Codex connected
// only on Windows listed OpenAI's models alone there (@not_4O4 on X).
//
// It runs once per distro, and not at all for one the user disconnected, one
// not running (asking a stopped distro starts it), one whose config names a
// provider or base URL that isn't magpie's, or while Windows' Codex isn't
// connected. Off Windows there are no distros and nothing happens.
func ConnectCodexTwins() {
	if !wslLooks() {
		return
	}
	var win *Agent
	for _, a := range All() {
		if a.ID == "codex" {
			win = a
			break
		}
	}
	if win == nil || !win.Wired() {
		return
	}
	for _, d := range wslDistros() {
		k := wslKindOf("codex")
		if !d.Running || !k.found(d) {
			continue
		}
		a := wslAgent(k, d)
		mark := a.ID + codexTwinAutoKey
		if stashLoad()[mark] == "1" {
			continue
		}
		if a.Disconnected() || a.Wired() {
			stash(map[string]string{mark: "1"})
			continue
		}
		if codexTwinHasOwn(d.place(a.ID)) {
			continue
		}
		if _, err := a.ConnectHow(); err != nil {
			// no model to connect to yet, or a file that can't be written
			// now: tried again at the next sync
			continue
		}
		stash(map[string]string{mark: "1"})
	}
}

// codexTwinHasOwn is whether the distro's Codex config names a provider or a
// base URL that isn't magpie's: the user's own, which magpie doesn't take
// over without being asked.
func codexTwinHasOwn(at place) bool {
	path := filepath.Join(at.codexHome(), "config.toml")
	if p, _ := edit.GetTOMLTop(path, "model_provider"); p != "" && p != magpieID {
		return true
	}
	if u, _ := edit.GetTOMLTop(path, "openai_base_url"); u != "" && !strings.Contains(u, "/backend-api/codex") {
		return true
	}
	return false
}
