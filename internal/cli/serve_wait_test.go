package cli

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// #4938: the service's magpie serve, started by launchd beside the app
// serving the gateway, ran the start-up moves, the plugin host and every
// account's model fetch, failed to bind, exited 1 and was started again
// 5s later, thousands of times. It now waits for the port before any of
// that, and serves once the app has let it go.
func TestServeWaitsForTheGatewayBeforeStartingUp(t *testing.T) {
	var asked atomic.Int32
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			asked.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"magpie","version":"0.1.2770","pid":4242,"serves":"app","window":true}`))
	}))
	defer app.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(app.URL, "http://"))

	wasEvery, wasStartUp := gatewayFreeEvery, startUp
	gatewayFreeEvery = 20 * time.Millisecond
	started := make(chan struct{})
	// the moves would be followed by serving: the test ends the run there
	startUp = func() {
		close(started)
		runtime.Goexit()
	}
	t.Cleanup(func() { gatewayFreeEvery, startUp = wasEvery, wasStartUp })

	ended := make(chan struct{})
	go func() {
		defer close(ended)
		_ = run([]string{"serve"})
	}()

	// the app is asked who serves, and asked again: magpie serve waits
	deadline := time.After(30 * time.Second)
	for asked.Load() < 3 {
		select {
		case <-started:
			t.Fatal("magpie serve ran the start-up moves while another magpie served the gateway")
		case <-deadline:
			t.Fatalf("the magpie serving was asked %d times in 30s", asked.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case <-started:
		t.Fatal("magpie serve ran the start-up moves while another magpie served the gateway")
	default:
	}

	// the app quits: the port is free, and magpie serve goes on to serve
	app.Close()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("magpie serve didn't start up once the port was free")
	}
	<-ended
}

// Only magpie serve for the gateway waits: --web shows the gateway another
// magpie serves, and a mistyped flag is refused at once, not after the
// app has quit.
func TestPlainServe(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{"--tls-cert", "c.pem", "--tls-key", "k.pem"}, true},
		{[]string{"--tls-cert=c.pem", "--tls-key=k.pem"}, true},
		{[]string{"--web"}, false},
		{[]string{"--web=127.0.0.1:3430"}, false},
		{[]string{"--tls-cert=c.pem", "--web"}, false},
		{[]string{"--lan"}, false},
		{[]string{"web"}, false},
	} {
		if got := plainServe(c.args); got != c.want {
			t.Errorf("plainServe(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}
