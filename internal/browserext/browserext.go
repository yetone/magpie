// Package browserext is Magpie Browser, the browser extension through which
// the agents magpie serves use the user's own browser: their tabs, signed
// in to the sites they use (gateway/browser.go relays the calls). magpie
// writes it to a folder of its own, pointed at its gateway, for the user to
// load in chrome://extensions.
package browserext

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// ID is the extension's ID in every Chromium browser, fixed by the public
// key in its manifest; the gateway takes a connection from this origin
// only.
const ID = "dmlhojidlfnkgamkekjhacgkpanbkeko"

// Origin is what the extension's requests carry as Origin.
const Origin = "chrome-extension://" + ID

//go:embed ext
var files embed.FS

// Version is the extension's version, from its manifest.
func Version() string {
	b, _ := files.ReadFile("ext/manifest.json")
	_, v, ok := bytes.Cut(b, []byte(`"version": "`))
	if !ok {
		return ""
	}
	v, _, _ = bytes.Cut(v, []byte(`"`))
	return string(v)
}

// File is one of the extension's files as magpie has it.
func File(name string) ([]byte, error) { return files.ReadFile("ext/" + name) }

// Write puts the extension in dir, pointed at the gateway at gatewayURL
// (http://127.0.0.1:3425), replacing what an older magpie wrote there.
// A file is written only when it changed, so a browser that has the folder
// loaded sees no change it has to reload for.
func Write(dir, gatewayURL string) error {
	cfg, err := config(gatewayURL, "")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return each(cfg, func(name string, b []byte) error {
		to := filepath.Join(dir, filepath.FromSlash(name))
		if old, err := os.ReadFile(to); err == nil && bytes.Equal(old, b) {
			return nil
		}
		tmp := to + ".tmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, to)
	})
}

// Zip is the extension as a zip of its folder, pointed at the gateway at
// gatewayURL and taken there with key: for a browser on another computer
// than magpie's, or outside the container magpie runs in, where magpie
// can't write the folder.
func Zip(gatewayURL, key string) ([]byte, error) {
	cfg, err := config(gatewayURL, key)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err = each(cfg, func(name string, b []byte) error {
		w, err := zw.Create(Folder + "/" + name)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	if err == nil {
		err = zw.Close()
	}
	return buf.Bytes(), err
}

// Folder is the name of the extension's folder.
const Folder = "Magpie Browser Extension"

// config is the extension's config.js for the gateway at gatewayURL
// (http://127.0.0.1:3425) and key ("" for none, a gateway on this
// computer's loopback).
func config(gatewayURL, key string) ([]byte, error) {
	ws := "ws" + strings.TrimPrefix(strings.TrimRight(gatewayURL, "/"), "http")
	if !strings.HasPrefix(ws, "ws://") && !strings.HasPrefix(ws, "wss://") {
		return nil, fmt.Errorf("the gateway's address %q isn't http", gatewayURL)
	}
	return fmt.Appendf(nil, "// The gateway this extension talks to, written by magpie.\nexport const GATEWAY = %q;\nexport const KEY = %q;\n", ws, key), nil
}

// each calls put with each of the extension's files, config.js as cfg.
func each(cfg []byte, put func(name string, b []byte) error) error {
	return fs.WalkDir(files, "ext", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(p, "ext/")
		if name == "config.js" {
			b = cfg
		}
		return put(name, b)
	})
}

// Dir is where the extension is written: in the home folder, where Finder
// and Explorer show it, to drag from or choose.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return OldDir()
	}
	return filepath.Join(home, Folder)
}

// OldDir is where v0.1.2442 wrote it, a hidden folder no one could find;
// a browser that loaded it from there keeps it.
func OldDir() string { return filepath.Join(settings.Dir(), "browser-extension") }

// Refresh writes this magpie's extension over every folder an older one
// was written to, so a browser that reloads it gets this version. A folder
// never written stays unwritten. It says whether any folder has it.
func Refresh(gatewayURL string) bool {
	written := false
	for _, dir := range []string{Dir(), OldDir()} {
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil {
			written = Write(dir, gatewayURL) == nil || written
		}
	}
	return written
}

// Older says whether version a (0.1.3) is before b: a newer extension's
// folder is never written over with an older magpie's.
func Older(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}
