package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// slowThinker streams its reasoning a piece at a time, as GLM on ZCode's
// Start Plan does, and notes when it has said its last.
type slowThinker struct {
	events []string
	gap    time.Duration
	done   chan time.Time
	sent   []time.Time // when each event was written; read after done
}

func (s *slowThinker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	f := w.(http.Flusher)
	for _, ev := range s.events {
		io.WriteString(w, ev+"\n\n")
		f.Flush()
		s.sent = append(s.sent, time.Now())
		time.Sleep(s.gap)
	}
	// a try that is asked again serves the same fake vendor a second
	// time; the channel holds one send, so a second handler would block
	// here forever and hold the reply it serves open, the package hung
	// on the server's Close with it until go test's own timeout (#4530,
	// Jackzigen). The tests read the time once; later sends can drop.
	select {
	case s.done <- time.Now():
	default:
	}
}

var glmThinksSlowly = []string{
	`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"m1","role":"assistant","model":"glm-5.1","content":[]}}`,
	`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
	`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"first thought"}}`,
	`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" second thought"}}`,
	`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" third thought"}}`,
	`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
	`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
	`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"done"}}`,
	`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":1}`,
	`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
	`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
}

var glmChatThinksSlowly = []string{
	`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"first thought"}}]}`,
	`data: {"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":" second thought"}}]}`,
	`data: {"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":" third thought"}}]}`,
	`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"done"}}]}`,
	`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	`data: [DONE]`,
}

// A model whose vendor never refuses after reasoning has its thinking
// streamed as it comes, through a group as directly: GLM on a ZCode
// account behind a route group showed Claude Code its thinking only once
// the text began (悠悠哥 on Discord), held there for a refusal (#248) that
// only Claude's, OpenAI's and Gemini's safety filters send.
func TestThinkingStreamsThroughGroup(t *testing.T) {
	for _, x := range []struct {
		name   string
		proto  provider.Protocol
		events []string
	}{
		{"anthropic", provider.Anthropic, glmThinksSlowly},
		{"chat", provider.Chat, glmChatThinksSlowly},
	} {
		t.Run(x.name, func(t *testing.T) {
			fresh(t)
			up := &slowThinker{events: x.events, gap: 200 * time.Millisecond, done: make(chan time.Time, 1)}
			vendor := httptest.NewServer(up)
			t.Cleanup(vendor.Close)
			p := provider.Provider{ID: "zc", Name: "ZCode", Key: "k", Models: []string{"GLM-5.1"}}
			if x.proto == provider.Anthropic {
				p.Anthropic = vendor.URL
			} else {
				p.Chat = vendor.URL + "/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			scriptedOn(t, "b", provider.Anthropic, &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}})
			refusalGroup(t, "zc/GLM-5.1", "b/m")
			gw := httptest.NewServer(New().Handler())
			t.Cleanup(gw.Close)

			res, err := http.Post(gw.URL+"/v1/messages", "application/json", strings.NewReader(claudeCodeAsk))
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			var firstThought, firstText time.Time
			rd := bufio.NewReader(res.Body)
			for {
				ln, err := rd.ReadString('\n')
				if firstThought.IsZero() && strings.Contains(ln, `"thinking_delta"`) {
					firstThought = time.Now()
				}
				if firstText.IsZero() && strings.Contains(ln, `"text_delta"`) {
					firstText = time.Now()
				}
				if err != nil {
					break
				}
			}
			var ended time.Time
			select {
			case ended = <-up.done:
			case <-time.After(5 * time.Second):
				t.Fatal("the vendor never finished")
			}
			if firstThought.IsZero() || firstText.IsZero() {
				t.Fatal("no thinking or no text reached the client")
			}
			// three thoughts 200ms apart come before the text
			if firstText.Sub(firstThought) < 300*time.Millisecond || ended.Sub(firstThought) < time.Second {
				t.Fatalf("thinking held: first thought %v before the text, %v before the vendor finished", firstText.Sub(firstThought), ended.Sub(firstThought))
			}
		})
	}
}

// Claude's reasoning is still held for a refusal after it (#248) while
// another candidate could answer, and so are OpenAI's, Gemini's and
// DeepSeek's (plugins #540); anybody else's is let through.
func TestThinkingHeldFor(t *testing.T) {
	for model, held := range map[string]bool{
		"claude-opus-5-5": true, "anthropic/claude-sonnet-4.5": true, "gpt-5.2-codex": true, "o3": true,
		"gemini-3-pro-preview": true, "models/gemini-2.5-flash": true,
		"deepseek-v4": true, "deepseek-v4.1-flash": true, "deepseek/deepseek-v4-pro": true,
		"GLM-5.1": false, "glm-4.6": false, "kimi-k2.5": false, "MiniMax-M2": false,
	} {
		if got := refusesAfterThinking(model); got != held {
			t.Errorf("%s: held %v, want %v", model, got, held)
		}
	}
}

// A DeepSeek chat reply that reasoned and went on to say it, and one whose
// filter ended it with nothing said (#540's shape), both as #4512's member
// serves them.
var deepseekThoughtThenAnswered = sse(
	`data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"The user is asking"},"finish_reason":null}]}`,
	`data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"reasoning_content":" about it."},"finish_reason":null}]}`,
	`data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"content":"from a"},"finish_reason":null}]}`,
	`data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`,
	`data: [DONE]`)

var deepseekThoughtThenRefused = sse(
	`data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"The user is asking"},"finish_reason":null}]}`,
	`data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
	`data: [DONE]`)

