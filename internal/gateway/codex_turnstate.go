package gateway

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// x-codex-turn-state is the ChatGPT backend's routing of a turn: the first
// reply of a turn says it, and Codex sends it back on every later request
// of that turn (each tool call's result), which the backend then serves
// as the same turn. Sent without it, each request of the turn was a new
// one there, admitted again (legeling567 on X: 529s, "high load", on Astra
// with three Pro accounts in magpie, none with Codex's own sign-in).
//
// Through the pool Codex never hears the state (the reply is the pool's),
// and on its socket it continues a turn by previous_response_id with no
// state at all, so magpie keeps it: the state the account's reply said
// for a turn (client_metadata's thread and turn_id) goes with that turn's
// later requests to that account. The state is one account's: another
// account gets the turn without it, as a new turn there. A state Codex
// sends itself goes on only to the account whose reply said it.

// codexTurnStateFor is how long a turn's state is kept after its last
// use: a turn's tool calls come seconds or minutes apart.
const codexTurnStateFor = 2 * time.Hour

var codexTurnStates = struct {
	sync.Mutex
	byTurn  map[string]codexTurnState // account + thread + turn → state
	byState map[string]codexTurnState // state → account
}{byTurn: map[string]codexTurnState{}, byState: map[string]codexTurnState{}}

type codexTurnState struct {
	acct, state string
	at          time.Time
}

// codexTurnAccount is who a Codex account's request is signed as: the
// account (agent and user) and the workspace it signs into, two seats of
// one email being two accounts.
func codexTurnAccount(p provider.Provider, signed http.Header) string {
	if p.Account == nil || p.Account.Agent != "codex" {
		return ""
	}
	return p.Account.UsageAgent() + "\x00" + strings.ToLower(p.Account.User) + "\x00" + signed.Get("chatgpt-account-id")
}

// codexTurnOf is the turn a Codex request is of, "" when it doesn't say.
func codexTurnOf(in http.Header, body []byte) string {
	m := requestSessionMetadata(in, body)
	if m.Turn == "" {
		return ""
	}
	return m.Thread + "\x00" + m.Turn
}

// heardCodexTurnState records the turn state acct's reply says for turn.
func heardCodexTurnState(acct, turn string, res *http.Response) {
	if acct == "" || res == nil {
		return
	}
	v := res.Header.Get("X-Codex-Turn-State")
	if v == "" {
		return
	}
	now := time.Now()
	codexTurnStates.Lock()
	defer codexTurnStates.Unlock()
	if len(codexTurnStates.byState) > 4096 {
		for k, o := range codexTurnStates.byState {
			if now.Sub(o.at) > codexTurnStateFor {
				delete(codexTurnStates.byState, k)
			}
		}
		for k, o := range codexTurnStates.byTurn {
			if now.Sub(o.at) > codexTurnStateFor {
				delete(codexTurnStates.byTurn, k)
			}
		}
	}
	st := codexTurnState{acct: acct, state: v, at: now}
	codexTurnStates.byState[v] = st
	if turn != "" {
		codexTurnStates.byTurn[acct+"\x00"+turn] = st
	}
}

// codexTurnStateOf is the turn state a request of turn goes to acct with:
// the one the agent sent (in, its headers) when acct's reply said it, else
// the one acct's reply said for the turn, else "".
func codexTurnStateOf(in http.Header, acct, turn string) string {
	if acct == "" {
		return ""
	}
	now := time.Now()
	codexTurnStates.Lock()
	defer codexTurnStates.Unlock()
	if v := in.Get("X-Codex-Turn-State"); v != "" {
		if o, ok := codexTurnStates.byState[v]; ok && o.acct == acct && now.Sub(o.at) <= codexTurnStateFor {
			o.at = now
			codexTurnStates.byState[v] = o
			return v
		}
	}
	if turn == "" {
		return ""
	}
	k := acct + "\x00" + turn
	o, ok := codexTurnStates.byTurn[k]
	if !ok || now.Sub(o.at) > codexTurnStateFor {
		return ""
	}
	o.at = now
	codexTurnStates.byTurn[k] = o
	return o.state
}
