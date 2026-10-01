package gui

import (
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// A magpie that found the gateway taken serves it once the one that had it
// is gone (one left running from before an update, a magpie serve).
func TestGatewayTakenOver(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ADDR", ln.Addr().String())
	other := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"magpie"}`))
	})}
	go other.Serve(ln)
	defer served.Store(nil)
	if serveGateway() != nil {
		t.Fatal("served over another magpie")
	}
	old := gatewayWatch
	gatewayWatch = 50 * time.Millisecond
	defer func() { gatewayWatch = old }()
	go watchGateway()
	time.Sleep(200 * time.Millisecond)
	if served.Load() != nil {
		t.Fatal("took the gateway while the other had it")
	}
	other.Close()
	for deadline := time.Now().Add(5 * time.Second); served.Load() == nil || !gateway.Running(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the gateway wasn't taken up")
		}
	}
}
