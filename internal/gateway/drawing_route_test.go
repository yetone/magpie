package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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

func drawingRelay(t *testing.T, id string, imageIDs ...string) *easel {
	t.Helper()
	e := &easel{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"resp-test","object":"response","status":"completed","model":"text","output":[{"type":"message","id":"msg-test","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		e.ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: id, Responses: up.URL + "/v1", Key: "key"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	models := []catalog.Model{{ID: "text"}}
	for _, model := range imageIDs {
		models = append(models, catalog.Model{ID: model, Draws: true})
	}
	if err := catalog.SaveLive(id, p.Responses, models); err != nil {
		t.Fatal(err)
	}
	return e
}

func drawingMainTurn(t *testing.T, s *Server, model, session, turn, kind, token string) {
	t.Helper()
	metadata, _ := json.Marshal(map[string]string{"turn_id": turn, "thread_source": "user"})
	body, _ := json.Marshal(map[string]any{"model": model, "input": "hi", "client_metadata": map[string]string{"x-codex-turn-metadata": string(metadata)}})
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", "codex")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("session-id", session)
	if kind == "luna_reserve" {
		r.Header.Set("x-openai-codex-luna-reserve", "1")
	} else if kind != "" {
		r.Header.Set("x-openai-subagent", kind)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("main turn: %d %s", w.Code, w.Body.String())
	}
}

func drawingToolCall(t *testing.T, s *Server, path, model, session, turn, token string) (int, imagesAnswer, string) {
	t.Helper()
	body := map[string]any{"model": model, "prompt": "a magpie"}
	if path == "/v1/images/edits" {
		body["image"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	}
	encoded, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", "codex")
	r.Header.Set("Authorization", "Bearer "+token)
	if session != "" {
		r.Header.Set("session-id", session)
	}
	if turn != "" {
		r.Header.Set("x-codex-image-turn-id", turn)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var answer imagesAnswer
	json.Unmarshal(w.Body.Bytes(), &answer)
	return w.Code, answer, w.Body.String()
}

func TestCodexDrawingFollowsItsTurnProvider(t *testing.T) {
	fresh(t)
	other := drawingRelay(t, "other", "gpt-image-2", "gpt-image-fallback")
	main := drawingRelay(t, "main", "gpt-image-2", "gpt-image-next")
	st := settings.Load()
	st.ImageGen = "other/gpt-image-fallback"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	drawingMainTurn(t, s, "main/text", "session-a", "turn-a", "", "magpie-codex")
	drawingMainTurn(t, s, "other/text", "session-b", "turn-b", "", "magpie-codex")
	drawingMainTurn(t, s, "other/text", "session-a", "turn-a", "review", "magpie-codex")
	for _, test := range []struct{ path, model string }{
		{"/v1/images/generations", "gpt-image-2"},
		{"/v1/images/edits", "gpt-image-2"},
		{"/v1/images/generations", "gpt-image-next"},
	} {
		code, answer, raw := drawingToolCall(t, s, test.path, test.model, "", "turn-a", "magpie-codex")
		if code != 200 || answer.Model != "main/"+test.model {
			t.Fatalf("%s %s: %d %s", test.path, test.model, code, raw)
		}
	}
	if len(other.got("/v1/images/generations")) != 0 || len(main.got("/v1/images/edits")) != 1 {
		t.Fatal("used another session or background request's provider")
	}
	code, answer, raw := drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "session-a", "unknown-turn", "magpie-codex")
	if code != 200 || answer.Model != st.ImageGen {
		t.Fatalf("unknown turn with known session: %d %s", code, raw)
	}
	code, answer, raw = drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "session-a", "", "magpie-codex")
	if code != 200 || answer.Model != "main/gpt-image-2" {
		t.Fatalf("session fallback: %d %s", code, raw)
	}
	drawingMainTurn(t, s, "other/text", "session-a", "turn-a-next", "", "magpie-codex")
	code, answer, raw = drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "", "turn-a-next", "magpie-codex")
	if code != 200 || answer.Model != "other/gpt-image-2" {
		t.Fatalf("changed main model: %d %s", code, raw)
	}
	st.ImageGen = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	code, answer, raw = drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "", "turn-a", "magpie-codex")
	if code != 200 || answer.Model != "main/gpt-image-2" {
		t.Fatalf("primary with fallback off: %d %s", code, raw)
	}
}

func TestCodexDrawingConcurrentTurns(t *testing.T) {
	fresh(t)
	drawingRelay(t, "first", "gpt-image-2")
	drawingRelay(t, "second", "gpt-image-2")
	configuration := settings.Load()
	configuration.ImageGen = "off"
	if err := settings.Save(configuration); err != nil {
		t.Fatal(err)
	}
	server := New()
	var turns sync.WaitGroup
	for index := range 12 {
		turns.Add(1)
		go func() {
			defer turns.Done()
			selected := []string{"first", "second"}[index%2]
			drawingMainTurn(t, server, selected+"/text", fmt.Sprintf("session-%d", index), fmt.Sprintf("turn-%d", index), "", "magpie-codex")
		}()
	}
	turns.Wait()
	for index := range 12 {
		turns.Add(1)
		go func() {
			defer turns.Done()
			selected := []string{"first", "second"}[index%2]
			code, answer, raw := drawingToolCall(t, server, "/v1/images/generations", "gpt-image-2", "", fmt.Sprintf("turn-%d", index), "magpie-codex")
			if code != 200 || answer.Model != selected+"/gpt-image-2" {
				t.Errorf("turn %d: %d %s", index, code, raw)
			}
		}()
	}
	turns.Wait()
}

func TestCodexDrawingFollowsLunaReserveTurn(t *testing.T) {
	fresh(t)
	drawingRelay(t, "main", "gpt-image-2")
	drawingRelay(t, "fallback", "gpt-image-fallback")
	configuration := settings.Load()
	configuration.ImageGen = "fallback/gpt-image-fallback"
	if err := settings.Save(configuration); err != nil {
		t.Fatal(err)
	}
	server := New()
	drawingMainTurn(t, server, "main/text", "session", "turn", "luna_reserve", "magpie-codex")
	code, answer, raw := drawingToolCall(t, server, "/v1/images/generations", "gpt-image-2", "", "turn", "magpie-codex")
	if code != 200 || answer.Model != "main/gpt-image-2" {
		t.Fatalf("reserve turn: %d %s", code, raw)
	}
}

func TestCodexDrawingFallsBackToImageSetting(t *testing.T) {
	fresh(t)
	drawingRelay(t, "unrelated", "gpt-image-2")
	drawingRelay(t, "fallback", "gpt-image-fallback")
	drawingRelay(t, "text-only")
	st := settings.Load()
	st.ImageGen = "fallback/gpt-image-fallback"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	drawingMainTurn(t, s, "text-only/text", "session", "turn", "", "magpie-codex")
	for _, test := range []struct{ model, session, turn, want string }{
		{"gpt-image-2", "", "turn", st.ImageGen},
		{"gpt-image-future", "", "turn", st.ImageGen},
		{"gpt-image-2", "", "unknown-turn", st.ImageGen},
		{"gpt-image-2", "", "", st.ImageGen},
		{"gpt-image-2", "session", "unknown-turn", st.ImageGen},
		{"unrelated/gpt-image-2", "", "turn", "unrelated/gpt-image-2"},
		{"", "", "turn", st.ImageGen},
	} {
		code, answer, raw := drawingToolCall(t, s, "/v1/images/generations", test.model, test.session, test.turn, "magpie-codex")
		if code != 200 || answer.Model != test.want {
			t.Fatalf("%+v: %d %s", test, code, raw)
		}
	}
	st.ImageGen = "off"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	code, _, raw := drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "", "turn", "magpie-codex")
	if code != 400 || !strings.Contains(raw, "Settings") {
		t.Fatalf("fallback off: %d %s", code, raw)
	}
	st.ImageGen = ""
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	code, answer, raw := drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "", "turn", "magpie-codex")
	if code != 200 || answer.Model != AutoDrawer() {
		t.Fatalf("automatic fallback: %d %s", code, raw)
	}
}

