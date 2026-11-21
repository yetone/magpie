package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

type codexPluginSubagentCtx struct{}

// withCodexPluginSubagent stores the Codex subagent thread id extracted
// from the incoming request before translation strips client_metadata.
// serve always overwrites it so internal re-entries (such as searchWith)
// never inherit the outer request's subagent.
func withCodexPluginSubagent(ctx context.Context, sub string) context.Context {
	return context.WithValue(ctx, codexPluginSubagentCtx{}, sub)
}

// tellPluginSession sets X-Magpie-Session and X-Magpie-Subagent on a
// plugin's fetch request so plugins keeping per-conversation state can
// tell subagents apart from their lead (plugins#620): Claude Code by
// x-claude-code-agent-id, Codex by its spawn thread id, and OpenCode
// by x-opencode-parent-session-id / x-opencode-session-id. Any
// client-sent headers of those names are dropped first; an explicit
// caller X-Magpie-Session still sets X-Magpie-Session. The plugin host
// strips both headers before anything reaches a vendor (host.js).
func tellPluginSession(ctx context.Context, h, in http.Header, body []byte) {
	h.Del(provider.SessionToPluginHeader)
	h.Del(provider.SubagentToPluginHeader)
	session, sub := pluginSessionOf(ctx, in, body)
	if session != "" {
		h.Set(provider.SessionToPluginHeader, session)
	}
	if sub != "" {
		h.Set(provider.SubagentToPluginHeader, sub)
	}
}

func pluginSessionOf(ctx context.Context, in http.Header, body []byte) (session, sub string) {
	session = sessionOf(in)
	if sub = subOf(in); sub != "" {
		return session, sub
	}
	if v, ok := ctx.Value(codexPluginSubagentCtx{}).(string); ok {
		sub = v
	} else {
		c, _ := ctx.Value(callerCtx{}).(caller)
		sub = codexPluginSubagent(c.agent, in, body)
	}
	if sub != "" {
		return session, sub
	}
	if parent, child, ok := openCodePluginSubagent(in); ok {
		if strings.TrimSpace(in.Get(SessionHeader)) != "" || (session != "" && session != child) {
			return session, child
		}
		return parent, child
	}
	return session, ""
}

// openCodePluginSubagent returns (parent, child, true) when in carries
// OpenCode's own subagent headers (x-opencode-parent-session-id and a
// distinct x-opencode-session-id). A user-created fork in OpenCode 2
// carries no x-opencode-parent-session-id and returns false.
func openCodePluginSubagent(in http.Header) (parent, child string, ok bool) {
	parent = validPluginID(in.Get("x-opencode-parent-session-id"))
	child = validPluginID(in.Get("x-opencode-session-id"))
	if parent == "" || child == "" || child == parent {
		return "", "", false
	}
	return parent, child, true
}

// validPluginID trims raw and rejects empty strings, values over 128 bytes
// (so truncation cannot alias two ids), and ASCII control characters
// (C0 and DEL) that a header must not carry.
func validPluginID(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" || len(v) > 128 {
		return ""
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return ""
		}
	}
	return v
}

func firstValidPluginID(candidates ...string) string {
	for _, c := range candidates {
		if v := validPluginID(c); v != "" {
			return v
		}
	}
	return ""
}

type codexTurnIdentity struct {
	Session      string `json:"session_id"`
	Thread       string `json:"thread_id"`
	Source       string `json:"thread_source"`
	SubagentKind string `json:"subagent_kind"`
}

func parseCodexTurnJSON(raw []byte) (id codexTurnIdentity) {
	if len(raw) == 0 {
		return id
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		raw = []byte(text)
	}
	_ = json.Unmarshal(raw, &id)
	return id
}

func rawNativeSession(in http.Header) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	return ""
}

// codexPluginSubagent returns the thread id of a Codex spawned subagent
// request (collab_spawn / thread_spawn / agent_job), or "" for the lead,
// non-spawn helper calls (thread_title, guardian, review, memory), or
// requests that carry no explicit thread id distinct from the root
// session.
func codexPluginSubagent(agent string, in http.Header, body []byte) string {
	if agent != "codex" && !fromCodex(in) && !codexTurnHeaders(in) {
		return ""
	}
	hdrSubagent := strings.TrimSpace(in.Get("x-openai-subagent"))
	if hdrSubagent != "" && usage.PurposeOf(hdrSubagent) != "kind:collab_spawn" {
		return ""
	}
	if hdrSubagent == "" {
		if k := callKind(in); k != "" && usage.PurposeOf(k) != "kind:collab_spawn" {
			return ""
		}
	}

	var bodyInner, bodyFlat codexTurnIdentity
	var bodySubagent string
	if len(body) > 0 && (len(body) >= validMemoMin || bodyHas(body, "client_metadata")) {
		var md map[string]json.RawMessage
		if metadataOf(body, &md) && len(md) > 0 {
			if raw := md["x-codex-turn-metadata"]; len(raw) > 0 {
				bodyInner = parseCodexTurnJSON(raw)
			}
			_ = json.Unmarshal(md["session_id"], &bodyFlat.Session)
			_ = json.Unmarshal(md["thread_id"], &bodyFlat.Thread)
			_ = json.Unmarshal(md["thread_source"], &bodyFlat.Source)
			_ = json.Unmarshal(md["subagent_kind"], &bodyFlat.SubagentKind)
			_ = json.Unmarshal(md["x-openai-subagent"], &bodySubagent)
		}
	}
	var hdrMeta codexTurnIdentity
	if raw := in.Get("x-codex-turn-metadata"); raw != "" {
		hdrMeta = parseCodexTurnJSON([]byte(raw))
	}
	if hdrSubagent == "" {
		for _, src := range []string{bodyInner.Source, bodyFlat.Source, hdrMeta.Source} {
			switch strings.TrimSpace(src) {
			case "", "user", "subagent":
			default:
				return ""
			}
		}
	}

	kind := hdrSubagent
	for _, k := range []string{bodySubagent, bodyInner.SubagentKind, bodyFlat.SubagentKind, hdrMeta.SubagentKind} {
		if kind != "" {
			break
		}
		kind = strings.TrimSpace(k)
	}
	if usage.PurposeOf(kind) != "kind:collab_spawn" {
		return ""
	}

	thread := firstValidPluginID(bodyInner.Thread, bodyFlat.Thread, hdrMeta.Thread, in.Get("thread-id"))
	if thread == "" {
		return ""
	}
	root := rawNativeSession(in)
	if root == "" {
		root = firstValidPluginID(bodyInner.Session, bodyFlat.Session, hdrMeta.Session)
	}
	if root != "" && thread == root {
		return ""
	}
	return thread
}
