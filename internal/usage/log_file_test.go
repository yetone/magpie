package usage

import (
	"os"
	"testing"
)

func TestLogFileInfoPreservesIdentity(t *testing.T) {
	pageHome(t)
	historyLog(t, 3)
	first, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wrapped := logFileInfo{FileInfo: first}
	if !sameLogFile(wrapped, second) || !sameLogFile(second, wrapped) || !sameLogFile(wrapped, logFileInfo{FileInfo: second}) {
		t.Fatal("wrapped stat lost native file identity")
	}
	if sameLogFile(wrapped, other) || sameLogFile(wrapped, nil) {
		t.Fatal("different file treated as the same log")
	}
}
