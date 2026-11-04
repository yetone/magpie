package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A model that pauses in the middle of a placeholder while it writes a
// tool call's arguments: magpie keeps an Anthropic client alive with a
// ping, and the agent's arguments still have the value, not the
// placeholder. The ping released the held first piece with the start of
// the placeholder in it, the next piece came with the rest, and Claude
// Code wrote {{…}} into the file (magpie-community/plugins#738).
func TestMaskedToolArgsSplitAroundPing(t *testing.T) {
	fresh(t)
	quietFast(t)
	pe := pingEvery
	pingEvery = 10 * time.Millisecond
	t.Cleanup(func() { pingEvery = pe })
	const email = "moon.shard@proton.me"
	heard := make(chan struct{})
	var timedOut atomic.Bool
	var sent []byte
	chunk := func(args string, finish any) string {
		b, _ := json.Marshal(map[string]any{"id": "c1", "model": "m1", "choices": []any{map[string]any{
			"index": 0, "finish_reason": finish,
			"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "Write", "arguments": args}}}}}}})
		return "data: " + string(b) + "\n\n"
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		p := regexp.MustCompile(`\{\{EMAIL_[a-z2-7]{8}\}\}`).FindString(string(sent))
		w.Header().Set("Content-Type", "text/event-stream")
		if p == "" {
			io.WriteString(w, chunk(`{}`, "tool_calls")+"data: [DONE]\n\n")
			return
		}
		args := `{"content":"mail ` + p + `"}`
		cut := strings.Index(args, p) + 9 // {{EMAIL_x
		io.WriteString(w, chunk(args[:cut], nil))
		w.(http.Flusher).Flush()
		select {
		case <-heard:
		case <-time.After(3 * time.Second):
			timedOut.Store(true)
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, chunk(args[cut:], nil)+chunk("", "tool_calls")+"data: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fixture", Name: "Fixture", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)
	body := `{"model":"fixture/m1","stream":true,"max_tokens":256,
	 "tools":[{"name":"Write","description":"write a file","input_schema":{"type":"object","properties":{"content":{"type":"string"}}}}],
	 "messages":[{"role":"user","content":"write my address ` + email + ` into notes.md"}]}`
	resp, err := http.Post(gw.URL+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var once sync.Once
	var all strings.Builder
	rd := bufio.NewReader(resp.Body)
	for {
		line, err := rd.ReadString('\n')
		all.WriteString(line)
		if strings.HasPrefix(line, "event: ping") && strings.Contains(all.String(), `"tool_use"`) {
			once.Do(func() { close(heard) })
		}
		if err != nil {
			break
		}
	}
	if strings.Contains(string(sent), email) {
		t.Fatalf("the address went to the vendor: %s", sent)
	}
	out := all.String()
	if timedOut.Load() || !strings.Contains(out, "event: ping") {
		t.Fatalf("no ping between the pieces (timed out %v):\n%s", timedOut.Load(), out)
	}
	var js strings.Builder
	for _, ev := range strings.Split(out, "\n\n") {
		_, d, ok := strings.Cut(ev, "data: ")
		if !ok {
			continue
		}
		var e struct {
			Delta struct {
				PartialJSON string `json:"partial_json"`
			}
		}
		json.Unmarshal([]byte(d), &e)
		js.WriteString(e.Delta.PartialJSON)
	}
	var in map[string]string
	if err := json.Unmarshal([]byte(js.String()), &in); err != nil || in["content"] != "mail "+email {
		t.Fatalf("the agent's arguments %q (%v):\n%s", js.String(), err, out)
	}
}
