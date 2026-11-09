package update

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The Windows app is linked as a GUI program, so a double click opens no
// console window; but a shell doesn't wait for a GUI program either. In
// PowerShell or cmd, `magpie plugin login` gave the prompt back at once,
// printed after it, and the shell and magpie both read the keys typed:
// a hidden password never reached magpie (TomXPRIME, #3737). A shell looks
// for `magpie` by PATHEXT, which lists .COM before .EXE, so magpie writes
// magpie.com beside the app: a small console program (./launcher) that
// the shell waits for, which runs the app with its console and exits with
// the app's code. Double clicks and the Start menu still open magpie.exe.
//
// It is written wherever a terminal runs magpie from: when the app's
// folder is on PATH, where Settings › Command line puts it, or a magpie.com
// or magpie.cmd is there already. On the Desktop or in Downloads only Add
// writes it, and a start only keeps one there up to date. The app writes it again as it starts
// when its bytes differ, so an update brings it along. They differ only
// when the launcher does: it is built without the release's commit in it
// (the Makefile's -buildvcs=false), or every update wrote a new magpie.com
// (lucky_eagle_38410 on Discord, TestLauncherSameAtEveryCommit).

// ComEnv is set by magpie.com for the app it runs: the standard handles it
// hands over are the terminal's console or a redirection, and its console
// is the one to borrow (cli's console_windows.go).
const ComEnv = "MAGPIE_COM"

// comName is the companion's file name; it runs as `magpie`.
const comName = "magpie.com"

// comMagic ends magpie.com; the app it runs is named just before it,
// after a NUL (launcher's targetName).
const comMagic = "\x00magpie-com-target"

// comLauncher is the launcher program, built for this system and put in
// the Windows app by `make release-windows` (com_embed_windows_*.go); nil
// in any other build, which writes no magpie.com.
var comLauncher []byte

// comText is the magpie.com that runs the program exe in its folder.
func comText(launcher []byte, exe string) []byte {
	name := filepath.Base(strings.ReplaceAll(exe, `\`, "/"))
	out := make([]byte, 0, len(launcher)+1+len(name)+len(comMagic))
	out = append(out, launcher...)
	out = append(out, 0)
	out = append(out, name...)
	return append(out, comMagic...)
}

// comTarget is the program name in a magpie.com's last bytes, "" when it
// has none.
func comTarget(b []byte) string {
	if !bytes.HasSuffix(b, []byte(comMagic)) {
		return ""
	}
	b = b[:len(b)-len(comMagic)]
	return string(b[bytes.LastIndexByte(b, 0)+1:])
}

// comRuns says whether cmd is a magpie.com beside exe that runs it.
func comRuns(cmd, exe string) bool {
	if !strings.EqualFold(filepath.Base(cmd), comName) || !strings.EqualFold(filepath.Dir(cmd), filepath.Dir(exe)) {
		return false
	}
	f, err := os.Open(cmd)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < int64(len(comMagic)) {
		return false
	}
	n := min(st.Size(), 1024)
	b := make([]byte, n)
	if _, err := f.ReadAt(b, st.Size()-n); err != nil {
		return false
	}
	return strings.EqualFold(comTarget(b), filepath.Base(exe))
}

// comOwner says whether exe is the program a magpie.com in its folder
// should run: a magpie.exe there is what `magpie` ran before, so another
// copy beside it (magpie-windows-amd64 (1).exe) leaves it be.
func comOwner(exe string) bool {
	if strings.EqualFold(filepath.Base(exe), "magpie.exe") {
		return true
	}
	st, err := os.Stat(filepath.Join(filepath.Dir(exe), "magpie.exe"))
	return err != nil || st.IsDir()
}

// EnsureCompanion writes the magpie.com that runs exe beside it when a
// terminal runs magpie from that folder (on PATH, or a magpie.com or
// magpie.cmd there), or brings one there up to this version. Nothing in
// a build without the launcher, or when it is as it should be already.
func EnsureCompanion(exe string) error {
	// a download that opens the installed magpie (Settle) leaves the
	// command to that one, in its folder on PATH
	if comLauncher == nil || !comOwner(exe) || forwards(exe) {
		return nil
	}
	dir := filepath.Dir(exe)
	if downloadFolder(dir) {
		// the Desktop or Downloads, which the user sees: one there is
		// kept up to date, never put back once deleted (nianshou5 on
		// Discord: it came back on every start, the Desktop being on
		// PATH). Settings › Command line's Add still writes it.
		if !exists(filepath.Join(dir, comName)) {
			return nil
		}
	} else if !onPath(dir, os.Getenv("PATH")) && !exists(filepath.Join(dir, comName)) && !exists(filepath.Join(dir, shimName)) {
		return nil
	}
	_, err := writeCom(comLauncher, exe)
	return err
}

// writeCom writes the magpie.com that runs exe beside it, unless it is
// there already, and is its path. A magpie.com running now (a `magpie
// serve` in a terminal) can't be written over, but can be moved aside,
// as an update moves the running app; the next run clears it.
func writeCom(launcher []byte, exe string) (string, error) {
	p := filepath.Join(filepath.Dir(exe), comName)
	want := comText(launcher, exe)
	// the one moved aside last time, running then, is let go of now
	RemoveOld(p)
	if b, err := os.ReadFile(p); err == nil && bytes.Equal(b, want) {
		return p, nil
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".magpie-com-*")
	if err != nil {
		return "", fmt.Errorf("writing %s, which runs %s as magpie: %w", p, filepath.Base(exe), err)
	}
	tmp := f.Name()
	_, err = f.Write(want)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if err = os.Rename(tmp, p); err != nil && exists(p) {
			if aerr := os.Rename(p, oldName(p)); aerr == nil {
				err = os.Rename(tmp, p)
			}
		}
	}
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("writing %s, which runs %s as magpie: %w", p, filepath.Base(exe), err)
	}
	return p, nil
}

// onPath is whether dir is one of path's (Windows', ;-separated) folders.
func onPath(dir, path string) bool {
	dir = strings.TrimRight(dir, `\/`)
	for _, d := range strings.Split(path, string(os.PathListSeparator)) {
		if d = strings.TrimRight(strings.TrimSpace(d), `\/`); d != "" && strings.EqualFold(d, dir) {
			return true
		}
	}
	return false
}

// exists is whether p is there and could be looked at: false too when
// that is unknown. Anything that makes, moves over or removes on its
// answer asks present instead, which tells unknown apart.
func exists(p string) bool {
	_, err := statPath(p)
	return err == nil
}

// present is whether p is there: an error when that is unknown (Stat
// failed for a reason other than its not being there, such as a folder
// that may not be looked into).
func present(p string) (bool, error) {
	_, err := statPath(p)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

// statPath is os.Stat; tests make it fail.
var statPath = os.Stat
