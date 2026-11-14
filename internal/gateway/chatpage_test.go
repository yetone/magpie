package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The page is DeepSeek's release, as published: its zip is the one
// whose sha256 the release has, its one API origin is magpie's own path,
// and magpie.js is loaded before its script.
func TestChatPageIsTheRelease(t *testing.T) {
	sum := sha256.Sum256(chatDemoZip)
	if hex.EncodeToString(sum[:]) != chatDemoSHA256 {
		t.Fatal("the embedded chat-demo zip isn't v0.1.1's")
	}
	files, err := chatPageFiles()
	if err != nil {
		t.Fatal(err)
	}
	main := string(files[chatDemoMain])
	if strings.Contains(main, "api.deepseek.com") || strings.Count(main, chatOrigin) != 1 {
		t.Fatal("the page still calls DeepSeek, or magpie's origin isn't in it once")
	}
	if _, ok := files["LICENSE"]; !ok {
		t.Fatal("the release's MIT license isn't served with it")
	}
	index := string(files["index.html"])
	js, page := strings.Index(index, `src="magpie.js"`), strings.Index(index, `src="`+chatDemoMain+`"`)
	if js < 0 || page < 0 || js > page {
		t.Fatalf("magpie.js isn't loaded before the page's script: %s", index)
	}
}

