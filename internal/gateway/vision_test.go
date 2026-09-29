package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// noVision turns describing off, for a test of images turned away.
func noVision(t *testing.T) {
	t.Helper()
	s := settings.Load()
	s.Vision = "off"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// eyeUp is an upstream with a text-only model and one that sees: the one
// that sees describes every image as a red square, or fails when fail is
// set; what each model is sent is kept.
type eyeUp struct {
	mu     sync.Mutex
	fail   bool
	bodies map[string][]string
}

func (u *eyeUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	model := modelOf(body)
	u.mu.Lock()
	if u.bodies == nil {
		u.bodies = map[string][]string{}
	}
	u.bodies[model] = append(u.bodies[model], string(body))
	fail := u.fail
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(model, "eye") && fail {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"eye is down"}}`)
		return
	}
	answer := "ok"
	if strings.HasSuffix(model, "eye") {
		answer = "A red square with the word HELLO in it."
	}
	if strings.Contains(string(body), `"stream":true`) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"`+answer+`"}}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"`+answer+`"},"finish_reason":"stop"}]}`)
}

func (u *eyeUp) sent(model string) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.bodies[model]...)
}

func eyed(t *testing.T) (*Server, *eyeUp) {
	t.Helper()
	fresh(t)
	u := &eyeUp{}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "probe", Name: "Probe", Chat: up.URL + "/v1", Key: "key", Models: []string{"text", "eye"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", up.URL+"/v1", []catalog.Model{
		{ID: "text", ImageInput: imageInputBool(false)},
		{ID: "eye", Images: true, ImageInput: imageInputBool(true)},
	}); err != nil {
		t.Fatal(err)
	}
	return New(), u
}

func TestTextOnlyModelIsGivenTheImagesDescription(t *testing.T) {
	s, u := eyed(t)
	cases := []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`},
		{"/v1/responses", `{"model":"probe/text","input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}]}`},
		{"/v1/messages", `{"model":"probe/text","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`},
		{"/v1beta/models/probe/text:generateContent", `{"contents":[{"role":"user","parts":[{"text":"read"},{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`},
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
		if !strings.Contains(b, "HELLO") || !strings.Contains(b, "probe/eye describes it") || strings.Contains(b, "aGVsbG8=") {
			t.Errorf("%s: text model was sent %s", cases[i].path, b)
		}
	}
	// the same image in four requests is described once
	if n := len(u.sent("eye")); n != 1 {
		t.Fatalf("the image was described %d times", n)
	}
	if !strings.Contains(u.sent("eye")[0], "aGVsbG8=") {
		t.Fatalf("the describer wasn't sent the image: %s", u.sent("eye")[0])
	}
}

func TestVisionSettingPicksTheDescriber(t *testing.T) {
	s, u := eyed(t)
	// a second model that sees, which the setting names
	up2 := &eyeUp{}
	srv := httptest.NewServer(up2)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Chat: srv.URL + "/v1", Key: "key", Models: []string{"bigeye"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("other", srv.URL+"/v1", []catalog.Model{{ID: "bigeye", Images: true, ImageInput: imageInputBool(true)}}); err != nil {
		t.Fatal(err)
	}
	st := settings.Load()
	st.Vision = "other/bigeye"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	code, body := postAs(t, s, "", `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"image_url","image_url":"https://example.com/a.png"}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if len(u.sent("eye")) != 0 || len(up2.sent("bigeye")) != 1 {
		t.Fatalf("eye asked %d times, bigeye %d", len(u.sent("eye")), len(up2.sent("bigeye")))
	}
	if !strings.Contains(u.sent("text")[0], "other/bigeye describes it") {
		t.Fatalf("text model was sent %s", u.sent("text")[0])
	}
}

func TestImageThatCantBeDescribed(t *testing.T) {
	s, u := eyed(t)
	u.fail = true
	code, body := postAs(t, s, "", `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`)
	if code != 502 || !strings.Contains(body, "couldn't describe the image") || len(u.sent("text")) != 0 {
		t.Fatalf("current image: %d %s; text model sent %d", code, body, len(u.sent("text")))
	}
	// an older turn's image that can't be described is left out
	code, body = postAs(t, s, "", `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,b2xk"}}]},{"role":"assistant","content":"seen"},{"role":"user","content":"and now?"}]}`)
	if code != 200 {
		t.Fatalf("older image: %d %s", code, body)
	}
	if got := u.sent("text"); len(got) != 1 || !strings.Contains(got[0], "couldn't be described") {
		t.Fatalf("text model was sent %v", got)
	}
	// a failure isn't kept
	u.mu.Lock()
	u.fail = false
	u.mu.Unlock()
	if code, body := postAs(t, s, "", `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`); code != 200 {
		t.Fatalf("after recovery: %d %s", code, body)
	}
}

func TestGroupsTextOnlyMemberIsGivenTheDescription(t *testing.T) {
	s, u := eyed(t)
	if err := provider.SaveGroup(provider.Group{Name: "Mixed", Members: []string{"probe/text", "probe/eye"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	code, body := postAs(t, s, "", `{"model":"group/mixed","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if got := u.sent("text"); len(got) != 1 || !strings.Contains(got[0], "HELLO") {
		t.Fatalf("the group's first member was sent %v", got)
	}
}

func TestDescriberThatCantSeeIsntAskedToDescribe(t *testing.T) {
	s, u := eyed(t)
	// the setting names a text-only model: its own request is turned away,
	// not described by itself again and again
	st := settings.Load()
	st.Vision = "probe/text"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	code, body := postAs(t, s, "", `{"model":"probe/text","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`)
	if code != 502 || len(u.sent("text")) != 0 {
		t.Fatalf("%d %s; text model sent %d", code, body, len(u.sent("text")))
	}
}

func TestSeerPicksAModelThatSees(t *testing.T) {
	eyed(t)
	if m, ok := seer(); !ok || m != "probe/eye" {
		t.Fatalf("seer = %q %v", m, ok)
	}
	noVision(t)
	if _, ok := seer(); ok {
		t.Fatal("seer with vision off")
	}
}
