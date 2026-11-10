package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A model can also loop across turns: it asks for the same tool call, the
// agent runs it and sends back the same result, and the model asks for it
// again. Codex Desktop on WorkBuddy's deepseek-v4-pro read the same code
// 308 times in half an hour, and 340 times that evening, 537 credits for
// nothing (audie on Discord). Every turn of such a loop carries the whole
// conversation, so the loop is in the request itself: its newest turns are
// the same calls with the same arguments answered with the same results.
//
// stoppedToolLoop answers a request whose last toolLoopRepeats turns are
// all that one exchange here, in the agent's own API, with a notice that
// ends the agent's turn, and sends it nowhere. A turn that got a different
// result (a poll whose answer moved, a file that changed) is progress and
// starts the count again, as does anything the user says: the notice
// itself is a turn without calls, so the user's next word goes on with
// the conversation. It is on with Settings' Stop looping replies
// (settings.NoLoopGuard off), as loopGuard is.

// toolLoopRepeats is how many turns in a row of the same calls answered
// with the same results stop the next: the ninth is answered here.
const toolLoopRepeats = 8

// toolLoopWho is who answered such a turn, as the Routing view names it.
const toolLoopWho = "Tool loop stopped"

// toolResultMarks are what each API's tool result is written with, for a
// body with fewer than toolLoopRepeats of them to be let by unparsed.
var toolResultMarks = map[provider.Protocol][][]byte{
	provider.Responses: {[]byte(`_output"`)}, // function_call_output, custom_tool_call_output, …
	provider.Chat:      {[]byte(`"tool_call_id"`)},
	provider.Anthropic: {[]byte(`"tool_result"`)},
	provider.Gemini:    {[]byte(`"functionResponse"`), []byte(`"function_response"`)},
}

// stoppedToolLoop answers r here when its conversation is stuck in a tool
// loop, and says whether it did.
func (s *Server) stoppedToolLoop(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte) bool {
	if settings.Load().NoLoopGuard || !mayLoop(from, body) {
		return false
	}
	req, err := parse(from, tailOf(from, body))
	if err != nil {
		return false // serve says what is wrong with it
	}
	loop, ok := toolLoopOf(req.Messages)
	if !ok {
		return false
	}
	why := loop.why()
	s.answeredHere(r, from, body, toolLoopWho, why, func() {
		writeLocalText(w, from, req, loop.notice(conversationLang(from, body)))
	})
	return true
}

// mayLoop: body has results enough to hold a loop.
func mayLoop(from provider.Protocol, body []byte) bool {
	n := 0
	for _, m := range toolResultMarks[from] {
		n += bytes.Count(body, m)
	}
	return n >= toolLoopRepeats
}

// conversationKey is where each API keeps a request's conversation.
var conversationKey = map[provider.Protocol]string{
	provider.Responses: "input",
	provider.Chat:      "messages",
	provider.Anthropic: "messages",
	provider.Gemini:    "contents",
}

// tailItems is how many of a conversation's newest messages (a Responses
// input's items) are read for a loop: toolLoopRepeats turns are 16
// messages, or 56 Responses items with reasoning and three calls a turn.
// A loop of more calls a turn than fit isn't seen.
const tailItems = 8 * toolLoopRepeats

// tailOf is body with only the newest tailItems of its conversation, for
// the guard to parse a turn's end and not the whole of a long one, which
// took ~30ms and 20MB a turn of 1.5MB; gjson finds the items without
// decoding them. A body whose conversation isn't such a list is as it came.
func tailOf(from provider.Protocol, body []byte) []byte {
	list := getIn(body, conversationKey[from])
	if !list.IsArray() || list.Index <= 0 {
		return body
	}
	var items []gjson.Result
	list.ForEach(func(_, v gjson.Result) bool {
		items = append(items, v)
		return true
	})
	if len(items) <= tailItems {
		return body
	}
	// a slice made to its size, not a bytes.Buffer: under -race the
	// Buffer's Grow allocates its slice twice
	b := make([]byte, 0, len(body)-list.Index-len(list.Raw)+items[len(items)-1].Index+len(items[len(items)-1].Raw)-items[len(items)-tailItems].Index+list.Index+2)
	b = append(b, body[:list.Index]...)
	b = append(b, '[')
	for i, v := range items[len(items)-tailItems:] {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, v.Raw...)
	}
	b = append(b, ']')
	return append(b, body[list.Index+len(list.Raw):]...)
}

