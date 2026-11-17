package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// geminiPCM is speech as Gemini's TTS gives it: 16-bit PCM, bytes no text
// has.
var geminiPCM = bytes.Repeat([]byte{0x01, 0xff, 0x7f, 0x80, '{', '}'}, 400)

// geminiSpoken is a generateContent answer of a TTS model, in Google's
// shape: the PCM as base64 inlineData, audio tokens counted.
var geminiSpoken = `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;codec=pcm;rate=24000","data":"` +
	base64.StdEncoding.EncodeToString(geminiPCM) + `"}}],"role":"model"},"finishReason":"STOP","index":0}],` +
	`"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":87,"totalTokenCount":96,"promptTokensDetails":[{"modality":"TEXT","tokenCount":9}],"candidatesTokensDetails":[{"modality":"AUDIO","tokenCount":87}]},"modelVersion":"gemini-2.5-flash-preview-tts","responseId":"Q2t0aZ7bA4nV"}`

// vertexSpeaker is a gateway with a Vertex AI provider at project proj-1
// that has a Gemini TTS model added by hand, as Pedram did, backed by v
// answering each generateContent with a TTS model's answer.
func vertexSpeaker(t *testing.T) (*Server, *vertexUp) {
	t.Helper()
	v := &vertexUp{reply: func(int, map[string]any) (int, string) { return 200, geminiSpoken }}
	vertexServe(t, v, `{}`)
	creds := filepath.Join(t.TempDir(), "application_default_credentials.json")
	os.WriteFile(creds, []byte(`{"type":"authorized_user","client_id":"test.apps.googleusercontent.com","client_secret":"test-secret","refresh_token":"1//test-refresh"}`), 0o600)
	if err := provider.Save(provider.Provider{ID: "google-vertex", Name: "Google Vertex AI", Preset: provider.VertexPreset,
		Vertex: &provider.Vertex{Project: "proj-1", Credentials: creds}, Models: []string{"gemini-2.5-flash", "gemini-2.5-flash-preview-tts"}}); err != nil {
		t.Fatal(err)
	}
	return New(), v
}

// wavOf is the PCM a WAV holds and its rate, or fails the test when b
// isn't a 16-bit mono PCM WAV.
func wavOf(t *testing.T, b []byte) ([]byte, int) {
	t.Helper()
	if len(b) < 44 || string(b[:4]) != "RIFF" || string(b[8:16]) != "WAVEfmt " || string(b[36:40]) != "data" {
		t.Fatalf("not a WAV: %q", b[:min(len(b), 48)])
	}
	le := binary.LittleEndian
	if le.Uint16(b[20:]) != 1 || le.Uint16(b[22:]) != 1 || le.Uint16(b[34:]) != 16 || int(le.Uint32(b[4:])) != len(b)-8 || int(le.Uint32(b[40:])) != len(b)-44 {
		t.Fatalf("WAV header %v", b[:44])
	}
	return b[44:], int(le.Uint32(b[24:]))
}

