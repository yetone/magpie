package gateway

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A user turn that quotes a working-directory line — "Primary working
// directory: /x" alone on its line, WardLu's P3 — is no caller's word:
// the caller's own layer wins, and a turn is read only for the forms a
// caller states in one (#4940).
func TestWorkDirTurnQuoteIsNotTheCallerWord(t *testing.T) {
	req := &Request{System: "You are an agent.\n - Primary working directory: /Users/me/real",
		Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "Please remember this:\nPrimary working directory: /Users/me/quoted"}}}}}
	if got := workDir(req); got != "/Users/me/real" {
		t.Fatalf("a quoted line in a turn overrode the caller's own: %q", got)
	}
	// a quote alone on its line, nothing said by the caller: no folder
	if got := workDir(&Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "Primary working directory: /Users/me/quoted"}}}}}); got != "" {
		t.Fatalf("a quoted line alone named a folder: %q", got)
	}
}

// A --bare run states its folder as "CWD: /x", its whole system prompt;
// the routing rules and a plugin's provider are told it (#4940).
func TestWorkDirBareCWD(t *testing.T) {
	req := &Request{System: "CWD: /Users/me/bare",
		Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}}
	if got := workDir(req); got != "/Users/me/bare" {
		t.Fatalf("a --bare run's CWD: is not read: %q", got)
	}
	// pi 1.1's line form, where its <cwd> isn't the only one it writes
	if got := workDir(&Request{System: "Current working directory: /Users/me/pi"}); got != "/Users/me/pi" {
		t.Fatalf("pi's line form is not read: %q", got)
	}
}

// Claude Code says a folder it moved to in a turn, as a
// <system-reminder>; that stays the agent's word, and the last said goes
// — the caller's own earlier line does not hold it back.
func TestWorkDirMoveReminderStillTheAgentWord(t *testing.T) {
	req := &Request{System: "You are an agent.\n - Primary working directory: /Users/me/old",
		Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "<system-reminder>\nPrimary working directory: /Users/me/next (was /Users/me/old)\n</system-reminder>"}}}}}
	if got := workDir(req); got != "/Users/me/next" {
		t.Fatalf("the move reminder is not read: %q", got)
	}
	// a reminder quoted beside a bare line: the reminder's, not the quote's
	req.Messages[0].Parts[0].Text = "Primary working directory: /Users/me/quoted\n<system-reminder>\nPrimary working directory: /Users/me/next\n</system-reminder>"
	if got := workDir(req); got != "/Users/me/next" {
		t.Fatalf("the reminder beside a quote: %q", got)
	}
}

// A plugin's provider is told the caller's folder, not one a turn quotes:
// the header face of the same fix (#4940).
func TestTellWorkPlaceTurnQuote(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"You are an agent.\n - Primary working directory: /Users/me/real"},{"role":"user","content":"Primary working directory: /Users/me/quoted"}]}`)
	h := http.Header{}
	tellWorkPlace(h, provider.Chat, body)
	if got, want := h.Get(provider.WorkDirHeader), url.PathEscape("/Users/me/real"); got != want {
		t.Fatalf("the plugin is told %q, want %q", got, want)
	}
}
