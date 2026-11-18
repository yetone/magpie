package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// nativeRetrieval is a retrieval some providers are asked in their own
// API's shape rather than OpenAI's: Gemini's text-to-speech, which Google
// serves on generateContent alone (Pedram on Discord: a Vertex AI TTS
// model added by hand was sent to …/audio/speech, which Google has not).
type nativeRetrieval interface {
	// nativeAt is how p is asked for model in its own shape: nil when p
	// takes the OpenAI shape, or why it can't be asked at all ("" when it
	// can)
	nativeAt(p provider.Provider, model string) (*vendorAsk, string)
}

// vendorAsk is one request in a vendor's own shape: where it goes, the
// body, the API it is signed for, and how the vendor's whole answer is
// turned into what the client is given (bytes, Content-Type and the
// tokens it counted).
type vendorAsk struct {
	url   string
	body  []byte
	proto provider.Protocol
	// answer is given the request's context, for an answer that is a URL
	// to fetch the audio from (DashScope's)
	answer func(ctx context.Context, b []byte) (out []byte, ctype string, in, outTokens int, err error)
}

// geminiSpeechBase is the Gemini API base p is asked for model at, a
// Gemini TTS model (gemini-2.5-flash-preview-tts…), on Google's own
// generateContent; "" when it is asked on OpenAI's /audio/speech. That is
// Vertex AI's project, AI Studio (whose OpenAI-compatible API has no
// /audio/speech; its Gemini API beside it, as drawGemini asks), or a
// provider of keys with a Gemini API and no OpenAI-style one. A plugin
// puts the request in its vendor's shape itself, and a relay with an
// OpenAI-style API of its own is asked there.
func geminiSpeechBase(p provider.Provider, model string) string {
	if p.IsPlugin() || p.IsRemoteMagpie() || p.Account != nil || catalog.ServesOf(model) != catalog.ServesSpeech || !strings.Contains(strings.ToLower(model), "gemini") {
		return ""
	}
	switch {
	case p.IsVertex():
		if provider.VertexPartner(model) || provider.VertexClaude(model) {
			return ""
		}
		return p.Base(provider.Gemini)
	case isGoogle(p):
		return strings.TrimSuffix(strings.TrimRight(p.Base(provider.Chat), "/"), "/openai")
	case p.OpenAIBase() == "":
		return p.Base(provider.Gemini)
	}
	return ""
}

// geminiVoices are Gemini's prebuilt voices, by their names in lower case.
var geminiVoices = func() map[string]string {
	m := map[string]string{}
	for _, v := range []string{"Zephyr", "Puck", "Charon", "Kore", "Fenrir", "Leda", "Orus", "Aoede", "Callirrhoe", "Autonoe", "Enceladus", "Iapetus", "Umbriel", "Algieba", "Despina", "Erinome", "Algenib", "Rasalgethi", "Laomedeia", "Achernar", "Alnilam", "Schedar", "Gacrux", "Pulcherrima", "Achird", "Zubenelgenubi", "Vindemiatrix", "Sadachbia", "Sadaltager", "Sulafat"} {
		m[strings.ToLower(v)] = v
	}
	return m
}()

// openAIVoices are OpenAI's voices, each given the Gemini voice nearest it
// as Google describes them (nova bright → Zephyr, onyx deep → Algenib
// gravelly, shimmer soft → Achernar…), so a client that names an OpenAI
// voice hears one of the same kind.
var openAIVoices = map[string]string{
	"alloy": "Kore", "ash": "Charon", "ballad": "Algieba", "coral": "Sulafat",
	"echo": "Iapetus", "fable": "Puck", "onyx": "Algenib", "nova": "Zephyr",
	"sage": "Vindemiatrix", "shimmer": "Achernar", "verse": "Rasalgethi",
	"marin": "Aoede", "cedar": "Schedar",
}

// defaultGeminiVoice is spoken in when the voice asked is none of Gemini's
// or OpenAI's: the one Google's own examples use.
const defaultGeminiVoice = "Kore"

// geminiVoice is the Gemini voice for one an OpenAI client named: a Gemini
// voice by its name in any case, an OpenAI voice by the one nearest it,
// anything else Kore.
func geminiVoice(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if g, ok := geminiVoices[v]; ok {
		return g
	}
	if g, ok := openAIVoices[v]; ok {
		return g
	}
	return defaultGeminiVoice
}