// toolLoop is a conversation's newest exchange and how many turns in a row
// it was made.
type toolLoop struct {
	calls []Part // the turn's calls, for the notice to name
	turns int
}

// toolLoopOf is the loop msgs end in, when their last toolLoopRepeats
// turns are one exchange: the same calls answered with the same results.
func toolLoopOf(msgs []Message) (toolLoop, bool) {
	turns := exchanges(msgs)
	if len(turns) < toolLoopRepeats {
		return toolLoop{}, false
	}
	last := turns[len(turns)-1]
	if allWaits(last.calls) {
		return toolLoop{}, false
	}
	n := 0
	for i := len(turns) - 1; i >= 0 && turns[i].sum == last.sum; i-- {
		n++
	}
	if n < toolLoopRepeats {
		return toolLoop{}, false
	}
	return toolLoop{calls: last.calls, turns: n}, true
}

// A call made to wait (clock__sleep {"duration_ms":60000} while CI runs)
// is asked again with the same arguments and answered the same by design;
// the time it takes is the progress, which a request doesn't carry. A turn
// whose calls are all waits is never a loop. A turn that waits and also
// runs another call is judged as before.

// waitWords are the words in a tool's name that make it a wait.
var waitWords = map[string]bool{"sleep": true, "wait": true, "delay": true, "pause": true, "poll": true}

// durationKeys are the argument names of a bare wait.
var durationKeys = map[string]bool{"duration": true, "duration_ms": true, "duration_s": true, "duration_seconds": true, "ms": true, "milliseconds": true, "seconds": true, "secs": true, "sec": true, "timeout": true, "timeout_ms": true, "delay": true, "delay_ms": true, "minutes": true}

func allWaits(calls []Part) bool {
	for _, c := range calls {
		if !isWait(c) {
			return false
		}
	}
	return len(calls) > 0
}

