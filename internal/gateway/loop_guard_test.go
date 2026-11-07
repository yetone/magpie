package gateway

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// deepseekLoop is the reporter's reasoning, line for line as dsh showed it
// under 思考 (#1359, Moody-Sin): DeepSeek Max's last lines, each its own
// paragraph, a round of seven changing a word or two from one to the next.
var deepseekLoop = []string{
	"Now.", "Writing.", "Go.", "Let me write.", "OK.", "Here.", "Produce.",
	"Now.", "Writing.", "Final.", "Go.", "Let me write.", "OK.", "Producing.",
	"Now.", "Writing.", "Go.", "Let me write.", "OK.", "Here.", "Produce.",
	"Now.", "Writing.", "Final.", "Go.", "Let me write.", "OK.", "Producing.",
}

// reasoningBefore is the kind of reasoning a reply has before it loops,
// which isn't one.
const reasoningBefore = `The user wants the config loader to accept both YAML and TOML. Let me look at how parse() picks the format today: it switches on the extension, and an unknown one falls through to JSON.
So I need a case for ".toml" and a decoder for it. The repo already depends on BurntSushi/toml for the plugin manifests, so no new dependency.
Edge cases: an uppercase extension (".TOML"), a file with no extension, and a TOML file that fails to parse should report the line number.
I'll write the change now.
`

// pieces streams s as a vendor does: in pieces a few characters long,
// which cut words and lines anywhere.
func pieces(s string, seed int64) []string {
	r := rand.New(rand.NewSource(seed))
	var out []string
	for len(s) > 0 {
		n := min(1+r.Intn(7), len(s))
		for n < len(s) && !utf8.RuneStart(s[n]) {
			n++ // a JSON string carries whole characters
		}
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}

// loopText is n of the reporter's lines, cycling, each its own paragraph.
func loopText(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(deepseekLoop[i%len(deepseekLoop)] + "\n\n")
	}
	return b.String()
}

func chatEvent(field, s string) string {
	b, _ := json.Marshal(map[string]any{"id": "c1", "object": "chat.completion.chunk", "model": "deepseek-v4-max",
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{field: s}}}})
	return "data: " + string(b) + "\n\n"
}

// feedAll feeds text to a guard as a channel's events, and says after how
// many of its units the guard found a loop, -1 for none.
func feedAll(t *testing.T, g *loopGuard, field, text string) (loopTrip, bool) {
	t.Helper()
	for _, c := range pieces(text, 7) {
		if trip, ok := g.feed([]byte(chatEvent(field, c))); ok {
			return trip, true
		}
	}
	return loopTrip{}, false
}

// The reporter's loop is found in the reasoning, however its pieces are
// cut, and soon: within the window's 256 units after it begins.
func TestLoopGuardFindsTheReportersLoop(t *testing.T) {
	g := &loopGuard{}
	if _, ok := feedAll(t, g, "reasoning_content", reasoningBefore); ok {
		t.Fatal("found a loop in ordinary reasoning")
	}
	var lines int
	for lines = 0; lines < 2000; lines++ {
		if trip, ok := g.feed([]byte(chatEvent("reasoning_content", deepseekLoop[lines%len(deepseekLoop)]+"\n\n"))); ok {
			if !trip.reasoning || len(trip.units) > loopDistinct || trip.units[0] != "Now." && trip.units[0] != "Writing." {
				t.Fatalf("trip %+v", trip)
			}
			msg := trip.message()
			for _, w := range []string{`"Now."`, `"Writing."`, "reasoning", "loop", "Stop looping replies"} {
				if !strings.Contains(msg, w) {
					t.Errorf("message %q doesn't say %s", msg, w)
				}
			}
			break
		}
	}
	if lines >= 2000 {
		t.Fatal("no loop found in 2000 of the reporter's lines")
	}
	if lines > loopWindow+4 {
		t.Fatalf("found only after %d lines", lines)
	}

	// as text too, cut anywhere; on one line, sentence after sentence;
	// and in Chinese
	for name, text := range map[string]string{
		"pieces":   loopText(600),
		"one line": strings.ReplaceAll(loopText(600), "\n\n", " "),
		"chinese":  strings.Repeat("好。现在写。开始。让我写。好的。这里。输出。现在写。最终。开始。", 60),
	} {
		g := &loopGuard{}
		trip, ok := feedAll(t, g, "content", text)
		if !ok || trip.reasoning {
			t.Errorf("%s: trip %+v %v", name, trip, ok)
		}
	}
}

