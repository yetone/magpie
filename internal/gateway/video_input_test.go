package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// the start of an MP4 (its ftyp box), as base64
const videoData = "AAAAIGZ0eXBpc29tAAACAGlzb21pc28yYXZjMW1wNDE="

// The reporter's request (#6135): MiniMax's Messages video block beside
// the words, sent to the MiniMax plugin as one JSON body.
const reporterVideoBody = `{"model":"m1","max_tokens":256,"messages":[{"role":"user","content":[{"type":"video","source":{"type":"base64","media_type":"video/mp4","data":"` + videoData + `"}},{"type":"text","text":"用一句话描述这段视频画面。"}]}]}`

// A Messages video block reaches a plugin, which is always sent a
// translated request (Account.Stream): before, the block had no case in
// the parser and was left out, so MiniMax answered 200 for the words
// alone and the model said it saw no video (#6135).
func TestPluginGetsTheVideoBlock(t *testing.T) {
	f, up := pluginFake(t)
	code, msg, _, asked := pluginTranslate(t, f, up, reporterVideoBody)
	if code != 200 {
		t.Fatalf("%d: %s", code, msg)
	}
	var wire struct {
		Messages []struct {
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source *struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(asked, &wire); err != nil || len(wire.Messages) != 1 {
		t.Fatalf("asked %s (%v)", asked, err)
	}
	c := wire.Messages[0].Content
	if len(c) != 2 || c[0].Type != "video" || c[0].Source == nil || c[0].Source.Type != "base64" ||
		c[0].Source.MediaType != "video/mp4" || c[0].Source.Data != videoData ||
		c[1].Type != "text" || c[1].Text != "用一句话描述这段视频画面。" {
		t.Fatalf("the video didn't reach the plugin as the client gave it: %s", asked)
	}
}

type videoUpstream struct {
	mu     sync.Mutex
	bodies []string
	models []string
}

func (v *videoUpstream) sent() ([]string, []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.bodies), slices.Clone(v.models)
}

func (v *videoUpstream) reset() {
	v.mu.Lock()
	v.bodies, v.models = nil, nil
	v.mu.Unlock()
}

// videoUp is an upstream that answers each API in its own shape.
func videoUp(t *testing.T) (*videoUpstream, *httptest.Server) {
	v := &videoUpstream{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct{ Model string }
		json.Unmarshal(b, &req)
		v.mu.Lock()
		v.bodies, v.models = append(v.bodies, string(b)), append(v.models, req.Model)
		v.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case strings.Contains(strings.ToLower(r.URL.Path), "generatecontent"):
			io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1}}\n\n")
		case strings.Contains(r.URL.Path, "messages"):
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"v\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		default:
			io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\n"+
				"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
		}
	}))
	t.Cleanup(up.Close)
	return v, up
}

var videoKinds = []string{"text", "image", "video"}

