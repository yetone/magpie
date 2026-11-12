package darwin

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// overlay runs footprint-overlay.sh into a test folder and returns its
// -overlay flag.
func overlay(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.Command("sh", "footprint-overlay.sh", filepath.Join(dir, "ovl")).CombinedOutput()
	if err != nil {
		t.Fatalf("footprint-overlay.sh: %v\n%s", err, out)
	}
	flag := strings.TrimSpace(string(out))
	if !strings.HasPrefix(flag, "-overlay=") {
		t.Fatalf("footprint-overlay.sh printed %q, want -overlay=…", flag)
	}
	b, err := os.ReadFile(filepath.Join(dir, "ovl", "mem_darwin.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "_MAP_ANON|_MAP_FIXED|_MAP_PRIVATE, -1, 0); p == v && err == 0 {") {
		t.Fatalf("the runtime copy does not map fresh pages in sysUnusedOS:\n%s", b)
	}
	return flag
}

// freed is a program that touches 128 MB of heap, lets it go, and prints
// its footprint in bytes after debug.FreeOSMemory.
const freed = `package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
)

var keep [][]byte

func main() {
	for range 128 {
		b := make([]byte, 1<<20)
		for i := range b {
			b[i] = 1
		}
		keep = append(keep, b)
	}
	keep = nil
	runtime.GC()
	debug.FreeOSMemory()
	out, err := exec.Command("footprint", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		fmt.Println("footprint:", err)
		os.Exit(2)
	}
	m := regexp.MustCompile(` + "`phys_footprint: ([0-9.]+) (KB|MB)`" + `).FindSubmatch(out)
	if m == nil {
		fmt.Printf("no phys_footprint in %s", out)
		os.Exit(2)
	}
	fmt.Println(string(m[1]), string(m[2]))
}
`

// The release build's Go runtime gives macOS back the heap it frees, so
// Activity Monitor's Memory drops with it. With Go's own
// MADV_FREE_REUSABLE, macOS 27 kept all 128 MB in the footprint.
func TestFreedHeapLeavesFootprint(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the runtime")
	}
	flag := overlay(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(freed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module freed\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "freed")
	build := exec.Command("go", "build", flag, "-o", bin, ".")
	build.Dir = dir
	if runtime.GOOS != "darwin" {
		// elsewhere the copy is only compiled, for the system it serves
		build.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0")
	}
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", flag, err, out)
	}
	if runtime.GOOS != "darwin" {
		return
	}
	if _, err := exec.LookPath("footprint"); err != nil {
		t.Skip("no footprint command")
	}
	out, err := exec.Command(bin).CombinedOutput()
	if err != nil {
		t.Fatalf("freed: %v\n%s", err, out)
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		t.Fatalf("freed printed %q", out)
	}
	n, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		t.Fatal(err)
	}
	if f[1] == "KB" {
		n /= 1024
	}
	if n > 48 {
		t.Fatalf("footprint after freeing 128 MB is %.1f MB, want under 48 MB", n)
	}
}
