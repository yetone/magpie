package sessions

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// Pi's and omp's calls (#1951, Elenoxe: Pi in WSL on its own provider
// never reached Usage). Read from the session files Sessions reads
// (pi.go, omp.go), as Pi 1.1.0 and omp 18.9.1 wrote them in a sandbox
// against a relay of their own: each model response is an assistant
// message, written as it ended, naming the provider and model that
// answered and its usage (input without the cache, output with the
// reasoning); Pi adds durationMs, omp duration and ttft:
//
//	{"type":"message","id":"36b3bbef","parentId":"807675bc",
//	 "timestamp":"2026-10-13T07:47:34.522Z","message":{"role":"assistant",
//	 "content":[…],"api":"openai-completions","provider":"relay",
//	 "model":"deepseek-v4","usage":{"input":201,"output":30,"cacheRead":1000,
//	 "cacheWrite":0,"reasoning":0,"totalTokens":1231,…},"stopReason":"stop",
//	 "timestamp":1791877653894,"responseId":"chatcmpl-fake1","durationMs":628}}
//
// The calls outside the conversation are counted too, as Sessions counts
// them: "usage" and omp's "model_usage" entries, each naming its provider
// and model, and a compaction's, a branch summary's or a tool's result's
// usage, on the model and provider in use. A call that failed is written
// with no usage, and is none.
//
// A call made through magpie's own entry (provider "magpie", which
// internal/agent's pi.go and omp.go write, "dial" before the rename) is the
// gateway's to count: it logged the call itself, with the provider and
// account that answered. Cindy's Pi names it otherwise: Cindy gives a
// custom provider it imports the link's id and eight hex digits of a new
// UUID (its import parser: `${id.slice(0,24)}-${randomUUID().slice(0,8)}`),
// so magpie's link, id "magpie" (internal/agent's cindy.go), is
// "magpie-beedee1a" there, and a call through it was counted twice: by the
// gateway, and again here as Cindy's own (jealousy03888 on Discord, ZCode's
// GLM-5.3 in Cindy listed once as a gateway request and once as a local
// session).
//
// A fork (pi --fork, /fork) starts with a copy of its parent's entries, ids
// and times kept: they are the parent's calls, left out of the fork as
// Sessions leaves them (written before its header), and a call is keyed by
// its entry's id and time, so Calls counts one twice in no case.

// piCallFiles are Pi's and omp's session files, here and in WSL.
func piCallFiles() []file {
	out := append(piFiles(), raftFiles()...)
	out = append(out, cindyFiles()...)
	out = append(out, ompFiles()...)
	return append(out, wslFiles("pi", "raft", "omp")...)
}

// piGateway is whether a provider is magpie's own entry, in agent's files.
func piGateway(agent, provider string) bool {
	if provider == "magpie" || provider == "dial" {
		return true
	}
	return agent == "cindy" && cindyGatewayProvider(provider)
}

// cindyGatewayProvider is whether a provider id is the one Cindy gives
// magpie's import link: "magpie-" and eight lowercase hex digits. A
// provider the user adds in Cindy by hand under the name Magpie is
// "magpie", or "magpie-2" beside another (its slug), never this shape.
func cindyGatewayProvider(id string) bool {
	rest, ok := strings.CutPrefix(id, "magpie-")
	if !ok || len(rest) != 8 {
		return false
	}
	for _, r := range rest {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f') {
			return false
		}
	}
	return true
}

type piCallLine struct {
	Type      string   `json:"type"`
	ID        string   `json:"id"`
	Timestamp string   `json:"timestamp"`
	Cwd       string   `json:"cwd"` // session
	Parent    string   `json:"parentSession"`
	Provider  string   `json:"provider"` // model_change, usage, model_usage
	ModelID   string   `json:"modelId"`  // model_change
	Model     string   `json:"model"`    // usage, model_usage; omp's model_change, as provider/model
	Usage     *piUsage `json:"usage"`
	Message   *struct {
		Role       string        `json:"role"`
		Provider   string        `json:"provider"`
		Model      string        `json:"model"`
		Usage      *piUsage      `json:"usage"`
		DurationMs float64       `json:"durationMs"` // Pi's
		Duration   float64       `json:"duration"`   // omp's, in milliseconds
		TTFT       float64       `json:"ttft"`       // omp's
		Details    *piSubResults `json:"details"`    // toolResult
	} `json:"message"`
}

