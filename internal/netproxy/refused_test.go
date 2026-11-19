package netproxy

import (
	"strings"
	"testing"
)

// Windows says a refused connection in words of its own, and a proxy or
// SSH server with nothing listening was explained on macOS and Linux
// only: TestSSHProxySaysWhyItFailed was red on Windows.
func TestRefusedIsExplainedOnEverySystem(t *testing.T) {
	for _, msg := range []string{
		"proxyconnect tcp: dial tcp 127.0.0.1:7890: connect: connection refused",
		"proxyconnect tcp: dial tcp 127.0.0.1:7890: connectex: No connection could be made because the target machine actively refused it.",
	} {
		w := Explain("http://127.0.0.1:7890", msg)
		if w == nil || !strings.HasPrefix(w.Why, "nothing answers at {addr}") {
			t.Errorf("%q: %+v, want nothing answers", msg, w)
		}
	}
}
