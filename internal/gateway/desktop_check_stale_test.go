package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Desktop's check that the gateway answers goes as asked when it
// asks for the model chosen in Desktop's own menu, and is never put on a
// model magpie can't serve now.
//
//   - CyberCaius (Discord, 11-20): MiniMax in the menu, the check asked
//     for MiniMax, and magpie sent it to zai-glm-5-3-flash on Mistral, the
//     model a turn had run on after the menu was set, which went before
//     the menu's choice.
//   - #6719: claude-desktop.model named a routing group since deleted; any
//     group/ id counted as served, so the check went there and was a 404
//     that named the model the client asked for ("magpie knows no model X;
//     did you mean X?").
//   - SivanCola on #6719: a model its provider no longer lists
//     (opencode-go/deepseek-flash), taken because the provider is on;
//     OpenCode Go answered 400.
//
// A title, which goes to the session's model, doesn't go to a gone one
// either.
func TestClaudeDesktopCheckStaysOnServedChoice(t *testing.T) {
	claude := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	mm := &fake{t: t, reply: deepseekReply}
	mistral := &fake{t: t, reply: deepseekReply}
	ogo := &fake{t: t, reply: deepseekReply}
	up := setup(t, provider.Anthropic, claude)
	var urls []string
	for _, f := range []*fake{mm, mistral, ogo} {
		s := httptest.NewServer(f)
		t.Cleanup(s.Close)
		urls = append(urls, s.URL+"/v1")
	}
	for _, p := range []provider.Provider{
		{ID: "leee", Name: "leee", Key: "k", Anthropic: up.URL, Models: []string{"claude-haiku-5-5"}},
		{ID: "minimax-cn", Name: "MiniMax", Key: "k", Chat: urls[0], Models: []string{"MiniMax-M3.1-Flash"}},
		{ID: "mistral", Name: "Mistral", Key: "k", Chat: urls[1], Models: []string{"zai-glm-5-3-flash"}},
		{ID: "opencode-go", Name: "OpenCode Go", Key: "k", Chat: urls[2], Models: []string{"glm-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "live", Name: "live", Members: []string{"minimax-cn/MiniMax-M3.1-Flash"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	// leee's haiku first: Desktop's default
	if err := provider.SetModelOrder("claude-desktop", []string{"leee/claude-haiku-5-5", "minimax-cn/MiniMax-M3.1-Flash", "mistral/zai-glm-5-3-flash"}); err != nil {
		t.Fatal(err)
	}
	idOf := func(id string) string {
		t.Helper()
		for _, e := range provider.Catalog() {
			if e.ID == id {
				return DesktopID(e)
			}
		}
		t.Fatalf("%s not in the catalog", id)
		return ""
	}
	haiku, mmID, glmID := idOf("leee/claude-haiku-5-5"), idOf("minimax-cn/MiniMax-M3.1-Flash"), idOf("mistral/zai-glm-5-3-flash")

	root := t.TempDir()
	old := desktopSelectorRoots
	desktopSelectorRoots = func() []string { return []string{root} }
	t.Cleanup(func() { desktopSelectorRoots = old })
	dir := filepath.Join(root, "local-agent-mode-sessions", "c774b2e6", "00000000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hour := time.Now().Add(-time.Hour).Truncate(time.Second)
	file := filepath.Join(dir, "cowork_account_settings.json")
	choose := func(state string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(`{"__model_selector_state":`+state+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(file, hour, hour); err != nil {
			t.Fatal(err)
		}
	}
	// a turn sent a minute after the menus were written
	pick := func(m string) {
		desktopPicked.Lock()
		desktopPicked.model, desktopPicked.from, desktopPicked.at = m, desktopPickedPath(), hour.Add(time.Minute)
		desktopPicked.Unlock()
	}
	t.Cleanup(func() { pick("") })
	srv := New()
	send := func(body string) (int, string) {
		t.Helper()
		claude.got, mm.got, mistral.got, ogo.got = nil, nil, nil, nil
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", Token+"-claude-desktop")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Claude/2.7032.0 Chrome/140.0 Electron/38.0")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	reached := func(why string, want *fake, wantModel string) {
		t.Helper()
		for _, f := range []*fake{claude, mm, mistral, ogo} {
			if f != want && f.got != nil {
				t.Fatalf("%s: went to the wrong provider (%s asked for)", why, modelOf(f.got))
			}
		}
		if want.got == nil {
			t.Fatalf("%s: never reached the provider it should", why)
		}
		if wantModel != "" && modelOf(want.got) != wantModel {
			t.Fatalf("%s: asked for %q, want %q", why, modelOf(want.got), wantModel)
		}
	}
	check := func(why, asked string, want *fake, wantModel string) {
		t.Helper()
		if code, body := send(`{"model":"` + asked + `","max_tokens":1,"messages":[{"role":"user","content":"."}]}`); code != 200 {
			t.Fatalf("%s: check %d %s", why, code, body)
		}
		reached(why, want, wantModel)
	}

	// CyberCaius: MiniMax in the menu, a turn on Mistral's GLM since; the
	// check asks for MiniMax and is answered there
	choose(`{"code":{"model":"` + mmID + `"}}`)
	pick("mistral/zai-glm-5-3-flash")
	check("asked for the menu's model, a turn elsewhere since", mmID, mm, "MiniMax-M3.1-Flash")
	// a check for another model still goes to the turn's, newer than the
	// menu (as before)
	check("asked for the default haiku", haiku, mistral, "zai-glm-5-3-flash")
	pick("")
	// with no turn since, the Code tab's choice comes first, though the
	// check asks for the Chat menu's (which holds Desktop's default until
	// the user changes it)
	choose(`{"code":{"model":"` + glmID + `"},"chat":{"model":"` + mmID + `"}}`)
	check("asked for the Chat menu's model", mmID, mistral, "zai-glm-5-3-flash")

	// SivanCola: a turn on a model its provider no longer lists
	choose(`{"code":{"model":"` + glmID + `"}}`)
	pick("opencode-go/deepseek-flash")
	check("newer turn on a model its provider no longer lists", haiku, mistral, "zai-glm-5-3-flash")
	// #6719: a turn on a routing group since deleted
	pick("group/gone")
	check("newer turn on a deleted group", haiku, mistral, "zai-glm-5-3-flash")
	pick("")
	// a menu choice that is a deleted group, or a model no longer listed:
	// the first listed, as with no choice
	for _, gone := range []string{"group/gone", "opencode-go/deepseek-flash"} {
		choose(`{"code":{"model":"` + gone + `"}}`)
		check("menu on "+gone, haiku, claude, "")
	}
	// a group that is there is a choice
	choose(`{"code":{"model":"group/live"}}`)
	check("menu on a live group", haiku, mm, "MiniMax-M3.1-Flash")

	// a title goes to the session's model, but not to a gone one: the
	// first listed then, as before any turn
	pick("mistral/zai-glm-5-3-flash")
	title := `{"model":"` + haiku + `","max_tokens":200,"stream":true,"system":"You write short session titles.","messages":[{"role":"user","content":"hi"}]}`
	if code, body := send(title); code != 200 {
		t.Fatalf("title: %d %s", code, body)
	}
	reached("title on the session's model", mistral, "zai-glm-5-3-flash")
	pick("group/gone")
	if code, body := send(title); code != 200 {
		t.Fatalf("title with the session's group deleted: %d %s", code, body)
	}
	reached("title with the session's group deleted", claude, "")
	pick("")
}

// A request magpie put on another model that it can't serve names that
// model in its 404, not the one the client asked for: #6719's "magpie
// knows no model X; did you mean X?" named a model magpie serves.
func TestClaudeDesktopMovedUnknownNamesTarget(t *testing.T) {
	claude := &fake{t: t, reply: deepseekReply}
	up := setup(t, provider.Anthropic, claude)
	if err := provider.Save(provider.Provider{ID: "leee", Name: "leee", Key: "k", Anthropic: up.URL, Models: []string{"claude-haiku-5-5"}}); err != nil {
		t.Fatal(err)
	}
	// the opus tier picked on a group since deleted
	if err := os.MkdirAll(filepath.Dir(desktopTiersPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktopTiersPath(), []byte(`{"opus":"group/gone"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := New()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5","max_tokens":1000,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", Token+"-claude-desktop")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Claude/2.7032.0 Chrome/140.0 Electron/38.0")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `magpie knows no model \"group/gone\"`) || !strings.Contains(body, `claude-opus-5-5`) {
		t.Fatalf("the 404 doesn't name the model it was put on and the one asked for: %s", body)
	}
	if claude.got != nil {
		t.Fatalf("sent on: %s", claude.got)
	}
}
