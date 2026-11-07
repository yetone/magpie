package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// codexFollowSetup is Codex signed in to ChatGPT, its conversation put on
// a relay's model (third/gpt-6-astra) through magpie, and Codex's config
// naming no model for its subagents or memories unless set says so.
func codexFollowSetup(t *testing.T, set bool) (*chatgptSeen, *relayOfChatGPT, *Server) {
	t.Helper()
	up := codexSubagentSetup(t)
	relay := relayOn(t, "third")
	was := CodexSideSet
	CodexSideSet = func(string) bool { return set }
	t.Cleanup(func() { CodexSideSet = was })
	s := New()
	if rec := codexSubTurn(s, "third/gpt-6-astra", "lead-1", "", leadAsk); rec.Code != 200 {
		t.Fatalf("lead: %d %s", rec.Code, rec.Body.String())
	}
	return up, relay, s
}

// codexSide is one of Codex's side calls of kind on one of its own models.
func codexSide(t *testing.T, s *Server, kind, input string) {
	t.Helper()
	if rec := codexAsks(s, "gpt-5.5", kind, input); rec.Code != 200 {
		t.Fatalf("%s: %d %s", kind, rec.Code, rec.Body.String())
	}
}

// mengxiao on Discord: Codex's subagent model left "Same as the lead" in
// magpie, the user asked the lead for a subagent on GPT-6.1 Terra, and
// magpie put it on the lead's model all the same. Codex starts a spawn
// that names no model on its lead's, so a subagent asking for one of
// Codex's own models under a lead on magpie's was named that model: it
// goes on it, through the ChatGPT sign-in, and the trace marks no move.
func TestCodexSubagentKeepsTheModelItWasNamed(t *testing.T) {
	up, relay, s := codexFollowSetup(t, false)
	codexSide(t, s, "collab_spawn", plainTask)
	if got := up.asked(); len(got) != 1 || got[0] != "gpt-5.5" {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if reqs := relay.requests(); len(reqs) != 1 {
		t.Fatalf("the relay got %d requests: %v", len(reqs), reqs)
	}
	if r := lastRoute(s); r.Subagent != nil {
		t.Fatalf("trace: model %q subagent %+v", r.Model, r.Subagent)
	}
}

// Codex's memories, written on gpt-5.6-luna and -terra whatever the
// conversation is on, follow it too.
func TestCodexMemoriesFollowTheSession(t *testing.T) {
	up, relay, s := codexFollowSetup(t, false)
	codexSide(t, s, "memgen", leadAsk)
	if got := up.asked(); len(got) != 0 {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if reqs := relay.requests(); len(reqs) != 2 {
		t.Fatalf("the relay got %d requests", len(reqs))
	}
}

// A model Codex's config names for them is the user's own pick: the side
// calls go as Codex sent them.
func TestCodexSideCallsKeepTheConfigsModel(t *testing.T) {
	up, relay, s := codexFollowSetup(t, true)
	codexSide(t, s, "collab_spawn", plainTask)
	codexSide(t, s, "memgen", leadAsk)
	if got := up.asked(); len(got) != 2 || got[0] != "gpt-5.5" || got[1] != "gpt-5.5" {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if reqs := relay.requests(); len(reqs) != 1 {
		t.Fatalf("the relay got %d requests", len(reqs))
	}
}

// A conversation on Codex's own model, and a task the ChatGPT backend
// sealed (which opens on a ChatGPT account alone), stay on ChatGPT.
func TestCodexSideCallsStayOnChatGPT(t *testing.T) {
	up := codexSubagentSetup(t)
	relay := relayOn(t, "third")
	was := CodexSideSet
	CodexSideSet = func(string) bool { return false }
	t.Cleanup(func() { CodexSideSet = was })
	s := New()
	codexSide(t, s, "", leadAsk)
	codexSide(t, s, "collab_spawn", plainTask)
	if got := up.asked(); len(got) != 2 {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if rec := codexSubTurn(s, "third/gpt-6-astra", "lead-1", "", leadAsk); rec.Code != 200 {
		t.Fatalf("lead: %d", rec.Code)
	}
	codexSide(t, s, "collab_spawn", sealedTaskFernet)
	if got := up.asked(); len(got) != 3 || got[2] != "gpt-5.5" {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if reqs := relay.requests(); len(reqs) != 1 {
		t.Fatalf("the relay got %d requests", len(reqs))
	}
}

// codexAppSide is one of the Codex app's hidden side turns of a thread
// (source: thread_title, thread_description, composer_predictions), asked
// on one of Codex's own models as the app asks for them.
func codexAppSide(t *testing.T, s *Server, source, session string) {
	t.Helper()
	body := `{"model":"gpt-6-luna","stream":true,"input":[` + leadAsk + `]}`
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	req.Header.Set("session_id", session)
	req.Header.Set("x-codex-turn-metadata", `{"thread_source":"`+source+`"}`)
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
}

// cybercaius on Discord: with the conversation put on one of magpie's
// models in the Codex app, and nothing in Codex's config, the app's thread
// titles, descriptions and composer predictions still went to ChatGPT on
// its own models. They follow the session now, as the memories do.
func TestCodexAppSideTurnsFollowTheSession(t *testing.T) {
	up, relay, s := codexFollowSetup(t, false)
	was := CodexSideModel
	CodexSideModel = func() string { return "" }
	t.Cleanup(func() { CodexSideModel = was })
	for _, src := range []string{"composer_predictions", "thread_title", "thread_description"} {
		codexAppSide(t, s, src, "lead-1")
		if got := up.asked(); len(got) != 0 {
			t.Fatalf("%s: ChatGPT was asked for %v", src, got)
		}
		reqs := relay.requests()
		if m := modelOf([]byte(reqs[len(reqs)-1])); len(reqs) < 2 || m != "gpt-6-astra" {
			t.Fatalf("%s: the relay got %d requests, the last on %q", src, len(reqs), m)
		}
	}
	if reqs := relay.requests(); len(reqs) != 4 {
		t.Fatalf("the relay got %d requests", len(reqs))
	}
}

// cybercaius on Discord: after switching Codex to another model, the first
// new conversation's title still came from the model before. Codex writes
// it on a temporary thread with no link to the conversation, started as
// the first message is sent, so it can reach magpie before that
// conversation's first turn, when the key's last turn is still the old
// conversation's. It waits for the turn now, and goes on its model.
func TestCodexTitleBeforeTheFirstTurnFollowsTheSwitch(t *testing.T) {
	up, old, s := codexFollowSetup(t, false)
	switched := relayOn(t, "fourth")
	wasModel, wasFresh, wasWait := CodexSideModel, codexSideFresh, codexSideWait
	CodexSideModel = func() string { return "" }
	// the old conversation's turn is long past; the title waits on the
	// new one's, not on the clock
	codexSideFresh, codexSideWait = 0, time.Minute
	t.Cleanup(func() { CodexSideModel, codexSideFresh, codexSideWait = wasModel, wasFresh, wasWait })
	done := make(chan struct{})
	go func() {
		defer close(done)
		codexAppSide(t, s, "thread_title", "title-thread")
	}()
	// the new conversation's first turn comes once the title is waiting
	// (on the old code it never waits, and is answered first)
	for waiting := false; !waiting; {
		select {
		case <-done:
			waiting = true
		default:
			s.codexLeads.mu.Lock()
			waiting = s.codexLeads.next != nil
			s.codexLeads.mu.Unlock()
			if !waiting {
				time.Sleep(time.Millisecond)
			}
		}
	}
	if rec := codexSubTurn(s, "fourth/gpt-6-astra", "lead-2", "", leadAsk); rec.Code != 200 {
		t.Fatalf("new conversation: %d %s", rec.Code, rec.Body.String())
	}
	<-done
	if got := up.asked(); len(got) != 0 {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if reqs := old.requests(); len(reqs) != 1 {
		t.Fatalf("the model switched from got %d requests, the title among them", len(reqs))
	}
	if reqs := switched.requests(); len(reqs) != 2 {
		t.Fatalf("the model switched to got %d requests, want the turn and the title", len(reqs))
	}
}

// tenacious_eagle_96548 on Discord: Codex's config on one provider's model
// (third), the conversation put on another's (fourth) in the Codex app,
// and the first new conversation after magpie started had its title
// written by the config's model. Nothing had come through magpie yet, so
// the title, there before the conversation's first turn, took the config's
// model at once. It waits for the turn now, and goes on the model picked.
func TestCodexFirstTitleFollowsThePickNotTheConfig(t *testing.T) {
	up := codexSubagentSetup(t)
	config := relayOn(t, "third")
	picked := relayOn(t, "fourth")
	wasSet, wasModel, wasWait := CodexSideSet, CodexSideModel, codexSideWait
	CodexSideSet = func(string) bool { return false }
	CodexSideModel = func() string { return "third/gpt-6-astra" }
	codexSideWait = time.Minute
	t.Cleanup(func() { CodexSideSet, CodexSideModel, codexSideWait = wasSet, wasModel, wasWait })
	s := New()
	beforeTheTurn(t, s, func() { codexAppSide(t, s, "thread_title", "title-thread") }, func() {
		if rec := codexSubTurn(s, "fourth/gpt-6-astra", "lead-1", "", leadAsk); rec.Code != 200 {
			t.Errorf("the conversation: %d %s", rec.Code, rec.Body.String())
		}
	})
	if got := up.asked(); len(got) != 0 {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if reqs := config.requests(); len(reqs) != 0 {
		t.Fatalf("the config's model got %d requests, the title among them", len(reqs))
	}
	if reqs := picked.requests(); len(reqs) != 2 {
		t.Fatalf("the picked model got %d requests, want the turn and the title", len(reqs))
	}
}

// codexOwnCall is a request Codex with magpie as its provider sends to
// /v1/responses on model, in the thread session: a turn of its own
// (source "") or one of the app's side turns.
func codexOwnCall(t *testing.T, s *Server, model, session, source string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"`+model+`","stream":true,"input":[`+leadAsk+`]}`))
	req.Header.Set("Authorization", "Bearer "+TokenFor("codex"))
	req.Header.Set("session_id", session)
	if source != "" {
		req.Header.Set("x-codex-turn-metadata", `{"thread_source":"`+source+`"}`)
	}
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
}

// tenacious_eagle_96548 on Discord, with magpie as Codex's provider: the
// conversation on fourth/ picked in the app, its title on third/. The app
// asks for a new conversation's title on a model it chooses itself (the
// provider list's default, else its first), never the conversation's, so
// the model in the request is not the user's pick. Each of the app's side
// turns goes on the conversation's model, the title waiting for the turn
// it is sent beside.
func TestCodexOwnTitleFollowsThePickNotTheAppsChoice(t *testing.T) {
	fresh(t)
	appChose := relayOn(t, "third")
	picked := relayOn(t, "fourth")
	wasModel, wasWait := CodexSideModel, codexSideWait
	CodexSideModel = func() string { return "" }
	codexSideWait = time.Minute
	t.Cleanup(func() { CodexSideModel, codexSideWait = wasModel, wasWait })
	s := New()
	beforeTheTurn(t, s, func() { codexOwnCall(t, s, "third/gpt-6-astra", "title-thread", "thread_title") }, func() {
		codexOwnCall(t, s, "fourth/gpt-6-astra", "conv-1", "")
	})
	for _, src := range []string{"thread_description", "thread_summary"} {
		codexOwnCall(t, s, "third/gpt-6-astra", "conv-1", src)
	}
	if reqs := appChose.requests(); len(reqs) != 0 {
		t.Fatalf("the app's choice got %d requests", len(reqs))
	}
	if reqs := picked.requests(); len(reqs) != 4 {
		t.Fatalf("the picked model got %d requests, want the turn, the title, the description and the summary", len(reqs))
	}
}

// A composer prediction runs on a fork of the conversation, a thread of
// its own: it follows the conversation it was forked from, not whichever
// one this key's Codex last had a turn in.
func TestCodexPredictionFollowsTheThreadItForks(t *testing.T) {
	_, lead, s := codexFollowSetup(t, false)
	other := relayOn(t, "fourth")
	was := CodexSideModel
	CodexSideModel = func() string { return "" }
	t.Cleanup(func() { CodexSideModel = was })
	if rec := codexSubTurn(s, "fourth/gpt-6-astra", "lead-2", "", leadAsk); rec.Code != 200 {
		t.Fatalf("other conversation: %d %s", rec.Code, rec.Body.String())
	}
	body := `{"model":"gpt-6-luna","stream":true,"input":[` + leadAsk + `]}`
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	req.Header.Set("session_id", "fork-of-lead-1")
	req.Header.Set("x-codex-turn-metadata", `{"thread_source":"composer_predictions","forked_from_thread_id":"lead-1"}`)
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if reqs := lead.requests(); len(reqs) != 2 {
		t.Fatalf("the forked conversation's model got %d requests", len(reqs))
	}
	if reqs := other.requests(); len(reqs) != 1 {
		t.Fatalf("the other conversation's model got %d requests", len(reqs))
	}
}

// A title for a conversation magpie has seen a turn of goes on its model
// at once, however long ago that turn was, with no wait.
func TestCodexTitleOfAKnownThreadDoesNotWait(t *testing.T) {
	_, relay, s := codexFollowSetup(t, false)
	wasModel, wasFresh, wasWait := CodexSideModel, codexSideFresh, codexSideWait
	CodexSideModel = func() string { return "" }
	codexSideFresh, codexSideWait = 0, time.Minute
	t.Cleanup(func() { CodexSideModel, codexSideFresh, codexSideWait = wasModel, wasFresh, wasWait })
	start := time.Now()
	codexAppSide(t, s, "thread_title", "lead-1")
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("the title waited %v", d)
	}
	if reqs := relay.requests(); len(reqs) != 2 {
		t.Fatalf("the relay got %d requests", len(reqs))
	}
}

// A conversation on Codex's own model keeps its side turns on ChatGPT,
// even with one of magpie's models in Codex's config: they follow the
// session, not the config.
func TestCodexAppSideTurnsStayWithAnOwnModelSession(t *testing.T) {
	up := codexSubagentSetup(t)
	relay := relayOn(t, "third")
	was := CodexSideModel
	CodexSideModel = func() string { return "third/gpt-6-astra" }
	t.Cleanup(func() { CodexSideModel = was })
	s := New()
	codexSide(t, s, "", leadAsk)
	codexAppSide(t, s, "thread_title", "worker-1")
	if got := up.asked(); len(got) != 2 || got[1] != "gpt-6-luna" {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if reqs := relay.requests(); len(reqs) != 0 {
		t.Fatalf("the relay got %d requests", len(reqs))
	}
}

// Codex consolidates its memories as it starts, before any turn of its
// has come through magpie: with one of magpie's models in its config,
// they go on that model, not to ChatGPT.
func TestCodexMemoriesBeforeAnyTurnFollowTheConfig(t *testing.T) {
	up := codexSubagentSetup(t)
	relay := relayOn(t, "third")
	wasSet, wasModel := CodexSideSet, CodexSideModel
	CodexSideSet = func(string) bool { return false }
	CodexSideModel = func() string { return "third/gpt-6-astra" }
	t.Cleanup(func() { CodexSideSet, CodexSideModel = wasSet, wasModel })
	s := New()
	codexSide(t, s, "memgen", leadAsk)
	if got := up.asked(); len(got) != 0 {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if reqs := relay.requests(); len(reqs) != 1 || modelOf([]byte(reqs[0])) != "gpt-6-astra" {
		t.Fatalf("the relay got %v", reqs)
	}
}

// codexSideAsk is one of Codex's side calls on one of its own models, with
// the headers Codex sends for it.
func codexSideAsk(s *Server, model string, h map[string]string) {
	body := `{"model":"` + model + `","stream":true,"input":[` + leadAsk + `]}`
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	req.Header.Set("User-Agent", "codex_cli_rs/0.159.0 (Mac OS 26.2.0; arm64)")
	for k, v := range h {
		req.Header.Set(k, v)
	}
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
}

// beforeTheTurn runs side, then turn once side is waiting for a turn or has
// been answered (on the old code a memory call never waits, and is
// answered first), and waits for side to finish.
func beforeTheTurn(t *testing.T, s *Server, side, turn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		side()
	}()
	for waiting := false; !waiting; {
		select {
		case <-done:
			waiting = true
		default:
			s.codexLeads.mu.Lock()
			waiting = s.codexLeads.next != nil
			s.codexLeads.mu.Unlock()
			if !waiting {
				time.Sleep(time.Millisecond)
			}
		}
	}
	turn()
	<-done
}

// cybercaius on Discord: after switching Codex's model, does the first of
// each side call go on the new model, as the title does? Each kind, sent
// before the first turn on the new model reaches magpie: a new
// conversation's title and description (a temporary thread of the app's),
// its composer prediction (a fork of it), and its memories, which Codex
// starts with every turn of a conversation (phase 1 under the
// conversation's thread, phase 2 on a consolidation thread of its own).
// Each goes on the model switched to, none on the one before, none to
// ChatGPT.
func TestCodexSideCallsBeforeTheFirstTurnFollowTheSwitch(t *testing.T) {
	for _, c := range []struct {
		name string
		h    map[string]string
	}{
		{"title", map[string]string{"session_id": "title-thread", "x-codex-turn-metadata": `{"thread_source":"thread_title"}`}},
		{"description", map[string]string{"session_id": "desc-thread", "x-codex-turn-metadata": `{"thread_source":"thread_description"}`}},
		{"prediction", map[string]string{"session_id": "fork-of-lead-2", "x-codex-turn-metadata": `{"thread_source":"composer_predictions","forked_from_thread_id":"lead-2"}`}},
		{"memory extraction", map[string]string{"session_id": "lead-2", "x-codex-turn-metadata": `{"thread_source":"memory_consolidation","request_kind":"memory"}`}},
		{"memory consolidation", map[string]string{"session_id": "consolidation-thread", "x-openai-memgen-request": "true", "x-codex-turn-metadata": `{"thread_source":"memory_consolidation"}`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			up, old, s := codexFollowSetup(t, false)
			switched := relayOn(t, "fourth")
			wasModel, wasFresh, wasWait := CodexSideModel, codexSideFresh, codexSideWait
			CodexSideModel = func() string { return "" }
			codexSideFresh, codexSideWait = 0, time.Minute
			t.Cleanup(func() { CodexSideModel, codexSideFresh, codexSideWait = wasModel, wasFresh, wasWait })
			beforeTheTurn(t, s, func() { codexSideAsk(s, "gpt-5.6-luna", c.h) }, func() {
				if rec := codexSubTurn(s, "fourth/gpt-6-astra", "lead-2", "", leadAsk); rec.Code != 200 {
					t.Errorf("new conversation: %d %s", rec.Code, rec.Body.String())
				}
			})
			if got := up.asked(); len(got) != 0 {
				t.Fatalf("ChatGPT was asked for %v", got)
			}
			if reqs := old.requests(); len(reqs) != 1 {
				t.Fatalf("the model switched from got %d requests, the %s among them", len(reqs), c.name)
			}
			if reqs := switched.requests(); len(reqs) != 2 {
				t.Fatalf("the model switched to got %d requests, want the turn and the %s", len(reqs), c.name)
			}
		})
	}
}

// The model switched in a conversation already under way: Codex starts its
// memories with the conversation's next turn, under that thread, and the
// memory call that beats the turn to magpie goes on the new model, not on
// the thread's last. A composer prediction of that thread asked after the
// turn is on the new model too.
func TestCodexMemoryOfASwitchedConversationFollowsTheSwitch(t *testing.T) {
	up, old, s := codexFollowSetup(t, false)
	switched := relayOn(t, "fourth")
	wasModel, wasFresh, wasWait := CodexSideModel, codexSideFresh, codexSideWait
	CodexSideModel = func() string { return "" }
	codexSideFresh, codexSideWait = 0, time.Minute
	t.Cleanup(func() { CodexSideModel, codexSideFresh, codexSideWait = wasModel, wasFresh, wasWait })
	memory := map[string]string{"session_id": "lead-1", "x-codex-turn-metadata": `{"thread_source":"memory_consolidation","request_kind":"memory"}`}
	beforeTheTurn(t, s, func() { codexSideAsk(s, "gpt-5.6-luna", memory) }, func() {
		if rec := codexSubTurn(s, "fourth/gpt-6-astra", "lead-1", "", leadAsk); rec.Code != 200 {
			t.Errorf("the switched turn: %d %s", rec.Code, rec.Body.String())
		}
	})
	codexSideAsk(s, "gpt-6-luna", map[string]string{"session_id": "fork-of-lead-1", "x-codex-turn-metadata": `{"thread_source":"composer_predictions","forked_from_thread_id":"lead-1"}`})
	if got := up.asked(); len(got) != 0 {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if reqs := old.requests(); len(reqs) != 1 {
		t.Fatalf("the model switched from got %d requests", len(reqs))
	}
	if reqs := switched.requests(); len(reqs) != 3 {
		t.Fatalf("the model switched to got %d requests, want the turn, the memory and the prediction", len(reqs))
	}
}

// Codex with magpie as its provider asks for its memories on a model of
// its own by its bare name (codexMemoryStandIn): the one that beats the
// first turn after a switch goes on the model switched to, not on the
// last turn's.
func TestCodexMemoryStandInFollowsTheSwitch(t *testing.T) {
	fresh(t)
	old, switched := relayOn(t, "third"), relayOn(t, "fourth")
	wasFresh, wasWait := codexSideFresh, codexSideWait
	codexSideFresh, codexSideWait = 0, time.Minute
	t.Cleanup(func() { codexSideFresh, codexSideWait = wasFresh, wasWait })
	s := New()
	if rec := codexCall(t, s, "third/gpt-6-astra", ""); rec.Code != 200 {
		t.Fatalf("the old turn: %d %s", rec.Code, rec.Body.String())
	}
	beforeTheTurn(t, s, func() {
		if rec := codexCall(t, s, "gpt-5.6-luna", "memory_consolidation"); rec.Code != 200 {
			t.Errorf("memory: %d %s", rec.Code, rec.Body.String())
		}
	}, func() {
		if rec := codexCall(t, s, "fourth/gpt-6-astra", ""); rec.Code != 200 {
			t.Errorf("the switched turn: %d %s", rec.Code, rec.Body.String())
		}
	})
	if reqs := old.requests(); len(reqs) != 1 {
		t.Fatalf("the model switched from got %d requests", len(reqs))
	}
	if reqs := switched.requests(); len(reqs) != 2 {
		t.Fatalf("the model switched to got %d requests, want the turn and the memory", len(reqs))
	}
}
