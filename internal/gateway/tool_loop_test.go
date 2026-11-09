package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// countingVendor is a Chat vendor that answers every request with a word,
// streamed or whole as asked, and counts the requests it got.
type countingVendor struct{ asked atomic.Int64 }

func (v *countingVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	v.asked.Add(1)
	if strings.Contains(string(body), `"stream":true`) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"read it"}}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"read it"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
}

// toolLoopOn is a gateway whose provider wb serves deepseek-v4-pro from v.
func toolLoopOn(t *testing.T, v *countingVendor) string {
	t.Helper()
	fresh(t)
	vendor := httptest.NewServer(v)
	t.Cleanup(vendor.Close)
	if err := provider.Save(provider.Provider{ID: "wb", Name: "WorkBuddy", Key: "k", Chat: vendor.URL + "/v1", Models: []string{"deepseek-v4-pro"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)
	return gw.URL
}

// codexRead is the reporter's turn: Codex Desktop's exec_command reading
// the same code, its output's wall time and chunk id differing each run.
func codexRead(i int) (args, result string) {
	return `{"cmd":"sed -n 1,200p src/parser.rs","workdir":"/Users/audie/code/app"}`,
		fmt.Sprintf("Chunk ID: %06x\nWall time: 0.0%d1 seconds\nProcess exited with code 0\nOriginal token count: 41\nOutput:\npub fn parse(s: &str) -> Ast {\n    todo!()\n}\n", 0xa1b2c3+i, i%9)
}

// pollJob is a poll whose answer moves: the same call, a new result.
func pollJob(i int) (args, result string) {
	return `{"cmd":"gh run view 123 --json status"}`, fmt.Sprintf(`{"status":"in_progress","steps_done":%d}`, i)
}

type exchangeOfTurn func(i int) (args, result string)

// toolTurns is a request of from's API holding n turns of tool use made
// by turn, after the user's ask, then tail (raw messages of that API).
func toolTurns(from provider.Protocol, n int, turn exchangeOfTurn, stream bool, tail ...string) string {
	var items []string
	switch from {
	case provider.Responses:
		items = append(items, `{"type":"message","role":"user","content":[{"type":"input_text","text":"why does parse panic?"}]}`)
		for i := range n {
			args, result := turn(i)
			items = append(items,
				fmt.Sprintf(`{"type":"reasoning","summary":[{"type":"summary_text","text":"Reading parser %d"}]}`, i),
				fmt.Sprintf(`{"type":"function_call","name":"exec_command","arguments":%q,"call_id":"call_%d"}`, args, i),
				fmt.Sprintf(`{"type":"function_call_output","call_id":"call_%d","output":%q}`, i, result))
		}
		items = append(items, tail...)
		return fmt.Sprintf(`{"model":"wb/deepseek-v4-pro","stream":%t,"store":false,"instructions":"You are Codex.","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}],"input":[%s]}`, stream, strings.Join(items, ","))
	case provider.Chat:
		items = append(items, `{"role":"user","content":"why does parse panic?"}`)
		for i := range n {
			args, result := turn(i)
			items = append(items,
				fmt.Sprintf(`{"role":"assistant","content":null,"tool_calls":[{"id":"call_%d","type":"function","function":{"name":"exec_command","arguments":%q}}]}`, i, args),
				fmt.Sprintf(`{"role":"tool","tool_call_id":"call_%d","content":%q}`, i, result))
		}
		items = append(items, tail...)
		return fmt.Sprintf(`{"model":"wb/deepseek-v4-pro","stream":%t,"tools":[{"type":"function","function":{"name":"exec_command","parameters":{"type":"object"}}}],"messages":[%s]}`, stream, strings.Join(items, ","))
	case provider.Anthropic:
		items = append(items, `{"role":"user","content":"why does parse panic?"}`)
		for i := range n {
			args, result := turn(i)
			items = append(items,
				fmt.Sprintf(`{"role":"assistant","content":[{"type":"text","text":"Let me look again."},{"type":"tool_use","id":"toolu_%d","name":"Bash","input":%s}]}`, i, args),
				fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%d","content":%q}]}`, i, result))
		}
		items = append(items, tail...)
		return fmt.Sprintf(`{"model":"wb/deepseek-v4-pro","max_tokens":8000,"stream":%t,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[%s]}`, stream, strings.Join(items, ","))
	}
	// Gemini
	items = append(items, `{"role":"user","parts":[{"text":"why does parse panic?"}]}`)
	for i := range n {
		args, result := turn(i)
		items = append(items,
			fmt.Sprintf(`{"role":"model","parts":[{"functionCall":{"name":"run_shell_command","args":%s}}]}`, args),
			fmt.Sprintf(`{"role":"user","parts":[{"functionResponse":{"name":"run_shell_command","response":{"output":%q}}}]}`, result))
	}
	items = append(items, tail...)
	return fmt.Sprintf(`{"tools":[{"functionDeclarations":[{"name":"run_shell_command","parameters":{"type":"object"}}]}],"contents":[%s]}`, strings.Join(items, ","))
}

var toolLoopAsks = []struct {
	name   string
	from   provider.Protocol
	path   string
	stream bool
	done   string // how the agent's reply ends well
}{
	{"responses", provider.Responses, "/v1/responses", true, `"type":"response.completed"`},
	{"responses whole", provider.Responses, "/v1/responses", false, `"status":"completed"`},
	{"codex", provider.Responses, CodexPath + "/responses", true, `"type":"response.completed"`},
	{"chat", provider.Chat, "/v1/chat/completions", true, "data: [DONE]"},
	{"chat whole", provider.Chat, "/v1/chat/completions", false, `"finish_reason":"stop"`},
	{"anthropic", provider.Anthropic, "/v1/messages", true, `"type":"message_stop"`},
	{"anthropic whole", provider.Anthropic, "/v1/messages", false, `"stop_reason":"end_turn"`},
	{"gemini", provider.Gemini, "/v1beta/models/wb/deepseek-v4-pro:streamGenerateContent?alt=sse", true, `"finishReason":"STOP"`},
}

func askAgent(t *testing.T, url, body string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("session_id", "01a15199-593c-77d1-9913-ce0e1efd48e8")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	return string(b)
}

// The reporter's loop (audie on Discord: Codex Desktop on WorkBuddy's
// deepseek-v4-pro reading the same code 308 times): once the same call has
// had the same result toolLoopRepeats turns in a row, the next turn is
// answered by magpie in the agent's own API, a finished reply saying why,
// and the vendor is never asked; Usage and the session's requests have it
// as magpie's, sent nowhere, with the reason.
func TestToolLoopIsStoppedOnEveryProtocol(t *testing.T) {
	for _, x := range toolLoopAsks {
		t.Run(x.name, func(t *testing.T) {
			v := &countingVendor{}
			gw := toolLoopOn(t, v)
			// a long conversation of progress first, which the guard reads
			// only the end of (tailOf)
			turn := func(i int) (string, string) {
				if i < 60 {
					return pollJob(i)
				}
				return codexRead(i)
			}
			got := askAgent(t, gw+x.path, toolTurns(x.from, 60+toolLoopRepeats, turn, x.stream))
			if !strings.Contains(got, "This turn was stopped") || !strings.Contains(got, x.done) || !strings.Contains(got, "sed -n 1,200p src/parser.rs") {
				t.Fatalf("the agent wasn't told of the loop in its API:\n%s", got)
			}
			if n := v.asked.Load(); n != 0 {
				t.Fatalf("the vendor was asked %d times for a looping turn", n)
			}
			var rec usage.Record
			for range 50 {
				if recs := usage.Load(time.Time{}); len(recs) > 0 {
					rec = recs[len(recs)-1]
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if rec.Provider != "magpie" || !rec.Rejected || !strings.Contains(rec.Error, "tool loop") || rec.Session != "01a15199-593c-77d1-9913-ce0e1efd48e8" {
				t.Fatalf("usage record %+v", rec)
			}
		})
	}
}

// Codex on its own sign-in, a turn relayed to the ChatGPT backend as it
// came, is stopped the same: its loop spends the user's ChatGPT plan.
func TestToolLoopStoppedOnCodexsOwnSignIn(t *testing.T) {
	v := &countingVendor{}
	toolLoopOn(t, &countingVendor{})
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)
	body := strings.Replace(toolTurns(provider.Responses, toolLoopRepeats, codexRead, true), `"model":"wb/deepseek-v4-pro"`, `"model":"gpt-5.5"`, 1)
	got := askAgent(t, gw.URL+CodexPath+"/responses", body)
	if !strings.Contains(got, "This turn was stopped") || !strings.Contains(got, `"type":"response.completed"`) {
		t.Fatalf("Codex wasn't told of the loop:\n%s", got)
	}
	if n := v.asked.Load(); n != 0 {
		t.Fatalf("the ChatGPT backend was asked %d times for a looping turn", n)
	}
	// one short goes on to the backend
	askAgent(t, gw.URL+CodexPath+"/responses", strings.Replace(toolTurns(provider.Responses, toolLoopRepeats-1, codexRead, true), `"model":"wb/deepseek-v4-pro"`, `"model":"gpt-5.5"`, 1))
	if n := v.asked.Load(); n != 1 {
		t.Fatalf("the ChatGPT backend was asked %d times for a turn short of a loop", n)
	}
}

// One turn short of the loop goes to the vendor, as does the reporter's
// loop with the guard turned off in Settings.
func TestToolLoopShortOrOffGoesOn(t *testing.T) {
	for _, x := range toolLoopAsks {
		t.Run(x.name, func(t *testing.T) {
			v := &countingVendor{}
			gw := toolLoopOn(t, v)
			got := askAgent(t, gw+x.path, toolTurns(x.from, toolLoopRepeats-1, codexRead, x.stream))
			if strings.Contains(got, "This turn was stopped") || v.asked.Load() != 1 {
				t.Fatalf("%d turns of the same call were stopped (vendor asked %d):\n%s", toolLoopRepeats-1, v.asked.Load(), got)
			}
			if err := settings.Save(settings.Settings{NoLoopGuard: true}); err != nil {
				t.Fatal(err)
			}
			got = askAgent(t, gw+x.path, toolTurns(x.from, 40, codexRead, x.stream))
			if strings.Contains(got, "This turn was stopped") || v.asked.Load() != 2 {
				t.Fatalf("the loop was stopped with Stop looping replies off (vendor asked %d):\n%s", v.asked.Load(), got)
			}
		})
	}
}

// Work that makes progress is never stopped, however long it goes on: a
// poll answered differently each time, and the reporter's read of the
// same code once the user has said something since, magpie's notice
// included in the conversation.
func TestToolLoopLetsProgressBy(t *testing.T) {
	notice := map[provider.Protocol][]string{
		provider.Responses: {`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"This turn was stopped"}]}`, `{"type":"message","role":"user","content":[{"type":"input_text","text":"read it once more, then fix it"}]}`},
		provider.Chat:      {`{"role":"assistant","content":"This turn was stopped"}`, `{"role":"user","content":"read it once more, then fix it"}`},
		provider.Anthropic: {`{"role":"assistant","content":"This turn was stopped"}`, `{"role":"user","content":"read it once more, then fix it"}`},
		provider.Gemini:    {`{"role":"model","parts":[{"text":"This turn was stopped"}]}`, `{"role":"user","parts":[{"text":"read it once more, then fix it"}]}`},
	}
	for _, x := range toolLoopAsks {
		t.Run(x.name, func(t *testing.T) {
			v := &countingVendor{}
			gw := toolLoopOn(t, v)
			got := askAgent(t, gw+x.path, toolTurns(x.from, 200, pollJob, x.stream))
			if strings.Contains(got, "This turn was stopped") || v.asked.Load() != 1 {
				t.Fatalf("a poll whose answer moves was stopped (vendor asked %d):\n%s", v.asked.Load(), got)
			}
			// the loop, magpie's notice and the user's word after it
			body := toolTurns(x.from, toolLoopRepeats, codexRead, x.stream, notice[x.from]...)
			got = askAgent(t, gw+x.path, body)
			if strings.Contains(got, "This turn was stopped before") || v.asked.Load() != 2 {
				t.Fatalf("the user's word after the notice was stopped (vendor asked %d):\n%s", v.asked.Load(), got)
			}
		})
	}
}

// What tells one exchange from another: the call's name and arguments
// (whatever their keys' order), its result less what Codex writes of the
// run, and whether it failed; never the call's id.
func TestToolLoopSums(t *testing.T) {
	call := func(id, name, args string) Part {
		return Part{Kind: ToolCall, ID: id, Name: name, Args: json.RawMessage(args)}
	}
	result := func(id, text string) Part { return Part{Kind: ToolResult, CallID: id, Text: text} }
	sum := func(c, r Part) uint64 {
		e, ok := exchangeOf([]Part{c}, []Part{r})
		if !ok {
			t.Fatal("no exchange")
		}
		return e.sum
	}
	base := sum(call("a", "read", `{"path":"x","limit":2}`), result("a", "Wall time: 0.0246 seconds\nOutput:\nfn x()"))
	same := []uint64{
		sum(call("b", "read", `{"limit":2, "path":"x"}`), result("b", "Wall time: 1.5 seconds\nOutput:\nfn x()")),
	}
	differ := []uint64{
		sum(call("c", "read", `{"path":"y","limit":2}`), result("c", "Wall time: 0.0246 seconds\nOutput:\nfn x()")),
		sum(call("d", "grep", `{"path":"x","limit":2}`), result("d", "Wall time: 0.0246 seconds\nOutput:\nfn x()")),
		sum(call("e", "read", `{"path":"x","limit":2}`), result("e", "Wall time: 0.0246 seconds\nOutput:\nfn y()")),
		sum(call("f", "read", `{"path":"x","limit":2}`), Part{Kind: ToolResult, CallID: "f", Text: "Wall time: 0.0246 seconds\nOutput:\nfn x()", IsError: true}),
	}
	for i, s := range same {
		if s != base {
			t.Errorf("same exchange %d summed apart", i)
		}
	}
	for i, s := range differ {
		if s == base {
			t.Errorf("different exchange %d summed the same", i)
		}
	}
	// a turn with no call, or no result, is no exchange
	if _, ok := exchangeOf([]Part{{Kind: Text, Text: "done"}}, []Part{result("a", "x")}); ok {
		t.Error("a turn with no call is an exchange")
	}
	if _, ok := exchangeOf([]Part{call("a", "read", `{}`)}, []Part{{Kind: Text, Text: "stop"}}); ok {
		t.Error("a turn with no result is an exchange")
	}
}

// The guard reads every agent turn with results enough: a long Codex
// conversation, ~1.5MB of 400 reads, costs it one parse.
func BenchmarkToolLoopCheck(b *testing.B) {
	big := strings.Repeat("let x = 1;\n", 300)
	body := []byte(toolTurns(provider.Responses, 400, func(i int) (string, string) {
		return fmt.Sprintf(`{"cmd":"sed -n %d,200p src/f.rs"}`, i), big
	}, true))
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if !mayLoop(provider.Responses, body) {
			b.Fatal("no results")
		}
		req, err := parse(provider.Responses, tailOf(provider.Responses, body))
		if err != nil {
			b.Fatal(err)
		}
		if _, ok := toolLoopOf(req.Messages); ok {
			b.Fatal("a loop")
		}
	}
}

// The conversation's tail is read where it sits in the body: GetBytes
// copied the whole input list to find the last items, 45 MiB of a Codex
// turn on every request (#4004).
func TestTailOfReadsInPlace(t *testing.T) {
	item := `{"type":"function_call_output","call_id":"c%d","output":"` + strings.Repeat("o", 20<<10) + `"}`
	items := make([]string, 200)
	for i := range items {
		items[i] = fmt.Sprintf(item, i)
	}
	body := []byte(`{"model":"m","input":[` + strings.Join(items, ",") + `],"stream":true}`)
	var tail []byte
	n := allocated(func() { tail = tailOf(provider.Responses, body) })
	want := `{"model":"m","input":[` + strings.Join(items[len(items)-tailItems:], ",") + `],"stream":true}`
	if string(tail) != want {
		t.Fatalf("tail is %.200s…", tail)
	}
	if n > uint64(len(want))+uint64(len(body))/4 {
		t.Fatalf("%d bytes allocated for a %d byte tail of a %d byte body", n, len(want), len(body))
	}
}

// A wait asked again with the same arguments gets the same answer by
// design (xiaozhi09400: clock__sleep {"duration_ms":60000} while CI ran).
// Any other call, however it is named, is still a loop.
func TestToolLoopLetsWaitsBy(t *testing.T) {
	loopOf := func(name, args string) bool {
		var msgs []Message
		for i := range toolLoopRepeats + 2 {
			id := fmt.Sprintf("call_%d", i)
			msgs = append(msgs,
				Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: id, Name: name, Args: json.RawMessage(args)}}},
				Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: id, Text: "ok"}}})
		}
		_, ok := toolLoopOf(msgs)
		return ok
	}
	for _, c := range []struct{ name, args string }{
		{"clock__sleep", `{"duration_ms":60000}`},
		{"mcp__clock__sleep", `{"duration_ms":60000}`},
		{"clock.sleep", `{}`},
		{"wait_for_ci", `{"run":"123"}`},
		{"waitForBuild", `{"id":"b1"}`},
		{"delay", `{"ms":500}`},
		{"pause", `{}`},
		{"timer__start", `{"seconds":30}`},
	} {
		if loopOf(c.name, c.args) {
			t.Errorf("%s %s was stopped as a loop", c.name, c.args)
		}
	}
	for _, c := range []struct{ name, args string }{
		{"exec_command", `{"cmd":"sed -n 1,200p src/parser.rs"}`},
		{"wait__read_file", `{"path":"a.rs"}`}, // a server named wait
		{"clock__now", `{"tz":"UTC"}`},
		{"timer__start", `{"seconds":30,"label":"x"}`},
	} {
		if !loopOf(c.name, c.args) {
			t.Errorf("%s %s was let by as a wait", c.name, c.args)
		}
	}
}