// A loop with no break in it, no line or sentence's end (#1489, bfxh: "the
// model loops, saying mcp over and over"), is found too, in the reasoning
// or the text, within some 2,000 tokens of it: run on, a unit is cut only
// every unitMost bytes, and 256 of them would be far past any reply's
// limit.
func TestLoopGuardFindsARunOnLoop(t *testing.T) {
	for name, loop := range map[string]string{
		"spaced":      "mcp ",
		"commas":      "mcp, MCP, ",
		"no space":    "mcp",
		"a few words": "call the mcp tool then call the mcp ",
		"chinese":     "重复说mcp",
	} {
		for _, field := range []string{"reasoning_content", "content"} {
			g := &loopGuard{}
			before := "Let me look at the MCP servers configured for this project and pick the one that lists files"
			trip, ok := feedAll(t, g, field, before+" "+strings.Repeat(loop, 20000/len(loop)))
			if !ok || !trip.runOn || trip.reasoning != (field == "reasoning_content") {
				t.Errorf("%s as %s: trip %+v %v", name, field, trip, ok)
				continue
			}
			msg := trip.message()
			if !strings.Contains(msg, "mcp") || !strings.Contains(msg, "without a break") || !strings.Contains(msg, "Stop looping replies") {
				t.Errorf("%s: message %q", name, msg)
			}
			if trip.window > 3*unitMost {
				t.Errorf("%s: found only after %d characters", name, trip.window)
			}
		}
	}
	// broken into lines longer than a unit, it is still found
	g := &loopGuard{}
	if _, ok := feedAll(t, g, "content", strings.Repeat(strings.Repeat("mcp ", 1500)+"\n", 4)); !ok {
		t.Error("a run-on loop in long lines was not found")
	}
}

// Every protocol's reasoning and text are read: Anthropic's thinking and
// text deltas, Responses' reasoning and output text, Chat's
// reasoning_content and reasoning, Gemini's thought parts.
func TestLoopGuardReadsEveryProtocol(t *testing.T) {
	for name, ev := range map[string]func(s string) string{
		"anthropic thinking": func(s string) string {
			b, _ := json.Marshal(s)
			return `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":` + string(b) + `}}` + "\n\n"
		},
		"anthropic text": func(s string) string {
			b, _ := json.Marshal(s)
			return `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":` + string(b) + `}}` + "\n\n"
		},
		"responses reasoning": func(s string) string {
			b, _ := json.Marshal(s)
			return `event: response.reasoning_summary_text.delta` + "\n" + `data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":` + string(b) + `}` + "\n\n"
		},
		"responses reasoning text": func(s string) string {
			b, _ := json.Marshal(s)
			return `data: {"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":` + string(b) + `}` + "\r\n\r\n"
		},
		"responses text": func(s string) string {
			b, _ := json.Marshal(s)
			return `event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":` + string(b) + `}` + "\n\n"
		},
		"chat reasoning":         func(s string) string { return chatEvent("reasoning", s) },
		"chat reasoning_content": func(s string) string { return chatEvent("reasoning_content", s) },
		"chat content":           func(s string) string { return chatEvent("content", s) },
		"gemini thought": func(s string) string {
			b, _ := json.Marshal(s)
			return `data: {"candidates":[{"content":{"role":"model","parts":[{"text":` + string(b) + `,"thought":true}]}}]}` + "\r\n\r\n"
		},
		"gemini text": func(s string) string {
			b, _ := json.Marshal(s)
			return `data: {"candidates":[{"content":{"role":"model","parts":[{"text":` + string(b) + `}]}}]}` + "\n\n"
		},
	} {
		g := &loopGuard{}
		var found bool
		for _, c := range pieces(loopText(400), 3) {
			if trip, ok := g.feed([]byte(ev(c))); ok {
				found = true
				if want := strings.Contains(name, "thinking") || strings.Contains(name, "reasoning") || strings.Contains(name, "thought"); trip.reasoning != want {
					t.Errorf("%s: read as reasoning %v", name, trip.reasoning)
				}
				break
			}
		}
		if !found {
			t.Errorf("%s: loop not found", name)
		}
	}
	// a call's arguments are no reply text: a tool writing a file of
	// repeated lines isn't stopped
	g := &loopGuard{}
	for _, c := range pieces(loopText(800), 5) {
		b, _ := json.Marshal(c)
		for _, ev := range []string{
			`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":` + string(b) + `}}` + "\n\n",
			`data: {"type":"response.function_call_arguments.delta","delta":` + string(b) + `}` + "\n\n",
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + string(b) + `}}]}}]}` + "\n\n",
		} {
			if _, ok := g.feed([]byte(ev)); ok {
				t.Fatalf("a call's arguments read as a loop: %s", ev)
			}
		}
	}
}

