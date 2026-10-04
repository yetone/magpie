package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

func titleTestKey(t testing.TB) {
	t.Helper()
	if err := os.MkdirAll(settings.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.Dir(), "codex-title-key"), []byte(strings.Repeat("synthetic-title-key", 2)[:32]), 0600); err != nil {
		t.Fatal(err)
	}
}

const titleTemplate = "You are a helpful assistant. You will be presented with a user prompt, create a title.\n\nUser prompt:\n"

func linkBody(prompt string) []byte {
	b, _ := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "developer", "content": "system"}, map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": "<environment_context>context</environment_context>"}}}, map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": prompt}}}}})
	return b
}
func TestTitlePromptEvidence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	titleTestKey(t)
	prompt := "标题关联测试：请只回答测试完成。\n"
	if a, b := titlePromptDigest(linkBody(prompt), false), titlePromptDigest(linkBody(titleTemplate+prompt), true); a == "" || a != b {
		t.Fatalf("prompt mismatch: %s %s", a, b)
	}
	for _, tc := range []struct {
		body  string
		title bool
	}{
		{`{"input":[{"role":"user","content":[{"type":"input_text","text":"prompt"},{"type":"input_image","image_url":"x"}]}]}`, false},
		{`{"input":[{"role":"assistant","content":"summary"},{"role":"user","content":"later turn"}]}`, false},
		{string(linkBody("unrecognized title instructions")), true},
		{`{"input":{"unexpected":{"role":"user","content":"prompt"}}}`, false},
		{`{broken`, false},
	} {
		if got := titlePromptDigest([]byte(tc.body), tc.title); got != "" {
			t.Fatalf("unsupported evidence matched: %s", got)
		}
	}
}
func TestTitleReplyEvidence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	titleTestKey(t)
	response := func(text string) []byte {
		b, _ := json.Marshal(map[string]any{"output": []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": text}}}}})
		return b
	}
	want := sessions.CodexTitleDigest("完成标题关联测试")
	if got := titleReplyDigest(response(`{"title":"完成标题关联测试"}`), nil); got != want {
		t.Fatalf("JSON reply: %s", got)
	}
	stream := fmt.Sprintf("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\",\"output\":[]}}\n\n", `{"title":"完成标题关联测试"}`)
	if got := titleReplyDigest([]byte(stream), nil); got != want {
		t.Fatalf("native output_item.done: %s", got)
	}
	if titleReplyDigest(response("完成标题关联测试"), nil) != "" {
		t.Fatal("native plain text accepted")
	}
	if titleReplyDigest(response("完成标题关联测试"), &titleShape{}) != want {
		t.Fatal("wrapped title differs from the title handed to Codex")
	}
	for _, text := range []string{`{"title":7}`, `{"title":""}`, `{"other":"title"}`} {
		if titleReplyDigest(response(text), nil) != "" {
			t.Fatalf("invalid title accepted: %s", text)
		}
	}
	if titleReplyDigest([]byte(stream+"data: {\"type\":\"response.failed\"}\n\n"), nil) != "" {
		t.Fatal("failed stream accepted")
	}
}

func TestResolveTitleParents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	titleTestKey(t)
	at := time.Now().UTC().Truncate(time.Second)
	link := TitleLink{Scope: "local", Prompt: promptDigest("unique prompt"), Reply: sessions.CodexTitleDigest("Applied title")}
	main := Route{Agent: "codex", Session: "main", Time: at, TitleLink: &TitleLink{Scope: link.Scope, Prompt: link.Prompt}}
	title := Route{Agent: "codex", Session: "hidden-title", Kind: "thread_title", Time: at, Millis: 1000, Done: true, Status: 200, TitleLink: &link}
	app := sessions.TitleApplication{Digest: link.Reply, Updated: at.Add(time.Second)}
	for _, tc := range []struct {
		name   string
		change func(*Route, *Route, map[string][]sessions.TitleApplication)
		extra  bool
		want   string
	}{
		{name: "all evidence", want: "main"},
		{name: "title first", want: "main"},
		{name: "pending name write", change: func(_, _ *Route, a map[string][]sessions.TitleApplication) { delete(a, "main") }},
		{name: "wrong title", change: func(_, _ *Route, a map[string][]sessions.TitleApplication) {
			a["main"] = []sessions.TitleApplication{{Digest: "other", Updated: app.Updated}}
		}},
		{name: "old name", change: func(_, _ *Route, a map[string][]sessions.TitleApplication) {
			a["main"] = []sessions.TitleApplication{{Digest: link.Reply, Updated: at.Add(-time.Second)}}
		}},
		{name: "late rename", change: func(_, _ *Route, a map[string][]sessions.TitleApplication) {
			a["main"] = []sessions.TitleApplication{{Digest: link.Reply, Updated: at.Add(3 * time.Minute)}}
		}},
		{name: "other installation", change: func(m, _ *Route, _ map[string][]sessions.TitleApplication) {
			m.TitleLink = &TitleLink{Scope: "other", Prompt: link.Prompt}
		}},
		{name: "other prompt", change: func(m, _ *Route, _ map[string][]sessions.TitleApplication) {
			m.TitleLink = &TitleLink{Scope: link.Scope, Prompt: "other"}
		}},
		{name: "unfinished", change: func(_, r *Route, _ map[string][]sessions.TitleApplication) { r.Done = false }},
		{name: "failure", change: func(_, r *Route, _ map[string][]sessions.TitleApplication) { r.Error = "failed" }},
		{name: "explicit parent", change: func(_, r *Route, _ map[string][]sessions.TitleApplication) { r.ParentSession = "explicit" }, want: "explicit"},
		{name: "ambiguous simultaneous twins", extra: true},
		{name: "unobserved chat with same title", change: func(_, _ *Route, a map[string][]sessions.TitleApplication) {
			a["unobserved"] = []sessions.TitleApplication{app}
		}},
		{name: "memory not title", change: func(_, r *Route, _ map[string][]sessions.TitleApplication) { r.Kind = "memory_consolidation" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, r := main, title
			a := map[string][]sessions.TitleApplication{"main": {app}}
			if tc.change != nil {
				tc.change(&m, &r, a)
			}
			rows := []Route{r, m}
			if tc.extra {
				other := m
				other.Session = "twin"
				rows = append(rows, other)
				a[other.Session] = []sessions.TitleApplication{app}
			}
			got := resolveTitleParents(rows, nil, func([]string) map[string][]sessions.TitleApplication { return a })
			if got[0].ParentSession != tc.want {
				t.Fatalf("parent=%q want=%q", got[0].ParentSession, tc.want)
			}
			if rows[0].ParentSession != r.ParentSession || got[0].Session != "hidden-title" {
				t.Fatal("changed native identity or input record")
			}
			if got[0].ParentMatched != (tc.want == "main") {
				t.Fatal("lost inferred provenance")
			}
		})
	}
	// A later conflict revokes an inference; the records keep native identities.
	inferred := title
	inferred.ParentSession = "main"
	inferred.ParentMatched = true
	twin := main
	twin.Session = "twin"
	got := resolveTitleParents([]Route{inferred, main, twin}, nil, func([]string) map[string][]sessions.TitleApplication {
		return map[string][]sessions.TitleApplication{"main": {app}, "twin": {app}}
	})
	if got[0].ParentSession != "" || got[0].ParentMatched {
		t.Fatal("late conflict did not revoke association")
	}
}