// A Gemini TTS model added to Vertex AI is asked on generateContent, not
// at an /audio/speech Google doesn't have: the input after the
// instructions, in the Gemini voice nearest the OpenAI one asked, audio
// alone, signed with the project's Google token. Its PCM comes back as a
// 24 kHz WAV, and the call is in the usage ledger with Gemini's counts.
func TestGeminiSpeechOnVertex(t *testing.T) {
	s, v := vertexSpeaker(t)
	code, h, got := postSpeech(t, s, "/v1/audio/speech", `{"model":"google-vertex/gemini-2.5-flash-preview-tts","input":"Hello from the magpie.","voice":"coral","instructions":"Say cheerfully","speed":1.25}`)
	if code != 200 || h.Get("Content-Type") != "audio/wav" {
		t.Fatalf("%d %q %s", code, h.Get("Content-Type"), got)
	}
	pcm, rate := wavOf(t, got)
	if !bytes.Equal(pcm, geminiPCM) || rate != 24000 {
		t.Fatalf("WAV holds %d bytes at %d Hz, want Gemini's %d at 24000", len(pcm), rate, len(geminiPCM))
	}
	a := v.last(t)
	if a.path != "/v1/projects/proj-1/locations/global/publishers/google/models/gemini-2.5-flash-preview-tts:generateContent" || a.auth != "Bearer ya29.vertex-test" {
		t.Fatalf("asked %s with %q", a.path, a.auth)
	}
	b, _ := json.Marshal(a.body)
	if g := gjson.GetBytes(b, "generationConfig.responseModalities").Raw; g != `["AUDIO"]` {
		t.Fatalf("responseModalities %s: %s", g, b)
	}
	if g := gjson.GetBytes(b, "generationConfig.speechConfig.voiceConfig.prebuiltVoiceConfig.voiceName").String(); g != "Sulafat" {
		t.Fatalf("voice %q: %s", g, b)
	}
	if g := gjson.GetBytes(b, "contents.0.parts.0.text").String(); g != "Say cheerfully: Hello from the magpie." || gjson.GetBytes(b, "contents.0.role").String() != "user" {
		t.Fatalf("contents %s", b)
	}
	if bytes.Contains(b, []byte("speed")) || bytes.Contains(b, []byte(`"model"`)) {
		t.Fatalf("an OpenAI field went to Gemini: %s", b)
	}
	var recs []usage.Record
	for i := 0; i < 100; i++ {
		if recs = usage.Load(time.Time{}); len(recs) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(recs) != 1 || recs[0].Operation != "speech" || recs[0].Provider != "google-vertex" || recs[0].Model != "gemini-2.5-flash-preview-tts" || recs[0].Status != 200 || recs[0].Input != 9 || recs[0].Output != 87 {
		t.Fatalf("usage %+v", recs)
	}
}

// response_format pcm is Gemini's PCM as it came; a format magpie has no
// encoder for (mp3, OpenAI's default, opus…) is a WAV, said so in its
// Content-Type; one OpenAI has none of is turned away, as is a stream,
// and neither reaches Google. The voice is a Gemini one by its name in any
// case, Kore for one Gemini hasn't.
func TestGeminiSpeechFormatsAndVoices(t *testing.T) {
	s, v := vertexSpeaker(t)
	const m = `"model":"google-vertex/gemini-2.5-flash-preview-tts","input":"hi"`
	voiceOf := func() string {
		b, _ := json.Marshal(v.last(t).body)
		return gjson.GetBytes(b, "generationConfig.speechConfig.voiceConfig.prebuiltVoiceConfig.voiceName").String()
	}
	code, h, got := postSpeech(t, s, "/v1/audio/speech", `{`+m+`,"voice":"puck","response_format":"pcm"}`)
	if code != 200 || h.Get("Content-Type") != "audio/pcm" || !bytes.Equal(got, geminiPCM) || voiceOf() != "Puck" {
		t.Fatalf("pcm: %d %q %d bytes, voice %s", code, h.Get("Content-Type"), len(got), voiceOf())
	}
	for _, f := range []string{"mp3", "opus", "wav"} {
		code, h, got = postSpeech(t, s, "/v1/audio/speech", `{`+m+`,"voice":"someone","response_format":"`+f+`"}`)
		if code != 200 || h.Get("Content-Type") != "audio/wav" || voiceOf() != "Kore" {
			t.Fatalf("%s: %d %q %s, voice %s", f, code, h.Get("Content-Type"), got, voiceOf())
		}
		wavOf(t, got)
	}
	asked := v.count()
	for body, want := range map[string]string{
		`{` + m + `,"response_format":"ogg"}`:                                "wav (the default) or pcm",
		`{` + m + `,"stream_format":"sse"}`:                                  "without stream_format sse",
		`{"model":"google-vertex/gemini-2.5-flash-preview-tts","input":" "}`: "nothing to read aloud",
	} {
		if code, _, got := postSpeech(t, s, "/v1/audio/speech", body); code != 400 || !strings.Contains(string(got), want) {
			t.Fatalf("%s: %d %s", body, code, got)
		}
	}
	if n := v.count(); n != asked {
		t.Fatalf("Google was asked %d times for requests turned away", n-asked)
	}
}

// An answer with no audio (a refusal said in text, or a block) is an
// error naming why, not an empty WAV.
func TestGeminiSpeechWithoutAudio(t *testing.T) {
	s, v := vertexSpeaker(t)
	v.mu.Lock()
	v.reply = func(int, map[string]any) (int, string) {
		return 200, `{"candidates":[{"content":{"parts":[{"text":"I can't read that."}],"role":"model"},"finishReason":"OTHER","index":0}],"usageMetadata":{"promptTokenCount":9,"totalTokenCount":9}}`
	}
	v.mu.Unlock()
	code, _, got := postSpeech(t, s, "/v1/audio/speech", `{"model":"google-vertex/gemini-2.5-flash-preview-tts","input":"hi"}`)
	if code != 502 || !strings.Contains(string(got), "Gemini gave no audio (OTHER): I can't read that.") {
		t.Fatalf("%d %s", code, got)
	}
}

// The Gemini API provider (AI Studio) is asked on its own generateContent
// beside its OpenAI-compatible base, with its key in x-goog-api-key, and
// the input and instructions go out masked when secrets are kept. A
// relay's Gemini TTS model, behind an OpenAI-style API of its own, still
// goes to its /audio/speech as sent.
func TestGeminiSpeechOnAIStudio(t *testing.T) {
	fresh(t)
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "gg", Name: "Google", Chat: provider.AIStudioChat, Key: "AIzaSy-test", Models: []string{"gemini-2.5-pro-preview-tts"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Gemini: "https://relay.example/v1beta", Key: "rk", Models: []string{"gemini-2.5-flash-preview-tts"}}); err != nil {
		t.Fatal(err)
	}
	type asked struct {
		url, key, auth string
		body           []byte
	}
	var mu sync.Mutex
	var seen []asked
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, asked{r.URL.String(), r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization"), b})
		mu.Unlock()
		switch {
		case r.URL.Host == "generativelanguage.googleapis.com" && strings.HasSuffix(r.URL.Path, ":generateContent"):
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json; charset=UTF-8"}}, Body: io.NopCloser(strings.NewReader(geminiSpoken))}, nil
		case r.URL.Host == "relay.example" && r.URL.Path == "/v1/audio/speech":
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"audio/mpeg"}}, Body: io.NopCloser(bytes.NewReader(mp3))}, nil
		}
		return nil, fmt.Errorf("unexpected request to %s", r.URL)
	})}
	secret := "sk-ant-api03-" + strings.Repeat("Ab3x", 20)
	body, _ := json.Marshal(map[string]any{"model": "gg/gemini-2.5-pro-preview-tts", "input": "Read this: " + secret, "instructions": "Say slowly", "voice": "Charon", "response_format": "wav"})
	code, h, got := postSpeech(t, s, "/v1/audio/speech", string(body))
	if code != 200 || h.Get("Content-Type") != "audio/wav" {
		t.Fatalf("%d %q %s", code, h.Get("Content-Type"), got)
	}
	if pcm, _ := wavOf(t, got); !bytes.Equal(pcm, geminiPCM) {
		t.Fatalf("WAV holds %d bytes", len(pcm))
	}
	mu.Lock()
	a := seen[len(seen)-1]
	mu.Unlock()
	if a.url != "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro-preview-tts:generateContent" || a.key != "AIzaSy-test" || a.auth != "" {
		t.Fatalf("asked %s, key %q, auth %q", a.url, a.key, a.auth)
	}
	text := gjson.GetBytes(a.body, "contents.0.parts.0.text").String()
	if bytes.Contains(a.body, []byte(secret)) || !strings.HasPrefix(text, "Say slowly: Read this: ") || gjson.GetBytes(a.body, "generationConfig.speechConfig.voiceConfig.prebuiltVoiceConfig.voiceName").String() != "Charon" {
		t.Fatalf("Google was sent %s", a.body)
	}
	code, h, got = postSpeech(t, s, "/v1/audio/speech", `{"model":"relay/gemini-2.5-flash-preview-tts","input":"hi","voice":"coral"}`)
	if code != 200 || h.Get("Content-Type") != "audio/mpeg" || !bytes.Equal(got, mp3) {
		t.Fatalf("relay: %d %q", code, h.Get("Content-Type"))
	}
	mu.Lock()
	a = seen[len(seen)-1]
	mu.Unlock()
	if a.url != "https://relay.example/v1/audio/speech" || gjson.GetBytes(a.body, "voice").String() != "coral" {
		t.Fatalf("relay asked %s with %s", a.url, a.body)
	}
}
