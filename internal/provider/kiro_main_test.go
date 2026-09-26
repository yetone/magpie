package provider

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps the package's tests off the Kiro sign-ins of whoever runs
// them: Accounts() reads them, and asks Kiro who the account is.
func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "magpie-kiro")
	kiroCLIDB = func() string { return filepath.Join(dir, "kiro-cli", "data.sqlite3") }
	kiroIDEDir = func() string { return filepath.Join(dir, "sso") }
	askKiroIdentity = func(string) (string, string) { return "", "" }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