func saveVideoProvider(t *testing.T, p provider.Provider, url string) {
	t.Helper()
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(p.ID, url, []catalog.Model{
		{ID: "v", Images: true, ImageInput: imageInputBool(true), InputModalities: videoKinds, OutputModalities: []string{"text"}},
		{ID: "t", Images: true, ImageInput: imageInputBool(true), InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"}},
		{ID: "u"},
	}); err != nil {
		t.Fatal(err)
	}
}

func serveVideo(s *Server, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec
}

// A video reaches the upstream on every API that carries content blocks,
// in that API's own shape: Chat's video_url, a Chat file of a video and a
// Responses input_file of one go to a Messages upstream as a video block,
// and a Messages video block goes to a Chat upstream as video_url
// (#6135, #4101). Where it can't be carried, the client is told.
func TestVideoReachesEveryAPI(t *testing.T) {
	fresh(t)
	noVision(t)
	anth, anthUp := videoUp(t)
	saveVideoProvider(t, provider.Provider{ID: "anth", Name: "Anth", Anthropic: anthUp.URL, Key: "k", Models: []string{"v", "t", "u"}}, anthUp.URL)
	chat, chatUp := videoUp(t)
	saveVideoProvider(t, provider.Provider{ID: "chat", Name: "Chat", Chat: chatUp.URL + "/v1", Key: "k", Models: []string{"v", "t", "u"}}, chatUp.URL+"/v1")
	resp, respUp := videoUp(t)
	saveVideoProvider(t, provider.Provider{ID: "resp", Name: "Resp", Responses: respUp.URL + "/v1", Key: "k", Models: []string{"v", "t", "u"}}, respUp.URL+"/v1")
	s := New()

	url := "data:video/mp4;base64," + videoData
	for name, body := range map[string]string{
		"chat video_url":   `{"model":"anth/v","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"` + url + `"}},{"type":"text","text":"用一句话描述这段视频画面。"}]}]}`,
		"chat file":        `{"model":"anth/v","messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"` + url + `","filename":"clip.mp4"}},{"type":"text","text":"用一句话描述这段视频画面。"}]}]}`,
		"chat AI SDK file": `{"model":"anth/v","messages":[{"role":"user","content":[{"type":"file","data":"` + videoData + `","mediaType":"video/mp4","filename":"clip.mp4"},{"type":"text","text":"用一句话描述这段视频画面。"}]}]}`,
		// #4101's own shape: the AI SDK's file with a data: URL
		"chat AI SDK file, data URL": `{"model":"anth/v","messages":[{"role":"user","content":[{"type":"text","text":"用一句话描述这段视频的画面内容。"},{"type":"file","mediaType":"video/mp4","data":"` + url + `"}]}]}`,
	} {
		anth.reset()
		rec := serveVideo(s, "/v1/chat/completions", body)
		bodies, _ := anth.sent()
		if rec.Code != 200 || len(bodies) != 1 || !strings.Contains(bodies[0], `{"type":"video","source":{"type":"base64","media_type":"video/mp4","data":"`+videoData+`"}`) {
			t.Errorf("%s to Messages: %d %s; upstream %v", name, rec.Code, rec.Body.String(), bodies)
		}
	}
	anth.reset()
	rec := serveVideo(s, "/v1/responses", `{"model":"anth/v","input":[{"role":"user","content":[{"type":"input_file","file_data":"`+url+`","filename":"clip.mp4"},{"type":"input_text","text":"用一句话描述这段视频画面。"}]}]}`)
	if bodies, _ := anth.sent(); rec.Code != 200 || len(bodies) != 1 || !strings.Contains(bodies[0], `"type":"video"`) || !strings.Contains(bodies[0], videoData) {
		t.Errorf("Responses input_file to Messages: %d %s; upstream %v", rec.Code, rec.Body.String(), bodies)
	}

	messages := strings.Replace(reporterVideoBody, `"m1"`, `"chat/v"`, 1)
	rec = serveVideo(s, "/v1/messages", messages)
	if bodies, _ := chat.sent(); rec.Code != 200 || len(bodies) != 1 || !strings.Contains(bodies[0], `{"type":"video_url","video_url":{"url":"`+url+`"}}`) {
		t.Errorf("Messages video to Chat: %d %s; upstream %v", rec.Code, rec.Body.String(), bodies)
	}

	// #4101's: Chat's video_url to Gemini's API, as inlineData
	gem, gemUp := videoUp(t)
	saveVideoProvider(t, provider.Provider{ID: "gem", Name: "Gem", Gemini: gemUp.URL + "/v1beta", Key: "k", Models: []string{"v", "t", "u"}}, gemUp.URL+"/v1beta")
	rec = serveVideo(s, "/v1/chat/completions", `{"model":"gem/v","messages":[{"role":"user","content":[{"type":"text","text":"用一句话描述这段视频的画面内容。"},{"type":"video_url","video_url":{"url":"`+url+`"}}]}]}`)
	if bodies, _ := gem.sent(); rec.Code != 200 || len(bodies) != 1 || !strings.Contains(bodies[0], `"mimeType":"video/mp4"`) || !strings.Contains(bodies[0], videoData) {
		t.Errorf("Chat video_url to Gemini: %d %s; upstream %v", rec.Code, rec.Body.String(), bodies)
	}

	// Responses has no way to carry one
	rec = serveVideo(s, "/v1/messages", strings.Replace(reporterVideoBody, `"m1"`, `"resp/v"`, 1))
	if bodies, _ := resp.sent(); rec.Code != 400 || !strings.Contains(rec.Body.String(), "Responses API has no way to carry") || len(bodies) != 0 {
		t.Errorf("Messages video to Responses: %d %s; upstream %v", rec.Code, rec.Body.String(), bodies)
	}

	// relayed as it came, on the client's own API
	anth.reset()
	rec = serveVideo(s, "/v1/messages", strings.Replace(reporterVideoBody, `"m1"`, `"anth/v"`, 1))
	if bodies, _ := anth.sent(); rec.Code != 200 || len(bodies) != 1 || !strings.Contains(bodies[0], `"type":"video"`) || !strings.Contains(bodies[0], videoData) {
		t.Errorf("Messages video to Messages: %d %s; upstream %v", rec.Code, rec.Body.String(), bodies)
	}
}

// A video for a model whose list says it takes none is turned away with
// why, not sent to answer the words alone; a model its list says nothing
// of is sent it, and a video of an earlier turn is told of in words to a
// model that takes none (#6135).
func TestVideoForAModelThatTakesNone(t *testing.T) {
	fresh(t)
	noVision(t)
	anth, anthUp := videoUp(t)
	saveVideoProvider(t, provider.Provider{ID: "anth", Name: "Anth", Anthropic: anthUp.URL, Key: "k", Models: []string{"v", "t", "u"}}, anthUp.URL)
	chat, chatUp := videoUp(t)
	saveVideoProvider(t, provider.Provider{ID: "chat", Name: "Chat", Chat: chatUp.URL + "/v1", Key: "k", Models: []string{"v", "t", "u"}}, chatUp.URL+"/v1")
	s := New()

	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		body := strings.Replace(reporterVideoBody, `"m1"`, `"anth/t"`, 1)
		if path == "/v1/chat/completions" {
			body = `{"model":"anth/t","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"data:video/mp4;base64,` + videoData + `"}},{"type":"text","text":"用一句话描述这段视频画面。"}]}]}`
		}
		rec := serveVideo(s, path, body)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "does not support video input") {
			t.Errorf("%s video to a model that takes none: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if bodies, _ := anth.sent(); len(bodies) != 0 {
		t.Fatalf("a video for a model that takes none reached upstream: %v", bodies)
	}

	rec := serveVideo(s, "/v1/messages", strings.Replace(reporterVideoBody, `"m1"`, `"anth/u"`, 1))
	if bodies, _ := anth.sent(); rec.Code != 200 || len(bodies) != 1 || !strings.Contains(bodies[0], videoData) {
		t.Errorf("video to a model nothing is known of: %d %s; upstream %v", rec.Code, rec.Body.String(), bodies)
	}

	earlier := `{"model":"chat/t","max_tokens":256,"messages":[` +
		`{"role":"user","content":[{"type":"video","source":{"type":"base64","media_type":"video/mp4","data":"` + videoData + `"}},{"type":"text","text":"用一句话描述这段视频画面。"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"一只猫。"}]},` +
		`{"role":"user","content":[{"type":"text","text":"谢谢"}]}]}`
	rec = serveVideo(s, "/v1/messages", earlier)
	bodies, _ := chat.sent()
	if rec.Code != 200 || len(bodies) != 1 || strings.Contains(bodies[0], videoData) || strings.Contains(bodies[0], "video_url") ||
		!strings.Contains(bodies[0], "[attachment video/mp4]") {
		t.Errorf("an earlier video to a model that takes none: %d %s; upstream %v", rec.Code, rec.Body.String(), bodies)
	}
}

// A routing group says it takes video when a member does, and gives a
// video only to the members that take one (#2898, #6135).
func TestGroupVideoGoesToMembersThatTakeIt(t *testing.T) {
	fresh(t)
	noVision(t)
	anth, anthUp := videoUp(t)
	saveVideoProvider(t, provider.Provider{ID: "anth", Name: "Anth", Anthropic: anthUp.URL, Key: "k", Models: []string{"v", "t", "u"}}, anthUp.URL)
	if err := provider.SaveGroup(provider.Group{Name: "Mixed", Members: []string{"anth/t", "anth/v"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Blind", Members: []string{"anth/t"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var list struct {
		Data []struct {
			ID         string `json:"id"`
			Modalities *struct {
				Input []string `json:"input"`
			} `json:"modalities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	inputs := map[string][]string{}
	for _, m := range list.Data {
		if m.Modalities != nil {
			inputs[m.ID] = m.Modalities.Input
		}
	}
	if !slices.Contains(inputs["group/mixed"], "video") {
		t.Errorf("group/mixed takes %v, want video among them", inputs["group/mixed"])
	}
	if in := inputs["group/blind"]; slices.Contains(in, "video") || !slices.Contains(in, "image") {
		t.Errorf("group/blind takes %v, want image and no video", in)
	}

	rec = serveVideo(s, "/v1/messages", strings.Replace(reporterVideoBody, `"m1"`, `"group/mixed"`, 1))
	bodies, models := anth.sent()
	if rec.Code != 200 || len(bodies) != 1 || models[0] != "v" || !strings.Contains(bodies[0], videoData) {
		t.Errorf("group video: %d %s; upstream %v to %v", rec.Code, rec.Body.String(), bodies, models)
	}
	// a request with no video keeps the members' order
	anth.reset()
	rec = serveVideo(s, "/v1/messages", `{"model":"group/mixed","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if _, models := anth.sent(); rec.Code != 200 || len(models) != 1 || models[0] != "t" {
		t.Errorf("group words: %d %s; upstream to %v", rec.Code, rec.Body.String(), models)
	}

	anth.reset()
	rec = serveVideo(s, "/v1/messages", strings.Replace(reporterVideoBody, `"m1"`, `"group/blind"`, 1))
	if bodies, _ := anth.sent(); rec.Code != 400 || !strings.Contains(rec.Body.String(), "does not support video input") || len(bodies) != 0 {
		t.Errorf("group with no member that takes video: %d %s; upstream %v", rec.Code, rec.Body.String(), bodies)
	}
}
