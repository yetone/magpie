package gateway

import (
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// unknownEyed is eyed with the text model's list saying nothing of images,
// as most vendors' /models lists don't: magpie counts it text-only (agents
// were told so before Vision told them every model takes images).
func unknownEyed(t *testing.T) (*Server, *eyeUp) {
	t.Helper()
	s, u := eyed(t)
	p, err := provider.Find("probe")
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", p.Chat, []catalog.Model{
		{ID: "text"},
		{ID: "eye", Images: true, ImageInput: imageInputBool(true)},
	}); err != nil {
		t.Fatal(err)
	}
	return s, u
}

func TestModelNotKnownToSeeIsGivenTheDescription(t *testing.T) {
	s, u := unknownEyed(t)
	cases := []struct{ path, body string }{
		{"/v1/responses", `{"model":"probe/text","input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}]}`},
		{CodexPath + "/responses", `{"model":"probe/text","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`},
		{"/v1/chat/completions", `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
		if rec.Code != 200 {
			t.Errorf("%s: %d %s", tc.path, rec.Code, rec.Body.String())
		}
	}
	got := u.sent("text")
	if len(got) != len(cases) {
		t.Fatalf("text model sent %d requests, want %d", len(got), len(cases))
	}
	for i, b := range got {
		if !strings.Contains(b, "HELLO") || strings.Contains(b, "aGVsbG8=") || strings.Contains(b, "example.com/a.png") {
			t.Errorf("%s: text model was sent %s", cases[i].path, b)
		}
	}
	if n := len(u.sent("eye")); n != 2 {
		t.Fatalf("the describer was asked %d times, want 2 (two images)", n)
	}
}

// A Codex function_call_output's image, an older turn's and a routing
// group's member whose list says nothing of images are described too.
func TestModelNotKnownToSeeToolAndGroupImages(t *testing.T) {
	s, u := unknownEyed(t)
	body := `{"model":"probe/text","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"open a.png"}]},{"type":"function_call","call_id":"call_1","name":"view_image","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"a.png"},{"type":"input_image","image_url":"data:image/png;base64,dG9vbA=="}]}]}`
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("tool image: %d %s", rec.Code, rec.Body.String())
	}
	if got := u.sent("text"); len(got) != 1 || !strings.Contains(got[0], "HELLO") || strings.Contains(got[0], "dG9vbA==") {
		t.Fatalf("tool image: text model was sent %v", got)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mixed", Members: []string{"probe/text", "probe/eye"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	code, out := postAs(t, s, "", `{"model":"group/mixed","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,Z3JvdXA="}}]}]}`)
	if code != 200 {
		t.Fatalf("group: %d %s", code, out)
	}
	if got := u.sent("text"); len(got) != 2 || !strings.Contains(got[1], "HELLO") || strings.Contains(got[1], "Z3JvdXA=") {
		t.Fatalf("group: text member was sent %v", got)
	}
}

// The description is in the Routing view as a call of its own, on the
// model that described, for the request and in its session.
func TestDescriptionIsInRouting(t *testing.T) {
	s, _ := eyed(t)
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"probe/text","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}]}`))
	req.Header.Set("session_id", "s-1")
	req.Header.Set("User-Agent", "codex_cli_rs/0.50.0")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var vision, turn *Route
	for _, r := range s.trace.routes {
		switch r.Model {
		case "probe/eye":
			vision = r
		case "probe/text":
			turn = r
		}
	}
	if vision == nil || turn == nil {
		t.Fatalf("routes: vision %v, turn %v", vision, turn)
	}
	if vision.Kind != "vision" || vision.For == nil || vision.For.Model != "probe/text" || vision.For.Agent != turn.Agent || vision.Session != turn.Session || turn.Session == "" {
		t.Fatalf("vision route: kind %q for %+v session %q; turn agent %q session %q", vision.Kind, vision.For, vision.Session, turn.Agent, turn.Session)
	}
}

// The description's row says why the model was counted as unable to see:
// nothing magpie knows says it sees (Unknown), or its list says it takes
// text only — a DeepSeek model's images went to Codex's GPT with nothing
// saying which, or where to change it (#1287). A group's text-only member
// says it the same way, on every route an agent sends an image by.
func TestDescriptionSaysWhyTheModelCantSee(t *testing.T) {
	s, _ := unknownEyed(t)
	p, _ := provider.Find("probe")
	if err := catalog.SaveLive("probe", p.Chat, []catalog.Model{
		{ID: "text"},
		{ID: "flat", ImageInput: imageInputBool(false)},
		{ID: "eye", Images: true, ImageInput: imageInputBool(true)},
	}); err != nil {
		t.Fatal(err)
	}
	p.Models = append(p.Models, "flat")
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{
		{Name: "Unknown", Members: []string{"probe/text", "probe/eye"}, Routing: provider.Ordered},
		{Name: "Flat", Members: []string{"probe/flat", "probe/eye"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	img := 0
	src := func() string {
		img++
		return fmt.Sprintf("data:image/png;base64,%s", base64.StdEncoding.EncodeToString([]byte(fmt.Sprint("img", img))))
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
			u := src()
			_, data, _ := strings.Cut(u, ",")
			return "/v1/messages", `{"model":"` + m + `","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}]}]}`
		},
	}
	for _, tc := range []struct {
		model   string
		unknown bool
	}{{"probe/text", true}, {"probe/flat", false}, {"group/unknown", true}, {"group/flat", false}} {
		for name, body := range bodies {
			path, b := body(tc.model)
			before := len(s.trace.routes)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(b)))
			if rec.Code != 200 {
				t.Fatalf("%s %s: %d %s", tc.model, name, rec.Code, rec.Body.String())
			}
			var vision *Route
			for _, r := range s.trace.routes[before:] {
				if r.Kind == "vision" {
					vision = r
				}
			}
			if vision == nil || vision.For == nil {
				t.Fatalf("%s %s: no description's row", tc.model, name)
			}
			if vision.For.Model != tc.model || vision.For.Unknown != tc.unknown {
				t.Errorf("%s %s: the description's row is for %+v, want unknown %v", tc.model, name, *vision.For, tc.unknown)
			}
		}
	}
}
