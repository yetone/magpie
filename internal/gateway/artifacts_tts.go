package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/catalog"
)

// A page's tts reads a line aloud through the viewer's shell: the page
// asks the shell (postMessage), and the shell, holding its own token,
// asks here for one line's audio. The page never reaches this route, so
// a page can't read aloud past the shell's gesture and Stop.
const (
	artifactTTSMost = 60   // lines a minute, for each artifact
	artifactTTSText = 2000 // characters in one line, as the contract has it
)

var artifactTTSLines = newMinuteLimit(artifactTTSMost)

// artifactVoices are the contract's voice names as OpenAI's voices.
var artifactVoices = map[string]string{"buttery": "nova", "airy": "shimmer", "glassy": "alloy", "rounded": "onyx"}

// artifactTTS is POST <page>/_api/tts: {t, text, voice, version} from the
// shell, answered with the line's audio (mp3), or an error the shell
// turns into the page's code.
func (s *Server) artifactTTS(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	var c struct {
		Token   string `json:"t"`
		Text    string `json:"text"`
		Voice   string `json:"voice"`
		Version int    `json:"version"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10+1))
	if err != nil || len(body) > 16<<10 || json.Unmarshal(body, &c) != nil {
		artifactAPIError(w, http.StatusBadRequest, "invalid_argument", "a line is a JSON object of at most 16 KiB")
		return
	}
	if !local(r) || !loopbackHost(r.Host) {
		artifactAPIError(w, http.StatusForbidden, "unavailable", "an artifact reads aloud in a browser on this computer, at "+URL())
		return
	}
	id, n, ok := ArtifactRef(r.PathValue("ref"))
	if !ok || strings.Contains(r.PathValue("ref"), "/") {
		artifactAPIError(w, http.StatusNotFound, "unavailable", "no such artifact here")
		return
	}
	a, err := artifact.Get(id)
	if err != nil {
		artifactAPIError(w, http.StatusNotFound, "unavailable", "the artifact is gone")
		return
	}
	if !artifact.ShellTokenOK(id, c.Token) {
		artifactAPIError(w, http.StatusForbidden, "not_granted", "only the viewer's shell reads a page aloud")
		return
	}
	if c.Version <= 0 {
		c.Version = a.Latest().N
		if n > 0 {
			c.Version = n
		}
	}
	if v := a.Version(c.Version); v == nil || !v.Capabilities.Has("tts") {
		artifactAPIError(w, http.StatusForbidden, "not_granted", "this version of the page doesn't declare tts")
		return
	}
	text := plainOfMarkdown(strings.TrimSpace(c.Text))
	if text == "" || utf8.RuneCountInString(text) > artifactTTSText {
		artifactAPIError(w, http.StatusBadRequest, "invalid_argument", fmt.Sprintf("a line is 1 to %d characters", artifactTTSText))
		return
	}
	voice := artifactVoices[c.Voice]
	if c.Voice == "" {
		voice = artifactVoices["buttery"]
	}
	if voice == "" {
		artifactAPIError(w, http.StatusBadRequest, "invalid_argument", "voice is buttery, airy, glassy or rounded")
		return
	}
	model := FirstServing(catalog.ServesSpeech)
	if model == "" {
		artifactAPIError(w, http.StatusServiceUnavailable, "unavailable", "reading aloud needs a text-to-speech model (tts-1, gpt-4o-mini-tts…): add a provider that serves one in magpie")
		return
	}
	if !artifactTTSLines.allow(a.ID, time.Now()) {
		h.Set("Retry-After", "10")
		artifactAPIError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("a page reads at most %d lines a minute", artifactTTSMost))
		return
	}
	b, _ := json.Marshal(map[string]any{"model": model, "input": text, "voice": voice, "response_format": "mp3"})
	in := sampleInner(r, b)
	in.URL.Path, in.RequestURI, in.Pattern = "/v1/audio/speech", "/v1/audio/speech", "POST /v1/audio/speech"
	in.Header.Del("Accept")
	in.Header.Del("anthropic-version")
	s.speak(w, in)
}
