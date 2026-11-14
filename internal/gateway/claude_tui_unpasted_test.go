package gateway

import (
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
)

// tuiUnpastedResult is what the interactive mode's helper answers a turn
// whose image Claude Code left as words (ok123now on X: Windows 10 LTSC
// 21H2, a 64x64 PNG). Its path has no backslash: macOS's sh echoes one
// as an escape.
var tuiUnpastedResult = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"` +
	claudetui.Unpastable + claudetui.ImageNotTaken + `: > C:/Users/me/AppData/Local/Temp/image-1.png"}`

// unpastedHarness is tuiThirdPartyHarness whose interactive Claude Code
// fails every turn as one whose image it didn't take.
func unpastedHarness(t *testing.T) (*resumeHarness, string) {
	t.Helper()
	tuiThirdPartyText = strings.ReplaceAll(tuiUnpastedResult, "'", `'"'"'`)
	t.Cleanup(func() { tuiThirdPartyText = tuiThirdPartyRefusal })
	return tuiThirdPartyHarness(t, "", "")
}

// askImage asks h's Claude subscription a first turn with one PNG, or
// with text alone.
func askImage(t *testing.T, h *resumeHarness, image, stream bool) (int, string, string, Usage) {
	t.Helper()
	content := `"what is it?"`
	if image {
		content = `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` +
			base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nred")) + `"}},{"type":"text","text":"what colour?"}]`
	}
	s := ""
	if stream {
		s = `"stream":true,`
	}
	body := `{"model":"claude-sonnet-5","max_tokens":100,` + s + `"messages":[{"role":"user","content":` + content + `}]}`
	rec := httptest.NewRecorder()
	var u Usage
	code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
	return code, msg, rec.Body.String(), u
}

// An image turn the interactive mode didn't take, nothing of its reply
// said, is asked again once as claude -p, which is sent the image, and the
// client has its answer (ok123now on X). Before, it had a 502, asked
// again, and had it again, and the conversation went no further.
func TestClaudeTUIUnpastedImageRedoneAsPrint(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "stream"}[stream], func(t *testing.T) {
			h, started := unpastedHarness(t)
			code, msg, body, u := askImage(t, h, true, stream)
			if code != 200 || !strings.Contains(body, "ok") || strings.Contains(body, claudetui.Unpastable) || u.ErrType != "" {
				t.Fatalf("%d %.300s %.400s %q", code, msg, body, u.ErrType)
			}
			if n := helperStarts(started); n != 1 {
				t.Fatalf("the interactive mode started %d times, want once", n)
			}
			runs := h.runs()
			if len(runs) != 2 {
				t.Fatalf("%d runs, want two: the interactive mode's, then claude -p", len(runs))
			}
			sent := 0
			for _, log := range runs {
				if strings.Contains(log, `"type":"image"`) {
					sent++
				}
			}
			// the interactive mode's fake logs nothing of what it was told
			if sent != 1 {
				t.Fatalf("the image went to %d runs, want claude -p's: %v", sent, runs)
			}
		})
	}
}

// Once this machine's interactive Claude Code didn't take an image, the
// next turns with images, of any conversation, run as claude -p from the
// start; a turn of text alone is still the interactive mode's.
func TestClaudeTUIUnpastedImagesRunAsPrint(t *testing.T) {
	h, started := unpastedHarness(t)
	if code, msg, body, _ := askImage(t, h, true, false); code != 200 {
		t.Fatalf("%d %.300s %.300s", code, msg, body)
	}
	if code, msg, body, _ := askImage(t, h, true, true); code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("%d %.300s %.300s", code, msg, body)
	}
	if n := helperStarts(started); n != 1 {
		t.Fatalf("the interactive mode started %d times, want once: images run as -p here now", n)
	}
	if runs := len(h.runs()); runs != 3 {
		t.Fatalf("%d runs, want 3", runs)
	}
	askImage(t, h, false, false)
	if n := helperStarts(started); n != 2 {
		t.Fatalf("the interactive mode started %d times, want twice: text is still tried there", n)
	}
}

// A failure to paste the message that does reach the client is a 400 no
// account rests for and the client doesn't ask again, never a 502: the
// same message fails the same way every try.
func TestClaudeUnpastableIsNoRetried502(t *testing.T) {
	code, _ := bridgeFailed("Claude Code", claudetui.Unpastable+claudetui.ImageNotTaken+": >", 0)
	if code != 400 {
		t.Fatalf("%d, want 400", code)
	}
	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Claude Code: ` + claudetui.ImageNotTaken + `"}}`)
	if retryable(code, body) {
		t.Fatal("asked of the next account")
	}
	switch f := failure(code, body); f {
	case failCredit, failQuota, failRate, failAuth:
		t.Fatalf("reads as the account's (%s)", f)
	}
	if code, _ := bridgeFailed("Claude Code", "Claude Code ended without an answer", 0); code != 502 {
		t.Fatalf("%d: another failure is still a 502", code)
	}
}
