package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A link made in a distro (WSLSymlink) is read back where Windows can't
// read it (WSLReadlink), made again over itself, never over a folder, and
// taken away alone (WSLRemoveLink), what it leads to left as it is.
func TestWSLLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake distro's links are this system's")
	}
	root := t.TempDir()
	t.Cleanup(FakeWSL(map[string]string{"U": "home:/home/me\ndir:.codex\n"}, map[string]string{"U": root}))
	wslDistros()
	a := &Agent{ID: "codex@wsl:U", WSL: "U"}
	home := filepath.Join(root, "home", "me")
	store := filepath.Join(home, ".local", "share", "magpie", "skills", "pdf")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".codex", "skills", "pdf")
	for range 2 {
		if err := WSLSymlink(a, store, link); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := WSLReadlink(link); !ok || got != store {
		t.Fatalf("read back: %q %v", got, ok)
	}
	if _, ok := WSLReadlink(filepath.Join(home, ".codex", "skills", "none")); ok {
		t.Error("a name that isn't there read as a link")
	}
	if _, ok := WSLReadlink(filepath.Join(t.TempDir(), "x")); ok {
		t.Error("a path in no distro read as a link")
	}
	if err := WSLSymlink(a, store, filepath.Dir(store)); err == nil || !strings.Contains(err.Error(), "is not a link") {
		t.Errorf("a link made over a folder: %v", err)
	}
	if err := WSLRemoveLink(store); err == nil {
		t.Error("a folder removed as a link")
	}
	if err := WSLRemoveLink(link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("the link is still there: %v", err)
	}
	if _, ok := WSLReadlink(link); ok {
		t.Error("a link taken away still reads as one")
	}
	if _, err := os.Stat(store); err != nil {
		t.Errorf("what it led to went with it: %v", err)
	}
}

// On a real WSL: MAGPIE_WSL_TEST_DISTRO names the distro, and the test
// works only in a folder of its own it makes in /tmp there. What Windows
// can't do through \\wsl.localhost (read, follow or remove a link the
// distro has) is done in the distro, and the distro follows the link.
func TestWSLLinksLive(t *testing.T) {
	name := os.Getenv("MAGPIE_WSL_TEST_DISTRO")
	if name == "" || runtime.GOOS != "windows" {
		t.Skip("MAGPIE_WSL_TEST_DISTRO not set")
	}
	t.Setenv("PATH", filepath.Join(os.Getenv("SystemRoot"), "System32")+string(os.PathListSeparator)+os.Getenv("PATH"))
	base := "/tmp/magpie-wsl-links-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	sh := func(script string, args ...string) string {
		t.Helper()
		out, err := realWSLRun(30*time.Second, append([]string{"-d", name, "--exec", "sh", "-c", script, "sh"}, args...)...)
		if err != nil {
			t.Fatalf("%s: %v %s", script, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	sh(`mkdir -p -- "$1/store/pdf" && printf 'Read PDFs' > "$1/store/pdf/SKILL.md"`, base)
	t.Cleanup(func() { sh(`rm -rf -- "$1"`, base) })
	wsl.Lock()
	seen, running, runAt, runOK := wsl.seen, wsl.running, wsl.runAt, wsl.runOK
	wsl.seen = map[string]*distro{name: {Name: name, Home: base, Root: wslRoot(name)}}
	wsl.running, wsl.runAt, wsl.runOK = map[string]bool{name: true}, time.Now(), true
	wsl.Unlock()
	t.Cleanup(func() {
		wsl.Lock()
		wsl.seen, wsl.running, wsl.runAt, wsl.runOK = seen, running, runAt, runOK
		wsl.Unlock()
	})
	d := wsl.seen[name]
	a := &Agent{ID: "codex@wsl:" + name, WSL: name}
	store, link := d.where(base+"/store/pdf"), d.where(base+"/agent/skills/pdf")
	if b, err := os.ReadFile(filepath.Join(store, "SKILL.md")); err != nil || string(b) != "Read PDFs" {
		t.Fatalf("the distro's folder through %s: %q %v", d.Root, b, err)
	}
	for range 2 {
		if err := WSLSymlink(a, store, link); err != nil {
			t.Fatal(err)
		}
	}
	if got := sh(`readlink -- "$1"`, base+"/agent/skills/pdf"); got != base+"/store/pdf" {
		t.Fatalf("the distro's link leads to %q", got)
	}
	if got := sh(`cat -- "$1/SKILL.md"`, base+"/agent/skills/pdf"); got != "Read PDFs" {
		t.Fatalf("the distro reads through the link: %q", got)
	}
	// what Windows makes of it, as the library meets it
	fi, err := os.Lstat(link)
	t.Logf("Windows' Lstat of the link: %v %v", fi, err)
	if err != nil {
		t.Fatalf("Windows doesn't see the link: %v", err)
	}
	if _, err := os.Readlink(link); err == nil {
		t.Log("Windows reads the link itself")
	}
	if got, ok := WSLReadlink(link); !ok || !strings.EqualFold(filepath.Clean(got), filepath.Clean(store)) {
		t.Fatalf("read back %q %v, want %q", got, ok, store)
	}
	if err := WSLSymlink(a, store, filepath.Dir(store)); err == nil || !strings.Contains(err.Error(), "is not a link") {
		t.Errorf("a link made over a folder: %v", err)
	}
	if err := WSLRemoveLink(store); err == nil {
		t.Error("a folder removed as a link")
	}
	if err := WSLRemoveLink(link); err != nil {
		t.Fatal(err)
	}
	if got := sh(`[ -e "$1" ] || [ -L "$1" ] && echo there || echo gone`, base+"/agent/skills/pdf"); got != "gone" {
		t.Errorf("the link: %s", got)
	}
	if got := sh(`cat -- "$1/SKILL.md"`, base+"/store/pdf"); got != "Read PDFs" {
		t.Errorf("what it led to: %q", got)
	}
}
