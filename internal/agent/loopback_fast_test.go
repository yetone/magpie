package agent

import (
	"net"
	"net/url"
	"testing"
)

// onAnotherMachine's quick look at a loopback base answers as url.Parse
// does, user info and odd ports included, and parses nothing for the
// gateway's own base, which a state poll asks of each agent.
func TestOnAnotherMachineQuickLookAgrees(t *testing.T) {
	slow := func(base string) bool {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return false
		}
		h := u.Hostname()
		if h == "localhost" {
			return false
		}
		ip := net.ParseIP(h)
		return ip == nil || !ip.IsLoopback() && !ip.IsUnspecified()
	}
	for _, b := range []string{
		"http://127.0.0.1:3425/v1", "https://127.0.0.1:3425", "http://localhost:3425/v1?x=1",
		"http://[::1]:3425/v1", "http://[::1]/v1", "http://127.0.0.1/v1#a", "http://127.0.0.1:/v1",
		"http://127.0.0.1:3425@evil.example/v1", "http://u:p@127.0.0.1:3425/v1",
		"http://127.0.0.1:34x5/v1", "http://LOCALHOST:3425", "http://127.0.0.2:1/v1",
		"http://0.0.0.0:1", "http://192.168.1.5:3425/v1", "https://api.example.com/v1",
		"ftp://127.0.0.1/", "127.0.0.1:3425", "http://", "", "http://[::1", "http://127.0.0.1%2f:1/",
	} {
		if got, want := onAnotherMachine(b), slow(b); got != want {
			t.Errorf("%q: %v, url.Parse says %v", b, got, want)
		}
	}
	if n := testing.AllocsPerRun(50, func() { onAnotherMachine("http://127.0.0.1:3425/v1") }); n != 0 {
		t.Errorf("the gateway's base: %.0f allocations, want none", n)
	}
}