// What ordinary replies are made of is never taken for a loop, as
// reasoning or as text: magpie's own source (gateway.go's 4,900 lines,
// the GUI's 20,000 of JavaScript and its five languages of strings), its
// docs and their tables, a log, a numbered list, a CSV, a hex table,
// base64 and a run of rules and braces.
func TestLoopGuardLeavesOrdinaryRepliesAlone(t *testing.T) {
	if raceOn {
		// one goroutine feeding one loopGuard, so the race detector has
		// nothing to find, and it made this corpus take 104s of the
		// gateway's race run on an M5 Pro, 7s without (#5361); the plain
		// Test step reads all of it
		t.Skip("one goroutine: no race to find, and 14 times as slow under -race")
	}
	root := filepath.Join("..", "..")
	var texts = map[string]string{}
	for _, f := range []string{
		"internal/gateway/gateway.go", "internal/gateway/fallback.go", "internal/gateway/anthropic.go",
		"internal/gui/assets/app.js", "internal/gui/assets/i18n.js", "internal/gui/assets/app.css", "internal/gui/assets/plugins/routing/client.js", "internal/gui/assets/plugins/sessions/client.js", "internal/gui/assets/index.html",
		"docs/code-standards.md", "LESSONS.md", "docs/subsystems/gateway-routing.md", "docs/subsystems/providers-accounts.md",
		"go.sum",
	} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		// app.js and i18n.js run to megabytes; 256K of each is still thousands
		// of lines, many times the window, and keeps -race on CI quick
		if len(b) > 256<<10 {
			b = b[:bytes.LastIndexByte(b[:256<<10], '\n')+1]
		}
		texts[f] = string(b)
	}
	var log, list, csv, hex, rules strings.Builder
	at := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&log, "%s INFO gateway: POST /v1/messages 200 in %dms\n", at.Add(time.Duration(i)*1300*time.Millisecond).Format(time.RFC3339Nano), 80+i%400)
		fmt.Fprintf(&list, "%d. Check the item and mark it done.\n", i+1)
		fmt.Fprintf(&csv, "%d,ok,ok,true,%d\n", i, i*7%13)
		fmt.Fprintf(&hex, "    0x00, 0x00, 0x00, 0x00, 0xff, 0xff, 0x00, 0x00,\n")
		rules.WriteString("}\n  }\n---\n| --- | --- |\n\n")
	}
	raw := make([]byte, 60000)
	rand.New(rand.NewSource(1)).Read(raw)
	texts["log"], texts["list"], texts["csv"], texts["hex"], texts["rules"] = log.String(), list.String(), csv.String(), hex.String(), rules.String()
	texts["base64"] = base64.StdEncoding.EncodeToString(raw)
	texts["base64 lines"] = base64.StdEncoding.EncodeToString(raw)
	for i := 76; i < len(texts["base64 lines"]); i += 77 {
		texts["base64 lines"] = texts["base64 lines"][:i] + "\n" + texts["base64 lines"][i:]
	}
	// with no break at all, as a model writes a long paragraph, JSON or
	// minified code on one line (#1489's run-on loops are found by how
	// few its words are)
	for _, f := range []string{"internal/gateway/gateway.go", "LESSONS.md", "internal/gui/assets/i18n.js", "log", "csv", "hex", "rules"} {
		texts[f+" on one line"] = strings.Join(strings.Fields(texts[f]), " ")
	}
	var records strings.Builder
	records.WriteString("[")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&records, `{"id":%d,"type":"file","done":false},`, i)
	}
	texts["json records"] = records.String() + "]"
	for name, text := range texts {
		for _, field := range []string{"reasoning_content", "content"} {
			g := &loopGuard{}
			if trip, ok := feedAll(t, g, field, text); ok {
				t.Errorf("%s as %s: taken for a loop: %s", name, field, trip.message())
			}
		}
	}
}