var deepseekChatThinksSlowly = []string{
	`data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"first thought"},"finish_reason":null}]}`,
	`data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"reasoning_content":" second thought"},"finish_reason":null}]}`,
	`data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"reasoning_content":" third thought"},"finish_reason":null}]}`,
	`data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":null}]}`,
	`data: {"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`,
	`data: [DONE]`,
}

// A DeepSeek member whose thinking has gone on to an answer
// thoughtCleanMost times running has its reasoning as it comes, GLM's
// way, though another member stands behind it: CNife's group paid the
// first 6.4s of every request for a filter that fired once in 33,245
// requests over 7 days (#4512). Held, the reasoning reaches the agent
// only once the text begins, all at once.
func TestDeepSeekThinkingStreamsOnceTrusted(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{
		{200, "text/event-stream", deepseekThoughtThenAnswered},
		{200, "text/event-stream", deepseekThoughtThenAnswered},
		{200, "text/event-stream", deepseekThoughtThenAnswered},
	}}
	scriptedOn(t, "a", provider.Chat, a)
	pa, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	pa.Models = []string{"deepseek-v4.1-flash"}
	if err := provider.Save(*pa); err != nil {
		t.Fatal(err)
	}
	scriptedOn(t, "b", provider.Anthropic, &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}})
	refusalGroup(t, "a/deepseek-v4.1-flash", "b/m")
	s := New()
	for i := 0; i < thoughtCleanMost; i++ {
		if code, body := sendTo(s, "/v1/messages", claudeCodeAsk); code != 200 || !strings.Contains(body, "from a") || strings.Contains(body, "from b") {
			t.Fatalf("warm-up %d: %d %s (a %d)", i+1, code, body, a.n)
		}
	}

	// trusted, the same member's next reply streams its reasoning as it
	// comes, the text a good while after the first thought
	up := &slowThinker{events: deepseekChatThinksSlowly, gap: 200 * time.Millisecond, done: make(chan time.Time, 1)}
	vendor := httptest.NewServer(up)
	t.Cleanup(vendor.Close)
	pa, err = provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	pa.Chat = vendor.URL + "/v1"
	if err := provider.Save(*pa); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	res, err := http.Post(gw.URL+"/v1/messages", "application/json", strings.NewReader(claudeCodeAsk))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var firstThought, firstText time.Time
	rd := bufio.NewReader(res.Body)
	for {
		ln, err := rd.ReadString('\n')
		if firstThought.IsZero() && strings.Contains(ln, `"thinking_delta"`) {
			firstThought = time.Now()
		}
		if firstText.IsZero() && strings.Contains(ln, `"text_delta"`) {
			firstText = time.Now()
		}
		if err != nil {
			break
		}
	}
	select {
	case <-up.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the vendor never finished")
	}
	if firstThought.IsZero() || firstText.IsZero() {
		t.Fatal("no thinking or no text reached the client")
	}
	// three thoughts 200ms apart come before the text
	if firstText.Sub(firstThought) < 300*time.Millisecond {
		t.Fatalf("thinking held: first thought only %v before the text", firstText.Sub(firstThought))
	}
	if a.n != thoughtCleanMost {
		t.Fatalf("tries: a %d", a.n)
	}
	if r := lastRoute(s); len(r.Tries) != 1 {
		t.Fatalf("the trusted member's reply was asked again: %+v", r.Tries)
	}
}

