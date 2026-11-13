package library

import (
	"os"
	"strings"
	"testing"
)

// Codex keeps its session across a magpie restart (#6392, Norethcy on X):
// the first call magpie sees is a tools/call under a session id it never
// started, so the server was initialized as "magpie" and Magpie Memory
// couldn't tell which agent saved. The route is Codex's alone, so the
// server is started as Codex.
func TestRunSessionFromBeforeARestartIsCodex(t *testing.T) {
	srv, logf, _ := runFixture(t)
	call(t, srv, "magpie-before-restart", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"}}`)
	if got := startNames(logf); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("started as %v, want codex", got)
	}
}

// An initialize that names no client is Codex's too; one that names one
// keeps its name.
func TestRunInitializeNamesCodexWhenNoneGiven(t *testing.T) {
	srv, logf, _ := runFixture(t)
	_, a := call(t, srv, "", `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`)
	call(t, srv, a, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"}}`)
	if got := startNames(logf); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("started as %v, want codex", got)
	}
}

// startNames are the client names the fake server was started as.
func startNames(logf string) []string {
	b, _ := os.ReadFile(logf)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			out = append(out, f[1])
		}
	}
	return out
}
