package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// openrouterWindow is a fake of OpenRouter's chat API that counts the
// reply asked for in the window with the prompt, and refuses in
// OpenRouter's words (H1_B1_K1 on X) a request whose two pass it.
type openrouterWindow struct {
	mu         sync.Mutex
	window     int
	text, tool int
	asked      []int64
}

func (o *openrouterWindow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	n := int64(-1)
	if v := gjson.GetBytes(b, "max_tokens"); v.Exists() {
		n = v.Int()
	}
	o.mu.Lock()
	o.asked = append(o.asked, n)
	o.mu.Unlock()
	if n < 0 || o.text+o.tool+int(n) > o.window {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		fmt.Fprintf(w, `{"error":{"message":"This endpoint's maximum context length is %d tokens. However, you requested about %d tokens (%d of text input, %d of tool input, %d in the output). Please reduce the length of either one, or use the context-compression plugin to compress your prompt automatically.","code":400}}`,
			o.window, o.text+o.tool+int(n), o.text, o.tool, n)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"id":"test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		`data: {"id":"test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	))
}

func (o *openrouterWindow) took() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := fmt.Sprint(o.asked)
	o.asked = nil
	return s
}

const openRouterRefusal = "This endpoint's maximum context length is 1048576 tokens. However, you requested about 1050086 tokens (101187 of text input, 5181 of tool input, 943718 in the output). Please reduce the length of either one, or use the context-compression plugin to compress your prompt automatically."

func TestReplyLeftOpenRouter(t *testing.T) {
	// the report's literal message: 1048576 - (101187 + 5181)
	if got := replyLeft([]byte(openRouterRefusal)); got != 942208 {
		t.Errorf("replyLeft(report) = %d, want 942208", got)
	}
	// an image part is a prompt part too
	msg := "This endpoint's maximum context length is 200000 tokens. However, you requested about 210000 tokens (10000 of text input, 4000 of image input, 196000 in the output)."
	if got := replyLeft([]byte(msg)); got != 186000 {
		t.Errorf("replyLeft(image) = %d, want 186000", got)
	}
}

// Codex names no max_output_tokens, magpie translates its Responses turn
// to OpenRouter's chat API and fills in the model's own limit, which the
// catalog gives as 943718 of a 1048576 window; OpenRouter counts it with
// the prompt and refused every turn, and magpie did not read its words to
// ask again. The first ask now stays within the window, and a refusal that
// still comes is read.
func TestOpenRouterReplyFitsTheWindow(t *testing.T) {
	fresh(t)
	o := &openrouterWindow{window: 1048576, text: 101187, tool: 5181}
	up := httptest.NewServer(o)
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "orx", Name: "OR", Key: "k", Chat: up.URL + "/v1", Models: []string{"deepseek/deepseek-v4-pro"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("orx", up.URL+"/v1", []catalog.Model{{ID: "deepseek/deepseek-v4-pro", Context: 1048576, Output: 943718}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(catalog.Reset)
	body := `{"model":"orx/deepseek/deepseek-v4-pro","stream":true,"input":"hi"}`

	// a small real prompt, which the fake counts as 106368: the first ask
	// is the model's own, refused, and asked again for what is left
	code, reply := post(t, "/v1/responses", body)
	if code != 200 {
		t.Fatalf("HTTP %d: %s", code, reply)
	}
	if got := o.took(); got != "[943718 942208]" {
		t.Fatalf("OpenRouter was asked max_tokens %s, want [943718 942208]", got)
	}

	// a real prompt of ~100K tokens: the first ask already leaves it room
	o.text, o.tool = 1, 1
	big := strings.Repeat("word ", 80000) // ~100K tokens by bytes
	code, reply = post(t, "/v1/responses", `{"model":"orx/deepseek/deepseek-v4-pro","stream":true,"input":"`+big+`"}`)
	if code != 200 {
		t.Fatalf("HTTP %d: %s", code, reply)
	}
	got := o.took()
	var n int
	if _, err := fmt.Sscanf(got, "[%d]", &n); err != nil || n >= 943718 || n < 8192 || n+len(big)/4 > 1048576 {
		t.Fatalf("OpenRouter was asked max_tokens %s, want one ask within the window", got)
	}
}
