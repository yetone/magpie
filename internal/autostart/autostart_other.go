//go:build !darwin && !windows && !android

package autostart

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
)

// record is the user's own autostart entry, the one magpie writes.
func record() string {
	dir := appdir.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", "magpie.desktop")
}

// systemRecords are the system's entries of the same name, one in each
// folder of $XDG_CONFIG_DIRS (/etc/xdg when it is unset), the most
// important first: a package ships its entry there (mtul0729 packaging
// magpie for nixpkgs, #3372). As the XDG Autostart spec has it, the user's
// entry stands over them, and of the entries of one name only the most
// important counts, its Hidden holding back every other.
func systemRecords() []string {
	sys := os.Getenv("XDG_CONFIG_DIRS")
	if sys == "" {
		sys = "/etc/xdg"
	}
	own, ownErr := os.Stat(filepath.Dir(record()))
	var out []string
	for _, d := range filepath.SplitList(sys) {
		// a relative folder is none, as the spec says; the user's own
		// folder listed there too, by any name, is not a system's
		dir := filepath.Join(d, "autostart")
		if !filepath.IsAbs(d) {
			continue
		}
		if fi, err := os.Stat(dir); err == nil && ownErr == nil && os.SameFile(fi, own) {
			continue
		}
		out = append(out, filepath.Join(dir, "magpie.desktop"))
	}
	return out
}

// keys are an entry's [Desktop Entry] keys as GLib's key file and
// systemd's parser read them: the spaces around = and a line's CR are no
// part of a key or its value, a key given again keeps its last value, and
// another group's keys (a [Desktop Action]'s Exec) are not the entry's.
func keys(b []byte) map[string]string {
	k := map[string]string{}
	in := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			in = line == "[Desktop Entry]"
			continue
		}
		if name, value, ok := strings.Cut(line, "="); in && ok && !strings.HasPrefix(line, "#") {
			k[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
	}
	return k
}

// a desktop entry's booleans, as GLib reads them
func isTrue(v string) bool  { return v == "true" || v == "1" }
func isFalse(v string) bool { return v == "false" || v == "0" }

// runs is whether some desktop starts an entry at login: an application
// with something to run, not Hidden. An entry that runs true opens nothing
// either: the one off writes over a package's is that, and a desktop that
// switches it back on itself (XFCE's Session and Startup writes
// Hidden=false into it) runs true, not magpie, so it still reads off and
// on takes it away.
func runs(k map[string]string) bool {
	return k["Type"] == "Application" && k["Exec"] != "" && k["Exec"] != "true" && !isTrue(k["Hidden"])
}

// starts is whether the desktop starts an entry at login, as Open at login
// reads it: as runs has it, and not switched off GNOME's way either
// (X-GNOME-Autostart-enabled=false), which GNOME's and Cinnamon's sessions
// read and systemd's generator (KDE's, COSMIC's) doesn't.
func starts(k map[string]string) bool {
	return runs(k) && !isFalse(k["X-GNOME-Autostart-enabled"])
}

// readEntry reads an entry's file. A var for the tests, which can't run
// magpie out of files.
var readEntry = os.ReadFile

// entryAt is the entry at p as the desktop finds it, and whether there is
// one. There is none where nothing is, nor where no regular file is (a
// folder, a link that leads nowhere or round in a loop, a path under a
// folder this user can't search or with a name too long), which systemd's
// generator and GLib pass over.
// One there that can't be read counts all the same, and starts nothing,
// as systemd's generator has it (GLib passes over it, so GNOME's session
// starts the next folder's). Any other failure, magpie out of files or
// memory, says nothing of what the desktop reads at login: an error.
func entryAt(p string) (map[string]string, bool, error) {
	fi, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENAMETOOLONG) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, nil
	}
	b, err := readEntry(p)
	switch {
	case err == nil:
		return keys(b), true, nil
	case errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EIO):
		return nil, true, nil
	}
	return nil, false, err
}

// counted is the entry that counts of these, the first one there, and
// whether there is one; an error when that can't be told.
func counted(paths []string) (map[string]string, bool, error) {
	for _, p := range paths {
		if k, ok, err := entryAt(p); ok || err != nil {
			return k, ok, err
		}
	}
	return nil, false, nil
}

func enabled() bool {
	k, ok, err := counted(append([]string{record()}, systemRecords()...))
	if err != nil {
		return true // not taken for a no: the desktop may well start it
	}
	return ok && starts(k)
}

// system is the system's own entry that counts, were there no entry of
// the user's over it.
func system() (map[string]string, error) {
	k, _, err := counted(systemRecords())
	return k, err
}

// an XDG autostart entry, which the desktop starts at login. When the
// system's own entry starts magpie, the user's could only hold it back: it
// goes, and no path of this copy's is written over the one the system's
// entry names, which its package keeps current.
func enable(exe string) error {
	sys, err := system()
	if err != nil {
		return err
	}
	if starts(sys) {
		return removeRecord()
	}
	// the Exec key's quoting: in double quotes, \ " ` $ escaped
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(exe)
	return writeRecord("[Desktop Entry]\nType=Application\nName=magpie\nComment=one place to pick every agent's model\n" +
		"Exec=\"" + q + "\" " + Arg + "\nTerminal=false\nX-GNOME-Autostart-enabled=true\n")
}

// off: the user's entry goes. When the system's own entry starts magpie
// on some desktop, that would leave it starting, so the user's entry
// stands over it Hidden instead, as the spec has a user switch a system's
// entry off. That includes one switched off GNOME's way, which systemd's
// generator (KDE's, COSMIC's) still starts.
func disable() error {
	sys, err := system()
	if err != nil {
		return err
	}
	if !runs(sys) {
		return removeRecord()
	}
	return writeRecord(hiddenEntry)
}

// hiddenEntry stands over the system's entry. A session holds an entry
// back only once it has read it, so it carries what each one needs:
// GNOME's and Cinnamon's (GLib) pass over an entry with no Type (on GLib's
// main branch, no Name either), or whose Exec names a program they can't
// find, such as a store path collected since; MATE's passes over one with
// no Exec. true is on every system, and COSMIC's session without systemd,
// which reads no Hidden, starts it in magpie's place.
const hiddenEntry = "[Desktop Entry]\nType=Application\nName=magpie\nExec=true\nHidden=true\n"

// writeRecord writes the user's entry where the desktop reads it. A link
// there (to magpie's launcher, the usual way to have an app open at
// login, or into a read-only store), or a file under another name too, is
// replaced, not written through, so the file it leads to stays as it was.
func writeRecord(body string) error {
	p := record()
	if linked(p) {
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return edit.WriteAtomic(p, []byte(body))
}

// linked is whether p is a link, or a file with other names (a hard
// link), which edit.WriteAtomic writes through.
func linked(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return fi.Mode()&fs.ModeSymlink != 0 || fi.Mode().IsRegular() && ok && st.Nlink > 1
}

// removeRecord takes the user's entry away: a file or a link, not a
// folder of its name, which is no entry.
func removeRecord() error {
	p := record()
	if fi, err := os.Lstat(p); err == nil && fi.IsDir() {
		return nil
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// refresh: nothing an older magpie wrote here needs writing again
func refresh() error { return nil }

// heldBack: nothing here is switched off apart from the record itself
func heldBack() bool { return false }
