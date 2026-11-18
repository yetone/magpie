package gateway

import (
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Code with tool search on (ENABLE_TOOL_SEARCH, which magpie writes
// "true", and which 2.1.304 and later turn on behind a gateway by
// themselves) names its deferred tools in a reminder and declares one only
// once ToolSearch loaded it: from that request on the tool is in the
// request's tools. Anthropic's Messages API keeps a deferred tool out of
// the cached prefix, and an OpenAI Responses upstream that takes an
// additional_tools item is given it there, after the result that loaded
// it (Request.LoadedInPlace, #2660). Every other route has only the
// top-level tools for it: Chat Completions, a Responses relay, Gemini. An
// OpenAI-style prompt cache is a prefix, with the tools before the
// messages, so each tool loaded there changed the start of the prompt and
// the whole conversation was read again uncached (Seapawn1 on X: GPT in
// Claude Code loses its cache with tool search on). WebFetch, WebSearch
// and the other deferred built-ins are loaded that way too, so it happens
// often.
//
// On such a route magpie turns the deferred tools away with a 400 that
// names defer_loading, which Claude Code answers by asking again with
// every tool declared in full, for the rest of that conversation: the
// tools stay as they were from the first request, and the cache holds. A
// conversation on a Claude model, on Anthropic's API, keeps tool search,
// and so does one on a Responses upstream that loads in place. Only a
// request with Claude Code's DeferredToolPlaceholder is turned away (a
// client that declares its deferred tools whole has a stable prefix
// already), and only while the tools it named are few enough to declare:
// a long list, such as an MCP router's hundreds, would fill the context on
// every request (cybercaius on Discord: 245 tools, 124K tokens), so it
// keeps tool search, and loses the cache on a load as before.

// deferredRefusal is the message of a 400 Claude Code takes as the
// endpoint not taking deferred tools (it looks for defer_loading).
const deferredRefusal = "tools.defer_loading: deferred tools are not loaded in place on this route, so a tool loaded later would change the start of the prompt and lose its cache; ask again with every tool declared in full"

// maxDeferredDeclared is how many deferred tools Claude Code may be asked
// to declare in full: at about 400 tokens a tool, 50 are the tenth of a
// 200K context that Claude Code's own auto mode lets tool definitions take
// before it searches for them.
const maxDeferredDeclared = 50

// deferredAnnouncement opens Claude Code's reminder listing its deferred
// tools, one name a line (2.1.327).
const deferredAnnouncement = "The following deferred tools are now available via ToolSearch"

// deferredCount is how many tools Claude Code's reminders name as
// deferred, each once, and whether a reminder was found at all.
func deferredCount(r *Request) (int, bool) {
	names := map[string]bool{}
	found := false
	see := func(s string) {
		for {
			i := strings.Index(s, deferredAnnouncement)
			if i < 0 {
				return
			}
			found = true
			s = s[i+len(deferredAnnouncement):]
			// the names start on the line after the announcement
			nl := strings.IndexByte(s, '\n')
			if nl < 0 {
				return
			}
			s = s[nl+1:]
			for s != "" {
				line, rest, _ := strings.Cut(s, "\n")
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "<") {
					break
				}
				names[line] = true
				s = rest
			}
		}
	}
	see(r.System)
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			if p.Kind == Text {
				see(p.Text)
			}
		}
	}
	return len(names), found
}

// refusesDeferred is whether a Claude Code request with deferred tools is
// turned away on its way to p over to (deferredRefusal): from Anthropic's
// Messages to another API, where the tools it loads couldn't be given in
// place, and naming few enough tools to declare. One whose reminder can't
// be found or read keeps tool search, as it did.
func (s *Server) refusesDeferred(p provider.Provider, from, to provider.Protocol, model string, r *Request) bool {
	if from != provider.Anthropic || to == provider.Anthropic || !r.DeferredNamed {
		return false
	}
	if to == provider.Responses && loadsInPlace(p) && s.fits(p.ID, additionalToolsItem, to) {
		return false
	}
	n, ok := deferredCount(r)
	return ok && n <= maxDeferredDeclared
}
