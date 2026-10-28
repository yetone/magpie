//go:build !darwin && !windows && !android

package autostart

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// sandbox gives a test a home of its own and system folders of its own,
// $XDG_CONFIG_DIRS among them: the machine's /etc/xdg/autostart is the
// real system's. It gives the system folder.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	sys := filepath.Join(home, "etc", "xdg")
	t.Setenv("XDG_CONFIG_DIRS", sys)
	t.Setenv("APPIMAGE", "/opt/my apps/magpie.AppImage")
	return sys
}

// shipped is an entry a package puts in a system folder, naming magpie by
// the name its package keeps on PATH.
const shipped = "[Desktop Entry]\nType=Application\nName=magpie\nComment=one place to pick every agent's model\nExec=magpie tray\nTerminal=false\n"

func writeEntry(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A record a write cut short is not one that opens magpie at login: the
// desktop starts no entry with no Exec, and none at all with nothing in it,
// so Open at login reads off rather than saying on for a record that never
// starts anything.
func TestSetThenTruncatedRecordReadsAsOff(t *testing.T) {
	sandbox(t)
	if err := Set(true); err != nil {
		t.Fatal(err)
	}
	if Enabled() == Off {
		t.Fatal("not on after Set(true)")
	}
	p := record()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// the head of the entry, cut off before the Exec that names the
	// program, which is what a write that goes no further leaves behind
	head := b[:strings.Index(string(b), "Exec=")]
	if err := os.WriteFile(p, head, 0o644); err != nil {
		t.Fatal(err)
	}
	if Enabled() != Off {
		t.Errorf("a desktop entry with no Exec reads as on:\n%s", head)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if Enabled() != Off {
		t.Error("an empty desktop entry reads as on")
	}
	// an Exec key with nothing after it names nothing to run either, and the
	// desktop starts no entry that names nothing
	if err := os.WriteFile(p, []byte("[Desktop Entry]\nType=Application\nName=magpie\nExec=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Enabled() != Off {
		t.Error("a desktop entry with an empty Exec reads as on")
	}
}

// mtul0729, packaging magpie for nixpkgs (#3372): the package's entry in
// $XDG_CONFIG_DIRS opens magpie at login, and Open at login read off over
// it and couldn't switch it off, as it only ever looked at, and took away,
// the user's own file. Now it reads the entry the desktop starts, and
// switches it as the XDG Autostart spec has a user do: off is an entry of
// the user's over the package's, Hidden; on again is that entry gone, so
// no path of this copy's is written over the one the package keeps.
func TestSystemEntryIsTheOneTheSwitchReaches(t *testing.T) {
	sys := sandbox(t)
	pkg := filepath.Join(sys, "autostart", "magpie.desktop")
	writeEntry(t, pkg, shipped)
	if got := Enabled(); got != On {
		t.Fatalf("the package's entry opens magpie at login, and Open at login reads %s", got)
	}
	for range 2 { // again, and it is no error and changes nothing
		if err := Set(false); err != nil {
			t.Fatal(err)
		}
		if got := Enabled(); got != Off {
			t.Fatalf("switched off, with the package's entry there: %s", got)
		}
		b, err := os.ReadFile(record())
		if err != nil || !strings.Contains(string(b), "\nHidden=true\n") {
			t.Fatalf("no Hidden entry of the user's stands over the package's (%v):\n%s", err, b)
		}
	}
	// XFCE's Session and Startup lists that entry as magpie, unticked, and
	// ticking it there writes Hidden=false into it: it runs true then, not
	// magpie, and on (below) takes it away for the package's
	writeEntry(t, record(), "[Desktop Entry]\nType=Application\nName=magpie\nExec=true\nHidden=false\n")
	if got := Enabled(); got != Off {
		t.Fatalf("the entry off wrote, ticked again in XFCE, over the package's: %s", got)
	}
	for range 2 {
		if err := Set(true); err != nil {
			t.Fatal(err)
		}
		if got := Enabled(); got != On {
			t.Fatalf("switched on again, with the package's entry there: %s", got)
		}
		if _, err := os.Stat(record()); !os.IsNotExist(err) {
			b, _ := os.ReadFile(record())
			t.Fatalf("switched on again, an entry of the user's stands over the package's (%v):\n%s", err, b)
		}
	}
	if b, err := os.ReadFile(pkg); err != nil || string(b) != shipped {
		t.Fatalf("the package's entry was written to (%v):\n%s", err, b)
	}

	// an entry magpie wrote before the package came is the user's, and
	// counts over the package's; switched on again, it gives way to it
	if err := os.Remove(pkg); err != nil {
		t.Fatal(err)
	}
	if err := Set(true); err != nil {
		t.Fatal(err)
	}
	mine, err := os.ReadFile(record())
	if err != nil || !strings.Contains(string(mine), "/opt/my apps/magpie.AppImage") {
		t.Fatalf("no entry of the user's without the package's (%v):\n%s", err, mine)
	}
	writeEntry(t, pkg, shipped)
	if got := Enabled(); got != On {
		t.Fatalf("the user's entry over the package's: %s", got)
	}
	if err := Set(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(record()); !os.IsNotExist(err) || Enabled() != On {
		t.Fatalf("on, with the package's entry there: the user's stays (%v), %s", err, Enabled())
	}

	// with no package's entry, off takes the user's entry away, as before
	if err := os.Remove(pkg); err != nil {
		t.Fatal(err)
	}
	if err := Set(true); err != nil {
		t.Fatal(err)
	}
	if err := Set(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(record()); !os.IsNotExist(err) {
		b, _ := os.ReadFile(record())
		t.Fatalf("off, with no package's entry, left an entry of the user's (%v):\n%s", err, b)
	}
}

// Which entry counts is the desktop's rule: the user's folder over the
// system's, and of the system's folders the first with one. An entry
// hidden, or switched off in GNOME's Startup Applications, reads off, and
// holds back the entries of its name after it. Each is read as GLib and
// systemd read it: keys of the [Desktop Entry] group alone, spaces around
// = and CRLF lines as any other, true or 1 for Hidden.
func TestTheEntryThatCountsIsTheDesktops(t *testing.T) {
	first := sandbox(t)
	second := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_DIRS", first+string(os.PathListSeparator)+second)
	hidden := shipped + "Hidden=true\n"
	gnome := shipped + "X-GNOME-Autostart-enabled=false\n"
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	for _, c := range []struct {
		name                string
		user, first, second string
		want                State
	}{
		{"the second system folder's alone", "", "", shipped, On},
		{"the first system folder's hidden, over the second's", "", hidden, shipped, Off},
		{"the first's, over the second's hidden", "", shipped, hidden, On},
		{"the user's hidden, over the system's", hidden, shipped, "", Off},
		{"the user's switched off in GNOME, over the system's", gnome, shipped, "", Off},
		{"the user's, over a system one hidden", shipped, hidden, "", On},
		{"the user's, Hidden=false, over a system one hidden", shipped + "Hidden=false\n", hidden, "", On},
		{"the system's alone, switched off in GNOME", "", gnome, "", Off},
		{"the system's alone, switched off in GNOME by 0", "", shipped + "X-GNOME-Autostart-enabled=0\n", "", Off},
		{"the system's alone, Hidden = true with spaces", "", shipped + "Hidden = true\n", "", Off},
		{"the system's alone, Hidden=1", "", shipped + "Hidden=1\n", "", Off},
		{"the system's alone, in CRLF lines", "", crlf(shipped), "", On},
		{"the system's alone, in CRLF lines, hidden", "", crlf(hidden), "", Off},
		{"the system's alone, keys with spaces around =", "", "[Desktop Entry]\nType = Application\nName = magpie\nExec = magpie tray\n", "", On},
		{"the system's alone, its Exec in a [Desktop Action] only", "", "[Desktop Entry]\nType=Application\nName=magpie\nActions=quit;\n\n[Desktop Action quit]\nName=Quit\nExec=magpie quit\n", "", Off},
		{"the system's alone, a link, not an application, though with an Exec", "", linkEntry, "", Off},
	} {
		for _, f := range []struct{ path, body string }{
			{record(), c.user},
			{filepath.Join(first, "autostart", "magpie.desktop"), c.first},
			{filepath.Join(second, "autostart", "magpie.desktop"), c.second},
		} {
			if f.body == "" {
				if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				continue
			}
			writeEntry(t, f.path, f.body)
		}
		if got := Enabled(); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// The system's folders are $XDG_CONFIG_DIRS, or /etc/xdg when it is unset.
// A folder there that no desktop of this user opens is none, so its hidden
// entry holds nothing back and off and on reach the system's entry after
// it: a relative folder, as the spec says, a file, a folder this user
// can't search, a name too long. And the user's own folder listed there
// too, by any name, is still the user's: on, twice, stays on.
func TestSystemFoldersAreXDGConfigDirs(t *testing.T) {
	sys := sandbox(t)
	t.Setenv("XDG_CONFIG_DIRS", "")
	if got, want := systemRecords(), []string{"/etc/xdg/autostart/magpie.desktop"}; !slices.Equal(got, want) {
		t.Errorf("XDG_CONFIG_DIRS unset: %q, want %q", got, want)
	}
	writeEntry(t, filepath.Join(sys, "autostart", "magpie.desktop"), shipped)
	t.Chdir(t.TempDir())
	rel := filepath.Join("etc", "xdg")
	writeEntry(t, filepath.Join(rel, "autostart", "magpie.desktop"), shipped+"Hidden=true\n")
	file := filepath.Join(t.TempDir(), "xdg")
	writeEntry(t, file, "")
	shut := filepath.Join(t.TempDir(), "xdg")
	writeEntry(t, filepath.Join(shut, "autostart", "magpie.desktop"), shipped+"Hidden=true\n")
	if err := os.Chmod(filepath.Join(shut, "autostart"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(shut, "autostart"), 0o755) })
	for _, c := range []struct{ name, dir string }{
		{"the relative " + rel + ", a hidden entry under it,", rel},
		{"the file " + file, file},
		{"a folder this user can't search, a hidden entry under it,", shut},
		{"a folder whose name is too long", filepath.Join(t.TempDir(), strings.Repeat("x", 300))},
	} {
		if c.dir == shut && os.Geteuid() == 0 {
			continue // root searches any folder
		}
		t.Setenv("XDG_CONFIG_DIRS", c.dir+string(os.PathListSeparator)+sys)
		if got := Enabled(); got != On {
			t.Errorf("%s in XDG_CONFIG_DIRS, the system's entry after it: %s, want on", c.name, got)
		}
		if err := Set(false); err != nil || Enabled() != Off {
			t.Errorf("off, %s in XDG_CONFIG_DIRS before the system's entry: %v, %s", c.name, err, Enabled())
		}
		if err := Set(true); err != nil || Enabled() != On {
			t.Errorf("on, %s in XDG_CONFIG_DIRS before the system's entry: %v, %s", c.name, err, Enabled())
		}
	}
	if err := os.Remove(filepath.Join(sys, "autostart", "magpie.desktop")); err != nil {
		t.Fatal(err)
	}
	user := filepath.Dir(filepath.Dir(record()))
	alias := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(user, alias); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{user, alias} {
		t.Setenv("XDG_CONFIG_DIRS", d)
		for range 2 {
			if err := Set(true); err != nil {
				t.Fatal(err)
			}
			if got := Enabled(); got != On {
				t.Fatalf("the user's folder in XDG_CONFIG_DIRS as %s, on: %s", d, got)
			}
		}
	}
}

// The entry off writes over a package's holds it back only in a session
// that reads it: GLib's (GNOME's and Cinnamon's) passes over an entry with
// no Type (on GLib's main branch, no Name either), or whose Exec it can't
// find, and MATE's over one with no Exec. So it has all three, and its
// Exec names a program every system has by the name PATH finds it by, not
// a path a package may have collected since or a system may not have
// (NixOS has no /bin/true).
func TestOffIsAnEntryEverySessionReads(t *testing.T) {
	sys := sandbox(t)
	writeEntry(t, filepath.Join(sys, "autostart", "magpie.desktop"), shipped)
	if err := Set(false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(record())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "[Desktop Entry]\n") {
		t.Errorf("does not start with its group, as GLib needs:\n%s", b)
	}
	for _, key := range []string{"Type=Application", "Name=magpie", "Hidden=true"} {
		if !regexp.MustCompile(`(?m)^` + key + `$`).Match(b) {
			t.Errorf("no %s:\n%s", key, b)
		}
	}
	m := regexp.MustCompile(`(?m)^Exec=(\S+)`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("no Exec, so MATE's session reads none of it:\n%s", b)
	}
	if strings.Contains(string(m[1]), "/") {
		t.Errorf("its Exec names %s by a path, which not every system has", m[1])
	}
	if _, err := exec.LookPath(string(m[1])); err != nil {
		t.Errorf("its Exec names %s, which GNOME's session doesn't find, so it reads none of it: %v", m[1], err)
	}
}

// linkEntry is an entry of another Type than an application, a link,
// which no desktop starts at login, though it has an Exec.
const linkEntry = "[Desktop Entry]\nType=Link\nName=magpie\nURL=https://github.com/yetone/magpie\nExec=magpie tray\n"

// A package's entry that opens magpie on no desktop (Hidden, or not an
// application) reads off, and so does one GNOME has off
// (X-GNOME-Autostart-enabled=false), as GNOME's and Cinnamon's sessions
// read it. On writes the user's own entry over each. Off takes the user's
// entry away again, but over the one GNOME has off it stands there Hidden:
// systemd's generator (KDE's, COSMIC's) reads no GNOME key and starts that
// one, and off left magpie opening there.
func TestAPackagesEntryThatOpensNothingHere(t *testing.T) {
	sys := sandbox(t)
	pkg := filepath.Join(sys, "autostart", "magpie.desktop")
	for _, c := range []struct {
		name, body string
		hidden     bool // off hides it
	}{
		{"hidden", shipped + "Hidden=true\n", false},
		{"a link", linkEntry, false},
		{"off in GNOME", shipped + "X-GNOME-Autostart-enabled=false\n", true},
	} {
		if err := os.Remove(record()); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		writeEntry(t, pkg, c.body)
		if got := Enabled(); got != Off {
			t.Errorf("the package's entry %s: %s, want off", c.name, got)
		}
		if err := Set(true); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(record()); err != nil || !strings.Contains(string(b), "/opt/my apps/magpie.AppImage") || Enabled() != On {
			t.Errorf("on, over the package's entry %s: %s (%v)\n%s", c.name, Enabled(), err, b)
		}
		if err := Set(false); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(record())
		if c.hidden && (err != nil || !strings.Contains(string(b), "\nHidden=true\n")) {
			t.Errorf("off, over the package's entry %s: no Hidden entry of the user's over it (%v)\n%s", c.name, err, b)
		}
		if !c.hidden && !os.IsNotExist(err) {
			t.Errorf("off, over the package's entry %s: the user's entry stays (%v)\n%s", c.name, err, b)
		}
		if got := Enabled(); got != Off {
			t.Errorf("off, over the package's entry %s: %s", c.name, got)
		}
	}
}

// A link at the user's entry (to magpie's launcher, which install.sh
// writes, the usual way to have an app open at login, or into a read-only
// store) is replaced by the entry, and the file it leads to is left as it
// was. Written through, off over a package's entry left the launcher
// running true, and over a read-only file it failed with the switch still
// on; on with no package's entry wrote the launcher too. A hard link is
// written through the same way.
func TestALinkedEntryIsReplacedNotWrittenThrough(t *testing.T) {
	sys := sandbox(t)
	pkg := filepath.Join(sys, "autostart", "magpie.desktop")
	home := os.Getenv("HOME")
	const launcher = "[Desktop Entry]\nType=Application\nName=magpie\nExec=/home/u/.local/bin/magpie\nIcon=magpie\n"
	app := filepath.Join(home, ".local", "share", "applications", "magpie.desktop")
	store := filepath.Join(home, "store", "share", "applications", "magpie.desktop")
	writeEntry(t, app, launcher)
	writeEntry(t, store, launcher)
	if err := os.Chmod(filepath.Dir(store), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Dir(store), 0o755) })
	if err := os.MkdirAll(filepath.Dir(record()), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, to string
		hard     bool // a hard link, not a symbolic one
		pkg, on  bool
	}{
		{"off, a link to the launcher, over the package's entry", app, false, true, false},
		{"off, a link into a read-only folder, over the package's entry", store, false, true, false},
		{"on, a link to the launcher, with no package's entry", app, false, false, true},
		{"off, a hard link to the launcher, over the package's entry", app, true, true, false},
		{"on, a hard link to the launcher, with no package's entry", app, true, false, true},
	} {
		for _, p := range []string{record(), pkg} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		if c.pkg {
			writeEntry(t, pkg, shipped)
		}
		link := os.Symlink
		if c.hard {
			link = os.Link
		}
		if err := link(c.to, record()); err != nil {
			t.Fatal(err)
		}
		if err := Set(c.on); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if b, err := os.ReadFile(c.to); err != nil || string(b) != launcher {
			t.Errorf("%s: the file it led to was written (%v)\n%s", c.name, err, b)
		}
		if fi, err := os.Lstat(record()); err != nil || !fi.Mode().IsRegular() || fi.Sys().(*syscall.Stat_t).Nlink != 1 {
			t.Errorf("%s: the user's entry is not a file of its own (%v)", c.name, err)
		}
		want := Off
		if c.on {
			want = On
		}
		if got := Enabled(); got != want {
			t.Errorf("%s: %s, want %s", c.name, got, want)
		}
	}
}

// A folder at an entry's path, or a link that leads nowhere or round in a
// loop, is no entry: systemd's generator and GLib pass over it, and start
// the package's entry after it. Read as an entry that starts nothing, it
// held the package's back, and Open at login read off while magpie opened.
// Nor is it a read that failed: with no package's entry, it reads off. And
// on over the package's entry has no entry of the user's to take away, so
// a folder stays.
func TestWhatIsNoEntry(t *testing.T) {
	sys := sandbox(t)
	pkg := filepath.Join(sys, "autostart", "magpie.desktop")
	if err := os.MkdirAll(filepath.Dir(record()), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		make func(p string) error
	}{
		{"a folder", func(p string) error {
			if err := os.Mkdir(p, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(p, "notes"), nil, 0o644)
		}},
		{"a link round in a loop", func(p string) error { return os.Symlink(p, p) }},
		{"a link that leads nowhere", func(p string) error { return os.Symlink(p+".gone", p) }},
	} {
		if err := os.RemoveAll(record()); err != nil {
			t.Fatal(err)
		}
		if err := c.make(record()); err != nil {
			t.Fatal(err)
		}
		for _, want := range []State{Off, On} {
			if want == On {
				writeEntry(t, pkg, shipped)
			} else if err := os.Remove(pkg); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if got := Enabled(); got != want {
				t.Errorf("%s at the user's entry, the package's entry there: %v: %s, want %s", c.name, want == On, got, want)
			}
		}
		if err := Set(true); err != nil || Enabled() != On {
			t.Errorf("on, %s at the user's entry, over the package's: %v, %s", c.name, err, Enabled())
		}
	}
	if err := os.RemoveAll(record()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(record(), "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Set(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(record(), "notes")); err != nil {
		t.Errorf("on took away a folder at the user's entry: %v", err)
	}
}

// An entry this user can't read counts all the same and starts nothing,
// as systemd's generator has it: the user's over the package's reads off,
// and on takes it away for the package's; the package's alone reads off,
// and on writes the user's own, which off takes away.
func TestAnUnreadableEntry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	sys := sandbox(t)
	pkg := filepath.Join(sys, "autostart", "magpie.desktop")
	writeEntry(t, pkg, shipped)
	writeEntry(t, record(), shipped)
	if err := os.Chmod(record(), 0); err != nil {
		t.Fatal(err)
	}
	if got := Enabled(); got != Off {
		t.Errorf("the user's entry unreadable, over the package's: %s, want off", got)
	}
	if err := Set(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(record()); !os.IsNotExist(err) || Enabled() != On {
		t.Errorf("on, the user's entry unreadable, over the package's: it stays (%v), %s", err, Enabled())
	}
	if err := os.Chmod(pkg, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(pkg, 0o644) })
	if got := Enabled(); got != Off {
		t.Errorf("the package's entry unreadable: %s, want off", got)
	}
	if err := Set(true); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(record()); err != nil || !strings.Contains(string(b), "/opt/my apps/magpie.AppImage") || Enabled() != On {
		t.Errorf("on, the package's entry unreadable: %s (%v)\n%s", Enabled(), err, b)
	}
	if err := Set(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(record()); !os.IsNotExist(err) || Enabled() != Off {
		t.Errorf("off, the package's entry unreadable: the user's entry stays (%v), %s", err, Enabled())
	}
}

// A read that fails on magpie's side (out of files, or of memory) says
// nothing of what the desktop reads at login: Set writes nothing and says
// why, and Open at login is not taken to read off.
func TestAFailedReadIsUnknown(t *testing.T) {
	sys := sandbox(t)
	writeEntry(t, filepath.Join(sys, "autostart", "magpie.desktop"), shipped)
	if err := Set(false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(record())
	if err != nil {
		t.Fatal(err)
	}
	readEntry = func(p string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: p, Err: syscall.EMFILE}
	}
	t.Cleanup(func() { readEntry = os.ReadFile })
	for _, on := range []bool{true, false} {
		if err := Set(on); !errors.Is(err, syscall.EMFILE) {
			t.Errorf("Set(%v), no entry read: %v, want the failed read", on, err)
		}
	}
	if got := Enabled(); got == Off {
		t.Error("no entry read, and Open at login reads off")
	}
	readEntry = os.ReadFile
	if after, err := os.ReadFile(record()); err != nil || string(after) != string(before) {
		t.Errorf("no entry read, and the user's was written over (%v)\n%s", err, after)
	}
}
