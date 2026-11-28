package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Gemini client is given an error as Google's API gives it:
// {"error":{"code","message","status"}}, its status the name Google's
// HTTP mapping has for the code (google.rpc.Code). 409, 422, 499, 501 and
// 504 were all "UNKNOWN".
func TestGeminiErrorStatusNames(t *testing.T) {
	for code, want := range map[int]string{
		400: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 403: "PERMISSION_DENIED", 404: "NOT_FOUND",
		408: "DEADLINE_EXCEEDED", 409: "ABORTED", 413: "INVALID_ARGUMENT", 422: "INVALID_ARGUMENT",
		429: "RESOURCE_EXHAUSTED", 499: "CANCELLED", 500: "INTERNAL", 501: "UNIMPLEMENTED",
		502: "UNAVAILABLE", 503: "UNAVAILABLE", 504: "DEADLINE_EXCEEDED", 529: "UNAVAILABLE", 418: "UNKNOWN",
	} {
		rec := httptest.NewRecorder()
		writeError(rec, provider.Gemini, code, "no")
		var got struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != code ||
			got.Error.Code != code || got.Error.Status != want || got.Error.Message == "" {
			t.Errorf("%d: %d %s, want code %d status %s", code, rec.Code, rec.Body, code, want)
		}
	}
}

// An error said in a Gemini client's stream, once the client has the
// stream's headers (streamError: a held stream's failure, failTo after a
// keepalive), keeps its own status, not always 502 UNAVAILABLE: a rate
// limit is 429 RESOURCE_EXHAUSTED, which Gemini CLI waits out, and a 400
// is INVALID_ARGUMENT, which it doesn't send again. An error of no known
// status is still 502 UNAVAILABLE.
func TestGeminiStreamErrorKeepsItsStatus(t *testing.T) {
	for code, want := range map[int]string{429: "RESOURCE_EXHAUSTED", 400: "INVALID_ARGUMENT", 504: "DEADLINE_EXCEEDED"} {
		rec := httptest.NewRecorder()
		streamError(rec, provider.Gemini, "m1", code, "no")
		var said map[string]any
		for _, e := range events(rec.Body.String()) {
			if e["error"] != nil {
				said = e["error"].(map[string]any)
			}
		}
		if said == nil || said["code"] != float64(code) || said["status"] != want {
			t.Errorf("%d: the stream's error %v, want code %d status %s:\n%s", code, said, code, want, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	enc := makeEncoder(provider.Gemini, newSSEWriter(rec), &Request{Model: "m1"})
	enc.event(Event{Kind: KError, Text: "cut short"})
	if s := rec.Body.String(); !strings.Contains(s, `"code":502`) || !strings.Contains(s, `"status":"UNAVAILABLE"`) {
		t.Errorf("an error of no status: %s", s)
	}
}

// A translated reply a Messages upstream cuts after its first words with
// an error naming its status, as a plugin past its 200 says one ("(HTTP
// 429)", httpSaid), asked again from there (streamTranslated) and cut the
// same each time, ends a Gemini client's stream with 429
// RESOURCE_EXHAUSTED: the error it ends with keeps the held one's status.
func TestGeminiTranslatedStreamErrorKeepsItsStatus(t *testing.T) {
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"rate_limit_error","message":"Resource has been exhausted (HTTP 429)"}}`)}
	setup(t, provider.Anthropic, f)
	_, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	var said map[string]any
	for _, e := range events(body) {
		if e["error"] != nil {
			said = e["error"].(map[string]any)
		}
	}
	if said == nil || said["code"] != float64(429) || said["status"] != "RESOURCE_EXHAUSTED" {
		t.Errorf("the stream's error %v, want code 429 RESOURCE_EXHAUSTED:\n%s", said, body)
	}
}
