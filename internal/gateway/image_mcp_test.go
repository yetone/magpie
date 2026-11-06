package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func imagePost(h http.Handler, addr, body string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, ImageMCPPath, strings.NewReader(body))
	r.RemoteAddr = addr
	r.Host = "127.0.0.1:3425"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func imageGet(h http.Handler, addr, rawURL string, hdr ...string) *httptest.ResponseRecorder {
	u, _ := url.Parse(rawURL)
	r := httptest.NewRequest(http.MethodGet, u.RequestURI(), nil)
	r.RemoteAddr = addr
	r.Host = "127.0.0.1:3425"
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func imageToolCall(name string, args map[string]any) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	return string(b)
}

type imageReply struct {
	Result struct {
		Content []struct {
			Type, Text, Data, MimeType string
		}
		IsError bool
		Meta    map[string]json.RawMessage `json:"_meta"`
	}
	Error *struct{ Message string }
}

// meta is what the answer's _meta holds at k, as JSON ("" for nothing):
// what `magpie tool` waits on and saves by, beside the text an agent reads
func (m imageReply) meta(k string) string { return string(m.Result.Meta[k]) }

func readImageReply(t *testing.T, w *httptest.ResponseRecorder) imageReply {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var m imageReply
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || len(m.Result.Content) == 0 {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return m
}

// the URLs a tool's text names, one a line ("- <url>")
func urlsIn(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if u, ok := strings.CutPrefix(l, "- "); ok && strings.Contains(u, "://") {
			out = append(out, u)
		}
	}
	return out
}

// magpie's image generation is an MCP server on the gateway (JonahFu on
// X), for an agent that has no Magpie Image, in a gateway in Docker or on
// another computer: it lists Magpie Image's tools with their arguments but
// where to save, draws with the model Settings picks, and answers with the
// image itself and a URL it can be downloaded from.
func TestImageMCPDrawsAndServesTheImage(t *testing.T) {
	s, e := easeled(t)
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Cleanup(s.imageStore.running.Wait)
	h := lanGuard(s.Handler())
	init := readMCP(t, imagePost(h, "127.0.0.1:5000", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	if init.Result.ServerInfo.Name != "magpie-image" || init.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("initialize: %+v", init)
	}
	var list struct {
		Result struct {
			Tools []struct {
				Name        string
				InputSchema struct {
					Properties map[string]any
					Required   []string
				}
			}
		}
	}
	w := imagePost(h, "127.0.0.1:5000", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	json.Unmarshal(w.Body.Bytes(), &list)
	var names []string
	for _, tl := range list.Result.Tools {
		names = append(names, tl.Name)
		if _, ok := tl.InputSchema.Properties["path"]; ok {
			t.Fatalf("%s takes a path the gateway can't write to", tl.Name)
		}
	}
	if strings.Join(names, ",") != "generate_image,generate_video,generation_result" || len(list.Result.Tools[0].InputSchema.Properties) != len(ImageToolProps(""))+1 {
		t.Fatalf("tools: %s", w.Body.String())
	}
	m := readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateImageTool, map[string]any{"prompt": "a magpie", "size": "1024x1024", "complexity": "low"})))
	if m.Result.IsError || len(m.Result.Content) != 2 {
		t.Fatalf("drawn: %+v", m)
	}
	img := m.Result.Content[1]
	if img.Type != "image" || img.MimeType != "image/png" || img.Data != base64.StdEncoding.EncodeToString(pngBytes) {
		t.Fatalf("image content %+v", img)
	}
	sent := e.got("/v1/images/generations")
	if len(sent) != 1 || !strings.Contains(sent[0], `"prompt":"a magpie"`) || !strings.Contains(sent[0], `"size":"1024x1024"`) || !strings.Contains(sent[0], `"complexity":"low"`) {
		t.Fatalf("vendor was sent %v", sent)
	}
	urls := urlsIn(m.Result.Content[0].Text)
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "http://127.0.0.1:3425"+ImageMCPFiles) || !strings.HasSuffix(bare(urls[0]), ".png") || !strings.Contains(m.Result.Content[0].Text, "art/") {
		t.Fatalf("text: %q", m.Result.Content[0].Text)
	}
	if got, want := m.meta(FilesMeta), `[{"url":"`+urls[0]+`","inline":true}]`; got != want || m.meta(PendingMeta) != "" {
		t.Fatalf("_meta %v, want files %s", m.Result.Meta, want)
	}
	got := imageGet(h, "127.0.0.1:5000", urls[0])
	if got.Code != 200 || got.Body.String() != string(pngBytes) || got.Header().Get("Content-Type") != "image/png" || got.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("file: %d %v %q", got.Code, got.Header(), got.Body.String())
	}
	if w := imageGet(h, "127.0.0.1:5000", "http://127.0.0.1:3425"+ImageMCPFiles+"nothing.png"); w.Code != 404 {
		t.Fatalf("no such file: %d", w.Code)
	}
	// MAGPIE_PUBLIC_URL is the address the agent is given
	t.Setenv("MAGPIE_PUBLIC_URL", "https://magpie.example.net/")
	m = readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateImageTool, map[string]any{"prompt": "a magpie"})))
	if u := urlsIn(m.Result.Content[0].Text); len(u) != 1 || !strings.HasPrefix(u[0], "https://magpie.example.net"+ImageMCPFiles) {
		t.Fatalf("public URL: %q", m.Result.Content[0].Text)
	}
	// a reference image makes it an edit
	m = readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateImageTool, map[string]any{"prompt": "bluer", "reference_images": []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)}})))
	if m.Result.IsError || len(e.got("/v1/images/edits")) != 1 {
		t.Fatalf("edit: %+v %v", m, e.got("/v1/images/edits"))
	}
}

