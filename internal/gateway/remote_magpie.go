package gateway

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"unicode"

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
)

// caller is who a request is recorded as: its agent, and the computer it
// was passed on from, "" for this one.
type caller struct{ agent, via string }

type callerCtx struct{}

// withCaller notes on each request who it is recorded as, and takes the
// headers that said so off it.
func withCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := caller{agent: agentOf(r)}
		if strings.HasPrefix(r.Header.Get("User-Agent"), "magpie/") {
			if a := label(r.Header.Get(AgentHeader)); a != "" {
				c.agent = usage.AgentOf(a)
				c.via = label(r.Header.Get(ViaHeader))
				if c.via == "" {
					c.via, _, _ = net.SplitHostPort(r.RemoteAddr)
				}
			}
		}
		r.Header.Del(AgentHeader)
		r.Header.Del(ViaHeader)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerCtx{}, c)))
	})
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

// passOnCaller names, on a request to a remote magpie, the agent it is for
// and the computer it comes from: the one it was passed on from, else this.
func passOnCaller(ctx context.Context, req *http.Request) {
	c, ok := ctx.Value(callerCtx{}).(caller)
	if !ok || c.agent == "" {
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
