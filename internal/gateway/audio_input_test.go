package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Chat input_audio part reaches a Gemini upstream as inlineData with its
// format's MIME type. It was dropped on the way in, so the model answered
// as if no audio had been sent (Parlevent_ on X, Gemini on Vertex AI).
func TestChatAudioReachesGemini(t *testing.T) {
	fresh(t)
	noVision(t)
	gem, gemUp := videoUp(t)
	saveVideoProvider(t, provider.Provider{ID: "gem", Name: "Gem", Gemini: gemUp.URL + "/v1beta", Key: "k", Models: []string{"v", "t", "u"}}, gemUp.URL+"/v1beta")
	s := New()
	const audio = "UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA="
	for _, c := range []struct{ format, mime string }{{"wav", "audio/wav"}, {"mp3", "audio/mp3"}} {
		gem.reset()
		rec := serveVideo(s, "/v1/chat/completions", `{"model":"gem/u","messages":[{"role":"user","content":[{"type":"text","text":"这段音频说了什么？"},{"type":"input_audio","input_audio":{"data":"`+audio+`","format":"`+c.format+`"}}]}]}`)
		bodies, _ := gem.sent()
		if rec.Code != 200 || len(bodies) != 1 || !strings.Contains(bodies[0], `"mimeType":"`+c.mime+`"`) || !strings.Contains(bodies[0], audio) {
			t.Errorf("%s audio: %d %s; upstream %v", c.format, rec.Code, rec.Body.String(), bodies)
		}
	}
}

func TestAudioType(t *testing.T) {
	for in, want := range map[string]string{"": "audio/wav", "wav": "audio/wav", "MP3": "audio/mp3", "mpeg": "audio/mp3", "flac": "audio/flac", "audio/ogg": "audio/ogg"} {
		if got := audioType(in); got != want {
			t.Errorf("audioType(%q) = %q, want %q", in, got, want)
		}
	}
}

// toolAudioChat is a Chat conversation whose tool message returns a
// recording, as Parlevent_'s client sends one: text and input_audio parts.
func toolAudioChat(model, audio string) string {
	return `{"model":"` + model + `","messages":[` +
		`{"role":"user","content":"听一下录音"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"record","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"recorded"},{"type":"input_audio","input_audio":{"data":"` + audio + `","format":"wav"}}]}]}`
}

// A recording a tool returned in a Chat tool message reaches a Gemini
// upstream as inlineData after the functionResponse, as its images do.
// toolOutput read only text and images, so it was dropped (Parlevent_ on
// X, after TestChatAudioReachesGemini's fix for a user's audio).
func TestChatToolAudioReachesGemini(t *testing.T) {
	fresh(t)
	noVision(t)
	gem, gemUp := videoUp(t)
	saveVideoProvider(t, provider.Provider{ID: "gem", Name: "Gem", Gemini: gemUp.URL + "/v1beta", Key: "k", Models: []string{"v", "t", "u"}}, gemUp.URL+"/v1beta")
	s := New()
	const audio = "UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA="
	rec := serveVideo(s, "/v1/chat/completions", toolAudioChat("gem/u", audio))
	bodies, _ := gem.sent()
	if rec.Code != 200 || len(bodies) != 1 {
		t.Fatalf("%d %s; upstream %v", rec.Code, rec.Body.String(), bodies)
	}
	b := bodies[0]
	resp, inline := strings.Index(b, `"functionResponse"`), strings.Index(b, `{"inlineData":{"data":"`+audio+`","mimeType":"audio/wav"}}`)
	if resp < 0 || inline < resp {
		t.Errorf("the tool's audio isn't inlineData after its response: %s", b)
	}
}

// Every other upstream is told of the recording in the result's text and
// is never handed it as an image, which Chat, Responses and Messages would
// turn away with a 400.
func TestToolAudioToldElsewhere(t *testing.T) {
	const audio = "UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA="
	r, err := parseChat([]byte(toolAudioChat("m", audio)))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"chat":      buildChat(r, "m", "api.example.com", false),
		"responses": buildResponses(r, "m", "api.example.com", false),
		"messages":  buildAnthropic(r, "m"),
	} {
		if s := string(body); strings.Contains(s, audio) || !strings.Contains(s, `recorded\n[attachment audio/wav]`) {
			t.Errorf("%s: %s", name, s)
		}
	}
}
