package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// John on Discord, again after v0.1.1119: Kimi Code (api.kimi.ai, Chat,
// k3-256k) answering OpenCode read 2,237 tok/s for 550 tokens, 22 of them
// reasoning, in 16 s with its first content at 5.2 s, and 2,012 tok/s for
// 395 tokens in 19 s. The burst those replies came in was let go over a
// couple of hundred milliseconds, many events, not in one write, so the
// time it took to come passed for the time it took to write.

// kimiStep is what a fake Kimi Code writes at a moment after the request.
type kimiStep struct {
	at     time.Duration
	events []string
}

// kimiTimed is a fake Kimi Code that writes each step at its moment.
type kimiTimed struct {
	steps map[string][]kimiStep // by the model asked for
}

func (k *kimiTimed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, _ := io.ReadAll(r.Body)
	var steps []kimiStep
	for m, s := range k.steps {
		if strings.Contains(string(body), `"model":"`+m+`"`) {
			steps = s
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	for _, s := range steps {
		time.Sleep(time.Until(start.Add(s.at)))
		io.WriteString(w, strings.Join(s.events, "\n\n")+"\n\n")
		f.Flush()
	}
}

func kimiChunk(delta string) string {
	return `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1791500000,"model":"k3-256k","choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}]}`
}

func kimiEnd(finish string, out, reasoning int) []string {
	return []string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1791500000,"model":"k3-256k","choices":[{"index":0,"delta":{},"finish_reason":"` + finish + `"}],"usage":{"prompt_tokens":538,"completion_tokens":` + fmt.Sprint(out) + `,"total_tokens":` + fmt.Sprint(538+out) + `,"completion_tokens_details":{"reasoning_tokens":` + fmt.Sprint(reasoning) + `}}}`,
		`data: [DONE]`,
	}
}

// kimiShape is one of the ways the reply can have come, each over the same
// times: its first content (reasoning) at first, ended at end.
type kimiShape struct {
	name string
	// the reply tells this speed, 0 for none
	want float64
	// reasoning: its usage's; steps gives its output's too
	reasoning int
	steps     func(first, end time.Duration) (s []kimiStep, out int)
}

// thought: 22 tokens of reasoning, in four deltas 30 ms apart from first.
func kimiThought(first time.Duration) []kimiStep {
	var s []kimiStep
	for i := range 4 {
		s = append(s, kimiStep{first + time.Duration(i)*30*time.Millisecond, []string{kimiChunk(`{"reasoning_content":"The user wants the file written. "}`)}})
	}
	return s
}

// burst: the tool call the vendor held, let go over the last 236 ms in 40
// writes of four argument deltas each (John's 550 tokens at 2,237 tok/s:
// 528 answer tokens in 236 ms), optionally with the text before it.
func kimiBurst(end time.Duration, text bool) []kimiStep {
	from := end - 236*time.Millisecond
	var s []kimiStep
	if text {
		s = append(s, kimiStep{from, []string{kimiChunk(`{"content":"I'll write the file now."}`)}})
	}
	s = append(s, kimiStep{from, []string{kimiChunk(`{"tool_calls":[{"index":0,"id":"write:0","type":"function","function":{"name":"write","arguments":""}}]}`)}})
	for i := range 40 {
		var evs []string
		for range 4 {
			evs = append(evs, kimiChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"line of the file, "}}]}`))
		}
		s = append(s, kimiStep{from + time.Duration(i)*6*time.Millisecond, evs})
	}
	return s
}

var kimiShapes = []kimiShape{
	// its text came after the thought, and the tool call it wrote over the
	// rest of the time came in a burst at the end: its flow (from its first
	// text) is the burst, its window from its first text the whole wait
	{"held call", 0, 22, func(first, end time.Duration) ([]kimiStep, int) {
		s := kimiThought(first)
		s = append(s, kimiStep{first + 200*time.Millisecond, []string{kimiChunk(`{"content":"I'll write the file."}`)}})
		s = append(s, kimiBurst(end, false)...)
		return append(s, kimiStep{end, kimiEnd("tool_calls", 550, 22)}), 550
	}},
	// its text came with the burst, long after its 22 reasoning tokens:
	// the window from its first text is the burst itself
	{"held answer", 0, 22, func(first, end time.Duration) ([]kimiStep, int) {
		s := kimiThought(first)
		s = append(s, kimiBurst(end, true)...)
		return append(s, kimiStep{end, kimiEnd("tool_calls", 550, 22)}), 550
	}},
	// a steady 50 tok/s answer after the same thought: one token every
	// 20 ms from 200 ms after the first content to the end
	{"steady", 50, 22, func(first, end time.Duration) ([]kimiStep, int) {
		s := kimiThought(first)
		n := 0
		for at := first + 200*time.Millisecond; at < end; at += 20 * time.Millisecond {
			s = append(s, kimiStep{at, []string{kimiChunk(`{"content":"word "}`)}})
			n++
		}
		return append(s, kimiStep{end, kimiEnd("stop", n+22, 22)}), n + 22
	}},
}

