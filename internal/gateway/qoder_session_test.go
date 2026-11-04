package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// The fixtures in testdata/qoder-1.1.82 are qodercli 1.1.82's own: each
// request body as it reached a fake provider of the user's own, byte for
// byte, and the session files it wrote beside them, in a sandbox HOME on
// a Mac. headless-* are `qodercli -p` turns (turn 2 has the model call
// the Agent tool, headless-subagent is that subagent's request, and
// headless-other a second session's); the others are one interactive
// session: its first title call (sent before the session's file was
// written), its two turns, a suggestion call and two later title calls.
const qoderFixtures = "testdata/qoder-1.1.82"

const (
	qoderHeadlessSession    = "8509b6c3-af2e-4b16-913b-2111141d297f"
	qoderOtherSession       = "9a8ea8d1-9987-404f-a9c5-45b3df6fe4b6"
	qoderInteractiveSession = "c734a21d-abbe-4f8f-a1de-f4cbd3a407d2"
)

// qoderHome is a copy of a fixture's session files as Qoder's folder
// (QODER_CONFIG_DIR), written now: a checkout's own times may be older
// than qoderRecent.
func qoderHome(t *testing.T, which ...string) string {
	t.Helper()
	home := t.TempDir()
	for _, w := range which {
		src := filepath.Join(qoderFixtures, w)
		err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(src, path)
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			to := filepath.Join(home, rel)
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return err
			}
			return os.WriteFile(to, b, 0o600)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func qoderBody(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(qoderFixtures, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// qoderLookup is the session a request of Qoder's is matched to in home.
func qoderLookup(t *testing.T, home, name string) string {
	t.Helper()
	body := qoderBody(t, name)
	x := &qoderIndex{root: filepath.Join(home, "projects"), files: map[string]*qoderFile{}}
	return x.find(context.Background(), qoderAsked(body), 0)
}

// Each request 1.1.82 sends, which names no session, is matched to the
// session whose file holds its prompts: a turn, a later turn, a subagent
// (its own file names the parent), a title call with the latest prompt
// only or a transcript, and a suggestion call. Two sessions in one folder
// stay apart.
func TestQoderRequestsFindTheirSession(t *testing.T) {
	home := qoderHome(t, "headless", "interactive")
	for _, tc := range []struct{ name, want string }{
		{"headless-turn2", qoderHeadlessSession},
		{"headless-subagent", qoderHeadlessSession},
		{"headless-other", qoderOtherSession},
		{"interactive-turn1", qoderInteractiveSession},
		{"interactive-turn2", qoderInteractiveSession},
		{"title-first", qoderInteractiveSession},
		{"title-turn2", qoderInteractiveSession},
		{"title-transcript", qoderInteractiveSession},
		{"suggestion", qoderInteractiveSession},
	} {
		if got := qoderLookup(t, home, tc.name); got != tc.want {
			t.Errorf("%s: matched %q, want %q", tc.name, got, tc.want)
		}
	}
	// a session's prompt asked of nobody's files is nobody's
	if got := qoderLookup(t, qoderHome(t, "headless"), "interactive-turn1"); got != "" {
		t.Errorf("another folder's turn matched %q", got)
	}
}

// Qoder's request text is the user's prompt after its <system-reminder>
// blocks; the reminders, the system prompt and tool results are no part
// of what is matched.
func TestQoderAskedLeavesQodersOwnText(t *testing.T) {
	got := qoderAsked(qoderBody(t, "headless-turn2"))
	want := []string{"first turn about rivers", "second turn please SPAWN"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("asked %q, want %q", got, want)
	}
}

// Two sessions opened with the same prompt: the turn is the one whose
// file was written last, and a later turn the one holding both prompts.
func TestQoderSamePromptTwoSessions(t *testing.T) {
	home := qoderHome(t, "interactive")
	dir := filepath.Join(home, "projects", "-Users-yetone-qsess-sb-ihome-proj")
	whole, err := os.ReadFile(filepath.Join(dir, qoderInteractiveSession+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// the session as it was before its second prompt, as another session
	cut := bytes.Index(whole, []byte(`"humanInput":{"text":"now another about mountains"`))
	first := whole[:bytes.LastIndexByte(whole[:cut], '\n')+1]
	other := "11111111-2222-4333-8444-555555555555"
	if err := os.WriteFile(filepath.Join(dir, other+".jsonl"), bytes.ReplaceAll(first, []byte(qoderInteractiveSession), []byte(other)), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(dir, qoderInteractiveSession+".jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	// each lookup's index is new, so the files are met in another order
	for range 20 {
		if got := qoderLookup(t, home, "interactive-turn1"); got != other {
			t.Fatalf("the first turn matched %q, want the file written last, %q", got, other)
		}
		if got := qoderLookup(t, home, "interactive-turn2"); got != qoderInteractiveSession {
			t.Fatalf("the second turn matched %q, want %q, the one with both prompts", got, qoderInteractiveSession)
		}
	}
}

// qoderGateway is a gateway with one provider, fake, that answers every
// Chat request, and Qoder's folders in home (both builds').
func qoderGateway(t *testing.T, home string) http.Handler {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello from fake.\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("QODER_CONFIG_DIR", home)
	t.Setenv("QODERCN_CONFIG_DIR", home)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"fake-model"}, Chat: up.URL}); err != nil {
		t.Fatal(err)
	}
	return New().Handler()
}

// qoderSend sends a fixture's body as Qoder sent it (its own headers, the
// model named as magpie writes it) from token's agent on loopback, with
// extra headers, and answers the usage record it made.
func qoderSend(t *testing.T, h http.Handler, name, token string, extra map[string]string) usage.Record {
	t.Helper()
	body := bytes.Replace(qoderBody(t, name), []byte(`{"model":"fake-model"`), []byte(`{"model":"fake/fake-model"`), 1)
	var sent map[string]string
	b, err := os.ReadFile(filepath.Join(qoderFixtures, "headers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &sent); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50123"
	for k, v := range sent {
		switch k {
		case "host", "content-length", "connection":
		case "authorization":
			req.Header.Set(k, "Bearer "+token)
		default:
			req.Header.Set(k, v)
		}
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	before := len(usage.Load(time.Time{}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
	}
	for range 200 {
		if rs := usage.Load(time.Time{}); len(rs) > before {
			return rs[len(rs)-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: no usage record", name)
	return usage.Record{}
}

// Qoder asks the first title the moment the prompt is sent, and was seen
// to write the session's file ~0.1s after: a side call (no tools) is held
// for it. A file read while its prompt's line was half written is read on
// from that line.
func TestQoderFirstTitleBeforeItsFile(t *testing.T) {
	home := t.TempDir()
	h := qoderGateway(t, home)
	src := filepath.Join(qoderFixtures, "interactive", "projects", "-Users-yetone-qsess-sb-ihome-proj", qoderInteractiveSession+".jsonl")
	whole, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "projects", "-Users-yetone-qsess-sb-ihome-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, qoderInteractiveSession+".jsonl")
	// the file's first lines, then the prompt's line cut before its
	// prompt: read by the gateway's index, and no session yet
	at := bytes.Index(whole, []byte(`{"type":"user","uuid":"182f0e51`)) + 20
	if err := os.WriteFile(path, whole[:at], 0o600); err != nil {
		t.Fatal(err)
	}
	asked := qoderAsked(qoderBody(t, "title-first"))
	if got := qoderIndexOf(filepath.Join(home, "projects")).find(context.Background(), asked, 0); got != "" {
		t.Fatalf("matched %q before the prompt was written", got)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		os.WriteFile(path, whole, 0o600)
	}()
	t.Cleanup(func() { <-done })
	start := time.Now()
	r := qoderSend(t, h, "title-first", TokenFor("qoder"), nil)
	if r.NativeSession != qoderInteractiveSession {
		t.Errorf("the first title was recorded under %q, want %q", r.NativeSession, qoderInteractiveSession)
	}
	if took := time.Since(start); took < 100*time.Millisecond {
		t.Errorf("answered after %v, before the file was whole", took)
	}
}

// coderlee233 on Discord (qoderclicn 1.1.82): Qoder's requests are
// recorded under the session of Qoder's they are a turn of, as 1.1.81's
// session-id had them, for Qoder and Qoder CN, the title and suggestion
// calls too, so the Sessions page joins them to the session Qoder titled.
// A session the request names itself (an older Qoder's session-id,
// X-Magpie-Session) is kept, and another agent's request is never matched
// to Qoder's files.
func TestQoderSessionThroughGateway(t *testing.T) {
	h := qoderGateway(t, qoderHome(t, "headless", "interactive"))
	for _, tc := range []struct {
		name, token string
		extra       map[string]string
		session     string
		native      string
	}{
		{"interactive-turn2", TokenFor("qoder"), nil, qoderInteractiveSession, qoderInteractiveSession},
		{"title-turn2", TokenFor("qoder"), nil, qoderInteractiveSession, qoderInteractiveSession},
		{"suggestion", TokenFor("qoder-cn"), nil, qoderInteractiveSession, qoderInteractiveSession},
		{"headless-subagent", TokenFor("qoder-cn"), nil, qoderHeadlessSession, qoderHeadlessSession},
		// 1.1.81 named its session in session-id
		{"headless-turn2", TokenFor("qoder"), map[string]string{"session-id": "older-qoder-session"}, "older-qoder-session", "older-qoder-session"},
		{"headless-turn2", TokenFor("qoder"), map[string]string{SessionHeader: "mine"}, "mine", ""},
		// another agent's request holding the same prompt
		{"headless-turn2", TokenFor("pi"), nil, "", ""},
	} {
		r := qoderSend(t, h, tc.name, tc.token, tc.extra)
		if r.Session != tc.session || r.NativeSession != tc.native {
			t.Errorf("%s as %s %v: recorded session %q native %q; want %q, %q", tc.name, tc.token, tc.extra, r.Session, r.NativeSession, tc.session, tc.native)
		}
	}
}
