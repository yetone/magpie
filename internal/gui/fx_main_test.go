package gui

import (
	"net"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/fx"
)

// TestMain keeps the rate off the network: a state read with no rate cached
// asks for one behind it (#541), which would otherwise reach the real
// endpoint and land in whatever test runs next.
func TestMain(m *testing.M) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		addr := l.Addr().String()
		l.Close() // refused at once from here on
		fx.SetRateURL("http://" + addr + "/")
	}
	os.Exit(m.Run())
}