// piCarry is what a Pi or omp session file's lines have said so far that a
// later line's call is read with: the header's cwd and, for a fork, when it
// began (what was written before is its parent's), and the provider and
// model in use. Pi and omp only append to the file, a line an entry, so a
// file that grew is read on from where the last read left off with it
// (lineCallReaders).
type piCarry struct {
	Header          bool
	Cwd             string
	Since           time.Time
	Provider, Model string
}

// piCallsBegin starts a session file read from its start.
func piCallsBegin(st *callFile, f file) {
	st.Session = strings.TrimPrefix(f.key, f.agent+":")
	st.Pi = &piCarry{}
}

// piCallsKept is whether st holds what reading on needs: a disk shard
// written before the carry was kept there does not, and its file is read
// whole again once it changes.
func piCallsKept(st *callFile) bool { return st.Pi != nil }

// readPiCallLine reads one line of a Pi or omp session file.
func readPiCallLine(st *callFile, b []byte) {
	if len(b) == 0 || len(b) > maxLine || !(bytes.Contains(b, piUsed) || bytes.Contains(b, piHeader) || bytes.Contains(b, piModel)) {
		return
	}
	var l piCallLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	p := st.Pi
	switch l.Type {
	case "session":
		if !p.Header {
			p.Header, p.Cwd = true, l.Cwd
			if l.Parent != "" {
				p.Since, _ = time.Parse(time.RFC3339Nano, l.Timestamp)
			}
		}
	case "model_change":
		prov, id := l.Provider, l.ModelID
		if id == "" {
			prov, id, _ = strings.Cut(l.Model, "/")
		}
		if id != "" {
			p.Provider, p.Model = prov, id
		}
	case "message":
		if m := l.Message; m != nil && m.Role == "assistant" && m.Model != "" {
			p.Provider, p.Model = m.Provider, m.Model
		}
	}
	if c, ok := piCall(st, st.Agent, l, p.Provider, p.Model); ok && !c.Time.Before(p.Since) {
		c.Cwd = st.str(p.Cwd)
		st.Calls = append(st.Calls, c)
	}
}

// piCall is an entry as a call, on the provider and model in use for the
// usage that names none.
func piCall(st *callFile, agent string, l piCallLine, provider, model string) (Call, bool) {
	var u *piUsage
	var millis, ttft int64
	switch l.Type {
	case "message":
		m := l.Message
		if m == nil {
			return Call{}, false
		}
		u = m.Usage
		// a pi-subagents result's: its subagents' own files hold the calls
		if m.Role != "assistant" && piSubagentsRead(agent, st.Session, m.Details) {
			return Call{}, false
		}
		if m.Role == "assistant" {
			provider, model = m.Provider, m.Model
			millis = int64(math.Round(max(m.DurationMs, m.Duration)))
			ttft = int64(math.Round(m.TTFT))
		}
	case "usage", "model_usage":
		u, provider, model = l.Usage, l.Provider, l.Model
	case "compaction", "branch_summary":
		u = l.Usage
	default:
		return Call{}, false
	}
	if u == nil || model == "" || piGateway(agent, provider) {
		return Call{}, false
	}
	if u.Input < 0 || u.Output < 0 || u.CacheRead < 0 || u.CacheWrite < 0 {
		return Call{}, false
	}
	// the entry's time: when the call ended, as a call's Time is
	at, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
	if at.IsZero() {
		return Call{}, false
	}
	c := Call{
		Agent:    agent,
		Time:     at,
		Session:  st.Session,
		Model:    st.str(model),
		Upstream: st.str(provider),
		Tokens:   u.tokens(),
		Millis:   max(millis, 0),
		TTFT:     max(ttft, 0),
	}
	if c.Tokens.zero() {
		return Call{}, false
	}
	if l.ID != "" {
		c.Msg = st.str(agent + ":" + l.ID + ":" + l.Timestamp)
	}
	return c, true
}
