package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// What Hana (HanaAgent 1.0.6-beta, pi-ai's openai-completions) sends
// (#1382). pi's read tool answers an image with the text "Read image file
// [image/png]" and the image; pi-ai sends the text as the tool message and
// the images of the results in a user message after them, "Attached
// image(s) from tool result:" first. Hana's pre-step hook then replaces
// each image before the last assistant message with hanaImageGone, in the
// result that held it, which pi-ai joins to the result's text with "\n";
// the user message of images is gone.
const (
	hanaImageGone = "[图片已省略：历史图片保留为文件引用，避免重复发送原始 base64]"
	onePNG        = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
)

func piUser(text string) string { return `{"role":"user","content":` + strconv.Quote(text) + `}` }

func piUserParts(parts ...string) string {
	return `{"role":"user","content":[` + strings.Join(parts, ",") + `]}`
}

func piText(text string) string { return `{"type":"text","text":` + strconv.Quote(text) + `}` }

func piImage() string {
	return `{"type":"image_url","image_url":{"url":"data:image/png;base64,` + onePNG + `"}}`
}

func piCalls(ids ...string) string {
	var calls []string
	for i, id := range ids {
		calls = append(calls, fmt.Sprintf(`{"id":%q,"type":"function","function":{"name":"read","arguments":"{\"path\":\"shot%d.png\"}"}}`, id, i+1))
	}
	return `{"role":"assistant","content":null,"tool_calls":[` + strings.Join(calls, ",") + `]}`
}

func piResult(id, text string) string {
	return `{"role":"tool","content":` + strconv.Quote(text) + `,"tool_call_id":` + strconv.Quote(id) + `}`
}

func piSays(text string) string { return `{"role":"assistant","content":` + strconv.Quote(text) + `}` }

// piImages is pi-ai's user message of the results' images.
func piImages(n int) string {
	parts := []string{piText("Attached image(s) from tool result:")}
	for range n {
		parts = append(parts, piImage())
	}
	return piUserParts(parts...)
}

func piBody(msgs ...string) string {
	return `{"model":"claude-opus-5-5","stream":false,"messages":[{"role":"system","content":"rules"},` + strings.Join(msgs, ",") + `],"tools":[{"type":"function","function":{"name":"read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`
}

func piMessages(t *testing.T, msgs ...string) []Message {
	t.Helper()
	req, err := parseChat([]byte(piBody(msgs...)))
	if err != nil {
		t.Fatal(err)
	}
	return req.Messages
}

