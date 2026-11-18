// Package codexsummary is the compaction magpie answers Codex with when it
// compacts a conversation itself (a model of magpie's, or Codex's own
// models through magpie's accounts), and the message it stands for.
//
// Codex keeps a compaction item whole, encrypted_content and all, in its
// rollout, and sends it back as it is with every later turn. magpie's is
// "magpie1:" and the summary in base64, which only magpie reads: sent to
// OpenAI itself (magpie quit, or disconnected) it is refused as
// invalid_encrypted_content, and the conversation can't go on (LukeMa on
// X). What it is read as is the message Codex's own local compaction
// keeps in its place: its summary prefix, a newline and the summary, as a
// user message, which Codex knows for a summary (is_summary_message).
// The gateway sends that in place of the item, and a rollout is made
// portable by writing it there (sessions.PortCodexRollouts).
package codexsummary

import (
	"encoding/base64"
	"strings"
)

// Mark begins the encrypted_content of a compaction item magpie made.
const Mark = "magpie1:"

// Prefix is Codex's own (Apache-2.0, openai/codex,
// prompts/templates/compact/summary_prefix.md).
const Prefix = `Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:`

// Encrypted is the encrypted_content of magpie's compaction item for a
// summary.
func Encrypted(summary string) string {
	return Mark + base64.StdEncoding.EncodeToString([]byte(summary))
}

// Summary is the summary in a compaction item's encrypted_content: ours
// is false for one magpie didn't make, and ok false for one of magpie's
// whose summary can't be read.
func Summary(encrypted string) (summary string, ours, ok bool) {
	b64, isOurs := strings.CutPrefix(encrypted, Mark)
	if !isOurs {
		return "", false, false
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", true, false
	}
	return string(b), true, true
}

// Message is what a compaction item's encrypted_content is read as: ours
// is false for one magpie didn't make (OpenAI's own, left as it is), and
// msg nil for one of magpie's whose summary can't be read, which is left
// out.
func Message(encrypted string) (msg map[string]any, ours bool) {
	summary, ours, ok := Summary(encrypted)
	if !ok {
		return nil, ours
	}
	return UserMessage(Prefix + "\n" + summary), true
}

// UserMessage is a Responses input message of the user's.
func UserMessage(text string) map[string]any {
	return map[string]any{"type": "message", "role": "user",
		"content": []map[string]any{{"type": "input_text", "text": text}}}
}
