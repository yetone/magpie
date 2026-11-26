package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/artifact"
)

// ttsCall posts a line to the artifact's tts route from this computer:
// the status, the body and the error's code if it is one.
func ttsCall(h http.Handler, ref string, call map[string]any, host ...string) (int, []byte, string) {
	b, _ := json.Marshal(call)
	r := httptest.NewRequest(http.MethodPost, ArtifactPagePath+ref+"/_api/tts", bytes.NewReader(b))
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
	if len(host) == 2 {
		r.RemoteAddr, r.Host = host[0], host[1]
	}
	r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, w.Body.Bytes(), errCode(out)
}

// ttsUp is a speech vendor and an artifacts store, and the gateway on
// both.
func ttsUp(t *testing.T) (http.Handler, *speaker) {
	t.Helper()
	noAgents(t)
	s, sp := speakerUp(t)
	dir := t.TempDir()
	was := artifact.Dir
	artifact.Dir = func() string { return dir }
	t.Cleanup(func() { artifact.CloseDBs(); artifact.Dir = was })
	return lanGuard(s.Handler()), sp
}

func shellTokenOf(t *testing.T, h http.Handler, id string) string {
	t.Helper()
	m := shellTokenRe.FindStringSubmatch(pageGet(h, ArtifactPagePath+id+"/", "Sec-Fetch-Dest", "document").Body.String())
	if m == nil {
		t.Fatal("no shell token")
	}
	return m[1]
}

// The viewer's shell reads a line of a page that declares tts: the first
// speech model, the line as plain text in the voice it names, and the
// vendor's audio back as it came.
func TestArtifactTTSReadsALine(t *testing.T) {
	h, sp := ttsUp(t)
	id := publishFor(t, h, map[string]any{"tts": map[string]any{}})
	tok := shellTokenOf(t, h, id)
	code, body, ec := ttsCall(h, id, map[string]any{"t": tok, "text": "Hello **there**, [friend](https://x.example)", "voice": "airy"})
	if code != 200 || !bytes.Equal(body, mp3) {
		t.Fatalf("%d %s, %d bytes", code, ec, len(body))
	}
	sent, _ := sp.got()
	var v map[string]any
	if len(sent) != 1 || json.Unmarshal(sent[0], &v) != nil || v["input"] != "Hello there, friend" || v["voice"] != "shimmer" || v["response_format"] != "mp3" || v["model"] != "gpt-4o-mini-tts" {
		t.Fatalf("the vendor was sent %s", sent)
	}
	// no voice is buttery's
	if code, _, _ := ttsCall(h, id, map[string]any{"t": tok, "text": "Again"}); code != 200 {
		t.Fatalf("no voice: %d", code)
	}
	if sent, _ := sp.got(); len(sent) != 2 || !strings.Contains(string(sent[1]), `"voice":"nova"`) {
		t.Fatalf("buttery: %s", sent)
	}
}

// Only the shell reads aloud, for a version that declares tts, a line of
// 1 to 2000 characters in a voice the contract names, from this computer,
// at most so many lines a minute; the vendor hears none of the refused.
func TestArtifactTTSRefuses(t *testing.T) {
	h, sp := ttsUp(t)
	id := publishFor(t, h, map[string]any{"tts": map[string]any{}})
	plain := publishFor(t, h, nil)
	tok := shellTokenOf(t, h, id)
	pageTok := tokenRe.FindStringSubmatch(pageGet(h, ArtifactPagePath+id+"/", "Sec-Fetch-Dest", "iframe").Body.String())
	if pageTok == nil {
		t.Fatal("no page token")
	}
	for _, c := range []struct {
		why  string
		ref  string
		call map[string]any
		host []string
		code int
		ec   string
	}{
		{"the page's own token", id, map[string]any{"t": pageTok[1], "text": "hi"}, nil, 403, "not_granted"},
		{"no token", id, map[string]any{"text": "hi"}, nil, 403, "not_granted"},
		{"another artifact's shell", plain, map[string]any{"t": tok, "text": "hi"}, nil, 403, "not_granted"},
		{"a version without tts", plain, map[string]any{"t": shellTokenOf(t, h, plain), "text": "hi"}, nil, 403, "not_granted"},
		{"empty", id, map[string]any{"t": tok, "text": " ** "}, nil, 400, "invalid_argument"},
		{"too long", id, map[string]any{"t": tok, "text": strings.Repeat("é", 2001)}, nil, 400, "invalid_argument"},
		{"an unknown voice", id, map[string]any{"t": tok, "text": "hi", "voice": "loud"}, nil, 400, "invalid_argument"},
		{"another computer", id, map[string]any{"t": tok, "text": "hi"}, []string{"192.168.1.9:50001", "127.0.0.1:3425"}, 403, ""},
		{"a rebound name", id, map[string]any{"t": tok, "text": "hi"}, []string{"127.0.0.1:50001", "evil.example:3425"}, 403, ""},
		{"no artifact", "nope", map[string]any{"t": tok, "text": "hi"}, nil, 404, "unavailable"},
	} {
		code, _, ec := ttsCall(h, c.ref, c.call, c.host...)
		if code != c.code || c.ec != "" && ec != c.ec {
			t.Errorf("%s: %d %q", c.why, code, ec)
		}
	}
	if code, _, _ := ttsCall(h, id, map[string]any{"t": tok, "text": strings.Repeat("é", 2000)}); code != 200 {
		t.Fatalf("2000 characters: %d", code)
	}
	if sent, _ := sp.got(); len(sent) != 1 {
		t.Fatalf("the vendor heard %d lines", len(sent))
	}

	was := artifactTTSLines
	artifactTTSLines = newMinuteLimit(2)
	t.Cleanup(func() { artifactTTSLines = was })
	for i := 1; i <= 3; i++ {
		code, _, ec := ttsCall(h, id, map[string]any{"t": tok, "text": "line"})
		if i <= 2 && code != 200 || i == 3 && (code != 429 || ec != "rate_limited") {
			t.Fatalf("line %d: %d %q", i, code, ec)
		}
	}
}

// With no provider that serves a speech model, a line is unavailable,
// and says what to add.
func TestArtifactTTSNoModel(t *testing.T) {
	noAgents(t)
	h := artifacts(t)
	id := publishFor(t, h, map[string]any{"tts": map[string]any{}})
	code, body, ec := ttsCall(h, id, map[string]any{"t": shellTokenOf(t, h, id), "text": "hi"})
	if code != 503 || ec != "unavailable" || !strings.Contains(string(body), "text-to-speech model") {
		t.Fatalf("%d %q %s", code, ec, body)
	}
}