// Off in Settings, the tools say so, and nothing is drawn.
func TestImageMCPSaysWhenOff(t *testing.T) {
	s, e := easeled(t)
	st := settings.Load()
	st.ImageGen = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(s.Handler())
	for _, tool := range []string{GenerateImageTool, GenerateVideoTool} {
		m := readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(tool, map[string]any{"prompt": "a magpie"})))
		if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "Image generation is off in magpie") {
			t.Fatalf("%s: %+v", tool, m)
		}
	}
	if n := len(e.got("/v1/images/generations")); n != 0 {
		t.Fatalf("drew %d times while off", n)
	}
}

// The server is guarded as the vision server is: another computer only on
// a shared magpie with a key, no web page, no file path from elsewhere;
// and an image it made is served only to the key that had it made.
func TestImageMCPGuarded(t *testing.T) {
	s, e := easeled(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Cleanup(s.imageStore.running.Wait)
	h := lanGuard(s.Handler())
	file, _ := pngFile(t)
	const there = "192.0.2.7:4000"
	draw := imageToolCall(GenerateImageTool, map[string]any{"prompt": "a magpie"})
	if w := imagePost(h, there, draw); w.Code != http.StatusForbidden {
		t.Fatalf("not shared: %d %s", w.Code, w.Body.String())
	}
	_, secrets := newCaller(t, "Laptop", "Phone")
	if w := imagePost(h, there, draw); w.Code != http.StatusUnauthorized {
		t.Fatalf("shared, no key: %d %s", w.Code, w.Body.String())
	}
	if w := imagePost(h, "127.0.0.1:50000", draw, "Origin", "http://localhost:5173"); w.Code != http.StatusForbidden {
		t.Fatalf("a page on this computer: %d %s", w.Code, w.Body.String())
	}
	for _, tool := range []string{GenerateImageTool, GenerateVideoTool} {
		args := map[string]any{"prompt": "a magpie", "reference_images": []string{file}}
		m := readImageReply(t, imagePost(h, there, imageToolCall(tool, args), "Authorization", "Bearer "+secrets[0]))
		if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, tool+" reads a file only for an agent on the computer magpie runs on") {
			t.Fatalf("%s, another computer's file path: %+v", tool, m)
		}
	}
	if n := len(e.got("/v1/images/edits")); n != 0 {
		t.Fatalf("drew from another computer's path %d times", n)
	}
	m := readImageReply(t, imagePost(h, there, draw, "Authorization", "Bearer "+secrets[0]))
	urls := urlsIn(m.Result.Content[0].Text)
	if m.Result.IsError || len(urls) != 1 {
		t.Fatalf("shared, with its key: %+v", m)
	}
	if w := imageGet(h, there, urls[0], "Authorization", "Bearer "+secrets[0]); w.Code != 200 || w.Body.String() != string(pngBytes) {
		t.Fatalf("its own key: %d %s", w.Code, w.Body.String())
	}
	// without the link's token, only its key opens it
	if w := imageGet(h, there, bare(urls[0]), "Authorization", "Bearer "+secrets[0]); w.Code != 200 {
		t.Fatalf("its own key, no token: %d %s", w.Code, w.Body.String())
	}
	if w := imageGet(h, there, bare(urls[0])); w.Code != http.StatusUnauthorized {
		t.Fatalf("no key: %d %s", w.Code, w.Body.String())
	}
	if w := imageGet(h, there, bare(urls[0]), "Authorization", "Bearer "+secrets[1]); w.Code != http.StatusNotFound {
		t.Fatalf("another key: %d %s", w.Code, w.Body.String())
	}
	if w := imageGet(h, "127.0.0.1:5000", bare(urls[0])); w.Code != http.StatusNotFound {
		t.Fatalf("keyless on this computer: %d %s", w.Code, w.Body.String())
	}
	if w := imageGet(h, "127.0.0.1:5000", urls[0], "Authorization", "Bearer "+secrets[0], "Origin", "http://localhost:5173"); w.Code != http.StatusForbidden {
		t.Fatalf("a page: %d %s", w.Code, w.Body.String())
	}
}

