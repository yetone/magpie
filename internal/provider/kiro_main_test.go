package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// TestMain keeps the package's tests off the Kiro sign-ins of whoever runs
// them: Accounts() reads them, and asks Kiro who the account is. It also
// gives the package a home of its own (testenv), so what a test leaves
// behind it — a CLI's answer kept by refresh after the test is over —
// never lands in the runner's magpie, nor in APPDATA on Windows. And it
// keeps them off the network (testenv.Offline): a test of a sign-in or a
// key at a vendor's real address asks only what it serves on loopback.
func TestMain(m *testing.M) {
	os.Exit(testenv.RunIn(m, func(home string) {
		testenv.Offline()
		kiroCLIDB = func() string { return filepath.Join(home, "kiro-test", "kiro-cli", "data.sqlite3") }
		kiroIDEDir = func() string { return filepath.Join(home, "kiro-test", "sso") }
		askKiroIdentity = func(string, string) (string, string) { return "", "" }
	}))
}
