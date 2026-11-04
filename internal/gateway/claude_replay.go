package gateway

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// replaySession is a saved session written for a conversation no run has
// that Claude Code's interactive mode will go on with: Claude Code's own
// transcript of the conversation as far as its last reply, which a run
// started with --resume on it loads, so that only the turn after the reply
// is typed. Told whole, the conversation was the run's first message,
// pasted: a run lost to an update told 229 messages and 3 images that way
// was refused, and every try of it rested an account (#4778), and 25
// images of earlier turns were pasted one by one until the paste timed
// out (#4780). In the transcript, images are image blocks, as Claude Code
// keeps a pasted one.
//
// The session is two lines, the ones a run started at the reply's turn
// would have written: the user's, the conversation before the reply as
// renderClaudePrompt tells it, every turn of it as history (none marked as
// the one to answer: that is the turn typed after the reply; the caller's
// instructions not in it when the run has them after Claude Code's system
// prompt, req.appendPrompt), and the assistant's, the reply's words. nil
// when there is no such reply (none that calls no tool, or one with no
// words), or the file can't be written; the run is then told the
// conversation whole.
func replaySession(dirs []string, work, model string, req *Request) *savedSession {
	split := historyEnd(req.Messages)
	if split < 2 || len(dirs) == 0 {
		return nil
	}
	reply := replyWords(req.Messages[split-1])
	if reply == "" {
		return nil
	}
	before := *req
	before.Messages = req.Messages[:split-1]
	// Claude Code puts its own marks on what it sends
	before.clientCache = nil
	// every turn before the reply is history, the last of them the one the
	// reply answers: none is the turn to answer, which is typed after it.
	// Rendered as a first message (renderClaudePrompt's own split), the
	// turn the reply answers was marked <current_turn>, "the turn to answer
	// now", and the resumed run answered it again, or said its tool calls
	// had no results (愛睏柴犬阿茶 on Discord)
	blocks, err := renderClaudePromptAt(&before, len(before.Messages))
	if err != nil {
		return nil
	}
	for _, b := range blocks {
		delete(b, "cache_control")
	}
	sid, user, assistant := newUUID(), newUUID(), newUUID()
	at := time.Now().UTC()
	line := func(parent any, uuid, kind string, message map[string]any, at time.Time) []byte {
		b, _ := json.Marshal(map[string]any{
			"parentUuid":  parent,
			"isSidechain": false,
			"userType":    "external",
			"cwd":         work,
			"sessionId":   sid,
			"type":        kind,
			"message":     message,
			"uuid":        uuid,
			"timestamp":   at.Format("2006-01-02T15:04:05.000Z"),
		})
		return append(b, '\n')
	}
	lines := line(nil, user, "user", map[string]any{"role": "user", "content": blocks}, at)
	lines = append(lines, line(user, assistant, "assistant", map[string]any{
		"id":            "msg_" + strings.ReplaceAll(newUUID(), "-", ""),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []map[string]any{{"type": "text", "text": reply}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
	}, at.Add(time.Millisecond))...)
	// where Claude Code looks: its working folder is the work folder with
	// every link followed (/var is /private/var on a Mac)
	dir := dirs[len(dirs)-1]
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("claude: a conversation's transcript couldn't be written (%v); a new Claude Code is told it whole", err)
		return nil
	}
	path := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(path, lines, 0o600); err != nil {
		_ = os.Remove(path)
		log.Printf("claude: a conversation's transcript couldn't be written (%v); a new Claude Code is told it whole", err)
		return nil
	}
	log.Printf("claude: a new Claude Code goes on with the conversation from a transcript of it (%d messages, %d images), typed the last turn's %d", split, countImages(req.Messages[:split]), len(req.Messages)-split)
	return &savedSession{session: sid, sessions: dirs, since: req.Messages[split:], tui: true, appended: req.appendPrompt}
}

// replyWords is what a reply says, its thinking left out.
func replyWords(m Message) string {
	var words []string
	for _, p := range m.Parts {
		if p.Kind == Text && strings.TrimSpace(p.Text) != "" {
			words = append(words, p.Text)
		}
	}
	return strings.Join(words, "\n\n")
}
