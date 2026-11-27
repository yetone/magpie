package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// A Claude subscription run that fails after Anthropic began its reply,
// its message_start's 90K written to the first account's cache, and the
// spare account answering the same request: the ledger has the first
// try's row too, with what it billed, marked as handed on, beside the
// request's own row. The Usage page counted the second alone, its "2
// tries" the only sign that the whole conversation went to two accounts
// (hhxk233 and Kayrainforest on X). A try that failed before anything was
// billed has its row too, with its status and no tokens (Dazzle_sys on X).
func TestClaudeSubscriptionFailedTryKeepsItsTokens(t *testing.T) {
	start := `    echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","model":"claude-sonnet-5","usage":{"input_tokens":3,"cache_creation_input_tokens":90000}}}}'
`
	failed := `    echo '{"type":"result","subtype":"success","is_error":true,"api_error_status":529,"result":"API Error: 529 overloaded"}'
`
	for _, billed := range []bool{true, false} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("billed=%v/stream=%v", billed, stream), func(t *testing.T) {
				first := failed
				if billed {
					first = start + failed
				}
				log := claudeSubscriptionFirstSays(t, first)
				s := New()
				body, _ := json.Marshal(map[string]any{"model": "claude/claude-sonnet-5", "max_tokens": 50, "stream": stream,
					"messages": []map[string]any{{"role": "user", "content": "ping"}}})
				code, reply := sendTo(s, "/v1/messages", string(body))
				b, _ := os.ReadFile(log)
				if code != 200 || !strings.Contains(reply, "pong") || strings.Join(strings.Fields(string(b)), ",") != "own,tok-spare" {
					t.Fatalf("status %d: %s (runs %q)", code, reply, b)
				}
				route := lastRoute(s).ID
				var rows []usage.Record
				deadline := time.Now().Add(10 * time.Second)
				for {
					rows = rows[:0]
					for _, u := range usage.Load(time.Time{}) {
						if u.RouteID == route {
							rows = append(rows, u)
						}
					}
					if len(rows) >= 1 && rows[len(rows)-1].ProviderAccount == "spare@example.com" || time.Now().After(deadline) {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				var answered, handed []usage.Record
				for _, u := range rows {
					if u.HandedOn {
						handed = append(handed, u)
					} else {
						answered = append(answered, u)
					}
				}
				if len(answered) != 1 || answered[0].ProviderAccount != "spare@example.com" || answered[0].Tries != 2 || answered[0].Status != 200 {
					t.Fatalf("the request's row: %+v", answered)
				}
				if !billed {
					if len(handed) != 1 || handed[0].ProviderAccount != "me@example.com" || handed[0].Status != 529 ||
						handed[0].Input+handed[0].Output+handed[0].CacheRead+handed[0].CacheWrite != 0 {
						t.Fatalf("the try that billed nothing: %+v", handed)
					}
					return
				}
				if len(handed) != 1 {
					t.Fatalf("handed-on rows: %+v", handed)
				}
				h := handed[0]
				if h.ProviderAccount != "me@example.com" || h.CacheWrite != 90000 || h.Input != 3 || h.Status != 529 || h.Tries != 0 {
					t.Fatalf("the first try's row: account %q, in %d, cache write %d, status %d, tries %d",
						h.ProviderAccount, h.Input, h.CacheWrite, h.Status, h.Tries)
				}
				// one request, its tokens both tries'
				tot := usage.NewPricer()(rows)
				if tot.Calls != 1 || tot.Errors != 0 || tot.Retried != 1 || tot.CacheWrite != 90000 {
					t.Fatalf("totals: %+v", tot)
				}
			})
		}
	}
}