// The images a client took out of its conversation, and only that, are
// told from every other change to it (lostMedia).
func TestLostMediaIsOnlyTheImagesTakenOut(t *testing.T) {
	read := "Read image file [image/png]"
	gone := read + "\n" + hanaImageGone
	ask := piUser("看看这几张截图")
	for _, c := range []struct {
		name     string
		was, now []string
		want     bool
	}{
		{"Hana: the images' message gone, a line added to the result",
			[]string{ask, piCalls("call_1"), piResult("call_1", read), piImages(1)},
			[]string{ask, piCalls("call_1"), piResult("call_1", gone)}, true},
		{"Hana: six reads at once",
			[]string{ask, piCalls("c1", "c2", "c3", "c4", "c5", "c6"), piResult("c1", read), piResult("c2", read), piResult("c3", read), piResult("c4", read), piResult("c5", read), piResult("c6", read), piImages(6)},
			[]string{ask, piCalls("c1", "c2", "c3", "c4", "c5", "c6"), piResult("c1", gone), piResult("c2", gone), piResult("c3", gone), piResult("c4", gone), piResult("c5", gone), piResult("c6", gone)}, true},
		{"Hana: a user's image replaced by the line",
			[]string{piUserParts(piText("这是什么"), piImage())},
			[]string{piUserParts(piText("这是什么"), piText(hanaImageGone))}, true},
		{"Hana: a user's image the text names left out",
			[]string{piUserParts(piText("看 [attached_image: /tmp/a.png]"), piImage())},
			[]string{piUserParts(piText("看 [attached_image: /tmp/a.png]"))}, true},
		{"images before the user's text replaced",
			[]string{piUserParts(piImage(), piImage(), piText("这两张有什么不同"))},
			[]string{piUserParts(piText(hanaImageGone), piText(hanaImageGone), piText("这两张有什么不同"))}, true},
		{"a message of images alone gone",
			[]string{ask, piCalls("call_1"), piResult("call_1", read), piUserParts(piImage())},
			[]string{ask, piCalls("call_1"), piResult("call_1", read)}, true},

		{"a result's own image replaced by the line",
			[]string{ask, piCalls("call_1"), `{"role":"tool","content":[` + piText(read) + `,` + piImage() + `],"tool_call_id":"call_1"}`},
			[]string{ask, piCalls("call_1"), piResult("call_1", gone)}, true},

		{"nothing taken out",
			[]string{ask, piCalls("call_1"), piResult("call_1", read)},
			[]string{ask, piCalls("call_1"), piResult("call_1", read)}, false},
		{"the result's text changed, not added to",
			[]string{ask, piCalls("call_1"), piResult("call_1", read), piImages(1)},
			[]string{ask, piCalls("call_1"), piResult("call_1", "Read image file [image/jpeg]\n"+hanaImageGone)}, false},
		{"the images' text gone with nothing in their place",
			[]string{ask, piCalls("call_1"), piResult("call_1", read), piImages(1)},
			[]string{ask, piCalls("call_1"), piResult("call_1", read)}, false},
		{"text added to a result with no image",
			[]string{ask, piCalls("call_1"), piResult("call_1", "A")},
			[]string{ask, piCalls("call_1"), piResult("call_1", "A and more")}, false},
		{"text added to a result with no image, an image taken out before it",
			[]string{piUserParts(piText("这是什么"), piImage()), piCalls("call_1"), piResult("call_1", "A")},
			[]string{piUserParts(piText("这是什么"), piText(hanaImageGone)), piCalls("call_1"), piResult("call_1", "A and more")}, false},
		{"text added after the user's words",
			[]string{piUserParts(piImage(), piText("这是什么"))},
			[]string{piUserParts(piText(hanaImageGone), piText("这是什么"), piText("还有这个"))}, false},
		{"the user's words changed beside the image",
			[]string{piUserParts(piText("这是什么"), piImage())},
			[]string{piUserParts(piText("这是谁"), piText(hanaImageGone))}, false},
		{"an earlier message changed beside the images",
			[]string{ask, piCalls("call_1"), piResult("call_1", read), piImages(1)},
			[]string{piUser("另一个问题"), piCalls("call_1"), piResult("call_1", gone)}, false},
		{"compacted, as Pi does (#459)",
			[]string{ask, piCalls("call_1"), piResult("call_1", read), piImages(1)},
			[]string{piUser("a summary of the conversation so far")}, false},
		{"a user's message of text gone with the images",
			[]string{ask, piCalls("call_1"), piResult("call_1", read), piUserParts(piText("also fix the title"), piImage())},
			[]string{ask, piCalls("call_1"), piResult("call_1", read)}, false},
		{"a placeholder where no image was",
			[]string{piUserParts(piText("这是什么"))},
			[]string{piUserParts(piText("这是什么"), piText(hanaImageGone))}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			was, now := heardRuns(piMessages(t, c.was...), true), heardRuns(piMessages(t, c.now...), false)
			if got := lostMedia(was, now); got != c.want {
				t.Fatalf("lostMedia: %t, want %t", got, c.want)
			}
		})
	}
}

// sendChat asks a turn of the harness's Claude Code as pi-ai's
// openai-completions does.
func (h *resumeHarness) sendChat(msgs ...string) {
	h.t.Helper()
	body := piBody(msgs...)
	var u Usage
	if code, msg := h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)), provider.Chat, h.p, "claude-opus-5-5", []byte(body), &u); code != 200 {
		h.t.Fatalf("%d %s", code, msg)
	}
}

// A turn whose tool read an image, answered, is sent back by Hana with
// the image taken out (#1382): its next turn goes on in the run that had
// it, told the new turn alone, where it was told to a new Claude Code
// whole, all of it written to the prompt cache again.
func TestClaudeTurnWithItsImagesTakenOutGoesOnInItsRun(t *testing.T) {
	read := "Read image file [image/png]"
	for _, c := range []struct {
		name     string
		was, now []string
	}{
		{"a tool's image",
			[]string{piUser("看看截图"), piCalls("call_1"), piResult("call_1", read), piImages(1)},
			[]string{piUser("看看截图"), piCalls("call_1"), piResult("call_1", read+"\n"+hanaImageGone)}},
		{"a user's image",
			[]string{piUserParts(piText("这是什么"), piImage())},
			[]string{piUserParts(piText("这是什么"), piText(hanaImageGone))}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newReplyHarness(t, "一只猫")
			h.sendChat(c.was...)
			h.sendChat(append(c.now, piSays("一只猫"), piUser("more"))...)
			if n := len(h.runs()); n != 1 {
				t.Fatalf("runs: %d, want the one that had the last turn", n)
			}
			told := h.toldLast()
			if !strings.Contains(told, "more") || strings.Contains(told, "看看") || strings.Contains(told, "这是什么") {
				t.Fatalf("the run was told more than the turn:\n%s", told)
			}
		})
	}
}