// loopingVendor is DeepSeek on its Chat API, its reasoning looping until
// the request is let go, or, with lines set, for that many lines and then
// the reply's end. It says whether the request was let go.
//
// Looping until let go, it sends loopMost lines and then waits, its stream
// open, for its request to be let go. How many lines it has sent by the
// time it sees that isn't how soon the guard cut: the lines it wrote ahead
// into the sockets' and transports' buffers, which the gateway hadn't read
// yet, count too. A gateway slowed by load let it get ~2,800 lines ahead
// of a cut made at the 259th (#1443). Stopping at loopMost makes being let
// go mean the guard found the loop within that many lines, however the
// test is scheduled; loopCut bounds how soon by what reached the agent.
type loopingVendor struct {
	reasoning string // what it reasons, else the reporter's loop
	lines     int    // 0: loop until let go
	loops     int    // how many of its requests loop, 0 for every one
	said      string // a text it says before its reasoning loops
	asked     atomic.Int64
	sent      atomic.Int64
	letGo     chan bool // each request's, sized for every one it gets
}

// loopMost is how many lines of its loop loopingVendor sends before it
// waits to be let go.
const loopMost = 2000

func (v *loopingVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	asked := v.asked.Add(1)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	f := w.(http.Flusher)
	io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4-max","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""}}]}`+"\n\n")
	send := func(s string) bool {
		if _, err := io.WriteString(w, s); err != nil {
			return false
		}
		f.Flush()
		return r.Context().Err() == nil
	}
	send(chatEvent("reasoning_content", reasoningBefore))
	if v.said != "" {
		send(chatEvent("content", v.said))
	}
	if v.loops > 0 && asked > int64(v.loops) {
		send(chatEvent("reasoning_content", "So: a case for .toml in parse(), and its test."))
	} else if v.reasoning != "" {
		for _, c := range pieces(v.reasoning, 11) {
			if !send(chatEvent("reasoning_content", c)) {
				v.letGo <- true
				return
			}
		}
	} else {
		for i := 0; v.lines == 0 || i < v.lines; i++ {
			if v.lines == 0 && i == loopMost {
				select {
				case <-r.Context().Done():
					v.letGo <- true
				case <-time.After(20 * time.Second):
					v.letGo <- false
				}
				return
			}
			v.sent.Add(1)
			if !send(chatEvent("reasoning_content", deepseekLoop[i%len(deepseekLoop)]+"\n\n")) {
				v.letGo <- true
				return
			}
			if i%64 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
	}
	send(chatEvent("content", "Done: parse() now takes .toml."))
	send(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":900,"total_tokens":940}}` + "\n\n")
	send("data: [DONE]\n\n")
	v.letGo <- false
}

