package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// DeepSeek Harness's calls, read as its session summaries are (dsh.go): an
// "assistant/message" carries the model and the usage of the request that
// answered with it, one per line, so every such line is one call.
//
// The Sessions page has read these files all along — dsh.go parses the same
// event for its summary — but the usage ledger never asked for them, so dsh's
// tokens reached the Sessions page and nothing else: the Usage page showed
// only what the gateway itself served, which is nothing before magpie was
// installed and nothing at all for a dsh pointed straight at a vendor.
//
// A call through magpie's gateway is logged by the gateway as well, and dsh
// sends it no session header, so the two records cannot be paired up
// afterwards: it is left out here as OpenCode's and ZCode's are
// (dshGatewayProvider), or it would be counted twice — once by the gateway
// with the provider and account that really answered, and once here as a
// nameless local session. The provider a session's record names is kept as
// history (Upstream), never as proof of the route.

// dshCallFiles are dsh's sessions as call sources. It is dshFiles with the
// size and time its summary reader already keeps, so a session that has not
// changed is not read again. A subagent's file is a source of its own, as
// WorkBuddy's subagents' are: nothing in this path reads a file's main (the
// Sessions page's mark for a session's own file), so it is left as dshFiles
// set it.
func dshCallFiles() []file {
	return dshFiles()
}

// readDshCalls reads a dsh session whole: packed in frames, it can't be read
// on from the middle of one (as parseDsh says).
func readDshCalls(f file) (*callFile, bool) {
	r, err := dshOpen(f.path)
	if err != nil {
		return &callFile{Path: f.path, Agent: "dsh"}, false
	}
	defer r.Close()
	st := &callFile{Agent: "dsh", Path: f.path, Session: strings.TrimPrefix(f.key, "dsh:")}
	br := bufio.NewReaderSize(r, 1<<20)
	var h dshHead
	// the events a fork was seeded with are the session it came from, not
	// this one: parseDsh skips them, and counting them here would spend the
	// same request once per fork (dsh-a's first reply is also dsh-c's and
	// dsh-d's first lines)
	var evs []dshEvent
	var seeds []bool
	cut := -1
	for first := true; ; first = false {
		b, err := br.ReadBytes('\n')
		if err != nil {
			// a line still being written, or a frame cut short
			if !errors.Is(err, io.EOF) || len(bytes.TrimSpace(b)) == 0 {
				break
			}
		}
		b = bytes.TrimSpace(b)
		if len(b) == 0 || len(b) > maxLine {
			if err != nil {
				break
			}
			continue
		}
		if first {
			if json.Unmarshal(b, &h) != nil || h.Type != "session" {
				return st, false
			}
			if st.Session == "" {
				st.Session = h.ID
			}
			continue
		}
		// only an assistant/message carries a call, and only the ones a
		// seed ends: every other line is passed over undecoded
		if bytes.Contains(b, []byte(`"assistant/message"`)) || bytes.Contains(b, []byte(`"session/end-seed"`)) {
			var e dshEvent
			if json.Unmarshal(b, &e) == nil && e.Type != "" {
				evs = append(evs, e)
				seeds = append(seeds, e.Type == "session/end-seed" && e.Data.Inherited)
			}
		}
		if err != nil {
			break
		}
	}
	if h.Version >= 2 && h.IsSeeded {
		for i, seed := range seeds {
			if seed {
				cut = i
			}
		}
	}
	for i, e := range evs {
		if e.Type != "assistant/message" {
			continue
		}
		// before format 2 the seed is counted in events, after it by the
		// marker it ends with
		if i <= cut || h.Version < 2 && e.Seq != nil && *e.Seq < h.SeedLength {
			continue
		}
		if c, ok := dshCall(st, e); ok {
			st.Calls = append(st.Calls, c)
		}
	}
	return st, true
}

// dshCall is an assistant/message as a call: its model, its tokens and the
// time it was written.
func dshCall(st *callFile, e dshEvent) (Call, bool) {
	u := e.Data.Usage
	if u == nil {
		return Call{}, false
	}
	at := ms(e.Time)
	if at.IsZero() {
		return Call{}, false
	}
	src := e.Data.Message.Source
	// a call dsh sent through magpie's own gateway is the gateway's record
	// to keep, not this one's: see dshGatewayProvider
	if dshGatewayProvider(src) {
		return Call{}, false
	}
	// dsh names the model it was told to use, and its reply's replay state
	// names the one the vendor answered with when the two differ. Call
	// keeps them as Claude Code's file does: Model is what answered (the
	// vendor's own name for it, which is what the served column reads) and
	// Requested what was asked for. The ledger turns the two into a row's
	// requested, sent and served models (#the usage report).
	answered := dshServed(src)
	if answered == "" {
		answered = src.Model
	}
	c := Call{
		Agent:     "dsh",
		Time:      at,
		Session:   st.Session,
		Model:     answered,
		Requested: src.Model,
		Upstream:  dshUpstream(src),
		Tokens: Tokens{
			Input:      u.Input,
			Output:     u.Output,
			CacheRead:  u.CacheRead,
			CacheWrite: u.CacheWrite,
		},
	}
	if c.Tokens.zero() {
		return Call{}, false
	}
	return c, true
}

// dshServed is the model the vendor's own reply named, "" when the file does
// not say or says the one that was asked for (nothing was swapped).
func dshServed(src dshSource) string {
	answered := src.ReplayState.Response.ResponseModel
	if answered == "" {
		answered = src.ReplayState.Response.Model
	}
	if answered == src.Model {
		return ""
	}
	return answered
}

// dshUpstream is the provider dsh was told to use, "" when it names none.
// It is what the file recorded, not proof the request went there: the
// ledger keeps it as the session's own record (SessionProvider), as Codex's
// is kept, and never lets it stand in for the route a row took.
func dshUpstream(src dshSource) string {
	if p := src.Provider; p != "" {
		return p
	}
	return src.ReplayState.Response.Provider
}

// dshGatewayProvider is whether a dsh provider is magpie's own gateway, by the
// names magpie writes it under ("magpie", and "dial" from before). A call
// through it is logged by the gateway itself, and dsh sends the gateway no
// session header (unlike Codex and OpenCode), so the two records cannot be
// paired afterwards: the token counts differ call to call and there is no id
// to match on. Such a call is therefore left out here, as OpenCode's and
// ZCode's are (ocGatewayProvider), or it would be counted twice — once by the
// gateway, with the provider and account that really answered, and once
// here as a nameless local session.
//
// It is the name alone: dsh's file records no baseURL, so a provider the user
// added under one of those names, pointing at a vendor of their own, is taken
// for the gateway too and its calls are read as neither source's. Adding the
// URL to the read would tell the two apart.
func dshGatewayProvider(src dshSource) bool {
	p := dshUpstream(src)
	if p == "" {
		return false
	}
	return p == "magpie" || p == "dial"
}
