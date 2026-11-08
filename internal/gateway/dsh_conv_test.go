package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

// Kumo31 on Discord: dsh's chat-completions and messages routes send no
// session id, so the gateway keys each request by its first prompt's hash
// (conversationID), and the Context page listed dsh's sessions as
// "magpie-a…" where the Sessions page has their titles. The requests here
// are the ones real dsh 0.2.0-rc.2 sent for the two sessions beside them,
// a turn and its title call on each route, headers and body as they came:
// every one's conversation id is one the session's file gives (Convs).
const dshFixture = "../sessions/testdata/dsh-gateway"

const dshChat, dshMsgs = "session-508e7b0a-37c0-41f7-ac62-e48ab3146111", "session-8d93476d-ca34-461c-9e8e-7031c1dedfff"

// dshRequests are the captured requests, by the session each was sent in.
var dshRequests = map[string]string{
	"completions-turn.json":  dshChat,
	"completions-title.json": dshChat,
	"messages-turn.json":     dshMsgs,
	"messages-title.json":    dshMsgs,
}

// dshFixtureSessions lists the fixture's dsh sessions, by id.
func dshFixtureSessions(t *testing.T) map[string]sessions.Session {
	t.Helper()
	// dsh's layout: $DSH_HOME/sessions/<project>/<session>/session.v4.jsonl
	home := t.TempDir()
	if err := os.CopyFS(filepath.Join(home, "sessions"), os.DirFS(filepath.Join(filepath.FromSlash(dshFixture), "sessions"))); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_HOME", home)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	byID := map[string]sessions.Session{}
	for _, s := range sessions.List(0) {
		if s.Agent == "dsh" {
			byID[s.ID] = s
		}
	}
	if len(byID) != 2 || byID[dshChat].Title == "" || byID[dshMsgs].Title == "" {
		t.Fatalf("sessions %+v", byID)
	}
	return byID
}

// dshRequest is a captured request's path, headers and body as dsh sent them.
func dshRequest(t *testing.T, name string) (string, http.Header, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.FromSlash(dshFixture), "requests", name))
	if err != nil {
		t.Fatal(err)
	}
	head, body, _ := bytes.Cut(b, []byte("\n"))
	var captured struct {
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(head, &captured); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for k, v := range captured.Headers {
		h.Set(k, v)
	}
	return captured.Path, h, body
}

func TestDshSessionConvsAreTheGatewayIDs(t *testing.T) {
	fresh(t)
	byID := dshFixtureSessions(t)
	for name, id := range dshRequests {
		_, h, body := dshRequest(t, name)
		conv := conversationID(h, body)
		if !strings.HasPrefix(conv, "magpie-") {
			t.Fatalf("%s: dsh sent a session id now (%s); the join isn't needed for it", name, conv)
		}
		if !slices.Contains(byID[id].Convs, conv) {
			t.Errorf("%s: %s is not among %s's %v", name, conv, id, byID[id].Convs)
		}
		for other, s := range byID {
			if other != id && slices.Contains(s.Convs, conv) {
				t.Errorf("%s: %s is %s's too", name, conv, other)
			}
		}
		t.Logf("%s %s", name, conv)
	}
}

// The same requests sent through the gateway are traced under those ids:
// what reaches conversationID is the body dsh sent, not one rewritten on
// the way in.
func TestDshRoutesCarryTheSessionConvs(t *testing.T) {
	fresh(t)
	byID := dshFixtureSessions(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/messages") {
			io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":1}}`)
			return
		}
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "FAKE", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for name, id := range dshRequests {
		path, h, body := dshRequest(t, name)
		// only the model is changed, to one this gateway has
		body = bytes.Replace(body, []byte(`"model":"group/flash"`), []byte(`"model":"fake/m1"`), 1)
		body = bytes.Replace(body, []byte(`"stream":true`), []byte(`"stream":false`), 1)
		r := httptest.NewRequest("POST", path, bytes.NewReader(body))
		r.Header = h.Clone()
		r.Header.Del("Content-Length")
		r.Header.Del("Accept-Encoding")
		if strings.HasSuffix(name, "-turn.json") {
			// package agent names dsh by its User-Agent, which this
			// package's tests don't register: its token says the same
			r.Header.Set("Authorization", "Bearer "+TokenFor("dsh"))
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		rs := s.Trace(context.Background(), 0, 0).Routes
		var last Route
		for _, x := range rs {
			if x.ID > last.ID {
				last = x
			}
		}
		// (its agent is package agent's to name: "dsh" by its User-Agent,
		// which gui's TestContextDshSessionTitles checks)
		if last.Session != "" || !slices.Contains(byID[id].Convs, last.Conv) {
			t.Errorf("%s: route session %q conv %q, not one of %s's %v", name, last.Session, last.Conv, id, byID[id].Convs)
		}
		if !strings.HasSuffix(name, "-turn.json") {
			continue
		}
		// the messages of its turn, as its own file keeps them, name the
		// session while the request is still answering (gui's ownerLast):
		// the one typed is that session's alone, the runtime context dsh
		// sends after it both sessions' of the folder
		own := 0
		for _, c := range last.LastConvs {
			if !slices.Contains(byID[id].TurnConvs, c) {
				t.Errorf("%s: turn message %s is not among %s's %v", name, c, id, byID[id].TurnConvs)
			}
			alone := true
			for other, s := range byID {
				if other != id && slices.Contains(s.TurnConvs, c) {
					alone = false
				}
			}
			if alone {
				own++
			}
		}
		if len(last.LastConvs) != 2 || own != 1 {
			t.Errorf("%s: turn messages %v, %d of them %s's alone; want the typed one and the runtime context", name, last.LastConvs, own, id)
		}
	}
}