func TestTitlePromptsCachedAndBounded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	titleTestKey(t)
	var p titlePrompts
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("User-Agent", "codex/1.0")
	req.Header.Set("Session-Id", "main")
	m := sessionMetadata{Source: "user", Installation: "test-installation"}
	first := p.observe(req, linkBody("original"), m, "", time.Now())
	if first == nil {
		t.Fatal("local prompt not observed")
	}
	if p.observe(req, linkBody("later"), m, "", time.Now()) != nil {
		t.Fatal("rehashes later requests")
	}
	req.Header.Set("Session-Id", "hidden")
	if p.observe(req, linkBody(titleTemplate+"original"), m, "thread_title", time.Now()).Prompt != first.Prompt {
		t.Fatal("title not matched to original")
	}
	for _, remote := range []string{"192.168.1.2:1234", ""} {
		req.RemoteAddr = remote
		if p.observe(req, linkBody("prompt"), m, "", time.Now()) != nil {
			t.Fatal("remote evidence accepted")
		}
	}
	req.RemoteAddr = "127.0.0.1:1234"
	m.Installation = ""
	if p.observe(req, linkBody("prompt"), m, "", time.Now()) != nil {
		t.Fatal("unknown scope accepted")
	}
	m.Installation = "test-installation"
	for i := 0; i < titlePromptLimit; i++ {
		req.Header.Set("Session-Id", fmt.Sprint(i))
		p.observe(req, linkBody("same"), m, "", time.Now())
	}
	if len(p.first) != titlePromptLimit || p.recent.Len() != titlePromptLimit {
		t.Fatalf("bound failed: %d", len(p.first))
	}
	scope := promptDigest(m.Installation)
	if p.first[scope+":main"] != nil {
		t.Fatal("oldest chat was not evicted")
	}
	// Touch chat 0, then force another eviction. New chats remain eligible.
	req.Header.Set("Session-Id", "0")
	p.observe(req, linkBody("later turn"), m, "", time.Now())
	req.Header.Set("Session-Id", "new-after-limit")
	if got := p.observe(req, linkBody("new prompt"), m, "", time.Now()); got == nil {
		t.Fatal("cache limit disabled inference for new chats")
	}
	if p.first[scope+":0"] == nil || p.first[scope+":1"] != nil {
		t.Fatal("did not evict the least recently used chat")
	}
	for _, entries := range p.byPrompt {
		for _, entry := range entries {
			if entry.Session == "main" || entry.Session == "1" {
				t.Fatal("evicted chat kept stale candidate evidence")
			}
		}
	}

}

