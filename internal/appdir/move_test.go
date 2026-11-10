package appdir

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// moveHome is a sandbox with magpie's folders in it, each with files: the
// config with a nested file and a link, the cache with one file.
func moveHome(t *testing.T) (cfg, cache, disk string) {
	t.Helper()
	r := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(r, "home", ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(r, "home", ".cache"))
	cfg, cache = Config(), Cache()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(cfg, "providers.json"), "providers")
	write(filepath.Join(cfg, "skills", "a", "SKILL.md"), "skill")
	write(filepath.Join(cache, "calls", "1.json"), "call")
	if runtime.GOOS != "windows" {
		if err := os.Symlink("skills/a", filepath.Join(cfg, "current")); err != nil {
			t.Fatal(err)
		}
	}
	disk = filepath.Join(r, "ssd")
	if err := os.MkdirAll(disk, 0o755); err != nil {
		t.Fatal(err)
	}
	return cfg, cache, disk
}

func readThrough(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func checkFiles(t *testing.T, cfg, cache string) {
	t.Helper()
	if got := readThrough(t, filepath.Join(cfg, "providers.json")); got != "providers" {
		t.Fatalf("providers.json = %q", got)
	}
	if got := readThrough(t, filepath.Join(cfg, "skills", "a", "SKILL.md")); got != "skill" {
		t.Fatalf("SKILL.md = %q", got)
	}
	if got := readThrough(t, filepath.Join(cache, "calls", "1.json")); got != "call" {
		t.Fatalf("cache file = %q", got)
	}
	if runtime.GOOS != "windows" {
		if got := readThrough(t, filepath.Join(cfg, "current", "SKILL.md")); got != "skill" {
			t.Fatalf("a link in the folder = %q", got)
		}
	}
}

// The folders go to the other disk with a link left where they were, so
// every path magpie and the agents know still reads them, and come back.
// Across disks (rename failing) they are copied, and the old copy goes.
func TestMoveToAnotherDiskAndBack(t *testing.T) { moveAndBack(t) }

func moveAndBack(t *testing.T) {
	for _, across := range []bool{false, true} {
		t.Run(map[bool]string{false: "one disk", true: "across disks"}[across], func(t *testing.T) {
			if across {
				old := rename
				rename = func(string, string) error { return errors.New("cross-device link") }
				t.Cleanup(func() { rename = old })
			}
			cfg, cache, disk := moveHome(t)
			root, err := Target(disk)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(disk, MoveName); root != want {
				t.Fatalf("Target = %s, want %s", root, want)
			}
			if err := Move(root); err != nil {
				t.Fatal(err)
			}
			p := Where()
			if p.ConfigAt != filepath.Join(root, "config") || p.CacheAt != filepath.Join(root, "cache") || p.Moved != root {
				t.Fatalf("Where = %+v", p)
			}
			checkFiles(t, cfg, cache)
			checkFiles(t, filepath.Join(root, "config"), filepath.Join(root, "cache"))
			for _, left := range []string{cfg + ".moved", cache + ".moved"} {
				if _, err := os.Lstat(left); err == nil {
					t.Fatalf("%s is left", left)
				}
			}
			if err := Unreachable(); err != nil {
				t.Fatal(err)
			}
			// a file written through the link lands on the disk
			os.WriteFile(filepath.Join(cfg, "new.json"), []byte("new"), 0o600)
			if got := readThrough(t, filepath.Join(root, "config", "new.json")); got != "new" {
				t.Fatalf("written through the link: %q", got)
			}

			if _, err := Target(disk); err == nil {
				t.Fatal("moved to the folder they are in already")
			}
			home, err := Target("")
			if err != nil || home != "" {
				t.Fatalf("Target(home) = %q, %v", home, err)
			}
			if err := Move(""); err != nil {
				t.Fatal(err)
			}
			for _, d := range []string{cfg, cache} {
				if _, ok := linkOf(d); ok {
					t.Fatalf("%s is a link still", d)
				}
				if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
					t.Fatalf("%s isn't a folder: %v", d, err)
				}
			}
			checkFiles(t, cfg, cache)
			if _, err := os.Stat(root); err == nil {
				t.Fatalf("%s is left on the disk", root)
			}
			if _, err := Target(""); err == nil {
				t.Fatal("moved home when home already")
			}
		})
	}
}

