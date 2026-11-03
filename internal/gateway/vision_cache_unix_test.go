//go:build !windows

package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSightAppendTightensExistingPermissions(t *testing.T) {
	fresh(t)
	s := New()
	if err := os.MkdirAll(filepath.Dir(s.sightFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.sightFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.sightFile, 0o644); err != nil {
		t.Fatal(err)
	}
	s.saveSight(fmt.Sprintf("%064x", 88), "private", "probe/eye")
	st, err := os.Stat(s.sightFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("cache mode = %04o, want 0600", got)
	}
}
