package provider

import (
	"net/url"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// A custom provider can be given a Gemini API (#1346): Google's own at
// generativelanguage.googleapis.com/v1beta, or one that answers as it does.
// Its base is the URL models/{model}:generateContent goes under, which is
// where Google's docs and SDKs put the version.

// geminiMethod is a Gemini endpoint pasted whole, cut back to its base:
// …/models/gemini-2.5-pro:streamGenerateContent?alt=sse, or …/models.
var geminiMethod = lazyre.New(`/models(?:/[^/?#]*)?(?:[?#].*)?$`)

// GeminiBase is the base a Gemini URL is kept as: trimmed, an endpoint or
// the model list pasted whole cut back to the base, and a bare host given
// Google's version (/v1beta), as the Gemini API's SDKs add it.
func GeminiBase(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		return ""
	}
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	u = strings.TrimRight(geminiMethod.ReplaceAllString(u, ""), "/")
	if pu, err := url.Parse(u); err == nil && pu.Host != "" && strings.Trim(pu.Path, "/") == "" {
		return u + "/v1beta"
	}
	return u
}

// GeminiPath is the path under a Gemini base model is asked at: streamed as
// server-sent events (alt=sse) when stream, else one JSON reply.
func GeminiPath(model string, stream bool) string {
	m := url.PathEscape(strings.TrimPrefix(model, "models/"))
	if stream {
		return "/models/" + m + ":streamGenerateContent?alt=sse"
	}
	return "/models/" + m + ":generateContent"
}

// AIStudioChat is Google AI Studio's OpenAI-compatible base, the Google
// Gemini preset's Chat URL.
const AIStudioChat = "https://generativelanguage.googleapis.com/v1beta/openai"

// AIStudioGemini is Google AI Studio's own Gemini API (generateContent),
// beside its OpenAI-compatible one.
const AIStudioGemini = "https://generativelanguage.googleapis.com/v1beta"

// aiStudioNative gives a provider at AI Studio's OpenAI-compatible base,
// the Google Gemini preset's or one typed in, the Gemini API at the same
// place with the same key (cocoasoda on Discord): Gemini's own clients are
// relayed there as they ask, and googleSearch, which the OpenAI-compatible
// API has no tool for, is run there. Its other requests stay on chat
// completions, which Speaks names first. It is filled in once
// (GeminiFilled): a Gemini URL of the provider's own is kept, and one the
// user cleared stays empty.
func (p *Provider) aiStudioNative() {
	if p.GeminiFilled || p.Account != nil || !strings.EqualFold(p.Chat, AIStudioChat) {
		return
	}
	// one filled in before it was recorded is recorded as it is
	if p.Gemini == "" || strings.EqualFold(p.Gemini, AIStudioGemini) {
		p.Gemini, p.GeminiFilled = AIStudioGemini, true
	}
}

// AIStudioPair is a provider at both of AI Studio's APIs: its models are
// the OpenAI-compatible list's, which is the Gemini API's list again.
func (p Provider) AIStudioPair() bool {
	return strings.EqualFold(p.Chat, AIStudioChat) && strings.EqualFold(p.Gemini, AIStudioGemini)
}
