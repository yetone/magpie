package gateway

// Alibaba's Token Plan (the Qwen AI platform's, Bailian's, Model Studio's
// and Qwen Cloud's) reads text aloud and transcribes audio with its
// qwen-audio models at DashScope's own API, which has no OpenAI-style
// /audio/speech or /audio/transcriptions (help.aliyun.com/zh/model-studio/
// qwen-audio-tts-http-api and qwen-audio-asr-http-api, read 11-18):
//
//	POST <root>/api/v1/services/audio/tts/SpeechSynthesizer
//	{"model": …, "input": {"text": …, "voice": …, "format": "mp3"|"wav"|
//	  "pcm"|"opus", "instruction": …, "rate": 0.5–2}}
//	→ {"output": {"audio": {"url": <the whole file, kept 24 hours>,
//	   "data": ""}}, "usage": {"characters": n}}
//
//	POST <root>/api/v1/services/aigc/multimodal-generation/generation
//	{"model": …, "input": {"messages": [{"role": "user", "content":
//	  [{"type": "input_audio", "input_audio": {"data": <data URI>}}]}]},
//	 "parameters": {"format": "wav"…, "language_hints": ["zh"]}}
//	→ {"output": {"text": …}, "usage": {"duration": seconds}}

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// dashScopeAudio is whether p is asked for model, a qwen-audio model of
// the kind given, at DashScope's own API: a key (not a subscription's
// account, another magpie or a plugin) of a DashScope provider whose plan
// names the model, or names it qwen-audio-…-tts/-asr.
func dashScopeAudio(p provider.Provider, model, kind string) bool {
	if p.Account != nil || p.IsPlugin() || p.IsRemoteMagpie() || !dashScope(p) {
		return false
	}
	listed := p.PlanSpeakers()
	tag := "tts"
	if kind == catalog.ServesTranscription {
		listed, tag = p.PlanTranscribers(), "asr"
	}
	if slices.ContainsFunc(listed, func(m catalog.Model) bool { return m.ID == model }) {
		return true
	}
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "qwen-audio") && strings.Contains(m, "-"+tag)
}

// dashScopeVoice is the voice DashScope reads in: the caller's own, or the
// model's first listed one for none and for OpenAI's names (alloy, nova…),
// which no qwen-audio model has. A voice belongs to its model, and another
// model's is refused (the voice list: qwen-audio-3.0-tts-plus has
// longanlingxin and longanlufeng, -flash longanhuan_v3.6 and others).
func dashScopeVoice(model, voice string) string {
	if v := strings.TrimSpace(voice); v != "" {
		if _, openai := openAIVoices[strings.ToLower(v)]; !openai {
			return v
		}
	}
	if strings.Contains(strings.ToLower(model), "flash") {
		return "longanhuan_v3.6"
	}
	return "longanlingxin"
}

// dashScopeSpeechTypes are the formats DashScope's speech is given in, with
// the Content-Type each goes back with.
var dashScopeSpeechTypes = map[string]string{"mp3": "audio/mpeg", "wav": "audio/wav", "pcm": "audio/pcm", "opus": "audio/ogg"}

