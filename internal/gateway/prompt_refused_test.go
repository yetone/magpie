package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// WorkBuddy's security policy turning away an agent's system prompt (Claude
// Code's own) "from an unapproved channel", whichever account it goes to:
// the next account is asked, and the one that answered so doesn't rest —
// before, lemon's two WorkBuddy AI accounts both rested over one agent's
// chat, for every agent (Discord). Told as the agent's prompt, not a
// failure of the account.
//
// The AutoClaw plugin's 406 over another agent's identity line in the
// system prompt (plugins#1094, ZCode's subagent line) is the same refusal
// in its own words, which the group's next member was never asked after.
func TestPromptRefusalRestsNobody(t *testing.T) {
	refusalOf := func(status int, said string) func(w http.ResponseWriter) {
		return func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, said)
		}
	}
	const autoclaw406 = `{"error":{"message":"406 Not Acceptable — AutoClaw refuses chats whose system prompt carries another agent's identity line (Claude Code, Claude Agent SDK, Codex, opencode, Cursor, Cline, Qwen Code, ZCode, Grok CLI, pi, DeepSeek Harness, Hermes Agent); this plugin rewrites the ones it knows, so an unknown one needs another provider in this group","type":"permission_error","code":null}}`
	for _, x := range []struct {
		name   string
		status int
		said   string
		word   string   // what the refusal passed on says
		also   []string // the second account refuses too
	}{
		{"next answers", 400, `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, "unapproved channel", nil},
		{"nobody left", 400, `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, "unapproved channel", []string{"acct-2"}},
		{"AutoClaw's 406, next answers", 406, autoclaw406, "identity line", nil},
		{"AutoClaw's 406, nobody left", 406, autoclaw406, "identity line", []string{"acct-2"}},
	} {
		refuse := refusalOf(x.status, x.said)
		t.Run(x.name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			if err := provider.SetRouting("codex", provider.Ordered); err != nil {
				t.Fatal(err)
			}
			var tried []string
			chatgptRefusing(t, &tried, refuse, x.also...)
			s := New()
			code, body := codexAskOn(s, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
			if strings.Join(tried, ",") != "acct-1,acct-2" {
				t.Fatalf("tried %v", tried)
			}
			r := lastRoute(s)
			if len(r.Tries) != 2 || r.Tries[0].Fail != failPrompt || r.Tries[0].Rest != nil {
				t.Fatalf("tries: %+v", r.Tries)
			}
			if x.also == nil {
				if code != 200 || !strings.Contains(body, "pong") || r.Tries[1].Status != 200 {
					t.Fatalf("status %d: %s", code, body)
				}
			} else if code != x.status || !strings.Contains(body, x.word) || r.Tries[1].Fail != failPrompt || r.Tries[1].Rest != nil {
				t.Fatalf("status %d: %s; tries %+v", code, body, r.Tries)
			}
			for _, c := range []string{"codex", "codex@me@example.com", "codex@spare@example.com"} {
				if _, ok := restOf(c); ok {
					t.Fatalf("%s set aside by a prompt the vendor turns away", c)
				}
			}
		})
	}
}
