package library

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
)

// Xichun123 (#1619): Figma's remote MCP server (mcp.figma.com/mcp) admits
// only the clients it lists, and its authorization server answers magpie's
// dynamic client registration 403 with the bare body "Forbidden". The
// Library's Sign in said "registering magpie with the server's sign-in
// failed (403): Forbidden", which reads as a fault to retry. It now says the
// server admits only its listed apps and that each agent signs in itself,
// and the agents given the server keep its own address, where they do.
func TestSignInRefusedByAnAllowListLeavesItToTheAgents(t *testing.T) {
	sandbox(t)
	f := mcpauthtest.New(t)
	f.RefuseRegistration = true
	agents := []string{"claude", "codex", "cursor"}
	figma := Server{Name: "figma", Transport: "http", URL: f.URL, Agents: agents}
	ok(t)(SaveServer("", figma))

	_, err := SignInServer(t.Context(), "figma")
	if !errors.Is(err, mcpauth.ErrClientRefused) {
		t.Fatalf("sign-in error %v, want the server admitting only its listed apps", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "admits only the apps it lists") || !strings.Contains(msg, "/mcp") || strings.Contains(msg, "(403)") {
		t.Fatalf("message %q", msg)
	}
	if mcpauth.SignedIn("figma", f.URL) {
		t.Fatal("signed in after a refused registration")
	}
	for _, id := range agents {
		got, err := targetByID(id).MCP.read()
		if err != nil {
			t.Fatal(err)
		}
		if s := got["figma"]; s == nil || !s.same(&figma) {
			t.Fatalf("%s isn't given the server's own address to sign in at: %+v", id, s)
		}
	}
}

// RockyShowlle on X: with the MCP router on, Figma's remote server went
// behind the router like any other, and the router reaches a server with
// magpie's sign-in, which Figma refuses. The agents had only the router's
// entry, so Figma couldn't be reached from any of them. A refused sign-in
// now keeps the server out of the router (direct), so each agent is given
// its own entry at the server's address and signs in there itself.
func TestSignInRefusedTakesItOutOfTheRouter(t *testing.T) {
	hubSandbox(t)
	f := mcpauthtest.New(t)
	f.RefuseRegistration = true
	ok(t)(SetMCPHub(true))
	agents := []string{"claude", "codex"}
	figma := Server{Name: "figma", Transport: "http", URL: f.URL, Agents: agents}
	ok(t)(SaveServer("", figma))
	given := func(id string) []string {
		t.Helper()
		e, err := targetByID(id).MCP.entries()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for k := range e {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	for _, id := range agents {
		if got := given(id); !slices.Equal(got, []string{HubEntry}) {
			t.Fatalf("%s before the sign-in: %v, want the router's entry alone", id, got)
		}
	}

	if _, err := SignInServer(t.Context(), "figma"); !errors.Is(err, mcpauth.ErrClientRefused) || !strings.Contains(err.Error(), "router") {
		t.Fatalf("sign-in error %v", err)
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if h := l.server("figma").Hub; h != HubDirect {
		t.Fatalf("Hub %q after a refused sign-in, want direct", h)
	}
	for _, id := range agents {
		got, err := targetByID(id).MCP.read()
		if err != nil {
			t.Fatal(err)
		}
		if s := got["figma"]; s == nil || !s.same(&figma) {
			t.Fatalf("%s isn't given the server's own address with the router on: %+v (%v)", id, s, given(id))
		}
	}
	for _, id := range []string{"claude", ""} {
		if slices.ContainsFunc(hubServers(id), func(s *Server) bool { return s.Name == "figma" }) {
			t.Fatalf("the router still serves figma to %q", id)
		}
	}
}

// Figma's refusal names the way that works in any agent: Figma Desktop,
// the market's server for Figma's desktop app. Another server's doesn't.
func TestFigmaRefusalNamesFigmaDesktop(t *testing.T) {
	sandbox(t)
	for _, u := range []string{"https://mcp.figma.com/mcp", "https://MCP.figma.com/mcp?x=1"} {
		err := refusedAt(&Server{Name: "figma", URL: u, Hub: HubDirect}, mcpauth.ErrClientRefused)
		if !errors.Is(err, mcpauth.ErrClientRefused) || !strings.Contains(err.Error(), "Figma Desktop") {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if err := refusedAt(&Server{Name: "x", URL: "https://mcp.example.com/mcp", Hub: HubDirect}, mcpauth.ErrClientRefused); err != mcpauth.ErrClientRefused {
		t.Fatalf("another server: %v", err)
	}
	m, found := marketServer("figma-desktop")
	if !found || m.server.URL != "http://127.0.0.1:3845/mcp" || m.server.Transport != "http" || m.SignIn {
		t.Fatalf("Figma Desktop in the market: %v %+v", found, m)
	}
}