// dashScopeSpeech asks a qwen-audio TTS model: the input read in the
// voice, OpenAI's instructions as DashScope's instruction, speed as its
// rate, in the format asked (mp3 by default, as OpenAI's), answered with
// the file at the URL DashScope gives. aac and flac, which it doesn't
// make, and a streamed answer (stream_format sse) are turned away.
func (sp *speechRequest) dashScopeSpeech(p provider.Provider, model string) (*vendorAsk, string) {
	str := func(k string) string {
		var v string
		json.Unmarshal(sp.req[k], &v)
		return v
	}
	format := strings.ToLower(str("response_format"))
	if format == "" {
		format = "mp3"
	}
	ctype, ok := dashScopeSpeechTypes[format]
	if !ok {
		return nil, fmt.Sprintf("response_format %q isn't one DashScope's speech is made in: mp3 (the default), wav, pcm or opus", format)
	}
	if str("stream_format") == "sse" {
		return nil, "DashScope's speech is answered whole here: ask without stream_format sse"
	}
	text := str("input")
	if strings.TrimSpace(text) == "" {
		return nil, "\"input\" is empty: there is nothing to read aloud"
	}
	input := map[string]any{"text": text, "voice": dashScopeVoice(model, str("voice")), "format": format}
	if how := strings.TrimSpace(str("instructions")); how != "" {
		input["instruction"] = how
	}
	var speed float64
	if json.Unmarshal(sp.req["speed"], &speed) == nil && speed > 0 {
		input["rate"] = min(max(speed, 0.5), 2)
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": input})
	return &vendorAsk{url: dashScopeRoot(p) + "/api/v1/services/audio/tts/SpeechSynthesizer", body: body, proto: provider.Chat,
		answer: func(ctx context.Context, b []byte) ([]byte, string, int, int, error) {
			var res struct {
				Output struct {
					Audio struct {
						URL  string `json:"url"`
						Data string `json:"data"`
					} `json:"audio"`
				} `json:"output"`
				Usage struct {
					Characters int `json:"characters"`
				} `json:"usage"`
			}
			if json.Unmarshal(b, &res) != nil {
				return nil, "", 0, 0, fmt.Errorf("the answer isn't DashScope's speech: %.200s", b)
			}
			a := res.Output.Audio
			if a.Data != "" {
				out, err := base64.StdEncoding.DecodeString(a.Data)
				if err != nil {
					return nil, "", 0, 0, errors.New("the speech's audio.data isn't base64")
				}
				return out, ctype, res.Usage.Characters, 0, nil
			}
			if a.URL == "" {
				if said := dashScopeSaid(b); said != "" {
					return nil, "", 0, 0, errors.New(said)
				}
				return nil, "", 0, 0, fmt.Errorf("the answer has no audio: %.200s", b)
			}
			out, err := fetchVendorFile(ctx, p, a.URL, maxSpeechBytes)
			if err != nil {
				return nil, "", 0, 0, fmt.Errorf("the speech couldn't be fetched: %v", err)
			}
			return out, ctype, res.Usage.Characters, 0, nil
		}}, ""
}

// maxSpeechBytes bounds a speech file fetched from a vendor's URL: an hour
// of 48 kHz WAV is ~350MB, and a TTS request reads far less than that.
const maxSpeechBytes = 256 << 20

// fetchVendorFile is the file at url, a URL a vendor's answer gave: fetched
// as one a caller names is, from a public address or the vendor's own host
// (vendorClient), through p's proxy, at most limit bytes.
func fetchVendorFile(ctx context.Context, p provider.Provider, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(p.Via(ctx), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := vendorClient(p).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%d %s", res.StatusCode, http.StatusText(res.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("it is over %d MB", limit>>20)
	}
	return b, nil
}

// nativeAt asks a DashScope plan's qwen-audio ASR model on its own API;
// any other is asked at OpenAI's /audio/transcriptions as the form came.
func (a *audioForm) nativeAt(p provider.Provider, model string) (*vendorAsk, string) {
	if !dashScopeAudio(p, model, catalog.ServesTranscription) {
		return nil, ""
	}
	return a.dashScopeTranscription(p, model)
}

// dashScopeTranscription asks a qwen-audio ASR model: the form's file as
// a data URI, in the format its name or type says (wav by default), and
// its language as DashScope's hint. The transcript is given back as
// OpenAI's json (the default), text or verbose_json; srt and vtt, which
// need timings DashScope's whole answer doesn't give per segment, and a
// translation, which it doesn't make, are turned away. The prompt has no
// field on DashScope's API and isn't sent.
func (a *audioForm) dashScopeTranscription(p provider.Provider, model string) (*vendorAsk, string) {
	if a.op == "translations" {
		return nil, model + " transcribes in the language spoken; DashScope's ASR makes no translation: ask /v1/audio/transcriptions"
	}
	format := strings.ToLower(a.format)
	switch format {
	case "", "json", "text", "verbose_json":
	default:
		return nil, fmt.Sprintf("response_format %q isn't one DashScope's transcript can be given back in: json (the default), text or verbose_json", a.format)
	}
	audio, name, ftype, err := a.file()
	if err != nil {
		return nil, err.Error()
	}
	if len(audio) == 0 {
		return nil, "the form has no \"file\": send the audio as its file part"
	}
	kind := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	mt, _, _ := mime.ParseMediaType(ftype)
	if kind == "" {
		if sub, ok := strings.CutPrefix(mt, "audio/"); ok {
			kind = strings.TrimPrefix(sub, "x-")
		}
	}
	switch kind {
	case "", "wave":
		kind = "wav"
	case "mpeg", "mpga":
		kind = "mp3"
	case "ogg", "oga":
		kind = "opus"
	}
	if !strings.HasPrefix(mt, "audio/") {
		mt = "audio/" + kind
	}
	params := map[string]any{"format": kind}
	if l := strings.TrimSpace(a.language); l != "" {
		params["language_hints"] = []string{l}
	}
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"input": map[string]any{"messages": []map[string]any{{"role": "user", "content": []map[string]any{{
			"type": "input_audio", "input_audio": map[string]string{"data": "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(audio)},
		}}}}},
		"parameters": params,
	})
	return &vendorAsk{url: dashScopeRoot(p) + "/api/v1/services/aigc/multimodal-generation/generation", body: body, proto: provider.Chat,
		answer: func(_ context.Context, b []byte) ([]byte, string, int, int, error) {
			var res struct {
				Output struct {
					Text     string `json:"text"`
					Sentence struct {
						Text string `json:"text"`
					} `json:"sentence"`
					Choices []struct {
						Message struct {
							Content json.RawMessage `json:"content"`
						} `json:"message"`
					} `json:"choices"`
				} `json:"output"`
				Usage struct {
					Duration float64 `json:"duration"`
				} `json:"usage"`
			}
			if json.Unmarshal(b, &res) != nil {
				return nil, "", 0, 0, fmt.Errorf("the answer isn't DashScope's transcript: %.200s", b)
			}
			text := res.Output.Text
			if text == "" {
				text = res.Output.Sentence.Text
			}
			if text == "" && len(res.Output.Choices) > 0 {
				text = chatContentText(res.Output.Choices[0].Message.Content)
			}
			if text == "" && !bytes.Contains(b, []byte(`"output"`)) {
				if said := dashScopeSaid(b); said != "" {
					return nil, "", 0, 0, errors.New(said)
				}
				return nil, "", 0, 0, fmt.Errorf("the answer has no transcript: %.200s", b)
			}
			switch format {
			case "text":
				return []byte(text), "text/plain; charset=utf-8", 0, 0, nil
			case "verbose_json":
				out, _ := json.Marshal(map[string]any{"task": "transcribe", "language": a.language, "duration": res.Usage.Duration, "text": text, "segments": []any{}})
				return out, "application/json", 0, 0, nil
			}
			out, _ := json.Marshal(map[string]string{"text": text})
			return out, "application/json", 0, 0, nil
		}}, ""
}

// chatContentText is a chat message's content as text: a string, or its
// text parts one after another (DashScope's multimodal answer gives
// [{"text": …}]).
func chatContentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
