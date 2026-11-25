package sessions

import (
	"bytes"
	"encoding/json"
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

// readDshCalls reads a dsh session's calls. dsh only appends to a session
// file, a line at a time or, packed, a frame of lines at a time, so a file
// read before is read on from where that read left off, a packed one from
// the frame it left off in (frames.go), as parseDsh reads it. A file cut
// short or replaced is read whole again, and a line or frame not yet
// written whole waits for the next read. kept is how many of st's calls
// were old's.
func readDshCalls(f file, old *callFile) (st *callFile, kept int, ok bool) {
	var c dshCarry
	var off, skip int64
	if old != nil && old.Dsh != nil && old.Dsh.Tail.holds(f.path) {
		st = old.clone()
		c, off, skip = *st.Dsh, st.Dsh.Tail.Off, st.Dsh.Tail.Skip
		kept = len(st.Calls)
	} else {
		st = &callFile{Agent: "dsh", Path: f.path, Session: strings.TrimPrefix(f.key, "dsh:")}
	}
	headed := c.Head.Type == "session"
	ok = true
	each := func(b []byte) bool {
		b = bytes.TrimSpace(b)
		if len(b) == 0 || len(b) > maxLine {
			return true
		}
		if !headed {
			headed = true
			if json.Unmarshal(b, &c.Head) != nil || c.Head.Type != "session" {
				ok = false
				return false
			}
			if st.Session == "" {
				st.Session = c.Head.ID
			}
			return true
		}
		// only an assistant/message carries a call, and only the ones a
		// seed ends: every other line is passed over undecoded
		if !bytes.Contains(b, []byte(`"assistant/message"`)) && !bytes.Contains(b, []byte(`"session/end-seed"`)) {
			return true
		}
		var e dshEvent
		if json.Unmarshal(b, &e) != nil {
			return true
		}
		switch e.Type {
		case "session/end-seed":
			// the events a fork was seeded with are the session it came
			// from, not this one: parseDsh skips them, and counting them
			// here would spend the same request once per fork (dsh-a's
			// first reply is also dsh-c's and dsh-d's first lines). From
			// format 2 the seed ends with this marker: what came before
			// it is not this session's.
			if e.Data.Inherited && c.Head.Version >= 2 && c.Head.IsSeeded {
				st.Calls, kept = nil, 0
			}
		case "assistant/message":
			// before format 2 the seed is counted in events
			if c.Head.Version < 2 && e.Seq != nil && *e.Seq < c.Head.SeedLength {
				return true
			}
			if call, ok := dshCall(st, e); ok {
				st.Calls = append(st.Calls, call)
			}
		}
		return true
	}
	var next, nextSkip, end int64
	var err error
	if packed(f.path) {
		next, nextSkip, end, err = scanFrames(f.path, off, skip, each)
	} else {
		next, err = scanAt(f.path, off, nil, func(b []byte, _, _ int64) bool { return each(b) })
		end = next
	}
	if !ok || err != nil && !headed {
		return &callFile{Path: f.path, Agent: "dsh"}, 0, false
	}
	c.Tail = nil
	if err == nil {
		c.Tail = markTail(f.path, next, nextSkip, end)
	}
	st.Dsh = &c
	return st, kept, true
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
// names magpie writes it under ("magpie", "magpie-responses",
// "magpie-anthropic" and "magpie-gemini", one route per protocol —
// agent.dshKinds — and "dial"
// from before). A call
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
	switch p {
	case "magpie", "magpie-responses", "magpie-anthropic", "magpie-gemini", "dial":
		return true
	}
	return false
}
