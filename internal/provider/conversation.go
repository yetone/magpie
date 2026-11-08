package provider

// The conversation a request is part of, for ${sessionId} and ${threadId}
// in a provider's Headers and ExtraBody (Axel Burks on Discord: a gateway
// in front of several Azure accounts routes a conversation to one account
// by a session id in a header, so its sealed reasoning can be read back).
//
//   - ${sessionId} is the session the agent names (its session header),
//     else the id magpie derives from the conversation's first user
//     message: the same for every turn of it.
//   - ${threadId} is the thread the agent names within that session, where
//     one names a thread apart from its session: Codex's thread_id (its
//     turn metadata, or its thread-id header), which a subagent has of its
//     own while it shares its lead's session_id. No other agent names one,
//     and there a conversation is one thread: ${threadId} is ${sessionId}.
//   - A request that is no turn of a conversation — a model list, a
//     balance, an embedding or image, the editor's Test and Detect — goes
//     on a conversation of its own: a new id each time ("magpie-" and 12
//     random letters; Test and Detect's "magpie-test-…", the same one in
//     its headers and body). A literal "${sessionId}" is never sent.
//   - In a header the ids go without control characters (CR, LF, tab…):
//     an id is the agent's, and a header can't carry them.

import (
	"context"
	"crypto/rand"
	"maps"
	"strings"
)

// ThreadPlaceholder is what a header or an extraBody string writes for the
// thread's id.
const ThreadPlaceholder = "${threadId}"

// SubagentPlaceholder is what a header or an extraBody string writes for
// the Claude Code subagent a request is of (yetone/magpie#2937).
const SubagentPlaceholder = "${subagentId}"

// Conversation is what ${sessionId}, ${threadId} and ${subagentId} stand
// for in a request: Thread "" is the session's own thread; Subagent is
// the Claude Code subagent the request is of (x-claude-code-agent-id), ""
// on the conversation's own turns, which never send it.
type Conversation struct{ Session, Thread, Subagent string }

type conversationCtxKey struct{}

// WithConversation is ctx carrying the conversation its requests are part
// of, for the headers Sign puts on them.
func WithConversation(ctx context.Context, c Conversation) context.Context {
	return context.WithValue(ctx, conversationCtxKey{}, c)
}

// conversationOf is the conversation ctx carries, else one of the
// request's own.
func conversationOf(ctx context.Context) Conversation {
	if c, ok := ctx.Value(conversationCtxKey{}).(Conversation); ok && c.Session != "" {
		return c
	}
	return OwnConversation("magpie-")
}

// OwnConversation is a conversation of a request's own: prefix and 12
// random letters, its thread the same.
func OwnConversation(prefix string) Conversation {
	return Conversation{Session: prefix + strings.ToLower(rand.Text()[:12])}
}

// expand is s with the placeholders as c's ids.
func (c Conversation) expand(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	thread := c.Thread
	if thread == "" {
		thread = c.Session
	}
	return strings.NewReplacer(SessionPlaceholder, c.Session, ThreadPlaceholder, thread, SubagentPlaceholder, c.Subagent).Replace(s)
}

// headerSafe is id without the bytes a header value can't carry: control
// characters, CR and LF among them.
func headerSafe(id string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, id)
}

// placeheld says s names the conversation.
func placeheld(s string) bool {
	return strings.Contains(s, SessionPlaceholder) || strings.Contains(s, ThreadPlaceholder) || strings.Contains(s, SubagentPlaceholder)
}

// AsksThread says p's headers or extraBody name ${threadId}, which the
// gateway reads out of the request only then.
// A header the user gave the model ctx says is asked counts (headersFor).
func (p Provider) AsksThread(ctx context.Context) bool {
	for _, v := range p.headersFor(p.modelOf(ctx)) {
		if strings.Contains(v, ThreadPlaceholder) {
			return true
		}
	}
	for _, v := range p.ExtraBody {
		if strings.Contains(string(v), ThreadPlaceholder) {
			return true
		}
	}
	return false
}

// SentHeaders are p's Headers, with those the user gave the model ctx
// says is asked after them (WithModel, headersFor), as they go out on a
// request of ctx's conversation (conversationOf): the placeholders in
// their values as its ids, without control characters. p.Headers itself
// when the model has none and no value has one.
func (p Provider) SentHeaders(ctx context.Context) map[string]string {
	return expandHeaders(p.headersFor(p.modelOf(ctx)), func() Conversation { return conversationOf(ctx) })
}

// expandHeaders is h with the placeholders in its values as the ids of
// the conversation conv gives, asked only when a value has one.
func expandHeaders(h map[string]string, conv func() Conversation) map[string]string {
	var out map[string]string
	var c Conversation
	for k, v := range h {
		if !placeheld(v) {
			continue
		}
		if out == nil {
			out = maps.Clone(h)
			c = conv()
			c = Conversation{Session: headerSafe(c.Session), Thread: headerSafe(c.Thread), Subagent: headerSafe(c.Subagent)}
		}
		out[k] = c.expand(v)
	}
	if out == nil {
		return h
	}
	return out
}
