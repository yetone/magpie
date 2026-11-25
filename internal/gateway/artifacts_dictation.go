package gateway

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
)

// A page's dictation is the viewer's speech as text: the shell, at
// magpie's origin, holds the microphone, shows its own Listening and
// Stop, and posts each piece of the recording here with its own token;
// the page gets the words, never the sound, and never reaches this
// route.
const (
	artifactDictationMost  = 120     // pieces a minute, for each artifact
	artifactDictationAudio = 8 << 20 // bytes in one piece (30s of opus is ~120 KiB)
)

var artifactDictationPieces = newMinuteLimit(artifactDictationMost)

// artifactAudioTypes are the recordings a browser's MediaRecorder makes,
// with the file name the vendor reads the format from.
var artifactAudioTypes = map[string]string{"audio/webm": "speech.webm", "audio/ogg": "speech.ogg", "audio/mp4": "speech.mp4", "audio/wav": "speech.wav"}

var dictationLang = lazyre.New(`^[a-z]{2}(-[A-Za-z]{2})?$`)

// artifactDictation is POST <page>/_api/dictation: a piece of recording
// as the body (its Content-Type a browser recording's), the shell's token
// in X-Magpie-Token, the version in X-Magpie-Version and the language, if
// the page named one, in X-Magpie-Language; answered {"text"}.
func (s *Server) artifactDictation(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if !local(r) || !loopbackHost(r.Host) {
		artifactAPIError(w, http.StatusForbidden, "unavailable", "an artifact takes dictation in a browser on this computer, at "+URL())
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
	if !artifact.ShellTokenOK(id, r.Header.Get("X-Magpie-Token")) {
		artifactAPIError(w, http.StatusForbidden, "not_granted", "only the viewer's shell takes dictation")
		return
	}
	v, _ := strconv.Atoi(r.Header.Get("X-Magpie-Version"))
	if v <= 0 {
		v = a.Latest().N
		if n > 0 {
			v = n
		}
	}
	if ver := a.Version(v); ver == nil || !ver.Capabilities.Has("dictation") {
		artifactAPIError(w, http.StatusForbidden, "not_granted", "this version of the page doesn't declare dictation")
		return
	}
	ctype, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	name := artifactAudioTypes[strings.TrimSpace(strings.ToLower(ctype))]
	if name == "" {
		artifactAPIError(w, http.StatusBadRequest, "invalid_argument", "a piece of dictation is audio/webm, audio/ogg, audio/mp4 or audio/wav")
		return
	}
	lang := r.Header.Get("X-Magpie-Language")
	if lang != "" && !dictationLang.MatchString(lang) {
		artifactAPIError(w, http.StatusBadRequest, "invalid_argument", "language is a two-letter language, optionally with a region (es, es-MX)")
		return
	}
	audio, err := io.ReadAll(io.LimitReader(r.Body, artifactDictationAudio+1))
	if err != nil || len(audio) == 0 || len(audio) > artifactDictationAudio {
		artifactAPIError(w, http.StatusBadRequest, "invalid_argument", fmt.Sprintf("a piece of dictation is 1 byte to %d MiB", artifactDictationAudio>>20))
		return
	}
	model := FirstServing(catalog.ServesTranscription)
	if model == "" {
		artifactAPIError(w, http.StatusServiceUnavailable, "unavailable", "dictation needs a speech-to-text model (whisper, gpt-4o-transcribe…): add a provider that serves one in magpie")
		return
	}
	if !artifactDictationPieces.allow(a.ID, time.Now()) {
		h.Set("Retry-After", "10")
		artifactAPIError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("a page takes at most %d pieces of dictation a minute", artifactDictationMost))
		return
	}
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	mw.WriteField("model", model)
	mw.WriteField("response_format", "json")
	if lang != "" {
		base, _, _ := strings.Cut(lang, "-") // ISO-639-1, as OpenAI's language takes it
		mw.WriteField("language", strings.ToLower(base))
	}
	ph := textproto.MIMEHeader{}
	ph.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	ph.Set("Content-Type", strings.TrimSpace(strings.ToLower(ctype)))
	part, _ := mw.CreatePart(ph)
	part.Write(audio)
	mw.Close()
	in := sampleInner(r, form.Bytes())
	in.URL.Path, in.RequestURI, in.Pattern = "/v1/audio/transcriptions", "/v1/audio/transcriptions", "POST /v1/audio/transcriptions"
	in.Header.Set("Content-Type", mw.FormDataContentType())
	in.Header.Del("Accept")
	in.Header.Del("anthropic-version")
	s.transcribe("/audio/transcriptions", "transcriptions")(w, in)
}
