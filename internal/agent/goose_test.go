package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// A goose on PATH that is pressly's migration tool (a Go program) is not the
// Goose agent (Discord: Jun, goose shown without ~/.config/goose); Block's
// goose, not a Go program, is.
func TestGooseDetectSkipsGoGoose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH lookup of a script")
	}
	home := t.TempDir()
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	a := goose(home, filepath.Join(home, ".config"))
	if a.Detected() {
		t.Fatal("detected with no goose at all")
	}

	src := filepath.Join(t.TempDir(), "main.go")
	os.WriteFile(src, []byte("package main\nfunc main() {}\n"), 0o644)
	gobin, err := exec.LookPath("go")
	if err != nil {
		gobin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	build := exec.Command(gobin, "build", "-o", filepath.Join(bin, "goose"), src)
	build.Env = append(os.Environ(), "PATH="+os.Getenv("PATH"))
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("go build: %v %s", err, out)
	}
	if a.Detected() {
		t.Fatal("a Go goose on PATH was taken for the agent")
	}

	os.WriteFile(filepath.Join(bin, "goose"), []byte("#!/bin/sh\necho 1.9.0\n"), 0o755)
	if !a.Detected() {
		t.Fatal("Block's goose on PATH not detected")
	}

	os.Remove(filepath.Join(bin, "goose"))
	os.MkdirAll(filepath.Join(home, ".config", "goose"), 0o755)
	if !a.Detected() {
		t.Fatal("goose's config dir not detected")
	}
}
