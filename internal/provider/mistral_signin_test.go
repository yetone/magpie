package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMistralConsole is console.mistral.ai's Vibe sign-in as it answered a
// real create and poll (2026-11-23): the create gives a sign-in page under
// /codestral/cli/authenticate and a poll under /api/vibe/sign-in/poll/,
// the poll answers pending, and the exchange gives the key. polls are the
// poll's replies in turn (the last one repeats); exchange is its reply.
type fakeMistralConsole struct {
	srv       *httptest.Server
	mu        sync.Mutex
	polls     []string // "pending", "completed", "denied", "expired", "error", "410", "500"
	exchange  func(w http.ResponseWriter)
	challenge string
	verifier  string
	token     string
	signIn    string // the sign_in_url given, default the console's own page
	poll      string // the poll_url given
}

func newFakeMistralConsole(t *testing.T) *fakeMistralConsole {
	t.Helper()
	f := &fakeMistralConsole{exchange: func(w http.ResponseWriter) {
		io.WriteString(w, `{"api_key":"vibe-plan-key-1"}`)
	}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/vibe/sign-in":
			var b struct{ CodeChallenge, CodeChallengeMethod string }
			raw, _ := io.ReadAll(r.Body)
			var m map[string]string
			_ = json.Unmarshal(raw, &m)
			b.CodeChallenge, b.CodeChallengeMethod = m["code_challenge"], m["code_challenge_method"]
			if b.CodeChallengeMethod != "S256" || b.CodeChallenge == "" {
				w.WriteHeader(422)
				return
			}
			f.challenge = b.CodeChallenge
			signIn := f.signIn
			if signIn == "" {
				signIn = f.srv.URL + "/codestral/cli/authenticate?process_id=p-1&complete_token=c"
			}
			poll := f.poll
			if poll == "" {
				poll = f.srv.URL + "/api/vibe/sign-in/poll/tok"
			}
			json.NewEncoder(w).Encode(map[string]string{"process_id": "p-1", "sign_in_url": signIn, "poll_url": poll,
				"expires_at": time.Now().Add(10 * time.Minute).UTC().Format("2006-01-02T15:04:05.000000+00:00")})
		case r.Method == http.MethodGet && r.URL.Path == "/api/vibe/sign-in/poll/tok":
			s := "pending"
			if len(f.polls) > 0 {
				s = f.polls[0]
				if len(f.polls) > 1 {
					f.polls = f.polls[1:]
				}
			}
			switch s {
			case "410":
				w.WriteHeader(http.StatusGone)
			case "500":
				w.WriteHeader(500)
			case "completed":
				io.WriteString(w, `{"status":"completed","exchange_token":"ex-1"}`)
			case "error":
				io.WriteString(w, `{"status":"error","message":"Workspace suspended."}`)
			default:
				io.WriteString(w, `{"status":"`+s+`"}`)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/api/vibe/sign-in/p-1/exchange":
			var m map[string]string
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &m)
			f.verifier, f.token = m["code_verifier"], m["exchange_token"]
			f.exchange(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	api, poll := mistralConsoleAPI, mistralSignInPoll
	mistralConsoleAPI, mistralSignInPoll = f.srv.URL+"/api", 5*time.Millisecond
	t.Cleanup(func() { mistralConsoleAPI, mistralSignInPoll = api, poll })
	return f
}

// waitMistral reads the sign-in's state until it is no longer pending.
func waitMistral(t *testing.T, id string) (state, why, key string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if state, why, key = MistralSignInState(id); state != "pending" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sign-in %s still pending", id)
	return
}

// A Vibe plan's key is got the way Vibe's own browser sign-in gets it: the
// page the user signs in on, a poll until they have, then the key traded
// for with the verifier the challenge was made from. The key is given
// once.
func TestMistralSignInGetsVibeKey(t *testing.T) {
	f := newFakeMistralConsole(t)
	f.polls = []string{"pending", "pending", "completed"}
	id, page, err := StartMistralSignIn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(page, f.srv.URL+"/codestral/cli/authenticate?") {
		t.Fatalf("page = %q", page)
	}
	state, why, key := waitMistral(t, id)
	if state != "done" || key != "vibe-plan-key-1" || why != "" {
		t.Fatalf("got %q %q %q", state, why, key)
	}
	f.mu.Lock()
	sum := sha256.Sum256([]byte(f.verifier))
	if f.verifier == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge || f.token != "ex-1" {
		t.Fatalf("exchange had verifier %q (challenge %q) and token %q", f.verifier, f.challenge, f.token)
	}
	f.mu.Unlock()
	if s, _, k := MistralSignInState(id); s != "" || k != "" {
		t.Fatalf("read again: %q %q", s, k)
	}
}

// A sign-in that ends on Mistral's side says why, in words the user can
// act on: refused, run out (as a status or a 410), Mistral's own message,
// a workspace that can't make a Vibe key (Vibe's 9004), polls that keep
// failing.
func TestMistralSignInEnds(t *testing.T) {
	for _, c := range []struct {
		name     string
		polls    []string
		exchange func(w http.ResponseWriter)
		want     string
	}{
		{"denied", []string{"denied"}, nil, "refused"},
		{"expired", []string{"pending", "expired"}, nil, "ran out"},
		{"gone", []string{"410"}, nil, "ran out"},
		{"error", []string{"error"}, nil, "Workspace suspended."},
		{"failing", []string{"500"}, nil, "500"},
		{"one bad poll", []string{"500", "completed"}, nil, ""},
		{"workspace", []string{"completed"}, func(w http.ResponseWriter) {
			w.WriteHeader(403)
			io.WriteString(w, `{"detail":"forbidden","error_code":9004}`)
		}, "pick another workspace"},
		{"no key", []string{"completed"}, func(w http.ResponseWriter) { io.WriteString(w, `{}`) }, "without a key"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeMistralConsole(t)
			f.polls = c.polls
			if c.exchange != nil {
				f.exchange = c.exchange
			}
			id, _, err := StartMistralSignIn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			state, why, key := waitMistral(t, id)
			if c.want == "" {
				if state != "done" || key == "" {
					t.Fatalf("got %q %q", state, why)
				}
				return
			}
			if state != "failed" || key != "" || !strings.Contains(why, c.want) {
				t.Fatalf("got %q %q %q, want failed with %q", state, why, key, c.want)
			}
		})
	}
}

// As Vibe does, magpie opens no sign-in page and asks no poll that the
// console's reply puts anywhere but the console (the poll under its /api).
func TestMistralSignInStaysOnTheConsole(t *testing.T) {
	for _, c := range []struct{ name, signIn, poll string }{
		{"page elsewhere", "https://evil.example/codestral/cli/authenticate", ""},
		{"page with user", "", ""},
		{"poll elsewhere", "", "https://evil.example/api/vibe/sign-in/poll/tok"},
		{"poll outside api", "", "/vibe/sign-in/poll/tok"},
		{"poll climbs out", "", "/api/../vibe/poll/tok"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeMistralConsole(t)
			f.signIn = c.signIn
			if c.name == "page with user" {
				f.signIn = strings.Replace(f.srv.URL, "://", "://x@", 1) + "/codestral/cli/authenticate"
			}
			if strings.HasPrefix(c.poll, "/") {
				f.poll = f.srv.URL + c.poll
			} else {
				f.poll = c.poll
			}
			if _, _, err := StartMistralSignIn(context.Background()); err == nil || !strings.Contains(err.Error(), "outside console.mistral.ai") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// A cancelled sign-in is forgotten and stops asking.
func TestMistralSignInCancel(t *testing.T) {
	newFakeMistralConsole(t)
	id, _, err := StartMistralSignIn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	CancelMistralSignIn(id)
	if s, _, _ := MistralSignInState(id); s != "" {
		t.Fatalf("state after cancel = %q", s)
	}
}
