package agent

import (
	"context"
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

func reasonixDetected(home string) bool {
	switch runtime.GOOS {
	case "windows":
		for _, dir := range reasonixStudioDirs() {
			if (isFile(filepath.Join(dir, "Reasonix Studio.exe")) || isFile(filepath.Join(dir, "reasonix-studio.exe"))) &&
				reasonixStudioPackage(filepath.Join(dir, "resources", "app.asar")) {
				return true
			}
		}
	case "darwin":
		for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
			if reasonixStudioPackage(filepath.Join(dir, "Reasonix Studio.app", "Contents", "Resources", "app.asar")) {
				return true
			}
		}
	case "linux":
		for _, bin := range []string{"reasonix-studio", "reasonix-studio-electron"} {
			if path, err := exec.LookPath(bin); err == nil {
				if resolved, err := filepath.EvalSymlinks(path); err == nil {
					path = resolved
				}
				if reasonixStudioPackage(filepath.Join(filepath.Dir(path), "resources", "app.asar")) {
					return true
				}
			}
		}
	}
	path, err := exec.LookPath("reasonix")
	return err == nil && reasonixCLI(path)
}

var reasonixVersions = struct {
	sync.Mutex
	m map[goProgramKey]bool
}{m: map[goProgramKey]bool{}}

// A shared config is also evidence of 1.x, and npm's reasonix still installs
// that line. Probe only a native Reasonix CLI, once per binary revision, without
// starting either its agent loop or the desktop host.
func reasonixCLI(path string) bool {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	key := goProgramKey{path, st.Size(), st.ModTime()}
	reasonixVersions.Lock()
	defer reasonixVersions.Unlock()
	if v, ok := reasonixVersions.m[key]; ok {
		return v
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return false
	}
	detected := false
	if info.Path == "reasonix/cmd/reasonix" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		b, err := proc.CommandContext(ctx, path, "--version").Output()
		if err != nil {
			return false // retry a failed probe; only a confirmed version is cached
		}
		version, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "reasonix v")
		detected = ok && strings.HasPrefix(version, "2.")
	}
	reasonixVersions.m[key] = detected
	return detected
}
