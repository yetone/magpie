// Package desktopiso makes the app that starts Claude Desktop in a folder
// of its own (#5671): ~/Applications/Claude Magpie.app, whose executable is
// a script that runs the user's Claude.app with --user-data-dir set to
// desktopdir.Isolated. Desktop's 3p folder is its userData with "-3p"
// added unless it ends so already (GY in Desktop 2.7032's and 4.4212's
// index.pre.js), so that folder holds magpie's whole 3p mode, and the
// user's own Desktop, signed in with Anthropic in
// ~/Library/Application Support/Claude, runs beside it untouched. The
// packaged Desktop drops CLAUDE_USER_DATA_DIR from its environment, so
// the flag is the one way in; it is no switch Desktop refuses to start
// with (its list: remote-debugging-port, host-rules and the like).
//
// Tried on a Mac mini with Desktop 4.4212: started from such a bundle it
// read the folder's configLibrary and asked the gateway for /v1/models,
// while a second Desktop ran in another folder; opening the bundle again
// while it ran started no second copy. macOS lists the running Desktop
// as Claude.app's own (lsappinfo: its bundle path), so the Dock shows it
// with Claude's icon; the badge is on the app that starts it.
package desktopiso

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	xdraw "golang.org/x/image/draw"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
)

// Name is the app's name, and its bundle's without ".app".
const Name = "Claude Magpie"

// BundleID is its CFBundleIdentifier, which says a bundle by that name is
// magpie's: one without it is the user's and is never written over.
const BundleID = "ai.usemagpie.claude-desktop"

// exeName is the script's name in Contents/MacOS.
const exeName = "claude-magpie"

// mark is in every script magpie writes.
const mark = "made by magpie (#5671)"

//go:embed magpie.png
var badgePNG []byte

// Path is where the app goes for home: ~/Applications/Claude Magpie.app.
func Path(home string) string {
	return filepath.Join(home, "Applications", Name+".app")
}

// Mine says the bundle at p is one magpie made.
func Mine(p string) bool {
	info, err := os.ReadFile(filepath.Join(p, "Contents", "Info.plist"))
	return err == nil && bytes.Contains(info, []byte("<string>"+BundleID+"</string>"))
}

// Here is the app for home when magpie made it, "" when there is none.
func Here(home string) string {
	if p := Path(home); Mine(p) {
		return p
	}
	return ""
}

// Script is the app's executable: Desktop's own executable exe in
// claudeApp (else in /Applications or ~/Applications, where it may have
// moved since) run on dir. A Desktop already running on dir is not started
// a second time on it: two copies on one userData share no lock.
//
// With claudeDir, the Claude Code Desktop's Code tab runs keeps its files
// there (#6459): Desktop takes CLAUDE_CONFIG_DIR from its environment when
// its own Settings set none (Desktop 4.23118 and 2.7032), and starts its
// sessions with it. "" leaves it ~/.claude, or whatever the user's own
// environment says.
func Script(claudeApp, exe, dir, claudeDir string) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	env := ""
	if claudeDir != "" {
		env = "export CLAUDE_CONFIG_DIR=" + q(claudeDir) + "\n"
	}
	return "#!/bin/sh\n" +
		"# Claude Desktop on magpie, in a folder of its own; " + mark + ".\n" +
		"# magpie's Claude Desktop settings take it away again.\n" +
		"dir=" + q(dir) + "\n" +
		env +
		"exe=" + q(exe) + "\n" +
		"if /usr/bin/pgrep -f -- \"--user-data-dir=$dir( |\\$)\" >/dev/null 2>&1; then exit 0; fi\n" +
		"for app in " + q(claudeApp) + " /Applications/Claude.app \"$HOME/Applications/Claude.app\"; do\n" +
		"\tif [ -x \"$app/Contents/MacOS/$exe\" ]; then\n" +
		"\t\texec \"$app/Contents/MacOS/$exe\" --user-data-dir=\"$dir\" \"$@\"\n" +
		"\tfi\n" +
		"done\n" +
		"echo 'Claude Desktop (Claude.app) is not installed' >&2\n" +
		"exit 1\n"
}