func TestCodexDrawingCallerIsolation(t *testing.T) {
	fresh(t)
	drawingRelay(t, "main", "gpt-image-2")
	drawingRelay(t, "fallback", "gpt-image-fallback")
	keys, secrets := newCaller(t, "First", "Second")
	st := settings.Load()
	st.ImageGen = "fallback/gpt-image-fallback"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	drawingMainTurn(t, s, "main/text", "session", "turn", "", secrets[0])
	if got := s.trace.routes[0].imageCaller; !strings.HasSuffix(got, keys[0].ID) {
		t.Fatalf("main caller scope = %q, want key %s (LAN=%v)", got, keys[0].ID, settings.Load().LAN)
	}
	for index, want := range []string{"main/gpt-image-2", st.ImageGen} {
		code, answer, raw := drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "", "turn", secrets[index])
		if code != 200 || answer.Model != want {
			t.Fatalf("caller %d: %d %s", index, code, raw)
		}
	}
}

func TestCodexDrawingDoesNotRetryThroughSetting(t *testing.T) {
	fresh(t)
	main := drawingRelay(t, "main", "gpt-image-2")
	fallback := drawingRelay(t, "fallback", "gpt-image-fallback")
	p, err := provider.Find("main")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "wrong"
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	st := settings.Load()
	st.ImageGen = "fallback/gpt-image-fallback"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	drawingMainTurn(t, s, "main/text", "session", "turn", "", "magpie-codex")
	code, _, raw := drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "", "turn", "magpie-codex")
	if code != 401 || len(main.got("/v1/images/generations")) != 1 || len(fallback.got("/v1/images/generations")) != 0 {
		t.Fatalf("image failure retried: %d %s", code, raw)
	}
}

