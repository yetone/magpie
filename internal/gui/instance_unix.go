//go:build !windows

package gui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// heldInstance is the open lock file of the magpie that holds its
// instance; it stays open, so locked, until this process ends.
var heldInstance *os.File

// heldPath is the lock heldInstance holds.
var heldPath string

// instanceLock is where the magpie running exe on the config in dir holds
// its lock. A copy elsewhere (the owner's magpie-dev) has its own, but
// every installed magpie — a magpie.app wherever it is, or Homebrew's,
// whose path has its version in it — shares one: the Mac reopening the
// copy the user ran after a shutdown while Open at login starts the one
// it was turned on for, or a copy run translocated from Downloads, put
// two birds in the menu bar (inaction on Discord).
func instanceLock(dir, exe string) string {
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if installedCopy(exe) {
		exe = "installed"
	}
	sum := sha256.Sum256([]byte(dir + "\x00" + exe))
	return filepath.Join(dir, "app-"+hex.EncodeToString(sum[:6])+".lock")
}

// installedCopy is whether exe is an installed magpie: the program of an
// app bundle, Homebrew's, or a Nix or Guix package's (a new store path
// each upgrade).
func installedCopy(exe string) bool {
	exe = filepath.ToSlash(exe)
	return strings.Contains(exe, ".app/Contents/MacOS/") || strings.Contains(exe, "/Cellar/magpie/") ||
		strings.HasPrefix(exe, "/nix/store/") || strings.HasPrefix(exe, "/gnu/store/")
}

// holdInstance takes the lock at path for this process, or reports that
// another magpie holds it. It is a flock, which the system lets go when
// its holder ends however it ends, so a Mac shut down with magpie running
// leaves nothing behind that looks held (Crispin on Discord).
func holdInstance(path string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		f.Close()
		return false, nil
	}
	if err != nil {
		f.Close()
		return false, err
	}
	heldInstance, heldPath = f, path
	return true, nil
}

// releaseInstance lets go of the instance lock, for the new version a
// restart hands the gateway to (handover.go) to take as it starts.
func releaseInstance() {
	if heldInstance != nil {
		heldInstance.Close()
		heldInstance = nil
	}
}

// retakeInstance takes the lock again after a handover that didn't
// happen; a new version that took it meanwhile keeps it.
func retakeInstance() {
	if heldInstance != nil || heldPath == "" {
		return
	}
	for i := 0; i < 10; i++ {
		if held, err := holdInstance(heldPath); held || err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// handoffAlive is whether the process pid runs.
func handoffAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