// The reply's speed as each agent's protocol has it, through the real
// gateway from a fake Kimi Code: the held replies tell none, the steady one
// its 50 tok/s. The wait is shorter than John's (first content at 1.3 s,
// the end at 4 s) to keep the test short; the burst is his, 236 ms.
// Before, the held call read 1,790 tok/s and the held answer 2,237.
func TestKimiHeldBurstTellsNoSpeed(t *testing.T) {
	fresh(t)
	const first, end = 1300 * time.Millisecond, 4000 * time.Millisecond
	agents := []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"kimi-code/%s","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"write the file"}],"tools":[{"type":"function","function":{"name":"write","parameters":{"type":"object","properties":{"content":{"type":"string"}}}}}]}`},
		{"/v1/responses", `{"model":"kimi-code/%s","stream":true,"input":"write the file","tools":[{"type":"function","name":"write","parameters":{"type":"object","properties":{"content":{"type":"string"}}}}]}`},
		{"/v1/messages", `{"model":"kimi-code/%s","stream":true,"max_tokens":4000,"messages":[{"role":"user","content":"write the file"}],"tools":[{"name":"write","input_schema":{"type":"object","properties":{"content":{"type":"string"}}}}]}`},
	}
	// a model for each agent and shape, which the usage is told apart by
	model := func(a, i int) string { return fmt.Sprintf("k3-%d-%d", a, i) }
	k := &kimiTimed{steps: map[string][]kimiStep{}}
	var models []string
	for a := range agents {
		for i, sh := range kimiShapes {
			models = append(models, model(a, i))
			k.steps[model(a, i)], _ = sh.steps(first, end)
		}
	}
	up := httptest.NewServer(k)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "kimi-code", Name: "Kimi Code", Key: "k", Models: models, Chat: up.URL + "/coding/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)
	var wg sync.WaitGroup
	for ai, a := range agents {
		for i := range kimiShapes {
			m := model(ai, i)
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := http.Post(gw.URL+a.path, "application/json", strings.NewReader(fmt.Sprintf(a.body, m)))
				if err != nil {
					t.Error(err)
					return
				}
				b, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if res.StatusCode != 200 {
					t.Errorf("%s %s: %d %s", a.path, m, res.StatusCode, b)
				}
			}()
		}
	}
	wg.Wait()
	recs := usage.Load(time.Time{})
	for ai, a := range agents {
		for i, sh := range kimiShapes {
			var rec *usage.Record
			for j := range recs {
				if strings.HasSuffix(recs[j].Model, "/"+model(ai, i)) || recs[j].Model == model(ai, i) {
					rec = &recs[j]
				}
			}
			if rec == nil {
				t.Errorf("%s %s: no usage", a.path, sh.name)
				continue
			}
			n, w := rec.Decode()
			got := 0.0
			if w > 0 {
				got = float64(n) * 1000 / float64(w)
			}
			ok := got == 0
			if sh.want > 0 {
				ok = got > sh.want*0.8 && got < sh.want*1.2
			}
			if !ok {
				t.Errorf("%s %s: %d tokens in %d ms read %.0f tok/s (out %d, reasoning %d, ms %d, ttft %d, first text %d, flow %d); want %v",
					a.path, sh.name, n, w, got, rec.Output, rec.Reasoning, rec.Millis, rec.TTFT, rec.FirstText, rec.Flow, sh.want)
			}
		}
	}
}

// The same three at John's own times (first content at 5.2 s, the end at
// 16 s, 550 tokens of which 22 reasoning), read by firstToken as the
// gateway reads a Chat stream, without waiting them out: the held ones
// read 2,237 tok/s before, the held call by its flow and the held answer
// by its window from its first text.
func TestKimiHeldBurstAtJohnsTimes(t *testing.T) {
	const first, end = 5200 * time.Millisecond, 16000 * time.Millisecond
	for _, sh := range kimiShapes {
		t.Run(sh.name, func(t *testing.T) {
			f := firstToken{}
			steps, out := sh.steps(first, end)
			for _, s := range steps {
				// as if the request were s.at ago
				f.start = time.Now().Add(-s.at)
				f.see([]byte(strings.Join(s.events, "\n\n") + "\n\n"))
			}
			reasoning := sh.reasoning
			ttft, text := f.ms()
			ms := end.Milliseconds()
			flow := f.flowFor(reasoning)
			n, w := usage.DecodeOf(out, reasoning, ms, ttft, text, flow)
			got := 0.0
			if w > 0 {
				got = float64(n) * 1000 / float64(w)
			}
			ok := got == 0
			if sh.want > 0 {
				ok = got > sh.want*0.9 && got < sh.want*1.1
			}
			if !ok {
				t.Errorf("%d tokens in %d ms read %.0f tok/s (out %d, ttft %d, first text %d, flow %d); want %v", n, w, got, out, ttft, text, flow, sh.want)
			}
		})
	}
}

// The rows John posted, as their records hold them: 550 tokens (22
// reasoning) in 16 s from a first content at 5.2 s, the answer's flow 236
// ms, whether its text came early or with the burst; and 395 tokens in
// 19 s at 2,012 tok/s, a flow of 196 ms (his row gives no first token; 5.2
// s is taken). Before, they read 2,237 and 2,015 tok/s.
func TestJohnsRowsTellNoSpeed(t *testing.T) {
	for _, c := range []struct {
		name                      string
		out, reasoning            int
		ms, ttft, firstText, flow int64
	}{
		{"23:11:07, text early", 550, 22, 16000, 5200, 5400, 236},
		{"23:11:07, text with the burst", 550, 22, 16000, 5200, 15764, 236},
		{"23:12:56", 395, 0, 19000, 5200, 0, 196},
	} {
		if n, w := usage.DecodeOf(c.out, c.reasoning, c.ms, c.ttft, c.firstText, c.flow); w != 0 {
			t.Errorf("%s: %d tokens in %d ms read %d tok/s; want none", c.name, n, w, int64(n)*1000/w)
		}
	}
}
