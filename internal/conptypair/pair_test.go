package conptypair

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func sum(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

func pair() (fstest.MapFS, []File) {
	src := fstest.MapFS{
		"amd64/conpty.dll":      {Data: []byte("dll")},
		"amd64/OpenConsole.exe": {Data: []byte("exe")},
	}
	return src, []File{{"conpty.dll", "", sum("dll")}, {"OpenConsole.exe", "", sum("exe")}}
}

// TestWrite: the pair is written where it is to be loaded, and a file
// there that isn't the pinned one (half-written, or put there by another)
// is written again.
func TestWrite(t *testing.T) {
	src, files := pair()
	dir := filepath.Join(t.TempDir(), "x")
	if err := write(src, "amd64", files, dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := Check(filepath.Join(dir, f.Name), f.SHA256); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "conpty.dll"), []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := write(src, "amd64", files, dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "conpty.dll")); string(b) != "dll" {
		t.Fatalf("a changed conpty.dll was left: %q", b)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(left) > 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

// TestWriteRefusesUnpinned: bundled bytes that aren't the pinned ones are
// never written, and the folder is not to be loaded.
func TestWriteRefusesUnpinned(t *testing.T) {
	src, files := pair()
	src["amd64/OpenConsole.exe"] = &fstest.MapFile{Data: []byte("changed")}
	dir := t.TempDir()
	err := write(src, "amd64", files, dir)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("got %v, want a SHA-256 mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "OpenConsole.exe")); err == nil {
		t.Fatal("an unpinned OpenConsole.exe was written")
	}
	if err := write(src, "386", nil, dir); err == nil {
		t.Fatal("an architecture with no pin was written")
	}
}

// TestPins: each architecture pins both files, with a full SHA-256.
func TestPins(t *testing.T) {
	for _, a := range []string{"amd64", "arm64"} {
		fs := Files[a]
		if len(fs) != 2 || fs[0].Name != "conpty.dll" || fs[1].Name != "OpenConsole.exe" {
			t.Fatalf("%s pins %v", a, fs)
		}
		for _, f := range fs {
			if b, err := hex.DecodeString(f.SHA256); err != nil || len(b) != 32 {
				t.Fatalf("%s %s: %q isn't a SHA-256", a, f.Name, f.SHA256)
			}
		}
	}
	if !strings.Contains(URL, "/"+Version+"/") || !strings.HasPrefix(URL, "https://api.nuget.org/") {
		t.Fatalf("URL %s", URL)
	}
}

// TestReadyWithoutPair: a build without -tags conptypair has nothing to
// write, and says so.
func TestReadyWithoutPair(t *testing.T) {
	if Bundled() {
		t.Skip("built with the pair")
	}
	if _, err := Ready(t.TempDir()); err == nil {
		t.Fatal("Ready without a pair")
	}
}