// The page's files are the same bytes for everyone, so another computer
// opens them with no key while magpie is shared; every call they make,
// the model list included, needs one. The page can load nothing from
// elsewhere and can't be framed.
func TestChatPageOpensWithoutAKey(t *testing.T) {
	h, _, secrets := keyPageGateway(t)
	remote := "192.168.1.9:5000"
	w := keyPageReq{method: "GET", path: ChatPagePath, from: remote}.do(h)
	if w.Code != http.StatusFound || w.Header().Get("Location") != ChatPagePath+"/" {
		t.Fatal("bare path", w.Code, w.Header())
	}
	for _, p := range []string{"/", "/" + chatDemoMain, "/magpie.js", "/LICENSE"} {
		w := keyPageReq{method: "GET", path: ChatPagePath + p, from: remote}.do(h)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body.String())
		}
		csp := w.Header().Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", p, csp, want)
			}
		}
		if w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: %v", p, w.Header())
		}
	}
	for _, q := range []keyPageReq{
		{method: "GET", path: ChatPagePath + "/models"},
		{method: "GET", path: ChatPagePath + "/no-such-file"},
		{method: "GET", path: ChatPagePath + "/static/../index.html"},
		{method: "POST", path: ChatPagePath + "/anthropic/v1/messages"},
		{method: "POST", path: ChatPagePath + "/anthropic/v1/files"},
		{method: "DELETE", path: ChatPagePath + "/anthropic/v1/files/file_x"},
		{method: "POST", path: ChatPagePath + "/v1/tools/asr"},
		{method: "POST", path: ChatPagePath + "/v1/tools/tts"},
		{method: "POST", path: ChatPagePath + "/"},
	} {
		q.from = remote
		if w := q.do(h); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no key: %d", q.method, q.path, w.Code)
		}
	}
	// Alice's key may use plan/m1 alone: the picker lists that, and a
	// model she may not use, picked before, isn't chatted with
	w = keyPageReq{method: "GET", path: ChatPagePath + "/models", from: remote, header: map[string]string{"Authorization": "Bearer " + secrets[0]}}.do(h)
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Default string `json:"default"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Data) != 1 || list.Data[0].ID != "plan/m1" || list.Default != "plan/m1" {
		t.Fatalf("Alice's models: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("GET", ChatPagePath+"/models", nil)
	r.AddCookie(&http.Cookie{Name: chatModelCookie, Value: "plan%2Fm2"})
	if got := chatModel(r); got != "plan/m1" && got != "plan/m2" {
		t.Fatal("the picker's model", got)
	}
}

// A conversation the page sends back is what the model is asked: the
// page's DeepSeek identity isn't, its reminder of the day is a system
// line, a file it attached is the file, one magpie no longer has says
// so, and a page read before goes after the question it was read for,
// its text from the result's signature.
func TestChatPageConversation(t *testing.T) {
	fresh(t)
	r := httptest.NewRequest("POST", ChatPagePath+"/anthropic/v1/messages", nil)
	chatFiles.Lock()
	chatFiles.m["file_txt"] = &chatUpload{owner: chatOwner(r), name: "notes.md", mime: "text/plain", kind: "text", data: []byte("# Notes\nhello")}
	chatFiles.m["file_bob"] = &chatUpload{owner: "someone else", name: "bob.txt", mime: "text/plain", kind: "text", data: []byte("Bob's")}
	chatFiles.Unlock()
	t.Cleanup(func() {
		chatFiles.Lock()
		delete(chatFiles.m, "file_txt")
		delete(chatFiles.m, "file_bob")
		chatFiles.Unlock()
	})
	body := `{"model":"deepseek-flash","system":"` + chatDemoSystem + `",
		"thinking":{"type":"enabled"},"title":{"enabled":true},
		"tools":[{"type":"deepseek_web_search","name":"deepseek_web_search"},{"type":"deepseek_read_url","name":"deepseek_read_url"}],
		"messages":[
		{"role":"latest_reminder","content":"2026-11-14,Saturday,,Web,zh-CN"},
		{"role":"user","content":[{"type":"text","text":"Read https://go.dev for me\nplease"},{"type":"document","source":{"type":"file","file_id":"file_txt"}}]},
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"hm","signature":""},
			{"type":"server_tool_use","id":"t1","name":"deepseek_read_url","kind":"read_link","input":{"action":"read_link","url":"https://go.dev"}},
			{"type":"web_fetch_tool_result","tool_use_id":"t1","kind":"read_link","content":{"type":"web_fetch_result","url":"https://go.dev/","content":{"type":"document","source":{"type":"text","media_type":"text/plain","data":""},"title":"Go"},"published_at":null},"signature":"` + chatSign("Go is a language.") + `"},
			{"type":"text","text":"Go is a language."}]},
		{"role":"user","content":[{"type":"text","text":"And Bob's?"},{"type":"document","source":{"type":"file","file_id":"file_bob"}}]}]}`
	var in chatRequest
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatal(err)
	}
	out, title := chatUpstream(r, &in, "deep/deepseek-v4.1-flash")
	if title != "Read https://go.dev for …" {
		t.Fatalf("title %q", title)
	}
	sys, _ := out["system"].(string)
	if strings.Contains(sys, "DeepSeek") || !strings.Contains(sys, "2026-11-14, Saturday") || !strings.Contains(sys, "zh-CN") || strings.Contains(sys, "The user is in") {
		t.Fatalf("system %q", sys)
	}
	got, _ := json.Marshal(out["messages"])
	for _, want := range []string{
		`"text":"File notes.md`,                                                       // the attached file
		`Content of https://go.dev/:\n\nGo is a language.`,                            // the page read before
		`{"content":[{"text":"Go is a language.","type":"text"}],"role":"assistant"}`, // no unsigned thinking, no tool blocks
		`no longer available`,                                                         // Bob's file isn't this caller's
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("messages lack %s:\n%s", want, got)
		}
	}
	if strings.Contains(string(got), "Bob's\"") || strings.Contains(string(got), "deepseek_read_url") {
		t.Errorf("messages: %s", got)
	}
	tools, _ := json.Marshal(out["tools"])
	if !strings.Contains(string(tools), `"web_search_20250305"`) || !strings.Contains(string(tools), `"name":"read_url"`) || strings.Contains(string(tools), "deepseek") {
		t.Errorf("tools %s", tools)
	}
	if out["stream"] != true {
		t.Error("not streamed")
	}
}