// A folder that has files in it already, one in magpie's own folder, or
// a path that isn't absolute, is refused before magpie quits for it.
func TestMoveTargetRefused(t *testing.T) {
	cfg, _, disk := moveHome(t)
	os.MkdirAll(filepath.Join(disk, MoveName), 0o755)
	os.WriteFile(filepath.Join(disk, MoveName, "theirs.txt"), []byte("x"), 0o600)
	for name, dir := range map[string]string{
		"has files":     disk,
		"magpie's own":  filepath.Join(cfg, "skills"),
		"magpie itself": cfg,
		"relative":      "ssd",
		"not there":     filepath.Join(disk, "gone"),
	} {
		if root, err := Target(dir); err == nil {
			t.Errorf("%s: Target(%s) = %s, want refused", name, dir, root)
		}
		if _, err := AskMove(dir); err == nil {
			t.Errorf("%s: AskMove(%s) asked", name, dir)
		}
	}
	if MoveAsked() {
		t.Fatal("a refused move is asked for")
	}
	if got := readThrough(t, filepath.Join(disk, MoveName, "theirs.txt")); got != "x" {
		t.Fatal("their file changed")
	}
}

// A copy that fails leaves everything where it was, and nothing on the
// other disk.
func TestMoveCopyFailsLeavesAll(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file can't be made unreadable by its mode on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	old := rename
	rename = func(string, string) error { return errors.New("cross-device link") }
	t.Cleanup(func() { rename = old })
	cfg, cache, disk := moveHome(t)
	locked := filepath.Join(cfg, "locked.json")
	os.WriteFile(locked, []byte("x"), 0o000)
	t.Cleanup(func() { os.Chmod(locked, 0o600) })
	root, err := Target(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := Move(root); err == nil {
		t.Fatal("moved with a file it couldn't read")
	}
	if _, ok := linkOf(cfg); ok {
		t.Fatal("magpie's folder is a link after a failed copy")
	}
	checkFiles(t, cfg, cache)
	if _, err := os.Stat(filepath.Join(root, "config")); err == nil {
		t.Fatal("a half copy is left on the disk")
	}
}

// The disk unplugged: magpie's folder leads nowhere, which is an error
// main stops on, never an empty magpie; a move can't be made from it.
func TestMovedDiskGoneIsUnreachable(t *testing.T) {
	cfg, _, disk := moveHome(t)
	root, err := Target(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := Move(root); err != nil {
		t.Fatal(err)
	}
	unplugged := disk + "-unplugged"
	if err := os.Rename(disk, unplugged); err != nil {
		t.Fatal(err)
	}
	var ue *UnreachableError
	if err := Unreachable(); !errors.As(err, &ue) || ue.Link != cfg || !strings.HasPrefix(ue.Target, disk) {
		t.Fatalf("Unreachable = %v", err)
	}
	if err := Move(""); !errors.As(err, &ue) {
		t.Fatalf("Move home with the disk gone = %v", err)
	}
	if _, ok := linkOf(cfg); !ok {
		t.Fatal("the link was taken away with the disk gone")
	}
	os.Rename(unplugged, disk)
	if err := Unreachable(); err != nil {
		t.Fatal(err)
	}
}

// A move asked for is made at the next start, once: one that fails is
// not asked for again, and why is kept for Settings.
func TestMovePendingOnce(t *testing.T) {
	cfg, cache, disk := moveHome(t)
	root, err := AskMove(disk)
	if err != nil {
		t.Fatal(err)
	}
	if p := Where(); p.Pending != root {
		t.Fatalf("Pending = %q, want %s", p.Pending, root)
	}
	// the folder filled after it was asked for
	os.MkdirAll(filepath.Join(root, "config"), 0o755)
	os.WriteFile(filepath.Join(root, "config", "x"), []byte("x"), 0o600)
	if moved, err := MovePending(); moved || err == nil {
		t.Fatalf("MovePending = %v, %v; want it refused", moved, err)
	}
	if MoveAsked() {
		t.Fatal("a failed move is asked for again")
	}
	if p := Where(); p.Error == "" || p.Pending != "" {
		t.Fatalf("Where after a failed move = %+v", p)
	}
	checkFiles(t, cfg, cache)
	os.RemoveAll(root)

	if _, err := AskMove(disk); err != nil {
		t.Fatal(err)
	}
	if p := Where(); p.Error != "" {
		t.Fatalf("an old failure is kept after a new ask: %q", p.Error)
	}
	if moved, err := MovePending(); !moved || err != nil {
		t.Fatalf("MovePending = %v, %v", moved, err)
	}
	if p := Where(); p.Moved != root || p.Pending != "" || p.Error != "" {
		t.Fatalf("Where = %+v", p)
	}
	checkFiles(t, cfg, cache)
	if moved, err := MovePending(); moved || err != nil {
		t.Fatalf("MovePending with none asked = %v, %v", moved, err)
	}
	if _, err := AskMove(""); err != nil {
		t.Fatal(err)
	}
	if p := Where(); p.Pending != "home" {
		t.Fatalf("Pending = %q, want home", p.Pending)
	}
	if err := CancelMove(); err != nil || MoveAsked() {
		t.Fatalf("CancelMove = %v, asked %v", err, MoveAsked())
	}
}