// isWait: the tool's own name (after any server__ or namespace prefix, so
// a server called "wait" doesn't make its tools waits) has a wait word, or
// its arguments are only a duration.
func isWait(c Part) bool {
	name := c.Name
	for _, sep := range []string{"__", ".", "/", ":"} {
		if i := strings.LastIndex(name, sep); i >= 0 {
			name = name[i+len(sep):]
		}
	}
	// split on anything but a letter, and before a capital (waitForCi)
	var words []string
	cur := []rune{}
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			flush()
			cur = append(cur, r)
		case r >= 'a' && r <= 'z':
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	for _, w := range words {
		if waitWords[w] {
			return true
		}
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(c.Args, &args) != nil || len(args) == 0 {
		return false
	}
	for k, v := range args {
		var n float64
		if !durationKeys[strings.ToLower(k)] || json.Unmarshal(v, &n) != nil {
			return false
		}
	}
	return true
}

// exchange is one turn of tool use: the assistant's calls and the results
// the agent sent back, summed without their ids, which differ every turn.
type exchange struct {
	sum   uint64
	calls []Part
}

// exchanges are the turns of tool use msgs end in, oldest first, back to
// the newest turn that wasn't one: an assistant turn with no call, or a
// user turn with no result. Consecutive messages of one role are one turn
// (a Responses reasoning item and its call, a call's several outputs).
func exchanges(msgs []Message) []exchange {
	type run struct {
		role  string
		parts []Part
	}
	var runs []run
	for _, m := range msgs {
		if n := len(runs); n > 0 && runs[n-1].role == m.Role {
			runs[n-1].parts = append(runs[n-1].parts, m.Parts...)
			continue
		}
		runs = append(runs, run{m.Role, append([]Part(nil), m.Parts...)})
	}
	var out []exchange
	for i := len(runs) - 1; i >= 1; i -= 2 {
		said, asked := runs[i], runs[i-1]
		if said.role != "user" || asked.role != "assistant" {
			break
		}
		e, ok := exchangeOf(asked.parts, said.parts)
		if !ok {
			break
		}
		out = append(out, e)
	}
	slices.Reverse(out)
	return out
}

// exchangeOf sums one turn: each call by its name and arguments, with the
// result the agent gave it, and anything else the user's side said beside
// the results (a reminder an agent adds to each, a word of the user's).
func exchangeOf(asked, said []Part) (exchange, bool) {
	var calls []Part
	for _, p := range asked {
		if p.Kind == ToolCall {
			calls = append(calls, p)
		}
	}
	results := map[string][]string{}
	var rest []string
	hasResult := false
	for _, p := range said {
		switch p.Kind {
		case ToolResult:
			hasResult = true
			results[p.CallID] = append(results[p.CallID], resultSum(p))
		case Text:
			rest = append(rest, "text\x00"+p.Text)
		default:
			rest = append(rest, string(p.Kind)+"\x00"+p.Text+p.Data+p.URL)
		}
	}
	if len(calls) == 0 || !hasResult {
		return exchange{}, false
	}
	keys := make([]string, 0, len(calls)+1)
	for _, c := range calls {
		got := results[c.ID]
		delete(results, c.ID)
		keys = append(keys, c.Name+"\x00"+steadyArgs(c.Args)+"\x00"+strings.Join(got, "\x01"))
	}
	for _, got := range results { // results of no call here
		keys = append(keys, "\x00\x00"+strings.Join(got, "\x01"))
	}
	slices.Sort(keys)
	h := fnv.New64a()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0xff})
	}
	for _, k := range rest {
		h.Write([]byte(k))
		h.Write([]byte{0xfe})
	}
	return exchange{sum: h.Sum64(), calls: calls}, true
}

