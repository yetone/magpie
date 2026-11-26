package gateway

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// llxth1 on X: the Routing page showed what the last requests said, with no
// way to turn it off. With Show request content off magpie keeps none of it
// in memory: what was kept before goes at the next read, a request after it
// keeps no body in the trace or the Recent-calls ring, and every reader of
// them (the card's text, the reply, a call's Request and response) has
// nothing to give. The call itself, and its route, are still recorded.
func TestHiddenRequestTextIsNotKept(t *testing.T) {
	f := &fake{ctype: "application/json", reply: `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"the secret reply"}],"stop_reason":"end_turn","usage":{"input_tokens":30,"output_tokens":3}}`}
	setup(t, provider.Anthropic, f)
	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func(said string) int64 {
		t.Helper()
		rec := httptest.NewRecorder()
		body := `{"model":"fake/m1","max_tokens":10,"system":"You are the release agent.","messages":[{"role":"user","content":"` + said + `"}]}`
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "the secret reply") {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		rs := s.Trace(context.Background(), 0, 0).Routes
		return rs[len(rs)-1].ID
	}
	kept := func() (bodies int, ring bool) {
		s.trace.mu.Lock()
		bodies = len(s.trace.bodies)
		s.trace.mu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, c := range s.recent {
			if c.RequestBody != "" || c.ResponseBody != "" || c.maskIn != nil {
				ring = true
			}
		}
		return
	}

	first := ask("the first secret question")
	if _, ok := s.PromptText(first, PartChat, true); !ok {
		t.Fatal("shown: the text isn't kept")
	}
	if _, body, _, ok := s.ReplyBody(first); !ok || !strings.Contains(string(body), "the secret reply") {
		t.Fatalf("shown: reply %v %s", ok, body)
	}
	if c, ok := s.RouteCall(first); !ok || !strings.Contains(c.RequestBody, "the first secret question") {
		t.Fatalf("shown: call %v %+v", ok, c)
	}

	// turned off: what was kept goes at the next read
	if err := settings.Save(settings.Settings{HideRequestText: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.PromptText(first, PartChat, true); ok {
		t.Fatal("hidden: the text was given")
	}
	if n, ring := kept(); n != 0 || ring {
		t.Fatalf("hidden: still kept %d bodies, ring %v", n, ring)
	}

	// and a request after it keeps none
	second := ask("the second secret question")
	if n, ring := kept(); n != 0 || ring {
		t.Fatalf("hidden: a new request kept %d bodies, ring %v", n, ring)
	}
	if _, _, _, ok := s.ReplyBody(second); ok {
		t.Fatal("hidden: the reply was given")
	}
	c, ok := s.RouteCall(second)
	if !ok || c.RequestBody != "" || c.ResponseBody != "" || c.Model != "fake/m1" || c.Status != 200 {
		t.Fatalf("hidden: the call is recorded without its bodies: %v %+v", ok, c)
	}
	if c, ok := s.RecentCall(c.ID); !ok || c.RequestBody != "" || c.ResponseBody != "" {
		t.Fatalf("hidden: recent call %v %+v", ok, c)
	}
	if rs := s.Trace(context.Background(), 0, 0).Routes; len(rs) != 2 || rs[1].Prompt == nil {
		t.Fatalf("hidden: the route is still recorded with its sizes: %+v", rs)
	}

	// on again: a new request is kept, the hidden ones aren't back
	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	third := ask("the third question")
	if _, ok := s.PromptText(third, PartChat, true); !ok {
		t.Fatal("shown again: the text isn't kept")
	}
	if _, ok := s.PromptText(second, PartChat, true); ok {
		t.Fatal("shown again: a hidden request's text came back")
	}
}

// A settings file that can't be read hides the content: a switch the user
// turned off isn't taken for on.
func TestUnreadableSettingsHideRequestText(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if HidesRequestText() {
		t.Fatal("a new install hides request content")
	}
	if err := os.MkdirAll(filepath.Dir(settings.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(), []byte(`{"hideRequestText": tr`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HidesRequestText() {
		t.Fatal("an unreadable settings file shows request content")
	}
}

// The request archive is its own switch: with Show request content off a
// call is archived whole, as before.
func TestHiddenRequestTextStillArchived(t *testing.T) {
	fresh(t)
	serveOn(t, "fake", "k", []string{"m1"}, archiveVendor{})
	was := ArchiveBucket
	ArchiveBucket = func() (Putter, bool) { return nil, false }
	t.Cleanup(func() { ArchiveBucket = was })
	if err := settings.Save(settings.Settings{RequestArchiveLocal: true, HideRequestText: true}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := archivePost(t, s)
	c := s.Recent()[0]
	if c.RequestBody != "" || c.ResponseBody != "" {
		t.Fatalf("the ring kept bodies: %+v", c)
	}
	name := c.Time.UTC().Format("2006-01-02") + "/" + rec.Header().Get(ArchiveHeader)
	data, err := os.ReadFile(filepath.Join(settings.Dir(), "request-archive", filepath.FromSlash(name)+".json"))
	if err != nil || !strings.Contains(string(data), "deploy with [REDACTED:API_KEY]") || !strings.Contains(string(data), `\"content\":\"done\"`) {
		t.Fatalf("archive: %v %s", err, data)
	}
}
