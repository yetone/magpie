package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/yetone/magpie/internal/provider"
)

// agBareError is the line Antigravity ended zRain's gemini-4-argon-high
// stream with, after its 200 and a few thinking events (Discord
// #feedback, v0.1.3039/v0.1.3041, Windows): no "data:" before it, and
// nothing after it but the end of the body.
const agBareError = `{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}`

// agThinking is Code Assist's thinking events before it, as Antigravity
// streams them (CRLF, each in its {response} wrapper); the reporter's own
// were not sent, only described.
const agThinking = "data: {\"response\": {\"candidates\": [{\"content\": {\"role\": \"model\",\"parts\": [{\"text\": \"**Weighing the request**\\n\\n\",\"thought\": true}]}}],\"usageMetadata\": {\"promptTokenCount\": 5210,\"totalTokenCount\": 5210},\"modelVersion\": \"gemini-4-argon\"},\"traceId\": \"9d1c0e2f\"}\r\n\r\n" +
	"data: {\"response\": {\"candidates\": [{\"content\": {\"role\": \"model\",\"parts\": [{\"text\": \"I'll look at the files first.\",\"thought\": true}]}}],\"usageMetadata\": {\"promptTokenCount\": 5210,\"totalTokenCount\": 5230},\"modelVersion\": \"gemini-4-argon\"},\"traceId\": \"9d1c0e2f\"}\r\n\r\n"

// the bare error ended as it came: with a line end, a CRLF, or none
var agBareEnds = map[string]string{
	"LF":   agThinking + agBareError + "\n",
	"CRLF": agThinking + agBareError + "\r\n",
	"EOF":  agThinking + agBareError,
}

func readEvents(t *testing.T, r io.Reader) []string {
	t.Helper()
	var got []string
	if err := readSSE(r, func(_, data string) error {
		got = append(got, data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

// The bare error is the stream's last event, as if it had come as data,
// however the body is split; an event of the stream's own is untouched.
func TestBareErrorLineIsAnEvent(t *testing.T) {
	for name, body := range agBareEnds {
		for _, split := range []bool{false, true} {
			var r io.Reader = strings.NewReader(body)
			if split {
				r = iotest.OneByteReader(r)
			}
			got := readEvents(t, r)
			if len(got) != 3 || got[2] != agBareError || !strings.Contains(got[1], "look at the files") {
				t.Errorf("%s (one byte at a time %v): %q", name, split, got)
			}
		}
	}

	// Google's own pretty-printed error, a list of one (its JSON array
	// stream), OpenAI's and Anthropic's shapes, right after a data line
	// with no blank line between
	for _, tc := range []struct{ bare, want string }{
		{"{\n  \"error\": {\n    \"code\": 400,\n    \"message\": \"Request contains an invalid argument.\",\n    \"status\": \"INVALID_ARGUMENT\"\n  }\n}\n", agBareError},
		{"[{\n  \"error\": {\n    \"code\": 429,\n    \"message\": \"Resource has been exhausted (e.g. check quota).\",\n    \"status\": \"RESOURCE_EXHAUSTED\"\n  }\n}\n]\n", `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`},
		{`{"error":{"message":"bad","type":"invalid_request_error","code":null}}` + "\n", `{"error":{"message":"bad","type":"invalid_request_error","code":null}}`},
		{`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
	} {
		got := readEvents(t, iotest.OneByteReader(strings.NewReader("data: {\"a\":1}\n"+tc.bare)))
		if len(got) != 2 || got[0] != `{"a":1}` || got[1] != tc.want {
			t.Errorf("%q: %q", tc.bare, got)
		}
	}

	// what isn't an error goes as it came, and SSE skips it as before
	for _, other := range []string{
		`{"candidates":[]}`,
		`{"error":null}`,
		`[{"error":{"code":1}},{"error":{"code":2}}]`,
		`{"error": {"code": 400,`,
		`not json`,
	} {
		body := "data: {\"a\":1}\n\n" + other + "\ndata: {\"b\":2}\n\n"
		out, _ := io.ReadAll(bareErrors(iotest.OneByteReader(strings.NewReader(body))))
		if string(out) != body {
			t.Errorf("%q changed: %q", other, out)
		}
	}

	// a stream with no bare line goes byte for byte
	for _, body := range []string{vertexThought, agThinking, "data: x\r\n\r\n: ping\r\nevent: e\r\ndata: y"} {
		out, _ := io.ReadAll(bareErrors(iotest.OneByteReader(strings.NewReader(body))))
		if string(out) != body {
			t.Errorf("a stream changed: %q", out)
		}
	}
}

// A line under way goes on before it ends: only one that may be a bare
// JSON value waits for its end, so a relayed stream isn't held back.
func TestBareErrorsHoldsNothingElse(t *testing.T) {
	pr, pw := io.Pipe()
	r := bareErrors(pr)
	go pw.Write([]byte("data: {\"text\":\"hel"))
	buf := make([]byte, 64)
	n, err := r.Read(buf)
	if err != nil || string(buf[:n]) != "data: {\"text\":\"hel" {
		t.Fatalf("a data line's start was held: %q %v", buf[:n], err)
	}
	pw.Close()
}

// zRain's case end to end: Antigravity's 200 stream that ends on the bare
// error is told as the 400 it is, with Google's words, not as a reply
// that "ended before it was complete". It is the request's fault: the
// account doesn't rest, and the route books the 400.
func TestAntigravityBareErrorIsTheVendors(t *testing.T) {
	for name, reply := range agBareEnds {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%v", name, stream), func(t *testing.T) {
				s, _ := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, reply)
				})
				code, out := turnOn(t, s, directTurn("You are a helpful assistant.", stream))
				if !strings.Contains(out, "Request contains an invalid argument.") || strings.Contains(out, "ended before") {
					t.Fatalf("the client wasn't told Google's error (%d): %s", code, out)
				}
				if !stream && code != http.StatusBadRequest {
					t.Errorf("a whole reply's error is at %d, not Google's 400: %s", code, out)
				}
				if _, ok := restOf("antigravity@u@example.com"); ok {
					t.Fatalf("the account rests for a request Google turned away: %v", rests())
				}
				r := lastRoute(s)
				// a stream's try is at the 200 its agent was sent, its
				// error Google's; a whole reply's at Google's 400
				if len(r.Tries) == 0 || r.Tries[0].Rest != nil || !strings.Contains(r.Tries[0].Error, "invalid argument") || !stream && r.Tries[0].Status != http.StatusBadRequest {
					t.Fatalf("the try: %+v", r.Tries)
				}
			})
		}
	}
}

// A Gemini stream relayed as it came (a custom provider's Gemini URL)
// reaches its client with the bare error as Gemini's own error event,
// which Google's SDK reads, and magpie appends no "ended before" of its
// own after it.
func TestGeminiRelayBareError(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"candidates\": [{\"content\": {\"role\": \"model\",\"parts\": [{\"text\": \"Weighing.\",\"thought\": true}]}}]}\r\n\r\n"+agBareError)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{"gemini-3.8-flash"}, Gemini: up.URL + "/v1beta"}); err != nil {
		t.Fatal(err)
	}
	_, body := post(t, "/v1beta/models/up/gemini-3.8-flash:streamGenerateContent?alt=sse",
		`{"contents":[{"role":"user","parts":[{"text":"Is 391 prime?"}]}]}`)
	if !strings.Contains(body, "data: "+agBareError) || strings.Contains(body, "ended before") {
		t.Fatalf("the relayed stream: %s", body)
	}
}
