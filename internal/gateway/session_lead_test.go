package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/routes"
)

// #5655 (jerrychoi; iamnil on Discord): two accounts, one session. Claude
// Desktop's own Sonnet calls beside an Opus chat were routed as a session
// of their own, their model's conversations apart from the chat's: put
// on the account the fewest others held, they ran on account B while the
// chat was on A, and B's 5 hours drained on calls the user never made
// there. A session's call to another model now goes to the account its
// main conversation is on (withSession).
func TestSessionSideCallGoesWithItsMainAccount(t *testing.T) {
	fresh(t)
	up := &keyed{}
	serveOn(t, "sl", "k1", []string{"m1", "m2"}, up, "k2")
	if err := provider.SetAffinity("sl", provider.AffinitySession); err != nil {
		t.Fatal(err)
	}
	routes.Forget("sl")
	s := New()
	post := func(session, model, msgs string) string {
		t.Helper()
		n := len(up.tried)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"sl/`+model+`","messages":[`+msgs+`]}`))
		req.Header.Set("x-session-id", session)
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || len(up.tried) != n+1 {
			t.Fatalf("%s %s: %d %s, tried %v", session, model, rec.Code, rec.Body.String(), up.tried[n:])
		}
		return up.tried[n]
	}
	// another session holds the first account on m2
	other := post("s0", "m2", `{"role":"user","content":"another session's call"}`)
	// the session's chat, on m1
	main := post("s1", "m1", `{"role":"user","content":"the chat"}`)
	if main != other {
		t.Fatalf("setup: the chat went to %s, the other session's call to %s; both were to be on the first", main, other)
	}
	// its own call on m2: on the chat's account, not the one m2's fewest
	// sessions are on
	if k := post("s1", "m2", `{"role":"user","content":"a call of the session's own"}`); k != main {
		t.Errorf("the session's m2 call went to %s while its chat is on %s", k, main)
	}
	// the chat never follows its smaller side call
	if k := post("s1", "m1", `{"role":"user","content":"the chat"},{"role":"assistant","content":"ok"},{"role":"user","content":"more"}`); k != main {
		t.Errorf("the chat's next turn went to %s, not %s", k, main)
	}
}

// A side call cached on another account than its session's main
// conversation goes to the main's at its next turn, and says so; mid-turn
// it stays on the reasoning it sealed, Strict keeps it, and a main
// conversation no larger than its own leads nothing.
func TestSessionSideCallFollowsItsMain(t *testing.T) {
	key := func(k string) candidate {
		return candidate{p: provider.Provider{ID: "sx", Key: k}, model: "m2", rest: "sx#" + k}
	}
	h := http.Header{}
	h.Set("x-claude-code-session-id", "session-lead")
	turn := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	within := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`)
	set := func(t *testing.T, side, main int) {
		t.Helper()
		cs := []candidate{key("k1"), key("k2")}
		sticks.Lock()
		sticks.m["sx/m2|session-lead"] = stick{rest: cs[1].rest, who: cs[1].who(), model: "m2", turn: 1, at: time.Now(), cacheRead: side}
		sticks.m["sx/m1|session-lead"] = stick{rest: cs[0].rest, who: cs[0].who(), model: "m1", turn: 1, at: time.Now(), cacheRead: main}
		sticks.Unlock()
		t.Cleanup(func() {
			sticks.Lock()
			delete(sticks.m, "sx/m2|session-lead")
			delete(sticks.m, "sx/m1|session-lead")
			sticks.Unlock()
		})
	}
	run := func(mode string, body []byte) ([]candidate, *Affinity) {
		cs := []candidate{key("k1"), key("k2")}
		got, _, aff, _ := affine("sx/m2", mode, false, false, false, false, h, provider.Chat, body, body, cs, planned{order: make([]Weighed, 2)})
		return got, aff
	}
	t.Run("new turn", func(t *testing.T) {
		set(t, 60_000, 120_000)
		for _, mode := range []string{"", provider.AffinitySession} {
			got, aff := run(mode, turn)
			if got[0].rest != "sx#k1" || aff.Why != "lead" || aff.Kept || aff.LeadModel != "m1" {
				t.Errorf("%q: first %s, why %s, kept %v, lead %q; want sx#k1 with its main", mode, got[0].rest, aff.Why, aff.Kept, aff.LeadModel)
			}
		}
	})
	t.Run("within a turn", func(t *testing.T) {
		set(t, 60_000, 120_000)
		if got, aff := run("", within); got[0].rest != "sx#k2" || aff.Why != "turn" {
			t.Errorf("first %s, why %s; want kept on sx#k2", got[0].rest, aff.Why)
		}
	})
	t.Run("strict", func(t *testing.T) {
		set(t, 60_000, 120_000)
		if got, aff := run(provider.AffinityStrict, turn); got[0].rest != "sx#k2" || aff.Why != "strict" {
			t.Errorf("first %s, why %s; want kept on sx#k2", got[0].rest, aff.Why)
		}
	})
	t.Run("main no larger", func(t *testing.T) {
		set(t, 120_000, 60_000)
		if got, aff := run("", turn); got[0].rest != "sx#k2" || aff.Why != "cache" {
			t.Errorf("first %s, why %s; want kept on sx#k2", got[0].rest, aff.Why)
		}
	})
	// two conversations that only open alike are not one session: with no
	// session named, the id is the opening's hash
	t.Run("no session named", func(t *testing.T) {
		hashed := conversationID(http.Header{}, turn)
		cs := []candidate{key("k1"), key("k2")}
		sticks.Lock()
		sticks.m["sx/m2|"+hashed] = stick{rest: cs[1].rest, who: cs[1].who(), model: "m2", turn: 1, at: time.Now(), cacheRead: 60_000}
		sticks.m["sx/m1|"+hashed] = stick{rest: cs[0].rest, who: cs[0].who(), model: "m1", turn: 1, at: time.Now(), cacheRead: 120_000}
		sticks.Unlock()
		t.Cleanup(func() {
			sticks.Lock()
			delete(sticks.m, "sx/m2|"+hashed)
			delete(sticks.m, "sx/m1|"+hashed)
			sticks.Unlock()
		})
		got, _, aff, _ := affine("sx/m2", "", false, false, false, false, http.Header{}, provider.Chat, turn, turn, cs, planned{order: make([]Weighed, 2)})
		if got[0].rest != "sx#k2" || aff.Why != "cache" {
			t.Errorf("first %s, why %s; want kept on sx#k2", got[0].rest, aff.Why)
		}
	})
	// a main conversation with no cache worth keeping goes where its own
	// next turn is sent, and leads nothing
	t.Run("main holds no cache", func(t *testing.T) {
		set(t, 0, cacheWorth-1)
		if _, aff := run("", turn); aff.Why == "lead" {
			t.Errorf("why %s; a main with %d cached led", aff.Why, cacheWorth-1)
		}
	})
	t.Run("main's account resting", func(t *testing.T) {
		set(t, 60_000, 120_000)
		cs := []candidate{key("k2"), key("k1")}
		order := []Weighed{{}, {Rest: &Rest{}}}
		got, _, aff, _ := affine("sx/m2", "", false, false, false, false, h, provider.Chat, turn, turn, cs, planned{order: order})
		if got[0].rest != "sx#k2" || aff.Why != "cache" {
			t.Errorf("first %s, why %s; want kept on sx#k2", got[0].rest, aff.Why)
		}
	})
}