// A trusted member that refuses after its thinking is held again: its
// reasoning went through as it came and the reply ended with the filter's
// content_filter and nothing said (#540's shape), and the next request's
// reasoning waits, its refusal going to the next member.
func TestDeepSeekRefusalHoldsATrustedMemberAgain(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{
		{200, "text/event-stream", deepseekThoughtThenAnswered},
		{200, "text/event-stream", deepseekThoughtThenAnswered},
		{200, "text/event-stream", deepseekThoughtThenAnswered},
		{200, "text/event-stream", deepseekThoughtThenRefused},
		{200, "text/event-stream", deepseekThoughtThenRefused},
	}}
	scriptedOn(t, "a", provider.Chat, a)
	pa, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	pa.Models = []string{"deepseek-v4.1-flash"}
	if err := provider.Save(*pa); err != nil {
		t.Fatal(err)
	}
	b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "b", provider.Anthropic, b)
	refusalGroup(t, "a/deepseek-v4.1-flash", "b/m")
	s := New()
	for i := 0; i < thoughtCleanMost; i++ {
		if code, body := sendTo(s, "/v1/messages", claudeCodeAsk); code != 200 || !strings.Contains(body, "from a") || strings.Contains(body, "from b") {
			t.Fatalf("warm-up %d: %d %s (a %d, b %d)", i+1, code, body, a.n, b.n)
		}
	}
	// trusted, the refusal's reasoning went through as it came, and the
	// refusal after it, with nothing yet said, went to the next member,
	// whose answer follows the reasoning in the same stream (refusalHandover)
	if code, body := sendTo(s, "/v1/messages", claudeCodeAsk); code != 200 || !strings.Contains(body, "The user is asking") || !strings.Contains(body, "from b") || a.n != thoughtCleanMost+1 || b.n != 1 {
		t.Fatalf("trusted refusal: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	// and the member is held again: the same refusal now goes to the next
	// member, its reasoning with nothing shown
	if code, body := sendTo(s, "/v1/messages", claudeCodeAsk); code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "The user is asking") || a.n != thoughtCleanMost+2 || b.n != 2 {
		t.Fatalf("held again: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	if r := lastRoute(s); len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[1].Fail != "" {
		t.Fatalf("tries: %+v", r.Tries)
	}
}

// A group's Claude member is trusted as a DeepSeek one is: once its
// thinking has gone on to an answer thoughtCleanMost times running, its
// reasoning goes as it comes while a refusal after it is still handed to
// the next member in the same stream (refusalHandover). Held, Claude Code
// on a group of one model's accounts saw the thinking only with the text,
// all at once, and never its "thinking with <effort> effort" (#4166). A
// refusal takes the trust back, as DeepSeek's does.
func TestClaudeThinkingStreamsOnceTrusted(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{
		{200, "text/event-stream", anthropicThoughtThenAnswered},
		{200, "text/event-stream", anthropicThoughtThenAnswered},
		{200, "text/event-stream", anthropicThoughtThenAnswered},
		{200, "text/event-stream", anthropicThoughtThenRefused},
		{200, "text/event-stream", anthropicThoughtThenRefused},
	}}
	scriptedOn(t, "a", provider.Anthropic, a)
	pa, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	pa.Models = []string{"claude-opus-5-5"}
	if err := provider.Save(*pa); err != nil {
		t.Fatal(err)
	}
	b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}, {200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "b", provider.Anthropic, b)
	refusalGroup(t, "a/claude-opus-5-5", "b/m")
	s := New()
	if !s.holdsThinking(candidate{p: *pa, model: "claude-opus-5-5"}, true) {
		t.Fatal("an untried Claude member's reasoning isn't held")
	}
	for i := 0; i < thoughtCleanMost; i++ {
		if code, body := sendTo(s, "/v1/messages", claudeCodeAsk); code != 200 || !strings.Contains(body, "from a") || strings.Contains(body, "from b") {
			t.Fatalf("warm-up %d: %d %s (a %d, b %d)", i+1, code, body, a.n, b.n)
		}
	}
	// trusted only where a refusal can still be handed over: where it
	// can't, it would reach the agent and end the turn with nothing said
	if s.holdsThinking(candidate{p: *pa, model: "claude-opus-5-5"}, true) {
		t.Fatal("a trusted Claude member's reasoning is still held")
	}
	if !s.holdsThinking(candidate{p: *pa, model: "claude-opus-5-5"}, false) {
		t.Fatal("a trusted Claude member's reasoning goes through with no handover behind it")
	}
	// trusted, the refused reply's reasoning went through as it came, and
	// the refusal after it, nothing yet said, went to the next member,
	// whose answer follows in the same stream, as one reply
	code, body := sendTo(s, "/v1/messages", claudeCodeAsk)
	if code != 200 || !strings.Contains(body, "The user wants a plan for") || !strings.Contains(body, "from b") || strings.Contains(body, `"refusal"`) ||
		strings.Count(body, "event: message_start") != 1 || a.n != thoughtCleanMost+1 || b.n != 1 {
		t.Fatalf("trusted refusal: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	if r := lastRoute(s); len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil || r.Tries[1].Fail != "" {
		t.Fatalf("tries: %+v", r.Tries)
	}
	// and the member is held again: the same refusal goes to the next
	// member with its reasoning shown to nobody
	code, body = sendTo(s, "/v1/messages", claudeCodeAsk)
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "The user wants a plan for") || a.n != thoughtCleanMost+2 || b.n != 2 {
		t.Fatalf("held again: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}
}

// Trusted, a group's Claude member's thinking reaches the agent as it
// comes, the text a good while after the first thought (#4166).
func TestClaudeTrustedThinkingStreamsThroughGroup(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{
		{200, "text/event-stream", anthropicThoughtThenAnswered},
		{200, "text/event-stream", anthropicThoughtThenAnswered},
		{200, "text/event-stream", anthropicThoughtThenAnswered},
	}}
	scriptedOn(t, "a", provider.Anthropic, a)
	pa, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	pa.Models = []string{"claude-opus-5-5"}
	if err := provider.Save(*pa); err != nil {
		t.Fatal(err)
	}
	scriptedOn(t, "b", provider.Anthropic, &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}})
	refusalGroup(t, "a/claude-opus-5-5", "b/m")
	s := New()
	for i := 0; i < thoughtCleanMost; i++ {
		if code, body := sendTo(s, "/v1/messages", claudeCodeAsk); code != 200 || !strings.Contains(body, "from a") {
			t.Fatalf("warm-up %d: %d %s (a %d)", i+1, code, body, a.n)
		}
	}
	up := &slowThinker{events: glmThinksSlowly, gap: 200 * time.Millisecond, done: make(chan time.Time, 1)}
	vendor := httptest.NewServer(up)
	t.Cleanup(vendor.Close)
	pa, err = provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	pa.Anthropic = vendor.URL
	if err := provider.Save(*pa); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	res, err := http.Post(gw.URL+"/v1/messages", "application/json", strings.NewReader(claudeCodeAsk))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var firstThought, firstText time.Time
	rd := bufio.NewReader(res.Body)
	for {
		ln, err := rd.ReadString('\n')
		if firstThought.IsZero() && strings.Contains(ln, `"thinking_delta"`) {
			firstThought = time.Now()
		}
		if firstText.IsZero() && strings.Contains(ln, `"text_delta"`) {
			firstText = time.Now()
		}
		if err != nil {
			break
		}
	}
	select {
	case <-up.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the vendor never finished")
	}
	if firstThought.IsZero() || firstText.IsZero() {
		t.Fatal("no thinking or no text reached the client")
	}
	// three thoughts 200ms apart come before the text
	if firstText.Sub(firstThought) < 300*time.Millisecond {
		t.Fatalf("thinking held: first thought only %v before the text", firstText.Sub(firstThought))
	}
	if r := lastRoute(s); len(r.Tries) != 1 {
		t.Fatalf("the trusted member's reply was asked again: %+v", r.Tries)
	}
}

