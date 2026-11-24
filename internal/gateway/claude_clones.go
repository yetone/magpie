package gateway

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A Claude Code clone is an agent that sends Claude Code's own headers,
// its User-Agent (claude-cli/…), X-Claude-Code-Session-Id, x-app: cli and
// the claude-code beta, so its requests read as Claude Code's
// everywhere magpie names the caller (the Routing card, usage, Sessions,
// the logs). omp is one: an anthropic-messages provider in its
// models.yml with an apiKey and no auth (internal/agent/omp.go writes one
// once an image model is listed, or the gateway asks for a named key) is
// taken for a Claude subscription (pi-coding-agent's
// resolveCustomModelIsOAuth), and pi-ai's OAuth shaping sends
// "claude-cli/2.1.326 (external, cli)" (anthropic-headers.ts), so omp's
// Antigravity calls were shown as Claude Code's (SadApple235141 on X,
// v0.1.4743). Nothing in its headers is its own; its system prompt is.
//
// Only what the caller is named changes. fromClaudeCode still answers for
// the User-Agent (its cache lifetime, claudeHere), and what goes upstream
// is what came.
type claudeClone struct {
	agent string
	// lines: the words a system block of its own begins with,
	// lower-cased; Claude Code's own prompt has none of them
	lines []string
}

var claudeClones = []claudeClone{{
	agent: "omp",
	lines: []string{
		// omp 19's system prompt (pi-coding-agent 19.1.1)
		"you are omp's trusted coding assistant.",
		// omp 18's (18.15.0, 18.20.0), the line codeassist.go knows
		agRFCLine,
	},
}}

type cloneCtx struct{}

// cloneSessions are the sessions a clone was known in by its prompt, so its
// side calls in them (a title, a commit message), whose prompts say
// nothing of it, are its too. Bounded: an old session is dropped for a new
// one past cloneSessionsMax.
var cloneSessions = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

const cloneSessionsMax = 4096

// cloneOf is the clone r is a request of: the one its body named
// (withClone), else the one its session was known as. "" for anything
// else, Claude Code's own requests among them.
func cloneOf(r *http.Request) string {
	if a, ok := r.Context().Value(cloneCtx{}).(string); ok {
		return a
	}
	if !fromClaudeCode(r.Header) {
		return ""
	}
	id := sessionOf(r.Header)
	if id == "" {
		return ""
	}
	cloneSessions.Lock()
	defer cloneSessions.Unlock()
	return cloneSessions.m[id]
}

// cloneByPrompt is the clone whose own words begin one of body's system
// blocks, "" when none's do.
func cloneByPrompt(body []byte) string {
	sys := gjson.GetBytes(body, "system")
	var texts []string
	switch {
	case sys.Type == gjson.String:
		texts = []string{sys.Str}
	case sys.IsArray():
		for _, b := range sys.Array() {
			texts = append(texts, b.Get("text").Str)
		}
	}
	for _, t := range texts {
		t = strings.ToLower(strings.TrimSpace(t))
		for _, c := range claudeClones {
			for _, l := range c.lines {
				if strings.HasPrefix(t, l) {
					return c.agent
				}
			}
		}
	}
	return ""
}

// withClone is r named as the Claude Code clone its body shows it to be:
// on its context for agentOf, in the caller it is recorded as
// (callerOf), and its session remembered. r as it came when it is not
// one, when its key or address names its agent already, or when another
// magpie passed it on (which named it).
func withClone(r *http.Request, from provider.Protocol, body []byte) *http.Request {
	if from != provider.Anthropic || !fromClaudeCode(r.Header) {
		return r
	}
	c := callerOf(r)
	// named by its User-Agent alone: not by a key, an address, Claude
	// Desktop's or Claude Science's marks, or another magpie
	if c.agent != usage.AgentOf(r.Header.Get("User-Agent")) || c.via != "" || pathAgent(r) != "" {
		return r
	}
	if id, ok := strings.CutPrefix(callerKey(r), Token+"-"); ok && id != "" {
		return r
	}
	agent := cloneByPrompt(body)
	if agent == "" {
		return r
	}
	if id := sessionOf(r.Header); id != "" {
		cloneSessions.Lock()
		if _, ok := cloneSessions.m[id]; !ok && len(cloneSessions.m) >= cloneSessionsMax {
			for k := range cloneSessions.m {
				delete(cloneSessions.m, k)
				break
			}
		}
		cloneSessions.m[id] = agent
		cloneSessions.Unlock()
	}
	c.agent = agent
	ctx := context.WithValue(r.Context(), cloneCtx{}, agent)
	return r.WithContext(context.WithValue(ctx, callerCtx{}, c))
}