// loopAsks are a streamed request on each of the agents' protocols, and
// how each one's stream says it ended well, and with magpie's error; asks
// is how many times the vendor is asked when every reply loops: once, and
// again once (loopReasks), whether magpie translates the reply to the
// agent's protocol or passes a Chat stream through as it comes (#2436).
var loopAsks = []struct {
	name, path, body string
	done, failed     string
	asks             int
}{
	{"chat", "/v1/chat/completions", `{"model":"ds/deepseek-v4-max","stream":true,"messages":[{"role":"user","content":"add TOML"}]}`,
		"data: [DONE]", `data: {"error":{"message":"magpie ended this reply`, 2},
	{"anthropic", "/v1/messages", `{"model":"ds/deepseek-v4-max","max_tokens":32000,"stream":true,"messages":[{"role":"user","content":"add TOML"}]}`,
		`"type":"message_stop"`, "event: error\ndata: {\"error\":{", 2},
	{"responses", "/v1/responses", `{"model":"ds/deepseek-v4-max","stream":true,"input":"add TOML"}`,
		`"type":"response.completed"`, `"type":"response.failed"`, 2},
	{"gemini", "/v1beta/models/ds/deepseek-v4-max:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"add TOML"}]}]}`,
		`"finishReason":"STOP"`, `"status":"UNAVAILABLE"`, 2},
}

