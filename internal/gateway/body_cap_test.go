package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// commandCodeCap is Command Code's refusal of a Codex session's
// compaction (EDwinpzy on Discord, v0.1.2605, Codex Desktop on Windows,
// auto/deepseek-v4.1-flash): the history held many screenshots, and the
// summary request was over the vendor's byte cap. It came under a status
// of its own, not 413.
const commandCodeCap = `{"error":{"message":"Request body is 40.1 MB, over the 32 MB limit for one request (33554432 bytes)","type":"commandcode_error","code":400}}`

const commandCodeMost = 32 << 20

// screenshot is a ~2 MB screenshot's data URL, its n in it so each is
// told apart.
func screenshot(n int) string {
	return fmt.Sprintf("data:image/png;base64,SHOT%02d", n) + strings.Repeat("iVBORw0KGgo", 190_000)
}

// screenshotSession is the reporter's shape: a Codex session of twenty
// screenshots, ~42 MB, each read by a tool (view_image's output) or
// pasted into a user message, then the latest user message with a
// screenshot of its own. compact ends it in Codex's compaction trigger.
func screenshotSession(compact bool) string {
	var b strings.Builder
	b.WriteString(`{"model":"fake/m1","stream":true,"store":false,"prompt_cache_key":"thread-shots","input":[`)
	for i := range 19 {
		if i%2 == 0 {
			fmt.Fprintf(&b, `{"type":"message","role":"user","content":[{"type":"input_text","text":"look at screen %d"},{"type":"input_image","image_url":"%s"}]},`, i, screenshot(i))
			continue
		}
		fmt.Fprintf(&b, `{"type":"function_call","call_id":"call_%d","name":"view_image","arguments":"{\"path\":\"shot%d.png\"}"},`, i, i)
		fmt.Fprintf(&b, `{"type":"function_call_output","call_id":"call_%d","output":[{"type":"input_text","text":"tool output %d kept"},{"type":"input_image","image_url":"%s"}]},`, i, i, screenshot(i))
		fmt.Fprintf(&b, `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"seen %d"}]},`, i)
	}
	fmt.Fprintf(&b, `{"type":"message","role":"user","content":[{"type":"input_text","text":"the latest ask"},{"type":"input_image","image_url":"%s"}]}`, screenshot(19))
	if compact {
		b.WriteString(`,{"type":"compaction_trigger"}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

// postResponses posts body to the gateway's /v1/responses as Codex.
func postResponses(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	req, _ := http.NewRequest("POST", gw.URL+"/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", codexUA)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// underCap checks a request sent after the cap was learned: under it, the
// oldest screenshots left out with the marker, the latest user message's
// screenshot and every text kept.
func underCap(t *testing.T, sent []byte) {
	t.Helper()
	s := string(sent)
	if len(sent) > commandCodeMost*3/4 {
		t.Errorf("sent %d bytes, over three quarters of the cap", len(sent))
	}
	if strings.Contains(s, "SHOT00") || !strings.Contains(s, imageLeftOut) {
		t.Error("the oldest screenshot wasn't left out with the marker")
	}
	if !strings.Contains(s, "SHOT19") {
		t.Error("the latest user message's screenshot was left out")
	}
	for i := range 19 {
		want := fmt.Sprintf("look at screen %d", i)
		if i%2 == 1 {
			want = fmt.Sprintf("tool output %d kept", i)
		}
		if !strings.Contains(s, want) {
			t.Errorf("text %q is gone", want)
		}
	}
	if strings.Contains(s, "This is the conversation so far") {
		t.Error("asked as text, not as the conversation with its later images")
	}
}

// Codex's compaction of a session over the vendor's byte cap was refused
// every time, three times, and then held for five minutes by the guard;
// the same oversized request again after that. Now the request is asked
// again with its oldest screenshots left out, and Codex gets the model's
// summary.
func TestCompactionOverByteCapLeavesOutOldImages(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Responses, provider.Chat} {
		t.Run(string(proto), func(t *testing.T) {
			up := &capped{proto: proto, most: commandCodeMost, status: 400, ctype: "application/json", body: commandCodeCap}
			cappedOn(t, up)
			code, body := codexPost(t, screenshotSession(true))
			if code != 200 {
				t.Fatalf("Codex's compaction failed: %d %.400s", code, body)
			}
			if got := compactedText(t, body); got != "SUMMARY" {
				t.Fatalf("summary %q", got)
			}
			calls := up.calls()
			if len(calls) != 2 || len(calls[0]) <= commandCodeMost {
				t.Fatalf("asked %d times (first %d bytes)", len(calls), len(calls[0]))
			}
			underCap(t, calls[1])
		})
	}
}

// A turn of the same session is fitted as its compaction is, and the next
// turn to the same provider goes fitted at once: the cap is learned.
func TestTurnOverByteCapLeavesOutOldImages(t *testing.T) {
	fresh(t)
	up := &capped{proto: provider.Responses, most: commandCodeMost, status: 400, ctype: "application/json", body: commandCodeCap}
	cappedOn(t, up)
	s := New()
	code, body := postResponses(t, s, screenshotSession(false))
	if code != 200 || !strings.Contains(body, "SUMMARY") {
		t.Fatalf("the turn failed: %d %.400s", code, body)
	}
	calls := up.calls()
	if len(calls) != 2 {
		t.Fatalf("asked %d times", len(calls))
	}
	underCap(t, calls[1])
	code, body = postResponses(t, s, screenshotSession(false))
	if code != 200 || !strings.Contains(body, "SUMMARY") {
		t.Fatalf("the next turn failed: %d %.400s", code, body)
	}
	if calls = up.calls(); len(calls) != 3 {
		t.Fatalf("the next turn was asked %d times, not once fitted", len(calls)-2)
	}
	underCap(t, calls[2])
}

// When the latest user message alone is over the cap there is nothing to
// leave out: the same body isn't sent again, and Codex is told it is a
// byte cap (context_length_exceeded, its cue to compact), with the size
// and the cap; another client gets a 413 saying so.
func TestOverByteCapNothingToLeaveOut(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"model":"fake/m1","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"all of these"}`)
	for i := range 20 {
		fmt.Fprintf(&b, `,{"type":"input_image","image_url":"%s"}`, screenshot(i))
	}
	b.WriteString(`]}]}`)
	fresh(t)
	up := &capped{proto: provider.Responses, most: commandCodeMost, status: 400, ctype: "application/json", body: commandCodeCap}
	cappedOn(t, up)
	code, body := postResponses(t, New(), b.String())
	if code != 200 || failedCode(body) != "context_length_exceeded" {
		t.Fatalf("Codex wasn't told to compact: %d %.600s", code, body)
	}
	for _, want := range []string{"over the 32 MB limit", "over the 32.0 MB 1017dp takes in one request", "no earlier image"} {
		if !strings.Contains(body, want) {
			t.Errorf("Codex's error lacks %q: %.600s", want, body)
		}
	}
	if n := len(up.calls()); n != 1 {
		t.Fatalf("the same body was sent %d times", n)
	}
}

// A plain refusal of a large request, a rate limit, or the model's window
// full is not a byte cap, and nothing is left out for it.
func TestByteCapRefusal(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, commandCodeCap, true},
		{500, commandCodeCap, true},
		{413, nginx413, true},
		{413, deepseekBodyCap, true},
		{413, `{"type":"error","error":{"type":"request_too_large","message":"Request exceeds the maximum size"}}`, true},
		{400, deepseekOverflow, false},
		{429, `{"error":{"message":"Request too large for gpt-4o on tokens per min (TPM): Limit 30000"}}`, false},
		{400, `{"error":{"message":"Invalid tool schema"}}`, false},
		{401, commandCodeCap, false},
	} {
		if got := byteCapRefusal(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%d %.60s: %v", c.status, c.body, got)
		}
	}
	if n := statedCap([]byte(commandCodeCap), 42<<20); n != 32<<20 {
		t.Errorf("stated cap %d", n)
	}
	if n := statedCap([]byte(deepseekBodyCap), 42<<20); n != 0 {
		t.Errorf("stated cap %d of none", n)
	}
}

