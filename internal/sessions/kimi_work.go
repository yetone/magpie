package sessions

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/filememo"
)

// Kimi's desktop app's Work (CNSA0422 on X: its calls never reached
// Usage). Read from Kimi 3.2.27's own bundle (com.moonshot.kimichat), whose
// Work agent is Daimon 1.0.10 running the Kimi Code runtime 0.5.1 it
// vendors. The app starts Daimon with KIMI_SHARE_DIR set to its share
// folder: <userData>/daimon-share, or the absolute shareDir the app keeps
// in <userData>/daimon-storage.json when Settings moved it. userData is
// Electron's, kimi-desktop under ~/Library/Application Support on a Mac and
// %APPDATA% on Windows (and ~/.config on Linux). Daimon run on its own,
// without the variable, shares ~/.kimi.
//
// Daimon keeps its state in <share>/daimon and gives Kimi Code its own
// home, <share>/daimon/runtime/kimi-code/home, so a Work session is a Kimi
// Code session there: sessions/<slug>/<id>/agents/<agent>/wire.jsonl with
// the same usage.record lines calls_kimi.go reads. Its calls are counted as
// Kimi Code's ("kimi"), as MiniMax Code Desktop's are as MiniMax Code's.
//
// Two things differ. A record's model is Daimon's alias ("daimon-kimi-code"
// by default), the key of a [models."<alias>"] table in the config.toml
// Daimon writes for the runtime beside home; its model (k2p6-agent on Kimi's
// coding endpoint) is the call's model. And magpie's kimi-desktop
// agent plugin points the alias's provider at a bridge on this computer
// that forwards to the gateway, which logs those calls itself: a call made
// while that config.toml's base_url is a loopback address is the
// gateway's. config.toml is only the current setting, so history read for
// the first time is judged by it.

// kimiWorkShares are the share folders Kimi's Work may keep sessions in,
// each once.
func kimiWorkShares() []string {
	home, _ := os.UserHomeDir()
	return kimiWorkSharesIn(home, runtime.GOOS)
}

func kimiWorkSharesIn(home, goos string) []string {
	var out []string
	add := func(d string) {
		if d == "" {
			return
		}
		d = filepath.Clean(d)
		for _, x := range out {
			if x == d {
				return
			}
		}
		out = append(out, d)
	}
	if ud := kimiDesktopUserData(home, goos); ud != "" {
		var s struct {
			ShareDir string `json:"shareDir"`
		}
		if b, err := os.ReadFile(filepath.Join(ud, "daimon-storage.json")); err == nil && json.Unmarshal(b, &s) == nil && filepath.IsAbs(s.ShareDir) {
			add(s.ShareDir)
		}
		add(filepath.Join(ud, "daimon-share"))
	}
	add(filepath.Join(home, ".kimi"))
	return out
}

// kimiDesktopUserData is the Kimi desktop app's Electron userData.
func kimiDesktopUserData(home, goos string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "kimi-desktop")
	case "windows":
		base := appdir.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "kimi-desktop")
	default:
		base := appdir.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "kimi-desktop")
	}
}

// kimiWorkRuntime is the Kimi Code runtime folder of a share.
func kimiWorkRuntime(share string) string {
	return filepath.Join(share, "daimon", "runtime", "kimi-code")
}

// kimiWorkCallFiles are the wire files of every Work session.
func kimiWorkCallFiles() []file {
	var out []file
	for _, s := range kimiWorkShares() {
		out = append(out, kimiFilesIn(filepath.Join(kimiWorkRuntime(s), "home", "sessions"))...)
	}
	return out
}

// kimiWorkRoot is the runtime folder of a Work session's wire file
// (<root>/home/sessions/<slug>/<id>/agents/<agent>/wire.jsonl), or "" for
// a Kimi Code session of its own.
func kimiWorkRoot(path string) string {
	d := path
	for range 5 {
		d = filepath.Dir(d)
	}
	if filepath.Base(d) != "sessions" || filepath.Base(filepath.Dir(d)) != "home" {
		return ""
	}
	root := filepath.Dir(filepath.Dir(d))
	if filepath.Base(root) != "kimi-code" || filepath.Base(filepath.Dir(root)) != "runtime" ||
		filepath.Base(filepath.Dir(filepath.Dir(root))) != "daimon" {
		return ""
	}
	return root
}

// kimiWorkModel is one alias of Daimon's config.toml.
type kimiWorkModel struct {
	model   string
	gateway bool // its provider's base_url is on this computer
}

// kimiWorkModels are the aliases of a runtime's config.toml, read again
// only when it changes. What it gives is shared and must not be changed.
func kimiWorkModels(root string) map[string]kimiWorkModel {
	m, _ := filememo.Read("sessions kimi work config", filepath.Join(root, "config.toml"), func(b []byte) (map[string]kimiWorkModel, error) {
		var c struct {
			Providers map[string]struct {
				BaseURL string `toml:"base_url"`
			} `toml:"providers"`
			Models map[string]struct {
				Provider string `toml:"provider"`
				Model    string `toml:"model"`
			} `toml:"models"`
		}
		if err := toml.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		out := map[string]kimiWorkModel{}
		for alias, t := range c.Models {
			out[alias] = kimiWorkModel{model: t.Model, gateway: loopbackURL(c.Providers[t.Provider].BaseURL)}
		}
		return out, nil
	})
	return m
}

// loopbackURL tells whether a base URL is on this computer.
func loopbackURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
