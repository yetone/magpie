package gateway

import "strings"

// An agent compacting its conversation asks a model to summarize it, with
// a prompt of its own in the system prompt or as the last user message; a
// rule may send that request to a cheaper, faster model (provider.Rule's
// Compact). The prompts are the agents' own, read from each one's release.

// KindCompact is the Kind of a call an agent made to compact its
// conversation (usage's "Compaction" purpose).
const KindCompact = "compact"

// compactLead is how far into the system prompt, or into a text part of the
// last user message, an agent's compaction prompt may begin. Each agent puts
// its prompt first or behind a short lead of its own (Claude Code's billing
// header and identity line; its "CRITICAL: Respond with TEXT ONLY" preamble,
// about 420 bytes, before the ask). Further in, it is something quoting the
// prompt: a memory or a note an agent puts in the system prompt behind its
// own, every turn (#1635, jueda12: a Minis memory holding Minis's compaction
// line sent every request of that conversation to the compaction rule's
// model), or a user writing about it.
const compactLead = 2048

// compactSystems open (or are) the system prompt of a compaction request.
var compactSystems = []string{
	// Claude Code 2.1, and OpenCode's before its own agent
	"You are a helpful AI assistant tasked with summarizing conversations.",
	// OpenCode's compaction agent (agent/prompt/compaction.txt)
	"You are a context summarization agent.",
	// Pi
	"You are a context summarization assistant.",
	// Gemini CLI
	"You are a specialized system component responsible for distilling chat history into a structured XML <state_snapshot>",
	// Qwen Code
	"You are the component that summarizes a conversation when its context window is about to overflow.",
	// Minis (OpenMinis), iOS and Android alike (compactSummarySystemPrompt)
	"You are a context compaction engine. Your summary will REPLACE the original messages in the conversation context window.",
	// Kimi Code (kimi_cli/soul/compaction.py's COMPACTION_SYSTEM_PROMPT)
	"You are a helpful assistant that compacts conversation context.",
	// Alma (0.4.335, its compaction's instructions): the first
	// compaction, and one that merges a summary it made before (#2833)
	"You are compacting a long conversation so it can continue WITHOUT losing context.",
	"You are UPDATING an existing conversation summary with new activity so the conversation can continue WITHOUT losing context.",
}

// compactAsk is said in a text part of the last user message of a
// compaction request: within compactLead of the part's start, or, for an
// agent that sends the conversation ahead of it in the same part, anywhere
// in a part that opens with that agent's own first line (opens).
type compactAsk struct {
	ask, opens string
}

// compactAsks are in the last user message of a compaction request.
var compactAsks = []compactAsk{
	// Claude Code's /compact and auto-compact
	{ask: "Your task is to create a detailed summary of the conversation so far"},
	// Codex (and magpie's own for a Codex compaction_trigger)
	{ask: "You are performing a CONTEXT CHECKPOINT COMPACTION."},
	// Kimi Code (prompts/compact.md, a part of its own after the conversation)
	{ask: "You are now given a task to compact this conversation context"},
	// Minis, around the conversation it sends as one user message
	{ask: "END OF CONVERSATION TO COMPACT.", opens: "Compact this conversation into a context summary:"},
	// Alma, which opens the user message of its compaction with an ask
	// and sends the conversation behind it: a later compaction merges the
	// summary it made before (#2833, as the report quotes it), the first
	// has none (Alma 0.4.335's own prompt). Both are also known by their
	// system prompt above, which a route that drops it loses.
	{ask: "Merge the previous summary with the new conversation."},
	{ask: "Here is the full conversation to compact."},
}

// leads reports whether p begins within compactLead bytes of text's start.
func leads(text, p string) bool {
	i := strings.Index(text, p)
	return i >= 0 && i <= compactLead
}

// compacting reports whether req is an agent compacting its conversation.
func compacting(req *Request) bool {
	if req == nil {
		return false
	}
	for _, p := range compactSystems {
		if leads(req.System, p) {
			return true
		}
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != "user" {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind != Text {
				continue
			}
			for _, a := range compactAsks {
				if a.opens == "" && leads(p.Text, a.ask) ||
					a.opens != "" && strings.HasPrefix(strings.TrimSpace(p.Text), a.opens) && strings.Contains(p.Text, a.ask) {
					return true
				}
			}
		}
		break
	}
	return false
}
