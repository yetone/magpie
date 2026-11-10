package appdir

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// MiniMaxDir is the data folder MiniMax Code keeps config.yaml, its
// sessions and its skills in. Every place magpie reads or writes MiniMax
// Code's files goes through here.
func MiniMaxDir() string {
	home, _ := os.UserHomeDir()
	return MiniMaxDirIn(home)
}

// MiniMaxDirIn is MiniMaxDir with home as the user's home folder. It is
// found as MiniMax Code 3.1.13 finds it (the desktop app's
// local-runtime/data-dir.js resolveLocalRuntimeDataDirSelection, and
// @mavis/config's config.js for the mcode runtime): $MINIMAX_DATA_DIR,
// else $MAVIS_DATA_DIR, else the desktop app's own setting — Settings'
// data folder, which moves the folder to <parent>/.minimax and keeps the
// parent as config.localRuntimeDataParentDir in its electron-store file —
// else ~/.minimax. YujunT46308 on X (MiniMax Code 3.1.13 CN): with the
// folder moved, magpie wrote ~/.minimax/config.yaml, which the app no
// longer read. A desktop file that is missing, can't be read or doesn't
// parse is the default folder, as electron-store reads one it can't parse
// as its defaults.
func MiniMaxDirIn(home string) string {
	for _, v := range []string{"MINIMAX_DATA_DIR", "MAVIS_DATA_DIR"} {
		if d := strings.TrimSpace(Getenv(v)); d != "" {
			return d
		}
	}
	if p := miniMaxDesktopParent(home, runtime.GOOS); p != "" {
		return filepath.Join(p, ".minimax")
	}
	return filepath.Join(home, ".minimax")
}

// miniMaxDesktopStores are MiniMax Code Desktop's electron-store files,
// under its Electron userData, named for its app name (app-identity.js:
// "MiniMax" for the China build, com.minimax.agent.cn, "MiniMax Agent"
// for the global one) and its store (storage.ipc.js STORE_NAME). The China
// build's is first: on a computer with both, the first that sets a parent
// is the one followed.
var miniMaxDesktopStores = [][2]string{
	{"MiniMax", "minimax-agent-cn-config.json"},
	{"MiniMax Agent", "minimax-agent-config.json"},
}

// miniMaxDesktopParent is the parent folder MiniMax Code Desktop moved its
// data folder to, or "" when it moved none. Electron's userData is in
// ~/Library/Application Support on a Mac and %APPDATA% on Windows; the app
// ships for no other system.
func miniMaxDesktopParent(home, goos string) string {
	var base string
	switch goos {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		if base = Getenv("APPDATA"); base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
	default:
		return ""
	}
	for _, s := range miniMaxDesktopStores {
		b, err := os.ReadFile(filepath.Join(base, s[0], s[1]))
		if err != nil {
			continue
		}
		var st struct {
			Config struct {
				Parent any `json:"localRuntimeDataParentDir"`
			} `json:"config"`
		}
		if json.Unmarshal(b, &st) != nil {
			continue
		}
		// the app takes only a string, trimmed, made absolute
		// (setConfiguredLocalRuntimeDataParentDir); a relative one would
		// be its working folder's, which isn't magpie's
		p, _ := st.Config.Parent.(string)
		if p = strings.TrimSpace(p); p != "" && filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
	}
	return ""
}
