package netproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// A connection to an address on the user's own network that nobody holds
// any more (le13ure on X: a Remote magpie whose computer was given
// another address) is given up on after lanDial, not after the system's
// minute or two; a name, a public address and Tailscale's are left to
// the request's own context.
func TestLANDialGivesUp(t *testing.T) {
	old := lanDial
	lanDial = 20 * time.Millisecond
	t.Cleanup(func() { lanDial = old })
	tr := &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("no deadline: " + addr)
		}
		<-ctx.Done() // an address that never answers
		return nil, ctx.Err()
	}}
	lanDialCap(tr)
	for _, addr := range []string{"192.168.1.20:3425", "10.0.0.5:3425", "172.16.3.4:3425", "169.254.1.1:3425"} {
		if _, err := tr.DialContext(context.Background(), "tcp", addr); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: %v, want given up on", addr, err)
		}
	}
	for _, addr := range []string{"a-pc.local:3425", "8.8.8.8:443", "100.100.1.2:3425", "api.example.com:443"} {
		if _, err := tr.DialContext(context.Background(), "tcp", addr); err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: %v, want left to the request's context", addr, err)
		}
	}
}