// steadyArgs is a call's arguments with their keys in one order, as two
// turns asking the same may write them differently.
func steadyArgs(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// resultSum is a result as told apart from another: its text without what
// an agent writes of the run itself rather than of what it found, and its
// images.
func resultSum(p Part) string {
	var b strings.Builder
	if p.IsError {
		b.WriteString("error\x00")
	}
	b.WriteString(steadyResult(p.Text))
	for _, im := range p.Images {
		b.WriteString("\x00" + im.MediaType + im.Data + im.URL)
	}
	return b.String()
}

// runNoise is what an agent's result says of the run, which differs every
// time the same command is run: Codex's wall time ("Wall time: 0.0246
// seconds", its exec's "Wall time 0.1 seconds"), its unified exec's chunk
// id, and its shell's duration_seconds.
var runNoise = []struct {
	mark string // in a result the expression may change
	re   *lazyre.RE
	with string
}{
	{"Wall time", lazyre.New(`(?m)^(Wall time:? )[0-9.]+( seconds?)`), "${1}#${2}"},
	{"Chunk ID: ", lazyre.New(`(?m)^(Chunk ID: )\S+`), "${1}#"},
	{`"duration_seconds"`, lazyre.New(`("duration_seconds"\s*:\s*)[0-9.eE+-]+`), "${1}#"},
}

func steadyResult(s string) string {
	for _, n := range runNoise {
		if strings.Contains(s, n.mark) {
			s = n.re.ReplaceAllString(s, n.with)
		}
	}
	return s
}

// called is a turn's calls as the notice names them, each cut short.
func (l toolLoop) called() string {
	names := make([]string, 0, len(l.calls))
	for _, c := range l.calls {
		args := steadyArgs(c.Args)
		if r := []rune(args); len(r) > 120 {
			args = string(r[:120]) + "…"
		}
		names = append(names, c.Name+" "+args)
	}
	return strings.Join(names, "; ")
}

// why is what the Routing, Usage and Sessions views say of the turn.
func (l toolLoop) why() string {
	return fmt.Sprintf("tool loop: the same tool call got the same result %d turns in a row (%s); answered by magpie, sent nowhere", l.turns, l.called())
}

// notice is what the agent shows the user, as the turn's answer, in lang
// (conversationLang). It goes to the vendor with the conversation's next
// turn, so it names neither magpie nor a vendor's limits; the setting is
// named as the GUI in that language names it (Korean, which the GUI
// hasn't, as English does).
func (l toolLoop) notice(lang string) string {
	f, ok := toolLoopNotices[lang]
	if !ok {
		f = toolLoopNotices[langEN]
	}
	return fmt.Sprintf(f, l.turns, l.called())
}

// toolLoopNotices are the notice in each language, given the turns and
// the calls.
var toolLoopNotices = map[string]string{
	langEN:   "This turn was stopped before it reached the model: the agent ran the same tool call %d times in a row and got the same result every time (%s). The model is stuck repeating it, and each further turn would be one more request with no progress, so this turn was sent nowhere. Tell the agent how to go on and the conversation continues; Settings › Models › Replies › Stop looping replies turns this check off.",
	langZH:   "这一轮在发给模型之前已被停止：Agent 连续 %d 次调用了同一个工具，每次得到的结果都一样（%s）。模型陷入了重复，再继续每一轮都只是多一次请求而没有进展，所以这一轮没有发出。告诉 Agent 接下来怎么做，对话就会继续；在 设置 › 模型 › 回复 › 停止循环的回复 中可以关闭这项检查。",
	langZHTW: "這一輪在送給模型之前已被停止：Agent 連續 %d 次呼叫了同一個工具，每次得到的結果都一樣（%s）。模型陷入了重複，再繼續每一輪都只是多一次請求而沒有進展，所以這一輪沒有送出。告訴 Agent 接下來怎麼做，對話就會繼續；在 設定 › 模型 › 回覆 › 停止循環的回覆 中可以關閉這項檢查。",
	langJA:   "このターンはモデルに送る前に止めました：エージェントが同じツール呼び出しを %d 回続けて実行し、毎回同じ結果を受け取りました（%s）。モデルが同じことを繰り返しており、続けても進展のないリクエストが増えるだけなので、このターンはどこにも送っていません。次にどうするかをエージェントに伝えれば会話は続きます。設定 › モデル › 応答 › ループする応答を止める でこのチェックをオフにできます。",
	langKO:   "이 턴은 모델에 보내기 전에 중단되었습니다: 에이전트가 같은 도구 호출을 %d번 연속으로 실행했고 매번 같은 결과를 받았습니다(%s). 모델이 같은 일을 반복하고 있어 계속해도 진전 없이 요청만 늘어나므로, 이 턴은 어디에도 보내지 않았습니다. 에이전트에게 어떻게 진행할지 알려 주면 대화가 이어집니다. Settings › Models › Replies › Stop looping replies 에서 이 검사를 끌 수 있습니다.",
	langDE:   "Dieser Zug wurde angehalten, bevor er das Modell erreichte: Der Agent hat denselben Tool-Aufruf %d-mal hintereinander ausgeführt und jedes Mal dasselbe Ergebnis erhalten (%s). Das Modell wiederholt sich, und jeder weitere Zug wäre eine weitere Anfrage ohne Fortschritt, daher wurde dieser Zug nirgendwohin gesendet. Sag dem Agenten, wie es weitergeht, und das Gespräch läuft weiter; Einstellungen › Modelle › Antworten › Schleifende Antworten beenden schaltet diese Prüfung aus.",
}

// writeLocalText answers req with text in from's API, as a whole reply or
// a stream as it asked: a finished assistant turn with no call in it.
func writeLocalText(w http.ResponseWriter, from provider.Protocol, req *Request, text string) {
	events := make(chan Event, 4)
	events <- Event{Kind: KStart, MsgID: fmt.Sprintf("msg_magpie_%d", time.Now().UnixNano()), Model: req.Model}
	events <- Event{Kind: KText, Text: text}
	events <- Event{Kind: KStop, Stop: "stop"}
	close(events)
	relayStatus(w, from, "magpie", req, events, &Usage{}, func() {})
}