func TestCodexDrawingFollowsActualGroupMember(t *testing.T) {
	fresh(t)
	first := drawingRelay(t, "first", "gpt-image-2")
	second := drawingRelay(t, "second", "gpt-image-2")
	failed := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/responses" {
			writer.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(writer, `{"error":{"message":"busy"}}`)
			return
		}
		first.ServeHTTP(writer, request)
	}))
	defer failed.Close()
	firstProvider, err := provider.Find("first")
	if err != nil {
		t.Fatal(err)
	}
	firstProvider.Responses = failed.URL + "/v1"
	if err := provider.Save(*firstProvider); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "drawing", Members: []string{"first/text", "second/text"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	st := settings.Load()
	st.ImageGen = "first/gpt-image-2"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	server := New()
	drawingMainTurn(t, server, "group/drawing", "session", "turn", "", "magpie-codex")
	code, answer, raw := drawingToolCall(t, server, "/v1/images/generations", "gpt-image-2", "", "turn", "magpie-codex")
	if code != 200 || answer.Model != "second/gpt-image-2" || len(first.got("/v1/images/generations")) != 0 || len(second.got("/v1/images/generations")) != 1 {
		t.Fatalf("group member: %d %s", code, raw)
	}
	secondProvider, err := provider.Find("second")
	if err != nil {
		t.Fatal(err)
	}
	secondProvider.Off = true
	if err := provider.Save(*secondProvider); err != nil {
		t.Fatal(err)
	}
	code, answer, raw = drawingToolCall(t, server, "/v1/images/generations", "gpt-image-2", "", "turn", "magpie-codex")
	if code != 200 || answer.Model != st.ImageGen {
		t.Fatalf("disabled preferred provider: %d %s", code, raw)
	}
}

func TestDrawingTurnMetadata(t *testing.T) {
	for _, id := range []string{strings.Repeat("x", 129), "turn\nother", "turn\tother"} {
		if got := drawingTurnID(id); got != "" {
			t.Fatalf("accepted invalid turn ID %q", got)
		}
	}
	if got := drawingTurnID(" turn "); got != "turn" {
		t.Fatalf("turn ID = %q", got)
	}
	for _, body := range []string{
		`{"client_metadata":{"x-codex-turn-metadata":"{\"turn_id\":\"turn\"}"}}`,
		`{"client_metadata":{"x-codex-turn-metadata":{"turn_id":"turn"}}}`,
		`{"client_metadata":{"turn_id":"turn"}}`,
	} {
		if metadata := requestSessionMetadata(http.Header{}, []byte(body)); metadata.Turn != "turn" {
			t.Fatalf("%s: %+v", body, metadata)
		}
	}
	header := http.Header{}
	header.Set("x-codex-turn-metadata", `{"turn_id":"turn"}`)
	if metadata := requestSessionMetadata(header, nil); metadata.Turn != "turn" {
		t.Fatal(metadata)
	}
	header.Set("x-codex-turn-metadata", `{"turn_id":"header-turn","thread_source":"thread_title","parent_thread_id":"parent"}`)
	metadata := requestSessionMetadata(header, []byte(`{"client_metadata":{"turn_id":"body-turn"}}`))
	if metadata.Turn != "body-turn" || metadata.Source != "thread_title" || metadata.Parent != "parent" {
		t.Fatalf("projected turn lost header metadata: %+v", metadata)
	}
	encoded, err := json.Marshal(Route{imageTurn: "private-turn", imageCaller: "private-caller", imageProvider: "private-provider"})
	if err != nil || strings.Contains(string(encoded), "private-") {
		t.Fatalf("private routing fields exposed: %s %v", encoded, err)
	}
}

// changqiu4890 on Discord: Codex set to a routing group of chat models
// and one image model, a relay's gpt-image-2.5, while Settings' pick and
// another relay have GPT Image models of their own. Codex's image tool
// draws on the group's image model, on its relay, whichever member served
// the turn: never on Settings' pick or the provider the chat went to.
func TestCodexDrawingTakesItsGroupsImageMember(t *testing.T) {
	fresh(t)
	chat := drawingRelay(t, "chat", "gpt-image-2")
	cpa := drawingRelay(t, "cpa", "gpt-image-2.5")
	other := drawingRelay(t, "other", "gpt-image-2", "gpt-image-2.5")
	if err := provider.SaveGroup(provider.Group{ID: "smart-split", Name: "smart split", Members: []string{"chat/text", "cpa/gpt-image-2.5"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	st := settings.Load()
	st.ImageGen = "other/gpt-image-2"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	drawingMainTurn(t, s, "group/smart-split", "session", "turn", "", "magpie-codex")
	code, answer, raw := drawingToolCall(t, s, "/v1/images/generations", "gpt-image-2", "", "turn", "magpie-codex")
	if code != 200 || answer.Model != "cpa/gpt-image-2.5" {
		t.Fatalf("drew on %d %s", code, raw)
	}
	if len(chat.got("/v1/images/generations")) != 0 || len(other.got("/v1/images/generations")) != 0 || len(cpa.got("/v1/images/generations")) != 1 {
		t.Fatalf("asked chat %v, other %v, cpa %v", chat.got("/v1/images/generations"), other.got("/v1/images/generations"), cpa.got("/v1/images/generations"))
	}
}