// bare is a file URL without its token.
func bare(u string) string { b, _, _ := strings.Cut(u, "?"); return b }

// The URL the image server hands out opens with no header, from another
// computer, in a browser (dhanyqaz on Discord: a model hands a person the
// link, and a browser sends no key): its token, signed by the gateway, is
// the only thing that lets a request without the key in, for that file,
// within its hour, while its key is on and magpie is shared, and it
// carries neither the key nor its id.
func TestImageMCPFileLinkOpensWithoutKey(t *testing.T) {
	s, _ := easeled(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Cleanup(s.imageStore.running.Wait)
	h := lanGuard(s.Handler())
	keys, secrets := newCaller(t, "Laptop", "Phone")
	const there = "192.0.2.7:4000"
	draw := imageToolCall(GenerateImageTool, map[string]any{"prompt": "a magpie"})
	var urls []string
	for range 2 {
		m := readImageReply(t, imagePost(h, there, draw, "Authorization", "Bearer "+secrets[0]))
		u := urlsIn(m.Result.Content[0].Text)
		if m.Result.IsError || len(u) != 1 || !strings.Contains(m.Result.Content[0].Text, "opens without a header") {
			t.Fatalf("drawn: %+v", m)
		}
		urls = append(urls, u...)
	}
	for _, u := range urls {
		if strings.Contains(u, secrets[0]) || strings.Contains(u, keys[0].ID) || !strings.Contains(u, "?"+imageTokenParam+"=") {
			t.Fatalf("the link %s carries the key or no token", u)
		}
	}
	get := func(u string, hdr ...string) int { return imageGet(h, there, u, hdr...).Code }
	// no header, from another computer and through a tunnel, and in a
	// browser's own navigation
	if w := imageGet(h, there, urls[0]); w.Code != 200 || w.Body.String() != string(pngBytes) || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("no header: %d %s", w.Code, w.Body.String())
	}
	if c := get(urls[0], "Cf-Connecting-IP", "198.51.100.9", "Sec-Fetch-Site", "none", "Sec-Fetch-Mode", "navigate"); c != 200 {
		t.Fatalf("a browser through a tunnel: %d", c)
	}
	if w := imageGet(h, "127.0.0.1:5000", urls[0]); w.Code != 200 {
		t.Fatalf("on this computer: %d", w.Code)
	}
	// the token is what opens it, beside another key too
	if c := get(urls[0], "Authorization", "Bearer "+secrets[1]); c != 200 {
		t.Fatalf("another key, with the token: %d", c)
	}
	// a wrong key is refused as it always was, token or not
	if c := get(urls[0], "Authorization", "Bearer sk-magpie-not-a-key-at-all"); c != http.StatusUnauthorized {
		t.Fatalf("a wrong key: %d", c)
	}
	q := func(u, tok string) string { return bare(u) + "?" + imageTokenParam + "=" + url.QueryEscape(tok) }
	tok := func(u string) string { pu, _ := url.Parse(u); return pu.Query().Get(imageTokenParam) }
	name := func(u string) string { return strings.TrimPrefix(bare(u), "http://127.0.0.1:3425"+ImageMCPFiles) }
	exp, sig, _ := strings.Cut(tok(urls[0]), ".")
	flipped := []byte(sig)
	flipped[3] ^= 1
	n, _ := strconv.ParseInt(exp, 10, 64)
	s.imageStore.mu.Lock()
	secret := s.imageStore.secret
	s.imageStore.mu.Unlock()
	for what, u := range map[string]string{
		"a tampered token":        q(urls[0], exp+"."+string(flipped)),
		"a later expiry":          q(urls[0], strconv.FormatInt(n+3600, 10)+"."+sig),
		"another file's token":    q(urls[0], tok(urls[1])),
		"an empty token":          q(urls[0], "."),
		"an expired token":        q(urls[0], imageFileToken(secret, name(urls[0]), keys[0].ID, time.Now().Unix()-1)),
		"a token for another key": q(urls[0], imageFileToken(secret, name(urls[0]), keys[1].ID, n)),
		"another secret's token":  q(urls[0], imageFileToken([]byte("not magpie's secret at all......"), name(urls[0]), keys[0].ID, n)),
		"the token on no file":    q(strings.Replace(urls[0], name(urls[0]), "0123456789abcdef0123456789abcdef.png", 1), tok(urls[0])),
		"the token past its path": bare(urls[0]) + "/x?" + imageTokenParam + "=" + url.QueryEscape(tok(urls[0])),
	} {
		if c := get(u); c != http.StatusUnauthorized && c != http.StatusNotFound {
			t.Fatalf("%s, no header: %d", what, c)
		}
		if c := get(u, "Authorization", "Bearer "+secrets[1]); c != http.StatusNotFound && c != http.StatusUnauthorized {
			t.Fatalf("%s, another key: %d", what, c)
		}
	}
	if c := get(q(urls[0], exp+"."+string(flipped))); c != http.StatusUnauthorized {
		t.Fatalf("a tampered token is refused as no key is: %d", c)
	}
	// a web page reading it is refused as it was
	if c := get(urls[0], "Origin", "https://evil.example"); c != http.StatusForbidden {
		t.Fatalf("a web page: %d", c)
	}
	// the token opens GET and HEAD only
	r := httptest.NewRequest(http.MethodPost, bare(urls[0])[len("http://127.0.0.1:3425"):]+"?"+imageTokenParam+"="+url.QueryEscape(tok(urls[0])), nil)
	r.RemoteAddr, r.Host = there, "127.0.0.1:3425"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a POST with the token: %d", w.Code)
	}
	// past its hour the file is gone, token and all
	s.imageStore.mu.Lock()
	s.imageStore.files[name(urls[1])].made = time.Now().Add(-imageMCPKeep - time.Second)
	s.imageStore.mu.Unlock()
	if c := get(urls[1]); c != http.StatusUnauthorized {
		t.Fatalf("past its hour: %d", c)
	}
	// turning its key off takes its links with it
	if _, err := access.Update("off-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if c := get(urls[0]); c != http.StatusUnauthorized {
		t.Fatalf("its key off: %d", c)
	}
	if _, err := access.Update("on-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if c := get(urls[0]); c != 200 {
		t.Fatalf("its key on again: %d", c)
	}
	// not shared, the token opens nothing from another computer
	st := settings.Load()
	st.LAN = false
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if c := get(urls[0]); c != http.StatusForbidden {
		t.Fatalf("not shared: %d", c)
	}
}