func plist(claudeApp string) string {
	version := "1.0"
	if v := plistString(filepath.Join(claudeApp, "Contents", "Info.plist"), "CFBundleShortVersionString"); v != "" {
		version = v
	}
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>` + exeName + `</string>
	<key>CFBundleIdentifier</key>
	<string>` + BundleID + `</string>
	<key>CFBundleName</key>
	<string>` + Name + `</string>
	<key>CFBundleDisplayName</key>
	<string>` + Name + `</string>
	<key>CFBundleIconFile</key>
	<string>` + exeName + `</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>` + esc(version) + `</string>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
	<key>NSHighResolutionCapable</key>
	<true/>
</dict>
</plist>
`
}

var plistKeyRe = lazyre.New(`(?s)<key>([^<]*)</key>\s*<string>([^<]*)</string>`)

// plistString is key's string in the XML Info.plist at path, "" when it
// has none or can't be read (Claude.app's is XML).
func plistString(path, key string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, m := range plistKeyRe.FindAllSubmatch(b, -1) {
		if string(m[1]) == key {
			return strings.TrimSpace(string(m[2]))
		}
	}
	return ""
}

// register has macOS read the app again (its icon among it); a var so
// tests run none.
var register = func(p string) {
	_ = proc.Command("/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister", "-f", p).Run()
}

// ErrTheirs: a bundle by the app's name is there and isn't magpie's.
var ErrTheirs = errors.New(Name + ".app is there already and isn't magpie's")

// Add makes the app for home, starting the Desktop at claudeApp on dir,
// its Code tab's Claude Code on claudeDir when one is given (Script), and
// gives its path. One of magpie's there already is made again; one that
// isn't magpie's stays, and Add fails (ErrTheirs).
func Add(home, claudeApp, dir, claudeDir string) (string, error) {
	p := Path(home)
	if _, err := os.Lstat(p); err == nil && !Mine(p) {
		return "", ErrTheirs
	}
	exe := plistString(filepath.Join(claudeApp, "Contents", "Info.plist"), "CFBundleExecutable")
	if exe == "" {
		exe = "Claude"
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(p), "."+Name+".app-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	for _, d := range []string{"MacOS", "Resources"} {
		if err := os.MkdirAll(filepath.Join(tmp, "Contents", d), 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "Contents", "Info.plist"), []byte(plist(claudeApp)), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, "Contents", "MacOS", exeName), []byte(Script(claudeApp, exe, dir, claudeDir)), 0o755); err != nil {
		return "", err
	}
	// the icon: Claude's with magpie's badge, or none when Claude's can't
	// be read (the app still starts Desktop)
	if icns, err := Icon(claudeApp); err == nil {
		if err := os.WriteFile(filepath.Join(tmp, "Contents", "Resources", exeName+".icns"), icns, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.RemoveAll(p); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", err
	}
	register(p)
	return p, nil
}

// Remove takes the app away when magpie made it, and gives the path it
// was at, "" when there was none of magpie's.
func Remove(home string) (string, error) {
	p := Here(home)
	if p == "" {
		return "", nil
	}
	if err := os.RemoveAll(p); err != nil {
		return "", err
	}
	return p, nil
}

// Icon is the app's icon, as an .icns: Claude.app's own (its
// CFBundleIconFile, electron.icns in 2.7032 and 4.4212), its largest
// picture with magpie's badge at the bottom right, at 1024, 512, 256 and
// 128 pixels.
func Icon(claudeApp string) ([]byte, error) {
	name := plistString(filepath.Join(claudeApp, "Contents", "Info.plist"), "CFBundleIconFile")
	if name == "" {
		name = "electron.icns"
	}
	if filepath.Ext(name) == "" {
		name += ".icns"
	}
	b, err := os.ReadFile(filepath.Join(claudeApp, "Contents", "Resources", filepath.Base(name)))
	if err != nil {
		return nil, err
	}
	base, err := largestPNG(b)
	if err != nil {
		return nil, err
	}
	big := Badged(base, 1024)
	var out bytes.Buffer
	out.WriteString("icns")
	binary.Write(&out, binary.BigEndian, uint32(0))
	for _, e := range []struct {
		kind string
		size int
	}{{"ic10", 1024}, {"ic09", 512}, {"ic08", 256}, {"ic07", 128}} {
		img := image.Image(big)
		if e.size != 1024 {
			img = scaled(big, e.size)
		}
		var pb bytes.Buffer
		if err := png.Encode(&pb, img); err != nil {
			return nil, err
		}
		out.WriteString(e.kind)
		binary.Write(&out, binary.BigEndian, uint32(8+pb.Len()))
		out.Write(pb.Bytes())
	}
	res := out.Bytes()
	binary.BigEndian.PutUint32(res[4:8], uint32(len(res)))
	return res, nil
}

// largestPNG is the largest PNG picture an .icns holds.
func largestPNG(b []byte) (image.Image, error) {
	if len(b) < 8 || string(b[:4]) != "icns" {
		return nil, errors.New("not an icns file")
	}
	var best image.Image
	area := 0
	for i := 8; i+8 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[i+4 : i+8]))
		if n < 8 || i+n > len(b) {
			break
		}
		data := b[i+8 : i+n]
		if bytes.HasPrefix(data, []byte("\x89PNG")) {
			if img, err := png.Decode(bytes.NewReader(data)); err == nil {
				if s := img.Bounds().Size(); s.X*s.Y > area {
					best, area = img, s.X*s.Y
				}
			}
		}
		i += n
	}
	if best == nil {
		return nil, errors.New("no PNG picture in the icns")
	}
	return best, nil
}

func scaled(src image.Image, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return dst
}

// Badged is base at size pixels with magpie's badge over its bottom
// right: a white disc with a hairline rim and magpie's bird in it, a
// little over a third of the icon across. macOS's icon grid keeps the
// picture inside ~80% of the canvas, so the disc sits on the artwork's
// corner, not the canvas's.
func Badged(base image.Image, size int) *image.NRGBA {
	out := scaled(base, size)
	d := size * 38 / 100
	right := size * 91 / 100
	r := image.Rect(right-d, right-d, right, right)
	disc := image.NewNRGBA(image.Rect(0, 0, d, d))
	// drawn at four times its size and scaled down, for smooth edges
	const ss = 4
	big := image.NewNRGBA(image.Rect(0, 0, d*ss, d*ss))
	c := float64(d*ss) / 2
	rim := float64(d*ss) * 0.03
	for y := 0; y < d*ss; y++ {
		for x := 0; x < d*ss; x++ {
			dx, dy := float64(x)+0.5-c, float64(y)+0.5-c
			dist2 := dx*dx + dy*dy
			switch {
			case dist2 <= (c-rim)*(c-rim):
				big.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
			case dist2 <= c*c:
				big.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 40})
			}
		}
	}
	xdraw.CatmullRom.Scale(disc, disc.Bounds(), big, big.Bounds(), xdraw.Src, nil)
	xdraw.Draw(out, r, disc, image.Point{}, xdraw.Over)
	if bird, err := png.Decode(bytes.NewReader(badgePNG)); err == nil {
		in := d * 74 / 100
		off := (d - in) / 2
		xdraw.CatmullRom.Scale(out, image.Rect(r.Min.X+off, r.Min.Y+off, r.Min.X+off+in, r.Min.Y+off+in), bird, bird.Bounds(), xdraw.Over, nil)
	}
	return out
}
