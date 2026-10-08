package tui

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// The tests run in a home of their own, never the user's real files, and in
// UTC, the zone the sessions fixtures' days are counted in. The zone is set
// here, before any test starts a goroutine: a test that swapped it would race
// the gateway's loops an earlier test left running, which read time.Local.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	testenv.Main(m)
}