// Trusted, a group's Claude member whose stream fails after its thinking
// went through, nothing yet said (Anthropic's overloaded_error), still
// fails over: the next member answers in the same stream, as one reply,
// as it did while the thinking was held (#4166). Let through, the error
// reached Claude Code and the group's next member was never asked.
func TestClaudeTrustedThinkingErrorFailsOver(t *testing.T) {
	fresh(t)
	overloaded := sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","model":"m","content":[],"usage":{"input_tokens":2}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"The user wants a plan for"}}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	a := &scripted{replies: []reply{
		{200, "text/event-stream", anthropicThoughtThenAnswered},
		{200, "text/event-stream", anthropicThoughtThenAnswered},
		{200, "text/event-stream", anthropicThoughtThenAnswered},
		{200, "text/event-stream", overloaded},
	}}
	scriptedOn(t, "a", provider.Anthropic, a)
	pa, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	pa.Models = []string{"claude-opus-5-5"}
	if err := provider.Save(*pa); err != nil {
		t.Fatal(err)
	}
	b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "b", provider.Anthropic, b)
	refusalGroup(t, "a/claude-opus-5-5", "b/m")
	s := New()
	for i := 0; i < thoughtCleanMost; i++ {
		if code, body := sendTo(s, "/v1/messages", claudeCodeAsk); code != 200 || !strings.Contains(body, "from a") {
			t.Fatalf("warm-up %d: %d %s (a %d)", i+1, code, body, a.n)
		}
	}
	code, body := sendTo(s, "/v1/messages", claudeCodeAsk)
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "Overloaded") ||
		strings.Count(body, "event: message_start") != 1 || b.n != 1 {
		t.Fatalf("error after trusted thinking: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	if r := lastRoute(s); len(r.Tries) != 2 || r.Tries[0].Status != 529 || r.Tries[1].Fail != "" {
		t.Fatalf("tries: %+v", r.Tries)
	}
}

