package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// markupPNG stands for the transparent layer: other bytes than pngBytes,
// so a test can tell which picture went where.
var markupPNG = []byte("\x89PNG\r\n\x1a\n markup layer")

func markupURL() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(markupPNG)
}

func sourceURL() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
}

// #5364 (kongjianguan): /v1/images/edits takes OpenAI's markup layer, a
// transparent PNG laid over the first image to steer the edit. magpie
// read only the mask: a JSON markup stopped in drawing.Extra, a
// multipart one was dropped, and neither the ChatGPT backend's JSON edit
// nor the plain multipart edit carried it.
func TestCodexEditSendsTheMarkup(t *testing.T) {
	fresh(t)
	e := &easel{}
	up := httptest.NewServer(e)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "art", Name: "Art", Responses: up.URL + "/v1", Key: "key",
		Account: &provider.Account{Agent: "codex", User: "u@example.com"}}
	if !drawsCodex(p) {
		t.Fatal("the provider is not a ChatGPT account")
	}
	s := New()
	r := httptest.NewRequest("POST", "/v1/images/edits", strings.NewReader(
		`{"model":"art/gpt-image-1","prompt":"apply the annotation","images":[{"image_url":"`+sourceURL()+`"}],"markup":{"image_url":"`+markupURL()+`"}}`))
	r.Header.Set("Content-Type", "application/json")
	d, err := readDrawing(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Extra["markup"]; ok {
		t.Error("the markup read as a layer is kept as an extra field too")
	}
	if _, code, err := s.drawImages(context.Background(), p, "gpt-image-1", d); code != 200 || err != nil {
		t.Fatalf("%d %v", code, err)
	}
	sent := e.got("/v1/images/edits")
	if len(sent) != 1 {
		t.Fatalf("vendor was sent %v", sent)
	}
	var req struct {
		Markup struct {
			ImageURL string `json:"image_url"`
		} `json:"markup"`
		Mask json.RawMessage `json:"mask"`
	}
	if err := json.Unmarshal([]byte(sent[0]), &req); err != nil {
		t.Fatalf("vendor got non-JSON: %v (%s)", err, sent[0])
	}
	if req.Markup.ImageURL != markupURL() {
		t.Errorf("the ChatGPT edit's markup is %q, want the layer as it came: %s", req.Markup.ImageURL, sent[0])
	}
	if req.Mask != nil {
		t.Errorf("a markup went out as a mask: %s", sent[0])
	}
}

func (e *easel) ctOf(path string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ct[path]
}

// partNamed is the body of the multipart part named name in a request
// sent with the given Content-Type.
func partNamed(t *testing.T, ct, body, name string) (string, bool) {
	t.Helper()
	_, boundary, ok := strings.Cut(ct, "boundary=")
	if !ok {
		t.Fatalf("not multipart: %s", ct)
	}
	mr := multipart.NewReader(strings.NewReader(body), boundary)
	for {
		part, err := mr.NextPart()
		if err != nil {
			return "", false
		}
		if part.FormName() == name {
			var b bytes.Buffer
			b.ReadFrom(part)
			return b.String(), true
		}
	}
}

// the plain OpenAI edit carries the layer as its own multipart part,
// whether it came as JSON or as a part of a multipart form.
func TestEditSendsTheMarkup(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		s, e := easeled(t)
		code, _, raw := postImages(t, s, "/v1/images/edits", "application/json",
			`{"model":"art/gpt-image-1","prompt":"apply the annotation","images":[{"image_url":"`+sourceURL()+`"}],"markup":{"image_url":"`+markupURL()+`"}}`)
		if code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		sent := e.got("/v1/images/edits")
		if len(sent) != 1 {
			t.Fatalf("vendor was sent %v", sent)
		}
		got, ok := partNamed(t, e.ctOf("/v1/images/edits"), sent[0], "markup")
		if !ok || got != string(markupPNG) {
			t.Fatalf("the markup layer never reached the vendor whole (part %v, %q)", ok, got)
		}
		if _, ok := partNamed(t, e.ctOf("/v1/images/edits"), sent[0], "mask"); ok {
			t.Error("a markup went out as a mask")
		}
	})
	t.Run("multipart", func(t *testing.T) {
		s, e := easeled(t)
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("model", "art/gpt-image-1")
		mw.WriteField("prompt", "apply the annotation")
		for name, b := range map[string][]byte{"image": pngBytes, "markup": markupPNG} {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="`+name+`"; filename="`+name+`.png"`)
			h.Set("Content-Type", "image/png")
			part, _ := mw.CreatePart(h)
			part.Write(b)
		}
		mw.Close()
		code, _, raw := postImages(t, s, "/v1/images/edits", mw.FormDataContentType(), buf.String())
		if code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		sent := e.got("/v1/images/edits")
		if len(sent) != 1 {
			t.Fatalf("vendor was sent %v", sent)
		}
		got, ok := partNamed(t, e.ctOf("/v1/images/edits"), sent[0], "markup")
		if !ok || got != string(markupPNG) {
			t.Fatalf("the form's markup part never reached the vendor whole (part %v, %q)", ok, got)
		}
		if img, _ := partNamed(t, e.ctOf("/v1/images/edits"), sent[0], "image"); img != string(pngBytes) {
			t.Errorf("the source image went out as %q", img)
		}
	})
}

// a markup that can't be read is the caller's 400, before anything is
// drawn; a 200 that left the layer out can't be told from one that used
// it. OpenAI's edit takes a markup or a mask, never both.
func TestEditWithAMarkupItCantSendIsRefused(t *testing.T) {
	for name, extra := range map[string]string{
		"not base64":    `"markup":{"image_url":"data:image/png;base64,%%%"}`,
		"beside a mask": `"markup":{"image_url":"` + markupURL() + `"},"mask":{"image_url":"` + markupURL() + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, e := easeled(t)
			code, _, raw := postImages(t, s, "/v1/images/edits", "application/json",
				`{"model":"art/gpt-image-1","prompt":"x","images":[{"image_url":"`+sourceURL()+`"}],`+extra+`}`)
			if code != 400 {
				t.Fatalf("%s: %d, want 400 (%s)", name, code, raw)
			}
			if sent := e.got("/v1/images/edits"); len(sent) != 0 {
				t.Fatalf("%s: the vendor was asked anyway: %v", name, sent)
			}
		})
	}
}

// an images API that takes a mask nowhere takes no markup either: the
// caller hears so rather than an edit that left the layer out.
func TestMarkupRefusedWhereNoLayerIsTaken(t *testing.T) {
	mk := picture{Mime: "image/png", Data: markupPNG}
	d := drawing{Prompt: "apply the annotation", N: 1, Images: []picture{{Mime: "image/png", Data: pngBytes}}, Markup: &mk}
	if _, err := arkBody("doubao-seedream-4-0", d); err == nil || !strings.Contains(err.Error(), "markup") {
		t.Errorf("Ark: %v, want a refusal naming the markup", err)
	}
	if _, err := dashScopeBody("qwen-image-edit", d); err == nil || !strings.Contains(err.Error(), "markup") {
		t.Errorf("DashScope: %v, want a refusal naming the markup", err)
	}
	s := New()
	p := provider.Provider{ID: "or", Name: "OpenRouter", Chat: "http://127.0.0.1:1/api/v1", Key: "key"}
	if _, code, err := s.drawOpenRouterImages(context.Background(), p, "openai/gpt-image-1", d); code != 400 || err == nil || !strings.Contains(err.Error(), "markup") {
		t.Errorf("OpenRouter: %d %v, want 400 naming the markup", code, err)
	}
}