func deepseekOn(t *testing.T, v *loopingVendor) string {
	t.Helper()
	fresh(t)
	vendor := httptest.NewServer(v)
	t.Cleanup(vendor.Close)
	if err := provider.Save(provider.Provider{ID: "ds", Name: "DeepSeek", Key: "k", Chat: vendor.URL + "/v1", Models: []string{"deepseek-v4-max"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)
	return gw.URL
}

func askStream(t *testing.T, url, body string) string {
	t.Helper()
	res, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	var out strings.Builder
	rd := bufio.NewReader(res.Body)
	done := make(chan struct{})
	go func() {
		io.Copy(&out, rd)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("the stream never ended")
	}
	return out.String()
}

// The reporter's loop, through the gateway on each of the agents'
// protocols: the agent gets the reasoning so far and then its stream's
// error, saying why; the vendor's request is let go; nobody rests; and
// the usage log keeps it as reply_loop.
func TestLoopEndsTheReplyOnEveryProtocol(t *testing.T) {
	for _, x := range loopAsks {
		t.Run(x.name, func(t *testing.T) {
			v := &loopingVendor{letGo: make(chan bool, 4)}
			gw := deepseekOn(t, v)
			loopCut(t, v, askStream(t, gw+x.path, x.body), x.done, x.failed, "ds", x.asks)
		})
	}
}

// loopCut checks what the agent got of a looping reply, got, and what came
// of it: the stream's error, not its end (done); the vendor asked asks
// times and let go each time; nobody resting; the usage record's
// reply_loop, for provider.
func loopCut(t *testing.T, v *loopingVendor, got, done, failed, provider string, asks int) {
	t.Helper()
	if !strings.Contains(got, failed) || !strings.Contains(got, "stuck in a loop") || strings.Contains(got, done) {
		t.Fatalf("the stream doesn't end with magpie's error:\n%s", got[max(len(got)-1500, 0):])
	}
	if !strings.Contains(got, "Producing") {
		t.Fatal("the reasoning before the loop was found didn't reach the agent")
	}
	for range asks {
		select {
		case letGo := <-v.letGo:
			if !letGo {
				t.Fatalf("the vendor's request was not let go (%d lines)", v.sent.Load())
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the vendor's request was not let go")
		}
	}
	if n := v.asked.Load(); n != int64(asks) {
		t.Fatalf("the vendor was asked %d times, want %d", n, asks)
	}
	// the guard cut within its window of the loop's start: what reached
	// the agent of the loop is that window and at most one 32K read of the
	// vendor's stream more, which the cut's write sends whole. It doesn't
	// depend on how far the vendor got ahead of the gateway (#1443).
	if n := loopLines(got); n > asks*(loopWindow+(32<<10)/100) {
		t.Fatalf("the loop was cut only after %d of its lines reached the agent", n)
	}
	restingUntil.Lock()
	rests := len(restingUntil.m)
	restingUntil.Unlock()
	if rests != 0 {
		t.Fatal("the provider rests for its model's loop")
	}
	var rec usage.Record
	for i := 0; i < 50; i++ {
		if recs := usage.Load(time.Time{}); len(recs) > 0 {
			rec = recs[len(recs)-1]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rec.ErrType != loopErrType || !strings.Contains(rec.Error, "stuck in a loop") || rec.Provider != provider {
		t.Fatalf("usage record %+v", rec)
	}
	// a reply asked again counts its tries, in place or not (#2436: five
	// reply_loop rows, tries empty)
	if want := asks; asks == 1 && rec.Tries != 0 || asks > 1 && rec.Tries != want {
		t.Fatalf("usage record's tries %d, the vendor asked %d times", rec.Tries, asks)
	}
}

// loopLines counts the lines of the reporter's loop in what the agent got,
// each a delta of its own in any protocol.
func loopLines(got string) int {
	var n int
	for _, l := range []string{"Now.", "Writing.", "Go.", "Let me write.", "OK.", "Here.", "Produce.", "Final.", "Producing."} {
		n += strings.Count(got, `"`+l+`\n\n"`)
	}
	return n
}

// The reporter's route (#1359): WorkBuddy's DeepSeek v4.1 flash at max
// effort, from dsh and Pi. A WorkBuddy subscription is served by the
// community plugin's fetch in the plugin host (AGENTS.md, "Which code
// actually runs"); the loop is cut on every protocol, and the plugin's
// request to WorkBuddy is let go.
func TestLoopEndsWorkBuddysReply(t *testing.T) {
	const model = "deepseek-v4-pro" // the fake plugin's DeepSeek
	for _, x := range loopAsks {
		t.Run(x.name, func(t *testing.T) {
			v := &loopingVendor{letGo: make(chan bool, 4)}
			t.Setenv("FAKE_DEEPSEEK", "1")
			id := besideFake(t, "workbuddy", v)
			gw := httptest.NewServer(New().Handler())
			t.Cleanup(gw.Close)
			path := strings.Replace(x.path, "ds/deepseek-v4-max", id+"/"+model, 1)
			body := strings.Replace(x.body, `"model":"ds/deepseek-v4-max"`, `"model":"`+id+"/"+model+`","reasoning_effort":"max"`, 1)
			loopCut(t, v, askStream(t, gw.URL+path, body), x.done, x.failed, id, x.asks)
		})
	}
}

// A reply whose reasoning loops before it says anything is asked again
// once: the agent keeps the reasoning it has, the next try's follows it,
// and the reply ends whole, the loop no error (cdredfox on X: WorkBuddy's
// DeepSeek v4 flash now and then looping in a long turn, the agent's turn
// ended). Translated to the agent's protocol, it is asked again in place;
// a Chat stream passed through as it comes is let go at the end of the
// event the loop was found in, and the next try's stream follows it
// (#2436: pi's Chat to WorkBuddy was ended at once, its tries empty).
func TestReasoningLoopIsAskedAgain(t *testing.T) {
	for _, x := range loopAsks {
		t.Run(x.name, func(t *testing.T) {
			v := &loopingVendor{loops: 1, letGo: make(chan bool, 4)}
			gw := deepseekOn(t, v)
			got := askStream(t, gw+x.path, x.body)
			if !strings.Contains(got, x.done) || strings.Contains(got, "magpie ended this reply") || !strings.Contains(got, "parse() now takes .toml") {
				t.Fatalf("the reply asked again didn't end whole:\n%s", got[max(len(got)-1500, 0):])
			}
			if !strings.Contains(got, "Producing") || !strings.Contains(got, "a case for .toml in parse()") {
				t.Fatal("the agent didn't get both tries' reasoning")
			}
			if n := v.asked.Load(); n != 2 {
				t.Fatalf("the vendor was asked %d times, want 2", n)
			}
			// The looping try is let go (true) and the next ends whole
			// (false), in whichever order the two handlers get there: the
			// next can finish before the first sees its request cancelled.
			// Only the next try sends false at once; the looping one sends
			// false only after 20s unreleased.
			if a, b := <-v.letGo, <-v.letGo; a == b {
				t.Fatalf("let go: %v and %v, want the looping try let go and the next not", a, b)
			}
			if x.name == "chat" {
				// both tries' chunks, each whole: the first ends on an
				// event's end, not halfway through one
				for _, ln := range strings.Split(got, "\n") {
					if d, ok := strings.CutPrefix(ln, "data: "); ok && d != "[DONE]" && !json.Valid([]byte(d)) {
						t.Fatalf("a chunk spliced halfway: %q", ln)
					}
				}
			}
			var rec usage.Record
			for i := 0; i < 50; i++ {
				if recs := usage.Load(time.Time{}); len(recs) > 0 {
					rec = recs[len(recs)-1]
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if rec.Tries != 2 || rec.Error != "" || rec.ErrType != "" {
				t.Fatalf("usage record %+v, want an answer in 2 tries", rec)
			}
		})
	}
}

// A reply that has said something before its reasoning loops is ended, not
// asked again: the agent has its text, which a second try would say again.
func TestLoopAfterTextIsNotAskedAgain(t *testing.T) {
	for _, x := range loopAsks {
		t.Run(x.name, func(t *testing.T) {
			v := &loopingVendor{said: "Looking at parse() first.", letGo: make(chan bool, 4)}
			gw := deepseekOn(t, v)
			loopCut(t, v, askStream(t, gw+x.path, x.body), x.done, x.failed, "ds", 1)
		})
	}
}

// A long reply that doesn't loop goes through whole, on every protocol:
// magpie's own gateway.go as the model's reasoning, 4,900 lines of code
// with its braces, returns and comments.
func TestLongReplyThatDoesNotLoopGoesThrough(t *testing.T) {
	src, err := os.ReadFile("gateway.go")
	if err != nil {
		t.Fatal(err)
	}
	// ~1000 lines of real Go, several times the guard's window, and small
	// enough that -race on a loaded CI runner streams it well inside
	// askStream's bound (all of gateway.go took over 20s on macOS CI)
	src = src[:bytes.LastIndexByte(src[:40<<10], '\n')+1]
	for _, x := range loopAsks {
		t.Run(x.name, func(t *testing.T) {
			v := &loopingVendor{reasoning: string(src), letGo: make(chan bool, 4)}
			gw := deepseekOn(t, v)
			got := askStream(t, gw+x.path, x.body)
			if !strings.Contains(got, x.done) || strings.Contains(got, "magpie ended this reply") || !strings.Contains(got, "parse() now takes .toml") {
				t.Fatalf("the reply didn't go through whole:\n%s", got[max(len(got)-1500, 0):])
			}
			if letGo := <-v.letGo; letGo {
				t.Fatal("the vendor's request was let go")
			}
		})
	}
}

// With Stop looping replies off (Settings' NoLoopGuard), a loop runs on
// as the vendor sends it, to its end.
func TestLoopGuardOff(t *testing.T) {
	v := &loopingVendor{lines: 3000, letGo: make(chan bool, 4)}
	gw := deepseekOn(t, v)
	if err := settings.Save(settings.Settings{NoLoopGuard: true}); err != nil {
		t.Fatal(err)
	}
	got := askStream(t, gw+"/v1/chat/completions", loopAsks[0].body)
	if !strings.Contains(got, "data: [DONE]") || strings.Contains(got, "magpie ended this reply") {
		t.Fatalf("the loop was cut with the guard off:\n%s", got[max(len(got)-800, 0):])
	}
	if letGo := <-v.letGo; letGo || v.sent.Load() != 3000 {
		t.Fatalf("let go %v after %d lines", letGo, v.sent.Load())
	}
}
