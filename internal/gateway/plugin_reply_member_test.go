package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// Settings' replyModel member names the routing group's member that
// answered when a plugin serves it, to Claude Code too, on Chat and on
// Messages, streamed or whole (ZoeCats on Discord, after v0.1.2760: with
// member picked their Claude Code still read the group's name).
func TestPluginReplyNamesMember(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)

	up := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path == "/v1/messages" {
			for _, e := range []string{
				`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"fake-claude-20261101","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi there"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`{"type":"message_stop"}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", e)
			}
			return
		}
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1-20261101","choices":[{"index":0,"delta":{"role":"assistant","content":"hi there"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1-20261101","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := provider.StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": "full"})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SubmitSignInCallback(st.ID, "good"); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{
		{Name: "chat", Routing: provider.Ordered, Members: []string{"fakeco/fake-1"}},
		{Name: "claude", Routing: provider.Ordered, Members: []string{"fakeco/fake-claude"}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}

	s := New()
	for _, c := range []struct {
		name, reply, path, body, want string
	}{
		{"chat, member", "member", "/v1/chat/completions", `{"model":"group/chat","messages":[{"role":"user","content":"hello"}]`, "fakeco/fake-1"},
		{"messages, member", "member", "/v1/messages", `{"model":"group/claude","max_tokens":100,"messages":[{"role":"user","content":"hello"}]`, "fakeco/fake-claude"},
		{"chat, asked", "", "/v1/chat/completions", `{"model":"group/chat","messages":[{"role":"user","content":"hello"}]`, "group/chat"},
		{"messages, asked", "", "/v1/messages", `{"model":"group/claude","max_tokens":100,"messages":[{"role":"user","content":"hello"}]`, "group/claude"},
	} {
		for _, stream := range []bool{false, true} {
			for _, token := range []string{"", TokenFor("claude")} {
				name := fmt.Sprintf("%s/stream %v/token %q", c.name, stream, token)
				t.Run(name, func(t *testing.T) {
					if err := settings.Save(settings.Settings{ReplyModel: c.reply}); err != nil {
						t.Fatal(err)
					}
					body := c.body
					if stream {
						body += `,"stream":true`
					}
					req := httptest.NewRequest("POST", c.path, strings.NewReader(body+"}"))
					if token != "" {
						req.Header.Set("Authorization", "Bearer "+token)
					}
					rec := httptest.NewRecorder()
					s.Handler().ServeHTTP(rec, req)
					if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi there") {
						t.Fatalf("%d %s", rec.Code, rec.Body)
					}
					names := modelsNamed(rec.Body.String(), stream)
					if len(names) == 0 {
						t.Fatalf("no model named in %s", rec.Body)
					}
					for _, n := range names {
						if n != c.want {
							t.Fatalf("the reply names %q, want %q:\n%s", n, c.want, rec.Body)
						}
					}
				})
			}
		}
	}
}