// A gateway key past its limit draws nothing through the server.
func TestImageMCPHeldToKeyLimit(t *testing.T) {
	s, e := easeled(t)
	t.Cleanup(s.imageStore.running.Wait)
	budget.Forget()
	pinLimitClock(t)
	calls := limitedUpstream(t, 0)
	keys, secrets := newCaller(t, "Capped")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 700})
	h := lanGuard(s.Handler())
	for range 2 {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReq))
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	for _, tool := range []string{GenerateImageTool, GenerateVideoTool} {
		m := readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(tool, map[string]any{"prompt": "a magpie"}), "Authorization", "Bearer "+secrets[0]))
		if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, `"Capped"`) {
			t.Fatalf("%s, a spent key: %+v", tool, m)
		}
	}
	if n := len(e.got("/v1/images/generations")); n != 0 {
		t.Fatalf("a spent key drew %d times", n)
	}
	// without a key, from this computer, it draws
	if m := readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateImageTool, map[string]any{"prompt": "a magpie"}))); m.Result.IsError {
		t.Fatalf("keyless: %+v", m)
	}
}

// A drawing that outlasts the call goes on, and generation_result gets it
// by the id the call answered with; for the key that started it only.
func TestImageMCPSlowDrawingIsWaitedOn(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	gate := make(chan struct{})
	e := &easel{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-gate
		e.ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "art", Name: "Art", Chat: up.URL + "/v1", Key: "key", Models: []string{"gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	opened := false
	open := func() {
		if !opened {
			opened = true
			close(gate)
		}
	}
	// the job ends once the vendor answers
	t.Cleanup(s.imageStore.running.Wait)
	t.Cleanup(open)
	h := lanGuard(s.Handler())
	_, secrets := newCaller(t, "Mine", "Theirs")
	was := imageMCPWithin
	t.Cleanup(func() { imageMCPWithin = was })
	imageMCPWithin = 10 * time.Millisecond
	m := readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateImageTool, map[string]any{"prompt": "a magpie"}), "Authorization", "Bearer "+secrets[0]))
	text := m.Result.Content[0].Text
	i := strings.Index(text, `"img_`)
	if m.Result.IsError || i < 0 || !strings.Contains(text, GenerationResultTool) {
		t.Fatalf("still drawing: %+v", m)
	}
	id := strings.Trim(strings.Fields(text[i:])[0], `".,`)
	if got := m.meta(PendingMeta); got != `"`+id+`"` || m.meta(FilesMeta) != "" {
		t.Fatalf("_meta %v, want pending %s", m.Result.Meta, id)
	}
	// another key's call can't have it
	m = readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerationResultTool, map[string]any{"id": id}), "Authorization", "Bearer "+secrets[1]))
	if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "no image is being made") {
		t.Fatalf("another key: %+v", m)
	}
	open()
	imageMCPWithin = time.Minute
	m = readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerationResultTool, map[string]any{"id": id}), "Authorization", "Bearer "+secrets[0]))
	if m.Result.IsError || len(m.Result.Content) != 2 || m.Result.Content[1].Data != base64.StdEncoding.EncodeToString(pngBytes) {
		t.Fatalf("waited on: %+v", m)
	}
	if m.meta(PendingMeta) != "" || !strings.Contains(m.meta(FilesMeta), `"inline":true`) {
		t.Fatalf("waited on _meta: %v", m.Result.Meta)
	}
	if n := len(e.got("/v1/images/generations")); n != 1 {
		t.Fatalf("drew %d times", n)
	}
}

