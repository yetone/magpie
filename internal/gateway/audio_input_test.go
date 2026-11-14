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
