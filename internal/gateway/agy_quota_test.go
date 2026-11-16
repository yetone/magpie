package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// @Airan_FS on X (11-17): an agy-cli account out of its quota answered
// "⚠ Individual quota reached. Please upgrade your subscription to
// increase your limits. Resets in …" as the model's reply, and the next
// account was never asked. The agy-cli plugin (plugins packages/agy-cli,
// 0.1.87) now ends such a turn with a rate_limit_error in these words,
// after the message_start it has already streamed; the reset is Google's,
// written as Go prints a duration ("Resets in 1h18m40s.", the words a
// user quoted on discuss.ai.google.dev). The account rests until then and
// the next member is asked.
const agyQuotaNotice = "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h18m40s."

func agyQuotaStream(text string) string {
	lines := []string{
		"event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":0,"output_tokens":0}}}`,
	}
	if text != "" {
		lines = append(lines,
			"event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+text+`"}}`,
			"event: content_block_stop\ndata: "+`{"type":"content_block_stop","index":0}`)
	}
	return sse(append(lines, "event: error\ndata: "+`{"type":"error","error":{"type":"rate_limit_error","message":"agy bridge: Antigravity quota reached: `+agyQuotaNotice+`"}}`)...)
}

func TestAgyCLIQuotaNoticeRestsUntilItsResetAndFallsOver(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", agyQuotaStream("")}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || strings.Contains(body, "quota") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Fail != failQuota || r.Tries[0].Rest == nil {
		t.Fatalf("the used-up try: %+v", r.Tries)
	}
	want := time.Hour + 18*time.Minute + 40*time.Second
	if d := time.Until(r.Tries[0].Rest.Until); r.Tries[0].Rest.By != "resets" || d < want-time.Minute || d > want {
		t.Fatalf("a rests %v by %q, not the %v its quota resets in", d, r.Tries[0].Rest.By, want)
	}
	// the next turn goes to b without asking a
	if code, _ := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"again"}]}`); code != 200 || a.n != 1 || b.n != 2 {
		t.Fatalf("next turn %d (a %d, b %d)", code, a.n, b.n)
	}
}

// A reply that had begun before the quota ran out can't be taken back: the
// client keeps its words and is told why it stopped, and the account rests
// until its reset all the same.
func TestAgyCLIQuotaNoticeMidReplyStillRests(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", agyQuotaStream("PARTIAL answer")}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	s := New()
	_, body := postAs(t, s, "", `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, "PARTIAL answer") || !strings.Contains(body, "Individual quota reached") {
		t.Fatalf("the client got %s", body)
	}
	rest, ok := restOf("a")
	want := time.Hour + 18*time.Minute + 40*time.Second
	if !ok || rest.By != "resets" {
		t.Fatalf("a rests %+v (%v)", rest, ok)
	}
	if d := time.Until(rest.Until); d < want-time.Minute || d > want {
		t.Fatalf("a rests %v, not %v", d, want)
	}
}

// The reset as agy's notice writes it, as Go prints a duration.
func TestAgyQuotaNoticeResetIsRead(t *testing.T) {
	for _, c := range []struct {
		body string
		want time.Duration
	}{
		{agyQuotaNotice, time.Hour + 18*time.Minute + 40*time.Second},
		{"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 45m2s.", 45*time.Minute + 2*time.Second},
		{"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 26s.", 26 * time.Second},
		{"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 120h5m0s.", 120*time.Hour + 5*time.Minute},
		{"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 4h.", 4 * time.Hour},
		{"Individual quota reached. Please upgrade your subscription to increase your limits.", 0},
	} {
		if got := resetsAfter([]byte(c.body)); got != c.want {
			t.Errorf("%s: %v, want %v", c.body, got, c.want)
		}
	}
}
