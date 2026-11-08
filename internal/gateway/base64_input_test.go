package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// dludream's picture (#5697): a 32x32 white PNG as PIL writes it,
// Image.new("RGB", (32, 32), "white").save(buf, format="PNG"), encoded as
// google-genai sends a part's bytes, base64.urlsafe_b64encode. It has a
// '-' where standard base64 has '+'.
const (
	whitePNGURLSafe  = "iVBORw0KGgoAAAANSUhEUgAAACAAAAAgCAIAAAD8GO2jAAAAN0lEQVR4nO3RwQ0AMAjDwJT9d05HMB9-vgGCZF7bXJrT9XhgwR8gEyETIRMhEyETIRMhEyEThXzH8QM9OMM6fAAAAABJRU5ErkJggg=="
	whitePNGStandard = "iVBORw0KGgoAAAANSUhEUgAAACAAAAAgCAIAAAD8GO2jAAAAN0lEQVR4nO3RwQ0AMAjDwJT9d05HMB9+vgGCZF7bXJrT9XhgwR8gEyETIRMhEyETIRMhEyEThXzH8QM9OMM6fAAAAABJRU5ErkJggg=="
)

// pdfBytes is a PDF's head whose base64 has both '-' and '_', unpadded.
var pdfBytes = []byte("%PDF-1.7\n%\xfb\xff\xbe\xef\xfe\x3f\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\ntrailer")

func TestWhitePNGFixtureIsURLSafe(t *testing.T) {
	if !strings.ContainsAny(whitePNGURLSafe, "-_") || strings.ContainsAny(whitePNGURLSafe, "+/") {
		t.Fatal("the fixture is not URL-safe base64 alone")
	}
	a, err1 := base64.URLEncoding.DecodeString(whitePNGURLSafe)
	b, err2 := base64.StdEncoding.DecodeString(whitePNGStandard)
	if err1 != nil || err2 != nil || string(a) != string(b) {
		t.Fatalf("the two spellings are not one PNG: %v %v", err1, err2)
	}
	if _, err := base64.StdEncoding.DecodeString(whitePNGURLSafe); err == nil {
		t.Fatal("standard base64 reads the URL-safe fixture: it tests nothing")
	}
	p := base64.RawURLEncoding.EncodeToString(pdfBytes)
	if !strings.Contains(p, "-") || !strings.Contains(p, "_") || len(p)%4 == 0 {
		t.Fatalf("the PDF fixture %s is not unpadded URL-safe with both '-' and '_'", p)
	}
}

