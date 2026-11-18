package library

import "testing"

// TestCodeRabbitIsItsOwnRemote holds the featured CodeRabbit to the server
// CodeRabbit's docs give (docs.coderabbit.ai/api/mcp, niuniu_8964 on X):
// Streamable HTTP at mcp.coderabbit.ai, signed in in the browser with
// OAuth, and no API key asked for.
func TestCodeRabbitIsItsOwnRemote(t *testing.T) {
	m, ok := marketServer("coderabbit")
	if !ok {
		t.Fatal("no featured coderabbit")
	}
	if !m.Featured || !m.SignIn || m.Transport != "http" || m.server.URL != "https://mcp.coderabbit.ai/" {
		t.Fatalf("coderabbit: featured %v, signIn %v, %s %s", m.Featured, m.SignIn, m.Transport, m.server.URL)
	}
	if len(m.Inputs) != 0 {
		t.Fatalf("coderabbit asks for %v; its sign-in needs no key", m.Inputs)
	}
	if m.Homepage != "https://docs.coderabbit.ai/api/mcp" || m.Icon != gh("coderabbitai") {
		t.Fatalf("coderabbit homepage %q, icon %q", m.Homepage, m.Icon)
	}
}
