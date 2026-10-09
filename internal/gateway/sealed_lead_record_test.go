package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// fernetSealed is a payload shaped as the ChatGPT backend seals one: a
// Fernet token, version byte 0x80 and a timestamp, base64url.
const fernetSealed = "gAAAAABpAbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJ=="

// overloadingRelay is a Responses provider in front of the ChatGPT backend
// whose reply, while overload is set, breaks off after it began: a 200,
// some text, then response.failed with the backend's overload.
type overloadingRelay struct {
	mu       sync.Mutex
	got      []string
	overload bool
}

func (f *overloadingRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = append(f.got, string(b))
	overload := f.overload
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	if overload {
		io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":"thinking"}`,
			`data: {"type":"response.failed","response":{"id":"r2","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`))
		return
	}
	io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":"on it"}`,
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
}

func (f *overloadingRelay) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func (f *overloadingRelay) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.got[len(f.got)-1]
}

func (f *overloadingRelay) setOverload(on bool) {
	f.mu.Lock()
	f.overload = on
	f.mu.Unlock()
}

func overloadingRelayOn(t *testing.T, id string) *overloadingRelay {
	t.Helper()
	f := &overloadingRelay{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "sk-" + id, Models: []string{"gpt-6-astra"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	return f
}

var (
	sealedTaskFernet  = strings.Replace(sealedHandoff, "gAAAAATest_ciphertext==", fernetSealed, 1)
	sealedReplyFernet = strings.Replace(sealedReply, "gAAAAAReply_ciphertext==", fernetSealed, 1)
)

// forgetSticks is what a day and more, or a restart past the 512
// conversations affinity.json keeps, leaves of who answered: nothing.
func forgetSticks(t *testing.T) {
	t.Helper()
	sticks.Lock()
	sticks.m = map[string]stick{}
	sticks.Unlock()
	if err := os.Remove(sticksPath()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// #1367 (Autsunset): Codex with the lead and its subagents all on
// xxxx/gpt-6-astra, a relay of ChatGPT Pro. The lead's turn broke off as
// the backend was overloaded, and the conversation was no longer
// remembered as answered by xxxx, so its next turn, carrying its subagent's
// sealed reply, and the subagent's own next turn, were refused as "sealed
// by the ChatGPT backend … xxxx/gpt-6-astra is neither" before xxxx was
// asked. Refused before anyone is asked, no turn could answer it again:
// the task stayed stuck.
func TestSealedTaskStillGoesToTheLeadsProviderAfterItsReplyBrokeOff(t *testing.T) {
	fresh(t)
	relay := overloadingRelayOn(t, "xxxx")
	s := New()
	if rec := codexSubTurn(s, "xxxx/gpt-6-astra", "lead-1", "", leadAsk); rec.Code != 200 {
		t.Fatalf("lead: %d %s", rec.Code, rec.Body.String())
	}
	if rec := codexSubTurn(s, "xxxx/gpt-6-astra", "worker-1", "lead-1", sealedTaskFernet); rec.Code != 200 {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	relay.setOverload(true)
	codexSubTurn(s, "xxxx/gpt-6-astra", "lead-1", "", leadAsk)
	codexSubTurn(s, "xxxx/gpt-6-astra", "worker-1", "lead-1", sealedTaskFernet)
	relay.setOverload(false)
	asked := relay.requests()
	rec := codexSubTurn(s, "xxxx/gpt-6-astra", "lead-1", "", leadAsk+","+sealedReplyFernet)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "on it") {
		t.Fatalf("lead after it broke off: %d %s", rec.Code, rec.Body.String())
	}
	if relay.requests() != asked+1 || !strings.Contains(relay.last(), fernetSealed) {
		t.Fatalf("xxxx was asked %d more times; last: %s", relay.requests()-asked, relay.last())
	}
}

// The same, with who answered the lead forgotten as a day and more, or a
// restart past the conversations affinity.json keeps, forgets it:
// continuing the task later still finds the provider in the usage log.
func TestSealedTaskStillGoesToTheLeadsProviderADayLater(t *testing.T) {
	fresh(t)
	relay := overloadingRelayOn(t, "xxxx")
	s := New()
	if rec := codexSubTurn(s, "xxxx/gpt-6-astra", "lead-1", "", leadAsk); rec.Code != 200 {
		t.Fatalf("lead: %d %s", rec.Code, rec.Body.String())
	}
	if rec := codexSubTurn(s, "xxxx/gpt-6-astra", "worker-1", "lead-1", sealedTaskFernet); rec.Code != 200 {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	forgetSticks(t)
	s = New()
	if rec := codexSubTurn(s, "xxxx/gpt-6-astra", "worker-1", "lead-1", sealedTaskFernet); rec.Code != 200 {
		t.Fatalf("subagent continued: %d %s", rec.Code, rec.Body.String())
	}
	forgetSticks(t)
	if rec := codexSubTurn(s, "xxxx/gpt-6-astra", "lead-1", "", leadAsk+","+sealedReplyFernet); rec.Code != 200 {
		t.Fatalf("lead continued: %d %s", rec.Code, rec.Body.String())
	}
	if n := relay.requests(); n != 4 || !strings.Contains(relay.last(), fernetSealed) {
		t.Fatalf("xxxx asked %d times; last: %s", n, relay.last())
	}
}

// What the log tells is still only who answered: a lead another provider
// answered, or one magpie refused and never asked anyone for, doesn't
// make this one able to read its seal; and the refusal says so, naming
// the provider when it is known.
func TestSealedTaskLogDoesNotOpenItToAnotherProvider(t *testing.T) {
	fresh(t)
	xxxx := overloadingRelayOn(t, "xxxx")
	other := overloadingRelayOn(t, "other")
	s := New()
	if rec := codexSubTurn(s, "other/gpt-6-astra", "lead-1", "", leadAsk); rec.Code != 200 {
		t.Fatalf("lead: %d %s", rec.Code, rec.Body.String())
	}
	forgetSticks(t)
	rec := codexSubTurn(s, "xxxx/gpt-6-astra", "worker-1", "lead-1", sealedTaskFernet)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "(other)") || strings.Contains(rec.Body.String(), "gAAAA") {
		t.Fatalf("subagent on another provider: %d %s", rec.Code, rec.Body.String())
	}
	// refused twice: the refusal itself is no answer by xxxx
	for range 2 {
		rec = codexSubTurn(s, "xxxx/gpt-6-astra", "worker-2", "lead-never", sealedTaskFernet)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "xxxx/gpt-6-astra is neither") {
			t.Fatalf("unknown lead: %d %s", rec.Code, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), "no record of it answering the lead in the last 30 days") || !strings.Contains(rec.Body.String(), "spawn the subagent again") {
		t.Fatalf("unknown lead, the way on not said: %s", rec.Body.String())
	}
	// an answer older than the log is read back for counts for nothing
	usage.Append(usage.Record{Time: time.Now().Add(-sealerKeep - time.Hour), Agent: "codex", Provider: "xxxx", Model: "gpt-6-astra", Status: 200, Session: "lead-old", NativeSession: "lead-old"})
	rec = codexSubTurn(s, "xxxx/gpt-6-astra", "worker-3", "lead-old", sealedTaskFernet)
	if rec.Code != 400 {
		t.Fatalf("lead answered %v ago: %d %s", sealerKeep+time.Hour, rec.Code, rec.Body.String())
	}
	if xxxx.requests() != 0 || other.requests() != 1 {
		t.Fatalf("xxxx asked %d times, other %d", xxxx.requests(), other.requests())
	}
}

// #1367's sibling in a group: the lead's ChatGPT account is put first for
// its subagent's sealed task (#619) by who affinity remembers answered the
// lead; with that forgotten, the usage log still names the account, and
// the task doesn't go to the group's other account first.
func TestSealedTaskGoesToTheLeadsLoggedAccountFirst(t *testing.T) {
	var tried []string
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		tried = append(tried, r.Header.Get("chatgpt-account-id"))
		io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":"on it"}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
	})
	codexSignedIn(t, "spare@example.com")
	forgetSticks(t)
	grok := &scripted{replies: []reply{{200, "text/event-stream", grokAnswer}}}
	scriptedOn(t, "xai", provider.Chat, grok)
	refusalGroup(t, "xai/m", "codex/gpt-5.5")
	ask := func(s *Server, session, parent, pin, input string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"group/g","stream":true,"input":[`+input+`]}`))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		req.Header.Set("session_id", session)
		if parent != "" {
			req.Header.Set("x-codex-parent-thread-id", parent)
		}
		if pin != "" {
			req.Header.Set(AccountHeader, pin)
		}
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := ask(New(), "lead-1", "", "spare@example.com", leadAsk); rec.Code != 200 || strings.Join(tried, ",") != "acct-2" {
		t.Fatalf("lead: %d %s (tried %v)", rec.Code, rec.Body.String(), tried)
	}
	forgetSticks(t)
	tried = nil
	rec := ask(New(), "worker-1", "lead-1", "", sealedTaskFernet)
	if rec.Code != 200 || grok.n != 0 || strings.Join(tried, ",") != "acct-2" {
		t.Fatalf("subagent: %d %s, grok asked %d times, accounts tried %v", rec.Code, rec.Body.String(), grok.n, tried)
	}
}
