package usage

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

func TestSessionOfficialLoginRequiresMatchedAccount(t *testing.T) {
	r := &sessionResolver{
		identities: []provider.SessionIdentity{
			{Agent: "codex", AccountID: "a", UserID: "u", User: "codex@example.com", OfficialLogin: true},
			{Agent: "codex", AccountID: "unknown", UserID: "u", User: "codex@example.com"},
			{Agent: "claude", AccountID: "c", OrganizationID: "org", User: "claude@example.com", OfficialLogin: true},
		},
		bySession: map[string][]desktopSessionIdentity{
			"matched":    {{account: "c", org: "org", email: "claude@example.com"}},
			"other-org":  {{account: "c", org: "other", email: "claude@example.com"}},
			"email-only": {{email: "claude@example.com"}},
		},
	}
	for _, tc := range []struct {
		name string
		call sessions.Call
		want bool
	}{
		{"custom route does not negate account login", sessions.Call{Agent: "codex", Upstream: "custom", AccountID: "a", UserID: "u"}, true},
		{"different member", sessions.Call{Agent: "codex", Upstream: "openai", AccountID: "a", UserID: "other"}, false},
		{"unknown auth mode", sessions.Call{Agent: "codex", Upstream: "openai", AccountID: "unknown", UserID: "u"}, false},
		{"model and provider alone", sessions.Call{Agent: "codex", Model: "gpt-6-sol", Upstream: "openai"}, false},
		{"Claude exact OAuth identity", sessions.Call{Agent: "claude-desktop", Session: "matched"}, true},
		{"Claude Code exact OAuth identity", sessions.Call{Agent: "claude", Session: "matched"}, true},
		{"Claude other organization", sessions.Call{Agent: "claude-desktop", Session: "other-org"}, false},
		{"Claude email alone", sessions.Call{Agent: "claude-desktop", Session: "email-only"}, false},
		{"unsupported agent", sessions.Call{Agent: "opencode", Session: "matched", AccountID: "a", UserID: "u"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := r.resolve(tc.call)
			if got := r.officialLogin(tc.call, account); got != tc.want {
				t.Fatalf("official=%v, want %v (account=%q)", got, tc.want, account)
			}
		})
	}
}
