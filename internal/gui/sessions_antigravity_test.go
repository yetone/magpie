package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

// @yansheng1003 on X: an Antigravity 2.0 conversation showed only its
// title and preview. The Sessions list marks it as one whose conversation
// can be shown, and /api/sessions/transcript, which Show conversation
// asks, gives what was said: the user's turn, the replies, the tool call
// and its result, read from the transcript the app's 2.30 language server
// wrote (sessions' testdata/antigravity-2.30-tools).
func TestSessionAntigravityConversation(t *testing.T) {
	h := sandboxHome(t)
	copyTree(t, "../sessions/testdata/antigravity-2.30-tools", filepath.Join(h, ".gemini", "antigravity"))
	if err := settings.SetSessionReads(func(*settings.SessionReads) *settings.SessionReads {
		return &settings.SessionReads{Asked: true, All: true}
	}); err != nil {
		t.Fatal(err)
	}
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	const id = "a9b3e500-3594-49ed-b821-3418225e1990"
	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	get := func(path string) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	var list struct {
		Sessions []struct {
			Agent, ID  string
			Transcript bool
		}
	}
	if err := json.Unmarshal(get("/api/sessions"), &list); err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, s := range list.Sessions {
		if s.Agent == "antigravity" && s.ID == id {
			listed = true
			if !s.Transcript {
				t.Fatalf("listed with no conversation to show: %+v", s)
			}
		}
	}
	if !listed {
		t.Fatalf("not listed: %+v", list.Sessions)
	}
	var tr sessions.Transcript
	if err := json.Unmarshal(get("/api/sessions/transcript?agent=antigravity&id="+id), &tr); err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, p := range tr.Parts {
		said = append(said, p.Role+"/"+p.Kind+"/"+p.Name+": "+p.Text)
	}
	want := []string{
		"user/text/: read a.txt",
		"assistant/text/: Let me read it.",
		`assistant/tool_use/read_file: {"Path":"C:\\Users\\yeton\\agyss\\e2e4\\home\\proj\\a.txt"}`,
		"tool/tool_result/: Showing lines 1-1 of 1\n1: hi",
		"assistant/text/: a.txt says hi.",
	}
	if strings.Join(said, "\n|") != strings.Join(want, "\n|") {
		t.Fatalf("transcript\n%s\nwant\n%s", strings.Join(said, "\n|"), strings.Join(want, "\n|"))
	}
}
