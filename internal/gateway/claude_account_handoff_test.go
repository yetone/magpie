package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Tool results identify a waiting process, but the router may have picked
// another account since that process made its call. The selected account
// must answer, including when the same account moved out of the agent's home.
// A fork sub-agent answered on another account leaves the lead's run waiting
// on the run's account for the lead's own results (recih, #7852).
func TestClaudeToolResultsKeepTheSelectedAccount(t *testing.T) {
	for _, ids := range []string{"exact", "remapped"} {
		// "named since": the run started on Claude Code's own sign-in before
		// it had a name (the first run on a new account writes the profile
		// that names it), and its tool results come back under that name.
		// "unnamed, another account": no such rename covers a saved account.
		for _, mode := range []string{"same owner", "another account", "another home", "named since", "unnamed, another account"} {
			t.Run(ids+"/"+mode, func(t *testing.T) {
				claudeMadeFirst(t, false)
				h := newRemapHarness(t)
				received := make(chan bool, 1)
				h.first = func(run *subscriptionRun, req *Request) {
					o := "claude\x00a@example.com\x00" + ownHome
					if strings.HasPrefix(mode, "named since") || strings.HasPrefix(mode, "unnamed") {
						o = "claude\x00Claude Max\x00" + ownHome + "\x00" + unnamedOwner
					}
					run.owner.Store(&o)
					waiters := h.calls(run, true, shownCall{"call-a", "read", `{}`})
					go func() {
						_, ok := <-waiters[0]
						if ok {
							h.says(run, "kept run")
						}
						received <- ok
					}()
				}
				h.ask(remapUser("read"))
				p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "a@example.com"}}
				want := "kept run"
				if mode == "unnamed, another account" {
					want = "tok-b turn 1"
				}
				if mode == "another account" || mode == "unnamed, another account" {
					for _, saved := range p.AlsoOn() {
						if saved.Account.User == "b@example.com" {
							p = saved
							break
						}
					}
					if p.Account.User != "b@example.com" {
						t.Fatal("saved B is missing")
					}
					want = "tok-b turn 1"
				} else if mode == "another home" {
					if err := provider.SwitchLogin("claude", "b@example.com"); err != nil {
						t.Fatal(err)
					}
					b := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "b@example.com"}}
					for _, saved := range b.AlsoOn() {
						if saved.Account.User == "a@example.com" {
							p = saved
							break
						}
					}
					if p.Account.AgentsOwn() {
						t.Fatal("A did not move to its saved home")
					}
					want = "tok-a turn 1"
				}
				callID := "call-a"
				if ids == "remapped" {
					callID = "client-a"
				}
				body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + remapUser("read") + `,` + remapCalls(callID+"={}") + `,` + remapResults(callID+"=result") + `]}`
				rec := httptest.NewRecorder()
				code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &Usage{})
				if code != 200 {
					t.Fatalf("%d %s", code, msg)
				}
				var reply struct{ Content []struct{ Text string } }
				if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || len(reply.Content) != 1 || reply.Content[0].Text != want {
					t.Fatalf("selected account replied %s; want %q (decode: %v)", rec.Body, want, err)
				}
				parentContinues := mode == "another account" || mode == "unnamed, another account"
				if parentContinues {
					// The child answered on B. Deliver the parent's own result
					// on A and require its original waiting process to answer.
					parent := h.started[0]
					parentBody := strings.Replace(body, `"content":"result"`, `"content":"parent result"`, 1)
					rec := httptest.NewRecorder()
					code, msg := h.s.serveSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(parentBody)), provider.Anthropic, "Claude Code", "claude-sonnet-5", parent.owned(), []byte(parentBody), &Usage{}, h.start)
					if code != 200 || !strings.Contains(rec.Body.String(), "kept run") || h.runs() != 1 {
						t.Fatalf("parent did not resume its original run: %d %s %s", code, msg, rec.Body)
					}
				}
				select {
				case got := <-received:
					if got != (parentContinues || mode == "same owner" || mode == "named since") {
						t.Fatalf("old process received the result: %v, mode %s", got, mode)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("old process was neither resumed nor released")
				}
			})
		}
	}
}

// Only a run started on Claude Code's own sign-in while it had no name
// takes a new owner, and only that sign-in named: a different account never
// takes the waiting process.
func TestClaudeRunTakesOnlyItsOwnSignInsName(t *testing.T) {
	unnamed := "claude\x00Claude Max\x00" + ownHome + "\x00" + unnamedOwner
	named := "claude\x00a@example.com\x00" + ownHome
	for _, c := range []struct {
		was, now string
		takes    bool
	}{
		{unnamed, named, true},
		{named, "claude\x00b@example.com\x00" + ownHome, false},
		{unnamed, "claude\x00a@example.com", false},
		{unnamed, "other\x00a@example.com\x00" + ownHome, false},
		{"claude\x00Claude Max\x00" + ownHome, named, false},
	} {
		r := &subscriptionRun{}
		was := c.was
		r.owner.Store(&was)
		if got := r.takeNamedOwner(c.now); got != c.takes {
			t.Fatalf("%q -> %q: took %v, want %v", c.was, c.now, got, c.takes)
		}
		want := c.was
		if c.takes {
			want = c.now
		}
		if r.owned() != want {
			t.Fatalf("%q -> %q: owner %q", c.was, c.now, r.owned())
		}
	}
}

// turnWriter is a run's stdin that has its agent answer the next turn it
// is told.
type turnWriter struct {
	once *sync.Once
	turn func()
}

func (w turnWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { go w.turn() })
	return len(p), nil
}

func (turnWriter) Close() error { return nil }

// A run begun on Claude Code's own sign-in before it had a name, whose
// first turn ends without a tool call, waits for the next turn under the
// name that sign-in has since: the next turn comes by it.
func TestClaudeIdleRunKeptUnderItsOwnSignInsName(t *testing.T) {
	for _, named := range []bool{true, false} {
		t.Run(map[bool]string{true: "named since", false: "still unnamed"}[named], func(t *testing.T) {
			claudeMadeFirst(t, false)
			h := newRemapHarness(t)
			old := claudeOwnName
			claudeOwnName = func() (string, bool) {
				if named {
					return "a@example.com", true
				}
				return "", false
			}
			t.Cleanup(func() { claudeOwnName = old })
			h.first = func(run *subscriptionRun, req *Request) {
				o := "claude\x00Claude Max\x00" + ownHome + "\x00" + unnamedOwner
				run.owner.Store(&o)
				run.stdin = turnWriter{once: &sync.Once{}, turn: func() { h.says(run, "kept run") }}
				run.timer = time.AfterFunc(time.Hour, func() { run.abort() })
				h.says(run, "hello")
			}
			if got := h.ask(remapUser("hi")); len(got) != 1 || got[0] != "text hello" {
				t.Fatalf("first turn: %v", got)
			}
			p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "a@example.com"}}
			body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + remapUser("hi") + `,{"role":"assistant","content":"hello"},` + remapUser("next") + `]}`
			rec := httptest.NewRecorder()
			code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &Usage{})
			if code != 200 {
				t.Fatalf("%d %s", code, msg)
			}
			var reply struct{ Content []struct{ Text string } }
			json.Unmarshal(rec.Body.Bytes(), &reply)
			want := "kept run"
			if !named {
				want = "tok-a turn 1"
			}
			if len(reply.Content) != 1 || reply.Content[0].Text != want {
				t.Fatalf("next turn replied %s; want %q", rec.Body, want)
			}
		})
	}
}
