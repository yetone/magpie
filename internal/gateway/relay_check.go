package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A relay check (skingstyle48487 on X, with more than ten relays: which
// one waters a model down, which one answers with another) makes a few
// small real calls to one model on one provider, through this gateway as
// an agent would, and says what each found: what the reply named itself,
// how many input tokens a short prompt was counted as, whether a fixed
// answer came back right, whether a tool was called, and how fast. Each
// is a finding, a number or a match, never a verdict on the provider: a
// model that misses a sum on one call is no proof of a swap. It costs
// what the calls cost, so it is made only when the user presses Check.

// The kinds of call a check makes. A check makes one count call, a few
// math calls and a few tool calls, each with an answer known in advance,
// so what came back is right or not, and a count of right answers can be
// set beside another provider's (skingstyle48487's second ask: accuracy
// and tool-call success, relay beside relay).
const (
	RelayPartCount = "count" // write 1 to 40: the reply, its speed, its tokens
	RelayPartMath  = "math"  // a multiplication with a known product
	RelayPartTool  = "tool"  // a question one of two tools answers
)

// RelayCheckResult is what a check of one model on one provider found.
type RelayCheckResult struct {
	Provider string      `json:"provider"`
	Model    string      `json:"model"`
	Parts    []RelayPart `json:"parts"`
}

// RelayPart is what one call found. Ask is what the call asked, shown
// beside its kind: the multiplication, or the tool it should call. OK is that the call was answered;
// Match that the answer was the one asked for. Served is the model the
// reply named when that reads as another model than the one asked for
// (usage.Swapped), and By the provider that answered when the gateway
// handed the call on. Input is what the provider counted the prompt as,
// PromptChars how long that prompt was.
type RelayPart struct {
	Kind        string  `json:"kind"`
	Ask         string  `json:"ask,omitempty"`
	OK          bool    `json:"ok"`
	Match       bool    `json:"match"`
	Status      int     `json:"status,omitempty"`
	Error       string  `json:"error,omitempty"`
	Want        string  `json:"want,omitempty"`
	Got         string  `json:"got,omitempty"`
	Served      string  `json:"served,omitempty"`
	By          string  `json:"by,omitempty"`
	Millis      int64   `json:"ms"`
	TTFT        int64   `json:"ttft_ms,omitempty"`
	TPS         float64 `json:"tps,omitempty"`
	Input       int     `json:"input,omitempty"`
	Output      int     `json:"output,omitempty"`
	PromptChars int     `json:"prompt_chars,omitempty"`
	Tries       int     `json:"tries,omitempty"`
}

// relayTap collects the usage records of one check call: what the
// provider counted, the model its reply named and how long it took are
// what the gateway records, read here rather than parsed from a reply
// each protocol words its own way.
type relayTap struct {
	mu   sync.Mutex
	recs []usage.Record
}

type relayTapKey struct{}

func tapUsage(r *http.Request, rec usage.Record) {
	if t, _ := r.Context().Value(relayTapKey{}).(*relayTap); t != nil {
		t.mu.Lock()
		t.recs = append(t.recs, rec)
		t.mu.Unlock()
	}
}

// relayCountTo is how far the count call counts: long enough for a speed
// to be measured, short enough to cost a few hundred tokens.
const relayCountTo = 40

// relayMaths are the multiplications the math calls ask, and their
// products: four-digit numbers, past what a model answers from memory.
var relayMaths = [][2]string{{"1234 * 5678", "7006652"}, {"987 * 654", "645498"}, {"2468 * 1357", "3349076"}}

// relayTools are the tool calls' questions, the tool each should call and
// the word its arguments should hold. Both calls offer both tools, so the
// right one has to be picked.
var relayTools = []struct{ q, tool, arg string }{
	{"What is the weather in Paris now? Use a tool.", "get_weather", "paris"},
	{"What time is it in Tokyo now? Use a tool.", "get_time", "tokyo"},
}

// CheckRelay checks model on providerID with its calls, three at a time. A provider or model the gateway can't resolve is each call's
// error, as an agent would meet it.
func (s *Server) CheckRelay(ctx context.Context, providerID, model string) RelayCheckResult {
	out := RelayCheckResult{Provider: providerID, Model: model}
	ref := providerID + "/" + model
	var nums []string
	for i := 1; i <= relayCountTo; i++ {
		nums = append(nums, strconv.Itoa(i))
	}
	count := "Write the whole numbers from 1 to " + strconv.Itoa(relayCountTo) + " in order, separated by single spaces, and nothing else."
	type call struct {
		kind, ask, prompt, want, arg string
		tools                        bool
	}
	calls := []call{{kind: RelayPartCount, prompt: count, want: strings.Join(nums, " ")}}
	for _, m := range relayMaths {
		calls = append(calls, call{kind: RelayPartMath, ask: strings.ReplaceAll(m[0], "*", "×"), prompt: "What is " + m[0] + "? Answer with the number only, without separators.", want: m[1]})
	}
	for _, x := range relayTools {
		calls = append(calls, call{kind: RelayPartTool, ask: x.tool, prompt: x.q, want: x.tool, arg: x.arg, tools: true})
	}
	out.Parts = make([]RelayPart, len(calls))
	var wg sync.WaitGroup
	// three at a time: a relay that limits a key's concurrent calls should
	// answer a check as it answers an agent
	slots := make(chan struct{}, 3)
	for i, c := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			p := s.relayCall(ctx, ref, c.prompt, c.tools)
			p.Kind, p.Ask, p.Want = c.kind, c.ask, c.want
			switch c.kind {
			case RelayPartCount:
				p.Match = p.OK && strings.Join(strings.Fields(p.Got), " ") == c.want
			case RelayPartMath:
				p.Match = p.OK && strings.Trim(strings.NewReplacer(",", "", " ", "", " ", "").Replace(p.Got), ".`*\n") == c.want
			case RelayPartTool:
				p.Match = p.OK && strings.HasPrefix(p.Got, c.want+"(") && strings.Contains(strings.ToLower(p.Got), c.arg)
			}
			if c.kind == RelayPartCount {
				p.PromptChars = len(c.prompt)
			}
			if p.Served != "" && !relayAnother(providerID, model, p.Served) {
				p.Served = ""
			}
			if p.By == providerID {
				p.By = ""
			}
			out.Parts[i] = p
		}()
	}
	wg.Wait()
	return out
}

