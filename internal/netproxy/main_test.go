package netproxy

import (
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// The tests run in a home of their own, never the user's real files. A
// loopback proxy a test names is taken as running (listening) unless the
// test says it is down (downPort, TestClosedShellProxyGoesDirect): most
// name one at a port nothing listens on, as an address and nothing more.
func TestMain(m *testing.M) {
	dialShell = func(string) bool { return true }
	testenv.Main(m)
}
