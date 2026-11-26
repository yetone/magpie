package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/artifact"
)

// hearCall posts a piece of recording to the artifact's dictation route
// from this computer, as the shell does: the status, the body and the
// error's code if it is one. hdr are header pairs set over the shell's.
func hearCall(h http.Handler, ref, tok string, audio []byte, hdr ...string) (int, []byte, string) {
	r := httptest.NewRequest(http.MethodPost, ArtifactPagePath+ref+"/_api/dictation", bytes.NewReader(audio))
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
	r.Header.Set("Content-Type", "audio/webm;codecs=opus")
	r.Header.Set("X-Magpie-Token", tok)
	for i := 0; i+1 < len(hdr); i += 2 {
		switch hdr[i] {
		case "RemoteAddr":
			r.RemoteAddr = hdr[i+1]
		case "Host":
			r.Host = hdr[i+1]
		default:
			r.Header.Set(hdr[i], hdr[i+1])
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, w.Body.Bytes(), errCode(out)
}

// hearUp is a speech-to-text vendor and an artifacts store, and the
// gateway on both.
func hearUp(t *testing.T) (http.Handler, *scribe) {
	t.Helper()
	noAgents(t)
	s, sc := scribed(t)
	dir := t.TempDir()
	was := artifact.Dir
	artifact.Dir = func() string { return dir }
	t.Cleanup(func() { artifact.CloseDBs(); artifact.Dir = was })
	return lanGuard(s.Handler()), sc
}

// The viewer's shell sends a phrase of a page that declares dictation:
// the first speech-to-text model gets it as a form, the recording byte for
// byte under the name its type reads as, the language's base part, and
// the vendor's text comes back.
func TestArtifactDictationHearsAPhrase(t *testing.T) {
	h, sc := hearUp(t)
	id := publishFor(t, h, map[string]any{"dictation": map[string]any{}})
	tok := shellTokenOf(t, h, id)
	code, body, ec := hearCall(h, id, tok, voice, "X-Magpie-Language", "es-MX", "X-Magpie-Version", "1")
	var out struct{ Text string }
	if code != 200 || json.Unmarshal(body, &out) != nil || out.Text != "Hello from the magpie." {
		t.Fatalf("%d %s %s", code, ec, body)
	}
	sent, types := sc.got("/v1/audio/transcriptions")
	if len(sent) != 1 {
		t.Fatalf("the vendor was sent %d pieces", len(sent))
	}
	vals, audio := formOf(t, types[0], sent[0])
	if vals["model"] != "whisper-1" || vals["response_format"] != "json" || vals["language"] != "es" || vals["filename"] != "speech.webm" || vals["file type"] != "audio/webm" || !bytes.Equal(audio, voice) {
		t.Fatalf("the vendor was sent %v and %d bytes", vals, len(audio))
	}
	// no language: none is sent, and the vendor tells it; mp4 is Safari's
	if code, _, ec := hearCall(h, id, tok, voice, "Content-Type", "audio/mp4"); code != 200 {
		t.Fatalf("no language: %d %s", code, ec)
	}
	sent, types = sc.got("/v1/audio/transcriptions")
	if vals, _ := formOf(t, types[1], sent[1]); vals["language"] != "" || vals["filename"] != "speech.mp4" {
		t.Fatalf("no language: %v", vals)
	}
}

// Only the shell is heard, for a version that declares dictation, a
// recording of a browser's type and of 1 byte to 8 MiB, a language the
// contract takes, from this computer, at most so many pieces a minute;
// the vendor hears none of the refused.
func TestArtifactDictationRefuses(t *testing.T) {
	h, sc := hearUp(t)
	id := publishFor(t, h, map[string]any{"dictation": map[string]any{}})
	plain := publishFor(t, h, map[string]any{"tts": map[string]any{}})
	tok := shellTokenOf(t, h, id)
	pageTok := tokenRe.FindStringSubmatch(pageGet(h, ArtifactPagePath+id+"/", "Sec-Fetch-Dest", "iframe").Body.String())
	if pageTok == nil {
		t.Fatal("no page token")
	}
	big := bytes.Repeat([]byte{1}, artifactDictationAudio+1)
	for _, c := range []struct {
		why   string
		ref   string
		tok   string
		audio []byte
		hdr   []string
		code  int
		ec    string
	}{
		{"the page's own token", id, pageTok[1], voice, nil, 403, "not_granted"},
		{"no token", id, "", voice, nil, 403, "not_granted"},
		{"another artifact's shell", plain, tok, voice, nil, 403, "not_granted"},
		{"a version without dictation", plain, shellTokenOf(t, h, plain), voice, nil, 403, "not_granted"},
		{"a version that isn't there", id, tok, voice, []string{"X-Magpie-Version", "9"}, 403, "not_granted"},
		{"not audio", id, tok, voice, []string{"Content-Type", "text/plain"}, 400, "invalid_argument"},
		{"a type no recorder makes", id, tok, voice, []string{"Content-Type", "audio/flac"}, 400, "invalid_argument"},
		{"empty", id, tok, nil, nil, 400, "invalid_argument"},
		{"over 8 MiB", id, tok, big, nil, 400, "invalid_argument"},
		{"a language that isn't one", id, tok, voice, []string{"X-Magpie-Language", "spanish"}, 400, "invalid_argument"},
		{"a language with a header in it", id, tok, voice, []string{"X-Magpie-Language", "es\r\nX: y"}, 400, "invalid_argument"},
		{"another computer", id, tok, voice, []string{"RemoteAddr", "192.168.1.9:50001"}, 403, ""},
		{"a rebound name", id, tok, voice, []string{"Host", "evil.example:3425"}, 403, ""},
		{"no artifact", "nope", tok, voice, nil, 404, "unavailable"},
	} {
		code, _, ec := hearCall(h, c.ref, c.tok, c.audio, c.hdr...)
		if code != c.code || c.ec != "" && ec != c.ec {
			t.Errorf("%s: %d %q", c.why, code, ec)
		}
	}
	if code, _, ec := hearCall(h, id, tok, bytes.Repeat([]byte{1}, artifactDictationAudio)); code != 200 {
		t.Fatalf("8 MiB: %d %s", code, ec)
	}
	if sent, _ := sc.got("/v1/audio/transcriptions"); len(sent) != 1 {
		t.Fatalf("the vendor heard %d pieces", len(sent))
	}

	was := artifactDictationPieces
	artifactDictationPieces = newMinuteLimit(2)
	t.Cleanup(func() { artifactDictationPieces = was })
	for i := 1; i <= 3; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, ArtifactPagePath+id+"/_api/dictation", bytes.NewReader(voice))
		r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
		r.Header.Set("Content-Type", "audio/ogg")
		r.Header.Set("X-Magpie-Token", tok)
		h.ServeHTTP(w, r)
		if i <= 2 && w.Code != 200 || i == 3 && (w.Code != 429 || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), "rate_limited")) {
			t.Fatalf("piece %d: %d %s", i, w.Code, w.Body)
		}
	}
	if sent, _ := sc.got("/v1/audio/transcriptions"); len(sent) != 3 {
		t.Fatalf("the vendor heard %d pieces, want 3", len(sent))
	}
}

// With no provider that serves a speech-to-text model, a piece is
// unavailable, and says what to add.
func TestArtifactDictationNoModel(t *testing.T) {
	noAgents(t)
	h := artifacts(t)
	id := publishFor(t, h, map[string]any{"dictation": map[string]any{}})
	code, body, ec := hearCall(h, id, shellTokenOf(t, h, id), voice, "X-Magpie-Version", strconv.Itoa(1))
	if code != 503 || ec != "unavailable" || !strings.Contains(string(body), "speech-to-text model") {
		t.Fatalf("%d %q %s", code, ec, body)
	}
}