func TestTitleParentSurvivesTraceEvictionAndHistory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	titleTestKey(t)
	t.Setenv("CODEX_HOME", t.TempDir())
	at := time.Now().UTC()
	s := &Server{}
	req := httptest.NewRequest("POST", "/responses", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("User-Agent", "codex/1.0")
	req.Header.Set("Session-Id", "main")
	m := sessionMetadata{Installation: "test", Source: "user"}
	first := s.titlePrompts.observe(req, linkBody("prompt"), m, "", at)
	main := Route{Agent: "codex", Session: "main", Time: at, TitleLink: first, Done: true}
	s.trace.begin(main)
	for i := 0; i < traceKeep; i++ {
		s.trace.begin(Route{Done: true})
	}
	link := *first
	link.Reply = sessions.CodexTitleDigest("Applied")
	title := Route{Agent: "codex", Session: "hidden", Kind: "thread_title", Time: at, Done: true, Status: 200, TitleLink: &link}
	// This represents Codex applying the generated title, then a manual rename.
	index := fmt.Sprintf("{\"id\":\"main\",\"thread_name\":\"Applied\",\"updated_at\":%q}\n{\"id\":\"main\",\"thread_name\":\"Manual rename\",\"updated_at\":%q}\n", at.Add(time.Second).Format(time.RFC3339Nano), at.Add(time.Hour).Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(sessions.CodexDir(), "session_index.jsonl"), []byte(index), 0600); err != nil {
		t.Fatal(err)
	}
	s.trace.begin(title)
	rows := s.Trace(context.Background(), 0, 0).Routes
	if got := s.ResolveTitleParents(rows); got[len(got)-1].ParentSession != "main" {
		t.Fatal("evicted first request lost evidence")
	}
	// Restart: first-request digests persisted with history are enough to resolve.
	b, _ := json.Marshal([]Route{main, title})
	var history []Route
	json.Unmarshal(b, &history)
	if got := (&Server{}).ResolveTitleParents(history); got[1].ParentSession != "main" {
		t.Fatal("history lost association after restart")
	}
}

func BenchmarkTitleLinkRequest(b *testing.B) {
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	titleTestKey(b)
	req := httptest.NewRequest("POST", "/responses", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("User-Agent", "codex/1.0")
	req.Header.Set("Session-Id", "main")
	m := sessionMetadata{Installation: "benchmark", Source: "user"}
	body := linkBody("original prompt")
	// Multi-megabyte assistant history after the original prompt.
	body = append(body[:len(body)-2], []byte(`,{"role":"assistant","content":"`+strings.Repeat("x", 5800000)+`"}]}`)...)
	b.Run("FirstPromptWith5MBHistory", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if titlePromptDigest(body, false) == "" {
				b.Fatal("no prompt")
			}
		}
	})
	var p titlePrompts
	p.observe(req, body, m, "", time.Now())
	b.Run("CachedWith5MBHistory", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			p.observe(req, body, m, "", time.Time{})
		}
	})
}