// A question the page asks is answered by a magpie model that can't
// search: magpie searches for it (shown as DeepSeek's search, with the
// pages found), reads a link it asks for (shown as DeepSeek's link
// reading, the model given the page's text), and the answer comes in
// the same reply, titled, with every round's tokens.
func TestChatPageSearchesAndReadsLinks(t *testing.T) {
	fresh(t)
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"data":[{"id":"claude-haiku-4-5"}]}`)
		case "/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"s","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[
				{"type":"server_tool_use","id":"srv","name":"web_search","input":{"query":"go"}},
				{"type":"web_search_tool_result","tool_use_id":"srv","content":[
					{"type":"web_search_result","title":"Go downloads","url":"https://www.go.dev/dl/","encrypted_content":"EqoB","page_age":"1 day ago"}]},
				{"type":"text","text":"Go 1.27.1 is out."}],
				"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer search.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><head><title>Release notes</title></head><body><p>Go 1.27.1 fixes the linker.</p></body></html>`)
	}))
	defer site.Close()
	was := webFetchClient
	webFetchClient = site.Client()
	defer func() { webFetchClient = was }()

	var mu sync.Mutex
	var asked []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		asked = append(asked, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case strings.Contains(string(b), "fixes the linker"):
			io.WriteString(w, sse(`data: {"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":"The linker is fixed."}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":4}}`,
				`data: [DONE]`))
		case strings.Contains(string(b), "Go downloads"):
			io.WriteString(w, sse(`data: {"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":"Reading it, from https://www.go.dev/dl/."}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"read_url","arguments":"{\"url\":\"`+site.URL+`/notes\"}"}}]}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":3}}`,
				`data: [DONE]`))
		default:
			io.WriteString(w, sse(`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"latest go\"}"}}]}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":2}}`,
				`data: [DONE]`))
		}
	}))
	defer model.Close()
	hosts := searchHosts[provider.Anthropic]
	searchHosts[provider.Anthropic] = append(hosts, provider.HostOf(search.URL))
	defer func() { searchHosts[provider.Anthropic] = hosts }()
	for _, p := range []provider.Provider{
		{ID: "srch", Name: "Search", Key: "k", Anthropic: search.URL},
		{ID: "deep", Name: "Deep", Key: "k", Chat: model.URL, Models: []string{"deepseek-v4.1-flash"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := provider.Find("srch")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	body := `{"model":"deepseek-flash","system":"` + chatDemoSystem + `","thinking":{"type":"disabled"},"title":{"enabled":true},
		"tools":[{"type":"deepseek_web_search","name":"deepseek_web_search"},{"type":"deepseek_read_url","name":"deepseek_read_url"}],
		"messages":[{"role":"latest_reminder","content":"2026-11-14,Saturday,Beijing,Web,en-US"},{"role":"user","content":"What's new in Go?"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", ChatPagePath+"/anthropic/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "magpie-deepseek-chat")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	evs := events(rec.Body.String())
	var kinds []string
	var text string
	var delta map[string]any
	var cited []string
	indices := map[float64]bool{}
	for _, ev := range evs {
		switch ev["type"] {
		case "content_block_start":
			cb := ev["content_block"].(map[string]any)
			i := ev["index"].(float64)
			if indices[i] {
				t.Fatalf("index %v twice: %s", i, rec.Body.String())
			}
			indices[i] = true
			k := cb["type"].(string)
			if n, ok := cb["name"].(string); ok {
				k += ":" + n
			}
			kinds = append(kinds, k)
			switch cb["type"] {
			case "server_tool_use":
				in := cb["input"].(map[string]any)
				if cb["name"] == "deepseek_web_search" && (cb["kind"] != "search" || in["action"] != "search" || in["queries"].([]any)[0] != "latest go") {
					t.Errorf("search call %v", cb)
				}
				if cb["name"] == "deepseek_read_url" && (cb["kind"] != "read_link" || in["url"] != site.URL+"/notes") {
					t.Errorf("read call %v", cb)
				}
			case "web_search_tool_result":
				items := cb["content"].([]any)
				it := items[0].(map[string]any)
				if cb["kind"] != "search" || it["url"] != "https://www.go.dev/dl/" || it["site_name"] != "go.dev" || it["page_age"] != "1 day ago" {
					t.Errorf("search result %v", cb)
				}
			case "web_fetch_tool_result":
				c := cb["content"].(map[string]any)
				doc := c["content"].(map[string]any)
				sig, _ := cb["signature"].(string)
				kept, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(sig, chatSigned))
				if cb["kind"] != "read_link" || c["type"] != "web_fetch_result" || doc["title"] != "Release notes" || !strings.Contains(string(kept), "fixes the linker") {
					t.Errorf("read result %v", cb)
				}
			}
		case "content_block_delta":
			switch d := ev["delta"].(map[string]any); d["type"] {
			case "text_delta":
				text += d["text"].(string)
			case "citations_delta":
				c := d["citation"].(map[string]any)
				cited = append(cited, c["type"].(string)+" "+c["url"].(string))
			}
		case "message_delta":
			delta = ev
		}
	}
	want := []string{"server_tool_use:deepseek_web_search", "web_search_tool_result", "text", "server_tool_use:deepseek_read_url", "web_fetch_tool_result", "text"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("blocks %v, want %v\n%s", kinds, want, rec.Body.String())
	}
	// the page shows a line's sources from its citations, as Anthropic's
	// search gives them
	if !slices.Equal(cited, []string{"web_search_result_location https://www.go.dev/dl/"}) {
		t.Errorf("citations %v\n%s", cited, rec.Body.String())
	}
	if !strings.Contains(text, "Reading it, from https://www.go.dev/dl/.") || !strings.HasSuffix(text, "The linker is fixed.") {
		t.Errorf("text %q", text)
	}
	d, _ := delta["delta"].(map[string]any)
	u, _ := delta["usage"].(map[string]any)
	if d["stop_reason"] != "end_turn" || d["title"] != "What's new in Go?" || u["output_tokens"].(float64) < 4+3 {
		t.Errorf("message_delta %v", delta)
	}
	if evs[len(evs)-1]["type"] != "message_stop" {
		t.Error("no message_stop")
	}
	mu.Lock()
	defer mu.Unlock()
	first := asked[0]
	if strings.Contains(first, "DeepSeek") || strings.Contains(first, "深度求索") || !strings.Contains(first, "The user is in Beijing") || !strings.Contains(first, `"read_url"`) {
		t.Errorf("the model was asked %s", first)
	}
}

// A file the page uploads is kept for its own caller, as what a model
// can take: an image, a PDF or text; anything else is refused with what
// to do instead, and the page's DELETE drops it.
func TestChatPageFiles(t *testing.T) {
	fresh(t)
	h := New().Handler()
	upload := func(name string, data []byte) (int, map[string]any) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		mw.WriteField("purpose", "user_data")
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write(data)
		mw.Close()
		r := httptest.NewRequest("POST", ChatPagePath+"/anthropic/v1/files", &b)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	for _, c := range []struct {
		name string
		data []byte
		mime string
	}{
		{"a.png", png, "image/png"},
		{"a.pdf", []byte("%PDF-1.7\n..."), "application/pdf"},
		{"a.md", []byte("# 标题\ntext"), "text/plain"},
	} {
		code, out := upload(c.name, c.data)
		if code != 200 || out["type"] != "file" || out["mime_type"] != c.mime || out["filename"] != c.name || out["size_bytes"].(float64) != float64(len(c.data)) {
			t.Fatalf("%s: %d %v", c.name, code, out)
		}
		id := out["id"].(string)
		r := httptest.NewRequest("POST", "/", nil)
		f := chatFileOf(r, id)
		if f == nil {
			t.Fatalf("%s isn't kept", c.name)
		}
		blk := chatFileBlock(f)
		if c.mime == "image/png" && blk["type"] != "image" || c.mime == "application/pdf" && blk["type"] != "document" || c.mime == "text/plain" && blk["type"] != "text" {
			t.Errorf("%s as %v", c.name, blk["type"])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("DELETE", ChatPagePath+"/anthropic/v1/files/"+id, nil))
		if w.Code/100 != 2 || chatFileOf(r, id) != nil {
			t.Errorf("%s not deleted: %d", c.name, w.Code)
		}
	}
	if code, out := upload("a.docx", []byte("PK\x03\x04\x00\x00binary\x00")); code != http.StatusUnsupportedMediaType || !strings.Contains(out["error"].(map[string]any)["message"].(string), "PDF") {
		t.Errorf("docx: %d %v", code, out)
	}
}