// Tool results after a step that read an image, sent by Hana with the
// image taken out (#1382), are the run's that read it: it goes on, where
// it was let go and the conversation told to a new one. A conversation
// changed otherwise still starts anew (#459).
func TestClaudeToolResultsAfterTheImagesTakenOutGoOn(t *testing.T) {
	read := "Read image file [image/png]"
	for _, c := range []struct {
		name string
		then []string // the conversation before call_2's result
		runs int
	}{
		{"images taken out", []string{piUser("看看截图"), piCalls("call_1"), piResult("call_1", read+"\n"+hanaImageGone)}, 1},
		{"the user's words changed", []string{piUser("另一个问题"), piCalls("call_1"), piResult("call_1", read+"\n"+hanaImageGone)}, 2},
		{"the result changed", []string{piUser("看看截图"), piCalls("call_1"), piResult("call_1", "Read image file [image/jpeg]\n"+hanaImageGone)}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := New()
			b := s.subscription
			calls := func(run *subscriptionRun, id string) chan mcpToolResult {
				waiter := make(chan mcpToolResult, 1)
				run.mu.Lock()
				run.pending[id] = waiter
				run.mu.Unlock()
				b.mu.Lock()
				b.calls[id] = run
				b.mu.Unlock()
				run.emit(Event{Kind: KStart, MsgID: "m", Model: "claude-opus-5-5"})
				run.emit(Event{Kind: KToolStart, ID: id, Name: "read"})
				run.emit(Event{Kind: KToolArgs, Text: `{"path":"shot.png"}`})
				run.emit(Event{Kind: KStop, Stop: "tool"})
				run.endSegment()
				return waiter
			}
			says := func(run *subscriptionRun, text string) {
				run.emit(Event{Kind: KStart, MsgID: "m", Model: "claude-opus-5-5"})
				run.emit(Event{Kind: KText, Text: text})
				run.emit(Event{Kind: KStop, Stop: "stop"})
				run.endSegment()
			}
			var started []*subscriptionRun
			var first chan mcpToolResult
			second := make(chan chan mcpToolResult, 1)
			start := func(_ context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
				run := &subscriptionRun{bridge: b, token: "run" + strconv.Itoa(len(started)), pending: map[string]chan mcpToolResult{}}
				b.mu.Lock()
				b.runs[run.token] = run
				b.mu.Unlock()
				events := run.attach()
				if len(started) == 0 {
					first = calls(run, "call_1")
				} else {
					says(run, "a new run")
				}
				started = append(started, run)
				return run, events, nil
			}
			ask := func(msgs ...string) string {
				t.Helper()
				body := piBody(msgs...)
				rec := httptest.NewRecorder()
				var u Usage
				if code, msg := s.serveSubscription(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)), provider.Chat, "Claude Code", "claude-opus-5-5", "", []byte(body), &u, start); code != 200 {
					t.Fatalf("%d %s", code, msg)
				}
				var res struct {
					Choices []struct {
						Message struct {
							Content   string
							ToolCalls []struct{ ID string } `json:"tool_calls"`
						}
					}
				}
				json.Unmarshal(rec.Body.Bytes(), &res)
				if len(res.Choices) == 0 {
					t.Fatalf("no answer: %s", rec.Body)
				}
				if m := res.Choices[0].Message; len(m.ToolCalls) > 0 {
					return "call " + m.ToolCalls[0].ID
				} else {
					return m.Content
				}
			}
			if got := ask(piUser("看看截图")); got != "call call_1" {
				t.Fatalf("the first step: %q", got)
			}
			go func(run *subscriptionRun) {
				if _, ok := <-first; ok {
					second <- calls(run, "call_2")
				}
			}(started[0])
			// the step that read the image: its result, and the image after it
			if got := ask(piUser("看看截图"), piCalls("call_1"), piResult("call_1", read), piImages(1)); got != "call call_2" || len(started) != 1 {
				t.Fatalf("the image read: %q from %d runs", got, len(started))
			}
			go func(run *subscriptionRun, waiter chan mcpToolResult) {
				if _, ok := <-waiter; ok {
					says(run, "the run that read the image")
				}
			}(started[0], <-second)
			got := ask(append(c.then, piCalls("call_2"), piResult("call_2", "B"))...)
			if len(started) != c.runs {
				t.Fatalf("runs: %d, want %d (answered %q)", len(started), c.runs, got)
			}
			if want := map[int]string{1: "the run that read the image", 2: "a new run"}[c.runs]; got != want {
				t.Fatalf("answered %q, want %q", got, want)
			}
			b.abortAll()
		})
	}
}
