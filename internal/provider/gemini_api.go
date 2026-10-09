package provider

import (
	"net/url"
	"regexp"
	"strings"
)

// A custom provider can be given a Gemini API (#1346): Google's own at
// generativelanguage.googleapis.com/v1beta, or one that answers as it does.
// Its base is the URL models/{model}:generateContent goes under, which is
// where Google's docs and SDKs put the version.

// geminiMethod is a Gemini endpoint pasted whole, cut back to its base:
// …/models/gemini-2.5-pro:streamGenerateContent?alt=sse, or …/models.
var geminiMethod = regexp.MustCompile(`/models(?:/[^/?#]*)?(?:[?#].*)?$`)

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