// nativeAt asks a DashScope plan's qwen-audio TTS model on its own API
// (speech_dashscope.go), and a Gemini TTS model on generateContent: the input to read,
// after the instructions on how (Gemini takes them in the prompt: "Say
// cheerfully: …"), in one prebuilt voice, answered with audio alone. speed
// has no Gemini field and is left out. The answer is 24 kHz PCM, given back
// as a WAV, or raw for response_format pcm; mp3, opus, aac and flac are
// answered as a WAV too, with its own Content-Type, since magpie has no
// encoder for them and OpenAI's clients ask mp3 by default (a 400 would
// fail them all, while players read a WAV by its header). A streamed
// answer (stream_format sse) is turned away: generateContent's is whole.
func (sp *speechRequest) nativeAt(p provider.Provider, model string) (*vendorAsk, string) {
	if dashScopeAudio(p, model, catalog.ServesSpeech) {
		return sp.dashScopeSpeech(p, model)
	}
	base := strings.TrimRight(geminiSpeechBase(p, model), "/")
	if base == "" {
		return nil, ""
	}
	str := func(k string) string {
		var v string
		json.Unmarshal(sp.req[k], &v)
		return v
	}
	format := strings.ToLower(str("response_format"))
	switch format {
	case "", "wav", "pcm", "mp3", "opus", "aac", "flac":
	default:
		return nil, fmt.Sprintf("response_format %q isn't one Gemini's speech can be given back in: wav (the default) or pcm", format)
	}
	if str("stream_format") == "sse" {
		return nil, "Gemini's speech comes whole from generateContent: ask without stream_format sse"
	}
	input := str("input")
	if strings.TrimSpace(input) == "" {
		return nil, "\"input\" is empty: there is nothing to read aloud"
	}
	text := input
	if how := strings.TrimRight(strings.TrimSpace(str("instructions")), " :"); how != "" {
		text = how + ": " + input
	}
	body, _ := json.Marshal(map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": text}}}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig":       map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]string{"voiceName": geminiVoice(str("voice"))}}},
		},
	})
	url := base + provider.GeminiPath(model, false)
	if p.IsVertex() {
		url = base + provider.VertexWholePath(model)
	}
	return &vendorAsk{url: url, body: body, proto: provider.Gemini, answer: func(_ context.Context, b []byte) ([]byte, string, int, int, error) {
		return geminiAudio(b, format == "pcm")
	}}, ""
}

// geminiAudio is the audio a generateContent answer holds, its inlineData
// parts' PCM (audio/L16;codec=pcm;rate=24000) one after another: in a WAV,
// or raw when pcm, with the tokens it counted. An answer with no audio is
// an error naming why Gemini stopped, and any text it gave instead.
func geminiAudio(b []byte, pcm bool) ([]byte, string, int, int, error) {
	var res struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text       string `json:"text"`
					InlineData *struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Usage struct {
			Prompt     int `json:"promptTokenCount"`
			Candidates int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, "", 0, 0, fmt.Errorf("the answer isn't Gemini's generateContent: %v", err)
	}
	var audio []byte
	rate := 24000
	var said, why []string
	for _, c := range res.Candidates {
		if c.FinishReason != "" && c.FinishReason != "STOP" {
			why = append(why, c.FinishReason)
		}
		for _, part := range c.Content.Parts {
			if part.InlineData == nil || !strings.HasPrefix(strings.ToLower(part.InlineData.MimeType), "audio/") {
				if t := strings.TrimSpace(part.Text); t != "" {
					said = append(said, t)
				}
				continue
			}
			data, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
			if err != nil {
				return nil, "", 0, 0, fmt.Errorf("Gemini's audio isn't base64: %v", err)
			}
			if r := pcmRate(part.InlineData.MimeType); r > 0 {
				rate = r
			}
			audio = append(audio, data...)
		}
	}
	if len(audio) == 0 {
		msg := "Gemini gave no audio"
		if res.PromptFeedback.BlockReason != "" {
			why = append(why, res.PromptFeedback.BlockReason)
		}
		if len(why) > 0 {
			msg += " (" + strings.Join(why, ", ") + ")"
		}
		if len(said) > 0 {
			msg += ": " + strings.Join(said, " ")
		}
		return nil, "", 0, 0, errors.New(msg)
	}
	in, out := res.Usage.Prompt, res.Usage.Candidates
	if pcm {
		return audio, "audio/pcm", in, out, nil
	}
	return pcmWAV(audio, rate), "audio/wav", in, out, nil
}

// pcmRate is the rate a PCM type names (audio/L16;codec=pcm;rate=24000),
// 0 when it names none.
func pcmRate(mime string) int {
	for _, f := range strings.Split(mime, ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(strings.ToLower(f)), "rate="); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// pcmWAV is 16-bit little-endian mono PCM at rate, as Gemini speaks it, in
// a WAV file.
func pcmWAV(pcm []byte, rate int) []byte {
	var b bytes.Buffer
	b.Grow(44 + len(pcm))
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + len(pcm)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1)) // PCM
	w(uint16(1)) // mono
	w(uint32(rate))
	w(uint32(rate * 2))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}