// relayAnother is whether a reply that named served was another model than
// model on providerID, as the ledger marks a swap: not the same model
// spelled another way, nor a name the provider lists it under.
func relayAnother(providerID, model, served string) bool {
	return usage.Swapped(model, served) && !usage.SameSpelled(model, served) && !usage.ListedAs(providerID, model, served)
}

// relayCheckTools are the tools a tool call offers.
var relayCheckTools = json.RawMessage(`[{"type":"function","function":{"name":"get_weather","description":"Get the current weather in a city.","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}},{"type":"function","function":{"name":"get_time","description":"Get the current local time in a city.","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]`)

// relayCall makes one streamed Chat Completions call for ref through the
// gateway and reads its reply: the text, or the tool calls as
// name(arguments), and the last answered try's usage record.
func (s *Server) relayCall(ctx context.Context, ref, prompt string, tools bool) RelayPart {
	var p RelayPart
	body := map[string]any{
		"model":      ref,
		"stream":     true,
		"max_tokens": 400,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	}
	if tools {
		body["tools"] = relayCheckTools
	}
	b, _ := json.Marshal(body)
	tap := &relayTap{}
	ctx = context.WithValue(ctx, relayTapKey{}, tap)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, URL()+"/v1/chat/completions", bytes.NewReader(b))
	req.RemoteAddr = "127.0.0.1:0"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+TokenFor(CheckAgent))
	start := time.Now()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	p.Millis = time.Since(start).Milliseconds()
	p.Status = rec.Code
	if rec.Code != http.StatusOK {
		p.Error = provider.APIError(rec.Body.Bytes(), http.StatusText(rec.Code))
	} else {
		text, calls, msg := readChatStream(rec.Body.Bytes())
		p.Got = text
		if len(calls) > 0 {
			p.Got = strings.Join(calls, " ")
		}
		switch {
		case msg != "":
			p.Error = msg
		case p.Got == "":
			p.Error = "the answer was empty"
		default:
			p.OK = true
		}
	}
	tap.mu.Lock()
	recs := tap.recs
	tap.mu.Unlock()
	p.Tries = len(recs)
	for _, r := range recs {
		if r.Failed() || r.Status >= 400 {
			continue
		}
		p.By = r.Provider
		if r.Served != "" {
			p.Served = r.Served
		}
		p.Input, p.Output = r.Input+r.CacheRead+r.CacheWrite, r.Output
		p.TTFT = r.TTFT
		if r.TTFT > 0 && r.Millis > r.TTFT && r.Output > 0 {
			p.TPS = float64(r.Output) / (float64(r.Millis-r.TTFT) / 1000)
		}
	}
	if len(p.Got) > 400 {
		p.Got = p.Got[:400] + "…"
	}
	return p
}

// readChatStream reads a Chat Completions stream: its text, its tool calls
// as name(arguments), and an error event's message. The model the reply
// named is the usage record's, which the gateway reads off the vendor's
// own reply.
func readChatStream(b []byte) (text string, calls []string, errMsg string) {
	var sb strings.Builder
	type call struct{ name, args strings.Builder }
	var cs []*call
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int `json:"index"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		if ev.Error != nil && ev.Error.Message != "" {
			errMsg = ev.Error.Message
		}
		for _, c := range ev.Choices {
			sb.WriteString(c.Delta.Content)
			for _, tc := range c.Delta.ToolCalls {
				for len(cs) <= tc.Index {
					cs = append(cs, &call{})
				}
				cs[tc.Index].name.WriteString(tc.Function.Name)
				cs[tc.Index].args.WriteString(tc.Function.Arguments)
			}
		}
	}
	for _, c := range cs {
		if c.name.Len() > 0 {
			calls = append(calls, c.name.String()+"("+c.args.String()+")")
		}
	}
	return strings.TrimSpace(sb.String()), calls, errMsg
}
