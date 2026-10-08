package gateway

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// pickVision saves the Image recognition model the user picked.
func pickVision(t *testing.T, v string) {
	t.Helper()
	st := settings.Load()
	st.Vision = v
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
}

// An Image recognition model the user picked that magpie can't find any
// more (its provider removed) is not replaced in silence by the one magpie
// picks: the description's row in Routing names it, on every route an
// agent sends an image by and for a group's member, and a request turned
// away for want of any describer says it is missing.
func TestMissingImageRecognitionModelIsSaid(t *testing.T) {
	s, u := eyed(t)
	pickVision(t, "gone/bigeye")
	if m, ok := seer(); !ok || m != "probe/eye" {
		t.Fatalf("seer = %q %v, want the automatic probe/eye", m, ok)
	}
	if got := VisionMissing(); got != "gone/bigeye" {
		t.Fatalf("VisionMissing = %q", got)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mixed", Members: []string{"probe/text", "probe/eye"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	img := 0
	src := func() string {
		img++
		return fmt.Sprintf("data:image/png;base64,aW1n%03d=", img)
	}
	bodies := map[string]func(model string) (string, string){
		"responses": func(m string) (string, string) {
			return "/v1/responses", `{"model":"` + m + `","input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_image","image_url":"` + src() + `"}]}]}`
		},
		"codex": func(m string) (string, string) {
			return CodexPath + "/responses", `{"model":"` + m + `","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_image","image_url":"` + src() + `"}]}]}`
		},
		"chat": func(m string) (string, string) {
			return "/v1/chat/completions", `{"model":"` + m + `","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"` + src() + `"}}]}]}`
		},
		"anthropic": func(m string) (string, string) {
			data := strings.TrimPrefix(src(), "data:image/png;base64,")
			return "/v1/messages", `{"model":"` + m + `","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}]}]}`
		},
	}
	for _, model := range []string{"probe/text", "group/mixed"} {
		for name, body := range bodies {
			before := len(s.trace.routes)
			path, b := body(model)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(b)))
			if rec.Code != 200 {
				t.Fatalf("%s %s: %d %s", model, name, rec.Code, rec.Body.String())
			}
			var vision *Route
			for _, r := range s.trace.routes[before:] {
				if r.Kind == "vision" {
					vision = r
				}
			}
			if vision == nil || vision.Model != "probe/eye" || vision.For == nil {
				t.Fatalf("%s %s: no description by probe/eye in Routing: %+v", model, name, vision)
			}
			if vision.For.Missing != "gone/bigeye" {
				t.Errorf("%s %s: the description's row is for %+v, want it to name the missing gone/bigeye", model, name, *vision.For)
			}
		}
	}
	if n := len(u.sent("eye")); n != 8 {
		t.Fatalf("the automatic describer was asked %d times, want 8", n)
	}

	// a picked model that resolves is not missing
	pickVision(t, "probe/eye")
	if got := VisionMissing(); got != "" {
		t.Fatalf("VisionMissing with probe/eye = %q", got)
	}
	code, out := postAs(t, s, "", `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,c2Vlbg=="}}]}]}`)
	if code != 200 {
		t.Fatalf("resolving vision: %d %s", code, out)
	}
	if r := s.trace.routes[len(s.trace.routes)-2]; r.Kind != "vision" || r.For == nil || r.For.Missing != "" {
		t.Fatalf("resolving vision's row: kind %q for %+v", r.Kind, r.For)
	}

	// no model that sees to fall back on: turned away, naming the missing one
	pickVision(t, "gone/bigeye")
	p, _ := provider.Find("probe")
	if err := catalog.SaveLive("probe", p.Chat, []catalog.Model{
		{ID: "text", ImageInput: imageInputBool(false)},
		{ID: "eye", ImageInput: imageInputBool(false)},
	}); err != nil {
		t.Fatal(err)
	}
	if m, ok := seer(); ok {
		t.Fatalf("seer = %q with no model that sees", m)
	}
	for _, model := range []string{"probe/text", "group/mixed"} {
		code, out = postAs(t, s, "", `{"model":"`+model+`","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,bm9uZQ=="}}]}]}`)
		if code != 400 || !strings.Contains(out, "gone/bigeye") || !strings.Contains(out, "Image recognition") {
			t.Errorf("%s with no describer: %d %s, want 400 naming the missing gone/bigeye", model, code, out)
		}
	}
}

// Image generation's pick is the same: one magpie can't find is named, and
// a request that names no model, with nothing else to draw with, says so.
func TestMissingImageGenerationModelIsSaid(t *testing.T) {
	s, _ := eyed(t)
	st := settings.Load()
	st.ImageGen = "gone/gpt-image-2"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if got := DrawerMissing(); got != "gone/gpt-image-2" {
		t.Fatalf("DrawerMissing = %q", got)
	}
	code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"prompt":"a magpie"}`)
	if code != 400 || !strings.Contains(raw, "gone/gpt-image-2") || !strings.Contains(raw, "Image generation") {
		t.Fatalf("no drawer: %d %s, want 400 naming the missing gone/gpt-image-2", code, raw)
	}
	st.ImageGen = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if got := DrawerMissing(); got != "" {
		t.Fatalf("DrawerMissing with off = %q", got)
	}
}
