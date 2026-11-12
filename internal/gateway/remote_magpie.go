package gateway

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/usage"
)

// A request passed on to another computer's magpie (a Remote magpie
// provider) goes as magpie's own, with magpie's User-Agent and the other
// magpie's key, so that magpie knew it only as "magpie" (Jorben on Discord).
// It now names the agent it came from (AgentHeader) and the computer
// (ViaHeader), and the other magpie keeps them in its usage, as "Claude
// Code · via <computer>". They are a label and nothing more: what magpie
// does with the request — the models an agent is shown, its stand-ins, a
// routing group's rules — still goes by who sent it, and they are taken
// only from a magpie's request (its User-Agent), one a client on the
// network could send only with the key anyway, and could name itself any
// agent by its User-Agent as well. No vendor is ever sent them: they come
// off every request as it arrives, and go on only to a remote magpie.
const (
	AgentHeader = "X-Magpie-Agent"
	ViaHeader   = "X-Magpie-Via"
	// HopsHeader is how many magpies a request was passed on through
	// before it came here. Two magpies each the other's remote (cdredfox
	// on Discord), or one that is its own (a Remote magpie provider sync
	// brought to the computer it names, hanslin1666), could pass a request
	// round and round, masked again on each pass: one passed on through
	// catalog.MaxVia magpies is answered 508 Loop Detected instead.
	HopsHeader = "X-Magpie-Hops"
	// ClientUAHeader is the User-Agent the agent sent, on a request
	// passed on to a remote magpie as magpie's own: that magpie sends its
	// key+URL providers the agent's, as this one would have (nb5p on
	// Discord). Taken, like AgentHeader, only from a magpie's request.
	ClientUAHeader = "X-Magpie-User-Agent"
	// ClaudeCacheTTLHeader is how long the prompt cache of a Claude Code
	// request is to be kept, as the user of the computer Claude Code runs
	// on picked it: "5m", "1h", or "default" for no pick. Claude Code
	// marks a five-minute cache with no ttl, picked or not, so the other
	// magpie's Claude relay can't tell a pick from its marks
	// (claudeTTLOf, runTTLFor; linkaww). Taken only from a magpie's
	// request.
	ClaudeCacheTTLHeader = "X-Magpie-Claude-Cache-TTL"
	// MaskedAsHeader says the magpie that passed a request on masked it
	// already, by the settings masking.Fingerprint names: this one masks
	// it again only when its own differ (maskedBefore). Taken only from a
	// magpie's request.
	MaskedAsHeader = "X-Magpie-Masked-As"
)

// caller is who a request is recorded as: its agent, and the computer it
// was passed on from, "" for this one. session and native are the session
// the caller named (sessionOf's X-Magpie-Session, and the agent's own
// header nativeSessionOf takes, by its name), passed on to a remote magpie
// with the request (#672).
type caller struct {
	agent, via string
	session    string
	native     [2]string // header, value
	// compact is DSH's compaction mark as it came, passed on with the
	// session so the other magpie records the call as one (#2505)
	compact string
	// ua is the User-Agent the request came with, as it came: the one a
	// provider that passes the agent's on is sent (userAgentFor). From
	// another magpie, the agent's it named (ClientUAHeader), relayed true.
	ua      string
	relayed bool
	// keepTurn: another magpie asked that its Responses turn be kept
	// though it says store false (KeepTurnHeader, remote_delta.go)
	keepTurn bool
	// hops: the magpies the request was passed on through before this
	// one (HopsHeader)
	hops int
	// cacheTTL is the Claude Code cache lifetime another magpie named
	// (ClaudeCacheTTLHeader); claudeHere, that the request is Claude
	// Code's on this computer, whose user's pick is read here
	// (claudeTTLOf)
	cacheTTL   string
	claudeHere bool
	// maskedAs: the magpie that passed it on masked it by the settings
	// this fingerprint names (MaskedAsHeader)
	maskedAs string
}

type callerCtx struct{}

// withCaller notes on each request who it is recorded as, and takes the
// headers that said so off it.
func withCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := callerFrom(r)
		if c.hops >= catalog.MaxVia {
			writeError(w, protoOfPath(r.URL.Path), http.StatusLoopDetected, "this request went round magpies that are each other's Remote magpie "+strconv.Itoa(c.hops)+" times: on one of them, turn off or remove the Remote magpie provider that points back")
			return
		}
		ctx := context.WithValue(r.Context(), callerCtx{}, c)
		if searchingFrom(r) {
			// another magpie's search: only the model itself searches
			ctx = context.WithValue(ctx, searchingKey{}, true)
		}
		r.Header.Del(AgentHeader)
		r.Header.Del(ViaHeader)
		r.Header.Del(SearchingHeader)
		r.Header.Del(KeepTurnHeader)
		r.Header.Del(HopsHeader)
		r.Header.Del(ClientUAHeader)
		r.Header.Del(ClaudeCacheTTLHeader)
		r.Header.Del(MaskedAsHeader)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// callerFrom reads who r is recorded as from its headers, before withCaller
