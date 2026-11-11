//go:build !windows

package usage

import (
	"os"
	"path/filepath"
	"testing"
)

// A stat of the log that fails for any reason but its absence (a busy
// disk, a share that blinked; here a folder without search permission)
// says nothing of the log: the last good read stands, and the ledger
// doesn't read empty until the next good stat (f2039c13f, @simonzyzy).
// Once the log is gone for real, the read is dropped.
func TestUnreadableLogKeepsItsLastRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through folder permissions")
	}
	pageHome(t)
	historyLog(t, 3)
	warm := readLogSnapshot()
	if warm == nil || warm.info == nil || len(warm.blocks) == 0 {
		t.Fatalf("fixture not read: %+v", warm)
	}
	dir := filepath.Dir(Path())
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	if _, err := statLogFile(Path()); err == nil || os.IsNotExist(err) {
		t.Fatalf("fixture: stat said %v, not a failure other than absence", err)
	}
	if got := readLogSnapshot(); got != warm {
		t.Fatalf("a failed stat replaced the last read: %+v", got)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(Path()); err != nil {
		t.Fatal(err)
	}
	if got := readLogSnapshot(); got == warm || len(got.blocks) != 0 {
		t.Fatalf("a removed log kept its last read: %+v", got)
	}
}
