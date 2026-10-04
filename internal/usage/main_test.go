package usage

import (
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// TestMain gives the package a home of its own (testenv): a test that
// doesn't set one reads and writes there, never in the runner's agents or,
// on Windows, its APPDATA and LOCALAPPDATA. The package's ledger reads what
// sessions finds, which asks each agent's own variable before its folder in
// the home: one left in the shell would point a test at the runner's real
// agent (#522), so testenv clears those too.
func TestMain(m *testing.M) { testenv.Main(m) }