// takes magpie's own off: the one rule for every place that names a
// request's agent (usage, sessions, the routing trace, the masking log,
// the requests in flight), so another magpie's request is shown as the
// agent that made it there, never as "magpie" (linkaww on Discord: the
// masking log named magpie where usage named Pi).
func callerFrom(r *http.Request) caller {
	c := caller{agent: agentOf(r), session: boundedSession(r.Header.Get(SessionHeader)), ua: r.Header.Get("User-Agent")}
	if dshCompacts(r.Header) {
		c.compact = boundedSession(r.Header.Get(dshCompactHeader))
	}
	for _, h := range sessionHeaders {
		if v := boundedSession(r.Header.Get(h)); v != "" {
			c.native = [2]string{h, v}
			break
		}
	}
	c.keepTurn = keepTurnAsks(r)
	if magpieAsks(r) {
		if n, err := strconv.Atoi(strings.TrimSpace(r.Header.Get(HopsHeader))); err == nil && n > 0 {
			c.hops = min(n, 1<<20)
		}
		if v := r.Header.Get(ClientUAHeader); v != "" && len(v) <= 1024 && !magpiesOwn(v) {
			c.ua, c.relayed = v, true
		}
		switch v := r.Header.Get(ClaudeCacheTTLHeader); v {
		case "5m", "1h", "default":
			c.cacheTTL = v
		}
		if v := strings.TrimSpace(r.Header.Get(MaskedAsHeader)); len(v) <= 128 {
			c.maskedAs = v
		}
		if a := label(r.Header.Get(AgentHeader)); a != "" {
			c.agent = usage.AgentOf(a)
			c.via = label(r.Header.Get(ViaHeader))
			if c.via == "" {
				c.via, _, _ = net.SplitHostPort(r.RemoteAddr)
			}
		}
	} else {
		// Claude Code, known by its token or its User-Agent; Claude
		// Desktop's keeps a settings.json of its own
		c.claudeHere = (c.agent == "claude" || fromClaudeCode(r.Header)) && c.agent != "claude-desktop" && local(r)
	}
	return c
}

// claudeTTLOf is how long the prompt cache of c's request is to be kept,
// as the user of the computer its Claude Code runs on picked it: "5m",
// "1h", "default" for no pick, "" when unknown (not Claude Code's, from
// a magpie that didn't say, or Claude Code's settings.json unreadable).
// Claude Code on this computer: this computer's pick, as claudeRunTTL
// reads it.
func claudeTTLOf(c caller) string {
	if c.cacheTTL == "" && c.agent == "claude-desktop" {
		// Claude Desktop reaches magpie with a gateway key, which Claude
		// Code takes for an API key and marks five minutes with no ttl
		// (its docs: automatic is 1 hour on a subscription, 5 minutes on an
		// API key), though magpie's run is on a subscription. Its marks
		// can't say a pick, so none is read from them: the run keeps
		// Claude Code's own default (DIYgod on X)
		return "default"
	}
	if c.cacheTTL != "" || !c.claudeHere {
		return c.cacheTTL
	}
	if !claudeSettingsKnown() {
		return ""
	}
	if ttl := claudePickedTTL(os.Environ()); ttl != "" {
		return ttl
	}
	return "default"
}

// callerOf is who the request is recorded as.
func callerOf(r *http.Request) caller {
	if c, ok := r.Context().Value(callerCtx{}).(caller); ok {
		return c
	}
	return caller{agent: agentOf(r)}
}

// label is a header's name for an agent or a computer, if it is a fit one
// to show: short, one word of printable characters.
func label(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 64 || strings.IndexFunc(v, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) }) >= 0 {
		return ""
	}
	return v
}

// boundedSession is a session header's value as magpie keeps it: trimmed,
// at most 128 bytes, as sessionOf takes it.
func boundedSession(v string) string {
	v = strings.TrimSpace(v)
	return v[:min(len(v), 128)]
}

// passOnCaller names, on a request to a remote magpie, the agent it is for
// and the computer it comes from: the one it was passed on from, else this.
// The session the caller named goes on too, as it named it — magpie's
// X-Magpie-Session and the agent's own header (session_id, …) — so the
// other magpie records the call under it and keeps a conversation on its
// account by it rather than by its first message (#672). Only a remote
// magpie is sent them; a vendor never is. It is asked, too, to name the
// vendor's model in its reply, as it did before a reply named the model
// asked for: this magpie records what served the call from it (served,
// swapped, routed; TestRemoteMagpieGroupNotSwapped), and gives its own
// agent the name it asked for itself.
func passOnCaller(ctx context.Context, req *http.Request) {
	req.Header.Set(responseModelHeader, "vendor")
	if keepTurnAsked(ctx) {
		// the turn kept though it says store false (remote_delta.go)
		req.Header.Set(KeepTurnHeader, magpieUA())
	}
	c, ok := ctx.Value(callerCtx{}).(caller)
	req.Header.Set(HopsHeader, strconv.Itoa(c.hops+1))
	req.Header.Del(MaskedAsHeader)
	if fp := maskedAsOf(ctx); fp != "" {
		// masked here already: the other magpie, masking by the same
		// settings, doesn't again
		req.Header.Set(MaskedAsHeader, fp)
	}
	if !ok {
		return
	}
	if c.session != "" {
		req.Header.Set(SessionHeader, c.session)
	}
	if c.native[0] != "" {
		req.Header.Set(c.native[0], c.native[1])
	}
	if c.compact != "" {
		req.Header.Set(dshCompactHeader, c.compact)
	}
	if v := claudeTTLOf(c); v != "" {
		req.Header.Set(ClaudeCacheTTLHeader, v)
	}

	if c.agent == "" {
		return
	}
	req.Header.Set(AgentHeader, c.agent)
	if c.via == "" {
		c.via = hostName()
	}
	if c.via != "" {
		req.Header.Set(ViaHeader, c.via)
	}
}

// hostName is this computer's name as people know it (MacBook-Pro, not
// MacBook-Pro.local).
var hostName = sync.OnceValue(func() string {
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(h, ".")
	return label(h)
})