func TestStdBase64(t *testing.T) {
	raw := []byte("\xfb\xff\xbe\xef\xfe\x3f!")
	std := base64.StdEncoding.EncodeToString(raw)
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{whitePNGURLSafe, whitePNGStandard, true},
		{whitePNGStandard, whitePNGStandard, false},
		{base64.URLEncoding.EncodeToString(raw), std, true},
		{base64.RawURLEncoding.EncodeToString(raw), std, true},
		{base64.RawStdEncoding.EncodeToString(raw), std, true},
		{std, std, false},
		{"", "", false},
		{"ab-c+d==", "ab-c+d==", false}, // both alphabets
		{"not base64!", "not base64!", false},
	} {
		got, ok := stdBase64(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("stdBase64(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Every request shape that carries an image inline has its URL-safe
// base64 written as standard, the block's other fields kept; a Gemini
// part of any type is, the system instruction's and a function
// response's too. Standard base64 leaves the body as it came.
func TestStdBase64Body(t *testing.T) {
	u, s := whitePNGURLSafe, whitePNGStandard
	pdfU, pdfS := base64.RawURLEncoding.EncodeToString(pdfBytes), base64.StdEncoding.EncodeToString(pdfBytes)
	cases := []struct {
		name        string
		proto       provider.Protocol
		body        string
		path, want  string
		kept        string
		unchangedOK bool
	}{
		{"gemini", provider.Gemini, `{"contents":[{"role":"user","parts":[{"text":"Reply with OK."},{"inlineData":{"mimeType":"image/png","data":"` + u + `"}}]}]}`,
			"contents.0.parts.1.inlineData.data", s, "contents.0.parts.0.text", false},
		{"gemini snake", provider.Gemini, `{"contents":[{"role":"user","parts":[{"inline_data":{"mime_type":"image/png","data":"` + u + `"}}]}]}`,
			"contents.0.parts.0.inline_data.data", s, "contents.0.role", false},
		{"gemini pdf", provider.Gemini, `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"application/pdf","data":"` + pdfU + `"}}]}]}`,
			"contents.0.parts.0.inlineData.data", pdfS, "contents.0.parts.0.inlineData.mimeType", false},
		{"gemini function response", provider.Gemini, `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"shot","response":{},"parts":[{"inlineData":{"mimeType":"image/png","data":"` + u + `"}}]}}]}]}`,
			"contents.0.parts.0.functionResponse.parts.0.inlineData.data", s, "contents.0.parts.0.functionResponse.name", false},
		{"gemini system", provider.Gemini, `  {"systemInstruction":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + u + `"}}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			"systemInstruction.parts.0.inlineData.data", s, "contents.0.parts.0.text", false},
		{"gemini standard", provider.Gemini, `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"` + s + `"}}]}]}`,
			"contents.0.parts.0.inlineData.data", s, "contents.0.role", true},
		{"chat", provider.Chat, `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + u + `","detail":"high"}}]}]}`,
			"messages.0.content.0.image_url.url", "data:image/png;base64," + s, "messages.0.content.0.image_url.detail", false},
		{"chat standard", provider.Chat, `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + s + `"}}]}]}`,
			"messages.0.content.0.image_url.url", "data:image/png;base64," + s, "model", true},
		{"responses", provider.Responses, `{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + u + `","detail":"auto"}]}]}`,
			"input.0.content.0.image_url", "data:image/png;base64," + s, "input.0.content.0.detail", false},
		{"messages", provider.Anthropic, `{"model":"m","max_tokens":9,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + u + `"},"cache_control":{"type":"ephemeral"}}]}]}`,
			"messages.0.content.0.source.data", s, "messages.0.content.0.cache_control.type", false},
		{"messages tool_result", provider.Anthropic, `{"model":"m","max_tokens":9,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + u + `"}}]}]}]}`,
			"messages.0.content.0.content.0.source.data", s, "messages.0.content.0.tool_use_id", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !c.unchangedOK && !mayHoldOddBase64([]byte(c.body)) {
				t.Fatal("the scan missed the URL-safe base64")
			}
			out := stdBase64Body(c.proto, []byte(c.body))
			if c.unchangedOK {
				if string(out) != c.body {
					t.Fatalf("standard base64's body changed: %.300s", out)
				}
				return
			}
			if !json.Valid(out) {
				t.Fatalf("not JSON: %.300s", out)
			}
			if got := gjson.GetBytes(out, c.path).Str; got != c.want {
				t.Fatalf("%s is %q, want %q", c.path, got, c.want)
			}
			before, after := gjson.Get(c.body, c.kept), gjson.GetBytes(out, c.kept)
			if !before.Exists() || before.Raw != after.Raw {
				t.Fatalf("%s was %s, is %s", c.kept, before.Raw, after.Raw)
			}
		})
	}
}

// A Gemini request's system instruction may come before its turns: both
// its parts and theirs are written, wherever each sits.
func TestGeminiStdBase64SystemFirst(t *testing.T) {
	pdfU, pdfS := base64.RawURLEncoding.EncodeToString(pdfBytes), base64.StdEncoding.EncodeToString(pdfBytes)
	body := `{"systemInstruction":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + whitePNGURLSafe + `"}}]},"contents":[{"role":"user","parts":[{"text":"hi"},{"inlineData":{"mimeType":"application/pdf","data":"` + pdfU + `"}}]}]}`
	out := stdBase64Body(provider.Gemini, []byte(body))
	if got := gjson.GetBytes(out, "systemInstruction.parts.0.inlineData.data").Str; got != whitePNGStandard {
		t.Errorf("system instruction's image is %q", got)
	}
	if got := gjson.GetBytes(out, "contents.0.parts.1.inlineData.data").Str; got != pdfS {
		t.Errorf("the turn's PDF is %q, want %q", got, pdfS)
	}
}

// strictGemini answers as dludream's upstream did: 400 "Failed to
// base64-decode image data" for inline data that isn't standard base64,
// "OK" otherwise. It reads every protocol's shape of an inline image.
func strictGemini(body []byte) (int, string) {
	var datas []string
	var walk func(gjson.Result)
	walk = func(r gjson.Result) {
		r.ForEach(func(k, v gjson.Result) bool {
			switch {
			case (k.Str == "data" || k.Str == "url" || k.Str == "image_url") && v.Type == gjson.String:
				d := v.Str
				if rest, ok := strings.CutPrefix(d, "data:"); ok {
					_, d, _ = strings.Cut(rest, ",")
				} else if k.Str != "data" {
					return true
				}
				datas = append(datas, d)
			case v.IsObject() || v.IsArray():
				walk(v)
			}
			return true
		})
	}
	walk(gjson.ParseBytes(body))
	if len(datas) == 0 {
		return 400, `{"error":{"code":400,"message":"no image reached the upstream","status":"INVALID_ARGUMENT"}}`
	}
	for _, d := range datas {
		if _, err := base64.StdEncoding.DecodeString(d); err != nil {
			return 400, `[{"error":{"code":400,"message":"Failed to base64-decode image data","status":"INVALID_ARGUMENT"}}]`
		}
	}
	return 0, ""
}

func b64Home(t *testing.T) {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
}

// dludream's request (#5697), as google-genai sent it, reaches a Gemini
// provider of the user's own ("gemini-2") with the image in standard
// base64, so an upstream that reads only that answers; as do the routes
// that translate it to another protocol, and those that send an image to
// a Gemini provider from another client's protocol.
func TestURLSafeBase64ReachesUpstreamAsStandard(t *testing.T) {
	geminiAsk := `{"contents":[{"parts":[{"text":"Reply with OK."},{"inlineData":{"data":"` + whitePNGURLSafe + `","mimeType":"image/png"}}],"role":"user"}]}`
	geminiReply := `data: {"candidates":[{"content":{"parts":[{"text":"OK"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}` + "\n\n"
	geminiUp := func(t *testing.T, f *fake) {
		up := httptest.NewServer(f)
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: "gemini-2", Name: "Gemini 2", Key: "AIza-test", Gemini: up.URL + "/v1beta", Models: []string{"gemini-3.8-flash"}}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name  string
		up    provider.Protocol
		reply string
		path  string
		body  string
	}{
		{"gemini to gemini", provider.Gemini, geminiReply, "/v1beta/models/gemini-2/models/gemini-3.8-flash:generateContent", geminiAsk},
		{"gemini to chat", provider.Chat, chatSSEReply("OK"), "/v1beta/models/fake/m1:generateContent", geminiAsk},
		{"gemini to messages", provider.Anthropic, anthropicReply(), "/v1beta/models/fake/m1:generateContent", geminiAsk},
		{"gemini to responses", provider.Responses, sse(`data: {"type":"response.output_text.delta","item_id":"m1","output_index":0,"content_index":0,"delta":"OK"}`, `data: `+responsesCompleted()), "/v1beta/models/fake/m1:generateContent", geminiAsk},
		{"chat to gemini", provider.Gemini, geminiReply, "/v1/chat/completions",
			`{"model":"gemini-2/gemini-3.8-flash","messages":[{"role":"user","content":[{"type":"text","text":"Reply with OK."},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + whitePNGURLSafe + `"}}]}]}`},
		{"messages to gemini", provider.Gemini, geminiReply, "/v1/messages",
			`{"model":"gemini-2/gemini-3.8-flash","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Reply with OK."},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + whitePNGURLSafe + `"}}]}]}`},
		{"gemini pdf to gemini", provider.Gemini, geminiReply, "/v1beta/models/gemini-2/gemini-3.8-flash:generateContent",
			`{"contents":[{"parts":[{"text":"Reply with OK."},{"inlineData":{"data":"` + base64.RawURLEncoding.EncodeToString(pdfBytes) + `","mimeType":"application/pdf"}}],"role":"user"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b64Home(t)
			f := &fake{t: t, reply: c.reply, refuse: strictGemini}
			if c.up == provider.Gemini {
				geminiUp(t, f)
			} else {
				setup(t, c.up, f)
			}
			code, body := post(t, c.path, c.body)
			if f.calls == 0 {
				t.Fatalf("the upstream was never asked: %d %s", code, body)
			}
			if code != 200 || !strings.Contains(body, "OK") {
				t.Fatalf("%d %s\nsent: %.600s", code, body, f.got)
			}
			if strings.Contains(string(f.got), whitePNGURLSafe) {
				t.Fatalf("the URL-safe base64 went upstream: %.600s", f.got)
			}
		})
	}
}
