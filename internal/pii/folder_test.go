package pii

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/settings"
)

// seed writes the models' files where the default folder keeps them, and
// returns what each holds by its path under the root.
func seed(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{
		filepath.Join(pfFolder, modelName()):                    strings.Repeat("model-bytes ", 5000),
		filepath.Join(pfFolder, serveTag+"-x", "pf-serve"):      "exe",
		filepath.Join(guardFolder, guardRev[:12], "model.onnx"): strings.Repeat("g", 777),
	}
	for rel, body := range files {
		p := filepath.Join(Root(), rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		settings.SetRedactModelDir("")
		os.RemoveAll(filepath.Join(appdir.Cache(), pfFolder))
		os.RemoveAll(filepath.Join(appdir.Cache(), guardFolder))
	})
	return files
}

func moved(t *testing.T) Moving {
	t.Helper()
	for end := time.Now().Add(60 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if s := MoveState(); !s.Moving {
			return s
		}
	}
	t.Fatal("the move never ended")
	return Moving{}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The models move to the picked folder, every path follows it, and the old
// folders are gone. One folder is moved by a rename and the other, with
// its target there already, by the copy a move to another disk makes.
func TestMoveToFollowsEveryPath(t *testing.T) {
	files := seed(t)
	def := Root()
	to := filepath.Join(t.TempDir(), "models")
	if err := os.MkdirAll(filepath.Join(to, guardFolder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MoveTo(to); err != nil {
		t.Fatal(err)
	}
	if s := moved(t); s.Error != "" || s.Left != "" {
		t.Fatalf("move: %+v", s)
	}
	if Root() != to || !Custom() {
		t.Fatalf("Root = %s, want %s", Root(), to)
	}
	for _, p := range []string{dir(), guardRoot(), cachePath(), modelPath(), guardDir()} {
		if !strings.HasPrefix(p, to) {
			t.Errorf("%s isn't under %s", p, to)
		}
	}
	for rel, body := range files {
		if got := read(t, filepath.Join(to, rel)); got != body {
			t.Errorf("%s changed in the move", rel)
		}
	}
	for _, d := range []string{pfFolder, guardFolder} {
		if _, err := os.Stat(filepath.Join(def, d)); !os.IsNotExist(err) {
			t.Errorf("the old %s is still there: %v", d, err)
		}
	}
	// the setting is saved, and a sync's settings keep this computer's
	if settings.Load().RedactModelDir != to {
		t.Error("the folder isn't saved")
	}
	// back to the default
	if err := MoveTo(""); err != nil {
		t.Fatal(err)
	}
	if s := moved(t); s.Error != "" {
		t.Fatal(s.Error)
	}
	if Custom() || Root() != def || settings.Load().RedactModelDir != "" {
		t.Errorf("not back to the default: %s %v", Root(), Custom())
	}
	for rel, body := range files {
		if got := read(t, filepath.Join(def, rel)); got != body {
			t.Errorf("%s lost on the way back", rel)
		}
	}
	// the folder the user picked is theirs: never deleted
	if _, err := os.Stat(to); err != nil {
		t.Errorf("the picked folder was deleted: %v", err)
	}
}

// A move that fails leaves every model where it was, and the setting as it
// was: the second folder can't be copied, and the first, already moved, is
// put back.
func TestFailedMoveLosesNothing(t *testing.T) {
	files := seed(t)
	def := Root()
	to := filepath.Join(t.TempDir(), "models")
	// astrlink-guard exists in the target, with a directory where a file of
	// the model's would be written
	block := filepath.Join(to, guardFolder, guardRev[:12], "model.onnx.part-move")
	if err := os.MkdirAll(block, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MoveTo(to); err != nil {
		t.Fatal(err)
	}
	if s := moved(t); s.Error == "" {
		t.Fatal("the move didn't fail")
	}
	if Custom() || Root() != def {
		t.Errorf("the setting changed: %s", Root())
	}
	for rel, body := range files {
		if got := read(t, filepath.Join(def, rel)); got != body {
			t.Errorf("%s changed", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(to, pfFolder)); !os.IsNotExist(err) {
		t.Errorf("the first folder's move wasn't undone: %v", err)
	}
	// a new move works once the cause is gone
	os.Remove(block)
	if err := MoveTo(to); err != nil {
		t.Fatal(err)
	}
	if s := moved(t); s.Error != "" {
		t.Fatal(s.Error)
	}
	if Root() != to {
		t.Errorf("Root = %s", Root())
	}
}

// A folder that can't be written, can't be made, or is inside what is
// moved is refused before anything is touched.
func TestMoveToRefusesBadFolders(t *testing.T) {
	files := seed(t)
	def := Root()
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"relative":     "models",
		"under a file": filepath.Join(file, "models"),
		"inside":       filepath.Join(def, pfFolder, "sub"),
	}
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		ro := filepath.Join(t.TempDir(), "ro")
		if err := os.Mkdir(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		bad["read only"] = ro
	}
	for name, to := range bad {
		if err := MoveTo(to); err == nil {
			t.Errorf("%s: %s was accepted", name, to)
		}
		if moving() {
			t.Errorf("%s: a move started", name)
		}
	}
	if Custom() {
		t.Error("the folder was saved")
	}
	for rel, body := range files {
		if read(t, filepath.Join(def, rel)) != body {
			t.Errorf("%s changed", rel)
		}
	}
}

// A picked folder that isn't there (its disk is out) is not made again by
// a download, and nothing starts while a move is under way.
func TestDownloadNeedsItsFolder(t *testing.T) {
	seed(t)
	gone := filepath.Join(t.TempDir(), "unplugged", "models")
	if err := settings.SetRedactModelDir(gone); err != nil {
		t.Fatal(err)
	}
	if err := Download(nil); err == nil || !strings.Contains(err.Error(), "isn't there") {
		t.Errorf("Download = %v", err)
	}
	if err := (guardModel{}).Download(nil); err == nil || !strings.Contains(err.Error(), "isn't there") {
		t.Errorf("guard Download = %v", err)
	}
	if _, err := os.Stat(filepath.Join(gone, "..")); !os.IsNotExist(err) {
		t.Errorf("the folder was made: %v", err)
	}
	mv.mu.Lock()
	mv.moving = true
	mv.mu.Unlock()
	defer func() { mv.mu.Lock(); mv.moving = false; mv.mu.Unlock() }()
	if err := Remove(); err != errMoving {
		t.Errorf("Remove = %v", err)
	}
	if err := (guardModel{}).Remove(); err != errMoving {
		t.Errorf("guard Remove = %v", err)
	}
}

// Moving to another disk: set PII_OTHER_DISK to a folder on one (the
// Windows box's D:), where the rename can't be used and the copy is.
func TestMoveAcrossDisks(t *testing.T) {
	other := os.Getenv("PII_OTHER_DISK")
	if other == "" {
		t.Skip("PII_OTHER_DISK names a folder on another disk")
	}
	files := seed(t)
	def := Root()
	to := filepath.Join(other, "magpie-models-test")
	t.Cleanup(func() { os.RemoveAll(to) })
	os.RemoveAll(to)
	if err := MoveTo(to); err != nil {
		t.Fatal(err)
	}
	if s := moved(t); s.Error != "" || s.Left != "" {
		t.Fatalf("move: %+v", s)
	}
	for rel, body := range files {
		if read(t, filepath.Join(to, rel)) != body {
			t.Errorf("%s changed", rel)
		}
		if _, err := os.Stat(filepath.Join(def, rel)); !os.IsNotExist(err) {
			t.Errorf("%s left behind", rel)
		}
	}
}