// generate_video starts a video with the model that makes them, waits on
// it, and answers with the gateway's URL to download it from; one that
// outlasts the call is waited on by its id.
func TestImageMCPMakesVideos(t *testing.T) {
	grokSignedIn(t)
	newGrokMedia(t)
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	s := New()
	h := lanGuard(s.Handler())
	was, wasPoll := imageMCPWithin, imageMCPPoll
	t.Cleanup(func() { imageMCPWithin, imageMCPPoll = was, wasPoll })
	// still being made when the call answers: its id is waited on
	imageMCPWithin, imageMCPPoll = 0, time.Millisecond
	m := readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateVideoTool, map[string]any{"prompt": "a kite sways", "seconds": 6})))
	text := m.Result.Content[0].Text
	i := strings.Index(text, `"video_`)
	if m.Result.IsError || i < 0 || !strings.Contains(text, GenerationResultTool) {
		t.Fatalf("still making: %+v", m)
	}
	id := strings.Trim(strings.Fields(text[i:])[0], `".,`)
	if got := m.meta(PendingMeta); got != `"`+id+`"` {
		t.Fatalf("_meta %v, want pending %s", m.Result.Meta, id)
	}
	imageMCPWithin = time.Minute
	m = readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerationResultTool, map[string]any{"id": id})))
	text = m.Result.Content[0].Text
	urls := urlsIn(text)
	if m.Result.IsError || len(urls) != 1 || urls[0] != "http://127.0.0.1:3425/v1/videos/"+id+"/content" || !strings.Contains(text, "6-second") || !strings.Contains(text, "grok/grok-imagine-video") {
		t.Fatalf("video: %+v", m)
	}
	if got, want := m.meta(FilesMeta), `[{"url":"`+urls[0]+`"}]`; got != want || m.meta(PendingMeta) != "" {
		t.Fatalf("video _meta %v, want files %s", m.Result.Meta, want)
	}
	if w := imageGet(h, "127.0.0.1:5000", urls[0]); w.Code != 200 || w.Body.String() != "fake-mp4-bytes" {
		t.Fatalf("content: %d %s", w.Code, w.Body.String())
	}
	// made within the call: its URL at once
	m = readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateVideoTool, map[string]any{"prompt": "a kite"})))
	if u := urlsIn(m.Result.Content[0].Text); m.Result.IsError || len(u) != 1 || !strings.HasSuffix(u[0], "/content") {
		t.Fatalf("at once: %+v", m)
	}
	if m := readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateVideoTool, map[string]any{"prompt": "a kite", "seconds": 40}))); !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "1 to 15") {
		t.Fatalf("seconds: %+v", m)
	}
	// the vendor's video failing is said
	m = readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateVideoTool, map[string]any{"prompt": "FAILING"})))
	if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "the video failed: image_url must") {
		t.Fatalf("failed: %+v", m)
	}
}

// The gateway serves the server (and its files) on the address `magpie
// web` and a shared magpie forward.
func TestImageMCPIsServed(t *testing.T) {
	for _, p := range []string{ImageMCPPath, ImageMCPFiles + "x.png"} {
		if !ClientPath(p) {
			t.Fatalf("%s isn't a client path", p)
		}
	}
	fresh(t)
	w := imageGet(New().Handler(), "127.0.0.1:5000", "http://127.0.0.1:3425/v1")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"`+ImageMCPPath+`"`) {
		t.Fatalf("apis: %d %s", w.Code, w.Body.String())
	}
}