// Real gateway requests, not pre-filled parentSession fields: both native
// Codex and Magpie's title wrapper retain enough evidence for the GUI.
func TestTitleLinkGatewayTransports(t *testing.T) {
	for _, tc := range []struct {
		name           string
		native, schema bool
	}{
		{name: "native", native: true},
		{name: "translated"},
		{name: "translated clipped title and description", schema: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			native := tc.native
			appliedTitle := "完成标题关联测试"
			if tc.schema {
				appliedTitle = "完成标题"
			}
			t.Setenv("CODEX_HOME", t.TempDir())
			f := &fake{t: t, reply: sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"完成标题关联测试"}}]}`, `data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":4}}`, `data: [DONE]`)}
			setup(t, provider.Chat, f)
			titleTestKey(t)
			chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"resp_test","output":[{"type":"message","content":[{"type":"output_text","text":"{\"title\":\"完成标题关联测试\"}"}]}],"usage":{"input_tokens":30,"output_tokens":4}}}`))
			})
			s := New()
			send := func(id, kind, model, path, prompt string) {
				var body map[string]any
				json.Unmarshal(linkBody(prompt), &body)
				body["model"], body["stream"] = model, true
				if tc.schema && kind == "thread_title" {
					body["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": map[string]any{
						"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string", "maxLength": 4}, "description": map[string]string{"type": "string"}},
						"required": []string{"title", "description"},
					}}}
				}
				meta, _ := json.Marshal(sessionMetadata{Source: kind, Installation: "test-installation"})
				body["client_metadata"] = map[string]string{"x-codex-turn-metadata": string(meta)}
				raw, _ := json.Marshal(body)
				encoder, _ := zstd.NewWriter(nil)
				compressed := encoder.EncodeAll(raw, nil)
				encoder.Close()
				if !strings.HasPrefix(path, CodexPath) {
					compressed = raw
				}
				r := httptest.NewRequest("POST", path, bytes.NewReader(compressed))
				r.RemoteAddr = "127.0.0.1:1"
				r.Header.Set("Session-Id", id)
				r.Header.Set("User-Agent", "codex/1.0")
				if strings.HasPrefix(path, CodexPath) {
					r.Header.Set("Content-Encoding", "zstd")
				}
				if native && kind == "thread_title" {
					r.Header.Set("Authorization", "Bearer test-token")
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("request %s: %d %s", kind, w.Code, w.Body)
				}
				if kind == "thread_title" && titleReplyDigest(w.Body.Bytes(), nil) != sessions.CodexTitleDigest(appliedTitle) {
					t.Fatal("caller received a different title from the recorded association evidence")
				}
			}
			mainPath := "/v1/responses"
			if native {
				mainPath = CodexPath + "/responses"
			}
			send("main-test", "user", "fake/m1", mainPath, "标题关联测试：请只回答测试完成。")
			model, path := "fake/m1", "/v1/responses"
			if native {
				model, path = "gpt-5.6-luna", CodexPath+"/responses"
			}
			send("hidden-title-test", "thread_title", model, path, titleTemplate+"标题关联测试：请只回答测试完成。")
			rows := s.Trace(context.Background(), 0, 0).Routes
			if len(rows) != 2 || rows[0].TitleLink == nil || rows[1].TitleLink == nil || rows[1].TitleLink.Reply == "" || rows[1].ParentSession != "" {
				t.Fatalf("transport evidence: %+v", rows)
			}
			if got := s.ResolveTitleParents(rows); got[1].ParentSession != "" {
				t.Fatal("inferred before Codex applied title")
			}
			line := fmt.Sprintf("{\"id\":\"main-test\",\"thread_name\":%q,\"updated_at\":%q}\n", appliedTitle, time.Now().UTC().Format(time.RFC3339Nano))
			os.WriteFile(filepath.Join(sessions.CodexDir(), "session_index.jsonl"), []byte(line), 0600)
			if got := s.ResolveTitleParents(rows); got[1].ParentSession != "main-test" || !got[1].ParentMatched {
				t.Fatalf("transport did not resolve: %+v", got[1])
			}
			if native && os.Getenv("TITLE_LINK_FIXTURE_FILE") != "" {
				data, _ := json.MarshalIndent(rows, "", "  ")
				if err := os.WriteFile(os.Getenv("TITLE_LINK_FIXTURE_FILE"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkTitleParentResolution(b *testing.B) {
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	titleTestKey(b)
	at := time.Now()
	s := &Server{}
	req := httptest.NewRequest("POST", "/responses", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("User-Agent", "codex/1.0")
	m := sessionMetadata{Source: "user", Installation: "benchmark"}
	for i := 0; i < 4000; i++ {
		req.Header.Set("Session-Id", fmt.Sprint(i))
		s.titlePrompts.observe(req, linkBody(fmt.Sprint("prompt-", i)), m, "", at)
	}
	link := TitleLink{Scope: promptDigest(m.Installation), Prompt: promptDigest("prompt-0"), Reply: sessions.CodexTitleDigest("Title")}
	// Include the file stat and application lookup, not only map operations.
	b.Setenv("CODEX_HOME", b.TempDir())
	os.WriteFile(filepath.Join(sessions.CodexDir(), "session_index.jsonl"), []byte(fmt.Sprintf("{\"id\":\"0\",\"thread_name\":\"Title\",\"updated_at\":%q}\n", at.Add(time.Second).UTC().Format(time.RFC3339Nano))), 0600)
	for _, count := range []int{60, 2000} {
		b.Run(fmt.Sprint(count, "Rows4000Chats"), func(b *testing.B) {
			rows := make([]Route, count)
			rows[0] = Route{Agent: "codex", Kind: "thread_title", Session: "title", Time: at, Done: true, Status: 200, TitleLink: &link}
			b.ReportAllocs()
			for b.Loop() {
				if s.ResolveTitleParents(rows)[0].ParentSession != "0" {
					b.Fatal("unresolved")
				}
			}
		})
	}
}
