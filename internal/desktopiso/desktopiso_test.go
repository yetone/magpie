package desktopiso

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeClaude is a Claude.app whose executable, Claude, writes the
// arguments it was started with to out, and whose icon is one blue PNG.
func fakeClaude(t *testing.T, root, out string) string {
	t.Helper()
	app := filepath.Join(root, "Claude.app")
	for _, d := range []string{"MacOS", "Resources"} {
		if err := os.MkdirAll(filepath.Join(app, "Contents", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	info := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
	<key>CFBundleExecutable</key>
	<string>Claude</string>
	<key>CFBundleIconFile</key>
	<string>electron.icns</string>
	<key>CFBundleIdentifier</key>
	<string>com.anthropic.claudefordesktop</string>
	<key>CFBundleShortVersionString</key>
	<string>4.4212.0</string>
</dict></plist>
`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}
	// and the CLAUDE_CONFIG_DIR it was given to out.env
	exe := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + out + "'\nprintf '%s' \"${CLAUDE_CONFIG_DIR-unset}\" > '" + out + ".env'\n"
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "Claude"), []byte(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			img.SetNRGBA(x, y, color.NRGBA{20, 40, 200, 255})
		}
	}
	var pb bytes.Buffer
	png.Encode(&pb, img)
	var icns bytes.Buffer
	icns.WriteString("icns")
	binary.Write(&icns, binary.BigEndian, uint32(16+pb.Len()))
	icns.WriteString("ic08")
	binary.Write(&icns, binary.BigEndian, uint32(8+pb.Len()))
	icns.Write(pb.Bytes())
	if err := os.WriteFile(filepath.Join(app, "Contents", "Resources", "electron.icns"), icns.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return app
}

func noRegister(t *testing.T) *[]string {
	var got []string
	old := register
	register = func(p string) { got = append(got, p) }
	t.Cleanup(func() { register = old })
	return &got
}

// The app runs Claude's own executable on the folder, with the
// arguments it was opened with, and has Claude's icon with the badge.
func TestAddStartsDesktopOnTheFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the app's executable is a shell script")
	}
	registered := noRegister(t)
	home := t.TempDir()
	out := filepath.Join(home, "args")
	claudeApp := fakeClaude(t, filepath.Join(home, "Applications"), out)
	dir := filepath.Join(home, "Library", "Application Support", "Claude-Magpie-3p")

	p, err := Add(home, claudeApp, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if p != Path(home) || Here(home) != p || !Mine(p) {
		t.Fatalf("app %q, here %q", p, Here(home))
	}
	if len(*registered) != 1 || (*registered)[0] != p {
		t.Errorf("registered %v", *registered)
	}
	info, _ := os.ReadFile(filepath.Join(p, "Contents", "Info.plist"))
	for _, want := range []string{"<string>" + BundleID + "</string>", "<string>claude-magpie</string>", "<string>Claude Magpie</string>", "<string>4.4212.0</string>"} {
		if !bytes.Contains(info, []byte(want)) {
			t.Errorf("Info.plist lacks %s:\n%s", want, info)
		}
	}
	icns, err := os.ReadFile(filepath.Join(p, "Contents", "Resources", "claude-magpie.icns"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := largestPNG(icns)
	if err != nil || img.Bounds().Dx() != 1024 {
		t.Fatalf("icon: %v", err)
	}
	// Claude's blue at the top left, the badge's white at the bottom right
	if r, g, b, _ := img.At(100, 100).RGBA(); r>>8 > 40 || b>>8 < 180 || g>>8 > 60 {
		t.Errorf("top left %v", img.At(100, 100))
	}
	if r, g, b, _ := img.At(736, 560).RGBA(); r>>8 < 200 || g>>8 < 200 || b>>8 < 200 {
		t.Errorf("badge edge %v, want white", img.At(736, 560))
	}

	cmd := exec.Command(filepath.Join(p, "Contents", "MacOS", "claude-magpie"), "claude://open")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	args, _ := os.ReadFile(out)
	if want := "--user-data-dir=" + dir + "\nclaude://open\n"; string(args) != want {
		t.Fatalf("Claude started with %q, want %q", args, want)
	}

	// made again: still one app, magpie's
	if _, err := Add(home, claudeApp, dir, ""); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 2 {
		t.Errorf("Applications holds %v", ents)
	}
	if got, err := Remove(home); err != nil || got != p {
		t.Fatalf("remove: %q %v", got, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("app still there")
	}
	if got, err := Remove(home); err != nil || got != "" {
		t.Fatalf("remove again: %q %v", got, err)
	}
}

// Claude.app moved since the app was made: it is looked for in the
// Applications folders.
func TestScriptFindsClaudeMoved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	out := filepath.Join(home, "args")
	fakeClaude(t, filepath.Join(home, "Applications"), out)
	dir := filepath.Join(home, "iso-3p")
	s := filepath.Join(home, "run.sh")
	os.WriteFile(s, []byte(Script(filepath.Join(home, "Gone", "Claude.app"), "Claude", dir, "")), 0o755)
	if b, err := exec.Command(s).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if args, _ := os.ReadFile(out); string(args) != "--user-data-dir="+dir+"\n" {
		t.Fatalf("args %q", args)
	}
	os.RemoveAll(filepath.Join(home, "Applications"))
	b, err := exec.Command(s).CombinedOutput()
	if err == nil || !strings.Contains(string(b), "not installed") {
		t.Fatalf("no Claude: %v %s", err, b)
	}
}

// A folder name with a quote in it reaches Claude whole.
func TestScriptQuotes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	home := t.TempDir()
	out := filepath.Join(home, "args")
	app := fakeClaude(t, home, out)
	dir := filepath.Join(home, "it's $HOME `x`")
	s := filepath.Join(home, "run.sh")
	os.WriteFile(s, []byte(Script(app, "Claude", dir, "")), 0o755)
	if b, err := exec.Command(s).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if args, _ := os.ReadFile(out); string(args) != "--user-data-dir="+dir+"\n" {
		t.Fatalf("args %q", args)
	}
}

// With a Claude Code folder of its own (#6459), Desktop is started with
// CLAUDE_CONFIG_DIR set to it, whatever the user's own environment says,
// a name with quotes in it whole; without one, the environment's own (or
// none) reaches it as it is.
func TestScriptClaudeConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	home := t.TempDir()
	out := filepath.Join(home, "args")
	app := fakeClaude(t, home, out)
	dir := filepath.Join(home, "iso-3p")
	cc := filepath.Join(home, ".claude-it's $HOME `x`")
	for _, c := range []struct{ claudeDir, env, want string }{
		{cc, "", cc},
		{cc, "/elsewhere", cc},
		{"", "", "unset"},
		{"", "/theirs", "/theirs"},
	} {
		s := filepath.Join(home, "run.sh")
		os.WriteFile(s, []byte(Script(app, "Claude", dir, c.claudeDir)), 0o755)
		cmd := exec.Command(s)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
		if c.env != "" {
			cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+c.env)
		}
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		if got, _ := os.ReadFile(out + ".env"); string(got) != c.want {
			t.Errorf("claudeDir %q, env %q: CLAUDE_CONFIG_DIR %q, want %q", c.claudeDir, c.env, got, c.want)
		}
		if args, _ := os.ReadFile(out); string(args) != "--user-data-dir="+dir+"\n" {
			t.Fatalf("args %q", args)
		}
	}
}

// A Claude Magpie.app of the user's own is never written over or removed.
func TestAddLeavesTheirs(t *testing.T) {
	noRegister(t)
	home := t.TempDir()
	theirs := Path(home)
	os.MkdirAll(filepath.Join(theirs, "Contents"), 0o755)
	os.WriteFile(filepath.Join(theirs, "Contents", "Info.plist"), []byte("<plist><dict><key>CFBundleIdentifier</key><string>me.mine</string></dict></plist>"), 0o644)
	if _, err := Add(home, filepath.Join(home, "Claude.app"), "/x", ""); !errors.Is(err, ErrTheirs) {
		t.Fatalf("add over theirs: %v", err)
	}
	if got, err := Remove(home); got != "" || err != nil {
		t.Fatalf("remove theirs: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(theirs, "Contents", "Info.plist")); err != nil {
		t.Fatal("theirs gone")
	}
}