// Each protocol's images are left out where it keeps them, the latest
// user message's kept and the rest of the body as it came.
func TestWithoutOldImagesEachProtocol(t *testing.T) {
	img := strings.Repeat("A", 4096)
	for _, c := range []struct {
		to   provider.Protocol
		body string
	}{
		{provider.Chat, `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,OLD` + img + `"}}]},{"role":"tool","tool_call_id":"c","content":[{"type":"text","text":"tool text"},{"type":"image_url","image_url":{"url":"data:image/png;base64,MID` + img + `"}}]},{"role":"user","content":[{"type":"text","text":"now"},{"type":"image_url","image_url":{"url":"data:image/png;base64,NEW` + img + `"}}]}]}`},
		{provider.Anthropic, `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"OLD` + img + `"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"tool text"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"MID` + img + `"}}]}]},{"role":"user","content":[{"type":"text","text":"now"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"NEW` + img + `"}}]}]}`},
		{provider.Gemini, `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"OLD` + img + `"}}]},{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"text":"tool text"}}},{"inlineData":{"mimeType":"image/png","data":"MID` + img + `"}}]},{"role":"user","parts":[{"text":"now"},{"inlineData":{"mimeType":"image/png","data":"NEW` + img + `"}}]}]}`},
	} {
		t.Run(string(c.to), func(t *testing.T) {
			out, left, ok := withoutOldImages(c.to, []byte(c.body), len(c.body)-6000)
			s := string(out)
			if !ok || left != 2 || strings.Contains(s, "OLD") || strings.Contains(s, "MID") || !strings.Contains(s, "NEW") || !strings.Contains(s, "tool text") || strings.Count(s, imageLeftOut) != 2 {
				t.Fatalf("ok %v, %d left out: %.300s", ok, left, s)
			}
			if _, _, ok := withoutOldImages(c.to, []byte(c.body), 4000); ok {
				t.Fatal("fitted under what the latest message alone holds")
			}
		})
	}
}

// A provider that refuses the summary request however small its images
// make it: asked smaller three times, then said as a 413, so the
// compaction is asked as text and then made without a model, not sent
// again into the compaction guard's hold.
func TestCompactionOverByteCapEveryTime(t *testing.T) {
	up := &capped{proto: provider.Responses, most: 0, status: 400, ctype: "application/json", body: commandCodeCap}
	cappedOn(t, up)
	code, body := codexPost(t, screenshotSession(true))
	if code != 200 {
		t.Fatalf("Codex's compaction failed: %d %.400s", code, body)
	}
	if got := compactedText(t, body); !strings.Contains(got, "the latest ask") || strings.Contains(got, "iVBORw0KGgo") {
		t.Fatalf("local summary: %.300s", got)
	}
	calls := up.calls()
	if len(calls) > 6 {
		t.Fatalf("asked %d times", len(calls))
	}
	for i := 1; i < 4 && i < len(calls); i++ {
		if len(calls[i]) >= len(calls[i-1]) {
			t.Fatalf("ask %d (%d bytes) no smaller than the one before (%d)", i, len(calls[i]), len(calls[i-1]))
		}
	}
}