// A try that is asked again asks the same fake vendor again: a reply
// that only reasoned is asked again in place (#667), and a full run's
// state may have any ask retried. The fake's done holds one send, and
// the ask's first serve has filled it: a second handler that blocks on
// the send never returns, the reply it serves never ends, and the
// package hangs on vendor's Close until go test's own timeout (#4530).
// Asked again, the fake must serve to its end regardless.
func TestThinkingVendorAskedAgainStillEnds(t *testing.T) {
	fresh(t)
	thinks := []string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"first thought","thought":true}]}}],"modelVersion":"gemini-3-pro"}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":" second thought","thought":true}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"thoughtsTokenCount":8},"modelVersion":"gemini-3-pro"}`,
	}
	up := &slowThinker{events: thinks, gap: 0, done: make(chan time.Time, 1)}
	vendor := httptest.NewServer(up)
	t.Cleanup(vendor.Close)
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gemini-3-pro"}}
	p.Gemini = vendor.URL + "/v1beta"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)

	res, err := http.Post(gw.URL+"/v1/messages", "application/json",
		strings.NewReader(strings.Replace(claudeCodeAsk, "group/g", "relay/gemini-3-pro", 1)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	// the reply that said nothing is asked again in place: read it to its
	// end, whichever try ends it — a handler stuck on done never ends it
	rd := bufio.NewReader(res.Body)
	for {
		if _, err := rd.ReadString('\n'); err != nil {
			break
		}
	}
	select {
	case <-up.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the vendor never finished")
	}
	// the ask-again came: the second serve went to its end, its events
	// noted after the first serve's
	if len(up.sent) < 2*len(thinks) {
		t.Fatalf("the vendor served %d events of %d asks, the ask-again never ended", len(up.sent), 2)
	}
}

// Claude's or OpenAI's reasoning at a provider nothing else stands behind
// is streamed as it comes: held for a refusal (#248) that would go to the
// agent anyway, as the last candidate's does, a relay's Claude showed
// Claude Code its thinking all at once with the text, and Codex none until
// then (iTianbao on X). Each of the relay's APIs, to Claude Code and
// Codex. Gemini's too, which a reply that only reasoned no longer needs
// held for (it is asked again in place, #667): held, Codex on a Gemini
// model had nothing, not even response.created, until the text began
// (LuckymanJC on Discord).
func TestThinkingStreamsWithNoOneElse(t *testing.T) {
	geminiThinks := []string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"first thought","thought":true}]}}],"modelVersion":"gemini-3-pro"}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":" second thought","thought":true}]}}],"modelVersion":"gemini-3-pro"}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":" third thought","thought":true}]}}],"modelVersion":"gemini-3-pro"}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"thoughtsTokenCount":8},"modelVersion":"gemini-3-pro"}`,
	}
	responsesThinks := []string{
		`event: response.created` + "\n" + `data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`event: response.reasoning_summary_part.added` + "\n" + `data: {"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
		`event: response.reasoning_summary_text.delta` + "\n" + `data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"first thought"}`,
		`event: response.reasoning_summary_text.delta` + "\n" + `data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":" second thought"}`,
		`event: response.reasoning_summary_text.delta` + "\n" + `data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":" third thought"}`,
		`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"first thought second thought third thought"}]}}`,
		`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","item_id":"m1","output_index":1,"content_index":0,"delta":"done"}`,
		`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"first thought second thought third thought"}]},{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":9,"total_tokens":10}}}`,
	}
	codexAsks := `{"model":"relay/MODEL","stream":true,"reasoning":{"effort":"medium","summary":"auto"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	claudeAsks := strings.Replace(claudeCodeAsk, "group/g", "relay/MODEL", 1)
	for _, x := range []struct {
		name   string
		proto  provider.Protocol
		events []string
		model  string
	}{
		{"anthropic", provider.Anthropic, glmThinksSlowly, "claude-opus-5-5"},
		{"chat", provider.Chat, glmChatThinksSlowly, "claude-opus-5-5"},
		{"chat gpt", provider.Chat, glmChatThinksSlowly, "gpt-5.2"},
		{"responses", provider.Responses, responsesThinks, "gpt-5.2"},
		{"gemini", provider.Gemini, geminiThinks, "gemini-3-pro"},
		{"chat gemini", provider.Chat, glmChatThinksSlowly, "gemini-3-pro"},
	} {
		for _, ask := range []struct{ path, body, thought, text string }{
			{"/v1/messages", claudeAsks, `"thinking_delta"`, `"text_delta"`},
			{"/v1/responses", codexAsks, `"response.reasoning_summary_text.delta"`, `"response.output_text.delta"`},
		} {
			t.Run(x.name+ask.path, func(t *testing.T) {
				fresh(t)
				up := &slowThinker{events: x.events, gap: 200 * time.Millisecond, done: make(chan time.Time, 1)}
				vendor := httptest.NewServer(up)
				t.Cleanup(vendor.Close)
				p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{x.model}}
				switch x.proto {
				case provider.Anthropic:
					p.Anthropic = vendor.URL
				case provider.Chat:
					p.Chat = vendor.URL + "/v1"
				case provider.Gemini:
					p.Gemini = vendor.URL + "/v1beta"
				default:
					p.Responses = vendor.URL + "/v1"
				}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				gw := httptest.NewServer(New().Handler())
				t.Cleanup(gw.Close)
				res, err := http.Post(gw.URL+ask.path, "application/json", strings.NewReader(strings.Replace(ask.body, "MODEL", x.model, 1)))
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				var firstThought, firstText time.Time
				rd := bufio.NewReader(res.Body)
				for {
					ln, err := rd.ReadString('\n')
					if firstThought.IsZero() && strings.Contains(ln, ask.thought) {
						firstThought = time.Now()
					}
					if firstText.IsZero() && strings.Contains(ln, ask.text) {
						firstText = time.Now()
					}
					if err != nil {
						break
					}
				}
				select {
				case <-up.done:
				case <-time.After(5 * time.Second):
					t.Fatal("the vendor never finished")
				}
				if firstThought.IsZero() || firstText.IsZero() {
					t.Fatal("no thinking or no text reached the client")
				}
				if firstText.Sub(firstThought) < 300*time.Millisecond {
					t.Fatalf("thinking held: first thought only %v before the text", firstText.Sub(firstThought))
				}
			})
		}
	}
}
