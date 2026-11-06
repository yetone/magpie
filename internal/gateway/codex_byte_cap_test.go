package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// nginx413 is the page nginx answers a body past client_max_body_size
// with, as a key provider in front of DeepSeek's chat API gave it to a
// Codex user (celestial_eagle_99230 on Discord: "1017dp: 413 Request
// Entity Too Large: a web page, not an API reply").
const nginx413 = "<html>\r\n<head><title>413 Request Entity Too Large</title></head>\r\n<body>\r\n<center><h1>413 Request Entity Too Large</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"

// askCodexStream posts a streamed Responses request for model as ua, and
// reads the reply whole, closing heard (when set) at its first keepalive.
func askCodexStream(t *testing.T, s *Server, model, ua string, heard chan struct{}) (int, string, string) {
	t.Helper()
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	req, _ := http.NewRequest("POST", gw.URL+"/v1/responses",
		strings.NewReader(`{"model":"`+model+`","stream":true,"input":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if heard == nil {
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}
	var once sync.Once
	var body strings.Builder
	rd := bufio.NewReader(resp.Body)
	for {
		line, err := rd.ReadString('\n')
		body.WriteString(line)
		if strings.HasPrefix(line, ": keepalive") {
			once.Do(func() { close(heard) })
		}
		if err != nil {
			return resp.StatusCode, resp.Header.Get("Content-Type"), body.String()
		}
	}
}

// failedCode is the code of a Responses stream's response.failed, "" for
// none.
func failedCode(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if d, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(d, "type").String() == "response.failed" {
			return gjson.Get(d, "response.error.code").String()
		}
	}
	return ""
}

func byteCapRestsNobody(t *testing.T, s *Server) {
	t.Helper()
	for _, try := range lastRoute(s).Tries {
		if try.Rest != nil || try.Fail == failOverflow && try.Status == 413 {
			t.Fatalf("a byte cap rested the provider or was booked as an overflow: %+v", lastRoute(s).Tries)
		}
	}
	restingUntil.Lock()
	n := len(restingUntil.m)
	restingUntil.Unlock()
	if n != 0 {
		t.Fatalf("a byte cap persisted %d rests", n)
	}
}

const codexUA = "codex_cli_rs/0.162.3 (Mac OS 26.6.0; arm64) kitty"

// A 413 after Codex's stream has begun (a large body takes the vendor
// past keepHeldAfter) reached Codex as response.failed with code
// server_error, which Codex reads as worth reconnecting for: it sent the
// same body five times ("reconnecting 5/5"), refused each time, and never
// compacted. context_length_exceeded is the one code Codex stops and
// compacts on (codex-api sse/responses_error.rs; core compact.rs trims the
// oldest items while its compaction gets it too), and the only one in the
// Responses API that says the input must be smaller.
func TestCodexByteCapInStreamCompacts(t *testing.T) {
	quietFast(t)
	fresh(t)
	hang := make(chan struct{})
	up := &scripted{hang: hang, replies: []reply{{413, "text/html", nginx413}}}
	scriptedOn(t, "dp", provider.Chat, up)
	heard := make(chan struct{})
	go func() {
		select {
		case <-heard:
		case <-time.After(5 * time.Second):
		}
		close(hang)
	}()
	s := New()
	code, _, body := askCodexStream(t, s, "dp/m", codexUA, heard)
	if code != 200 || !strings.Contains(body, ": keepalive") {
		t.Fatalf("the stream's headers never went out ahead of the 413: %d %q", code, body)
	}
	if got := failedCode(body); got != "context_length_exceeded" {
		t.Fatalf("Codex was told %q, which it reconnects for: %s", got, body)
	}
	if !strings.Contains(body, "request_too_large") {
		t.Fatalf("the text no longer says request_too_large, which pi compacts on: %s", body)
	}
	if up.n != 1 {
		t.Fatalf("the refused body was sent %d times", up.n)
	}
	byteCapRestsNobody(t, s)
}

// The same 413 before the stream began went to Codex as an HTTP 413,
// which Codex takes as an unexpected status: the turn ends and nothing
// compacts, and a compaction refused the same way fails outright. Codex
// asked for a stream, so it is told in one, as response.failed
// context_length_exceeded; and so is an overflow, whose 400
// context_length_exceeded Codex reads as an invalid request.
func TestCodexByteCapBeforeStreamCompacts(t *testing.T) {
	for _, c := range []struct {
		name  string
		reply reply
	}{
		{"nginx 413", reply{413, "text/html", nginx413}},
		{"overflow 400", reply{400, "application/json", `{"error":{"message":"This model's maximum context length is 131072 tokens. However, you requested 140000 tokens.","type":"invalid_request_error","code":"invalid_request_error"}}`}},
		// the reporter's vendor (#4040), whose words carry no 's on the
		// model and only the code names the context
		{"overflow 400 without the model's", reply{400, "application/json", `{"error":{"code":"context_length_exceeded","message":"The message is longer than the model context length","type":"invalid_request_error"}}`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			up := &scripted{replies: []reply{c.reply}}
			scriptedOn(t, "dp", provider.Chat, up)
			s := New()
			code, ctype, body := askCodexStream(t, s, "dp/m", codexUA, nil)
			if code != 200 || !strings.HasPrefix(ctype, "text/event-stream") || failedCode(body) != "context_length_exceeded" {
				t.Fatalf("Codex wasn't told in its stream to compact: %d %s %s", code, ctype, body)
			}
			if up.n != 1 {
				t.Fatalf("the refused body was sent %d times", up.n)
			}
			byteCapRestsNobody(t, s)
		})
	}
}

// Another Responses client keeps the status it is told today before its
// stream begins: only Codex reads nothing but the stream's code.
func TestByteCapBeforeStreamStaysStatusForOthers(t *testing.T) {
	fresh(t)
	scriptedOn(t, "dp", provider.Chat, &scripted{replies: []reply{{413, "text/html", nginx413}}})
	code, _, body := askCodexStream(t, New(), "dp/m", "opencode/1.4.0 ai-sdk/openai", nil)
	if code != 413 || !strings.Contains(body, "request_too_large") {
		t.Fatalf("another client's 413: %d %s", code, body)
	}
}
