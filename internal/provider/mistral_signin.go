package provider

// A Mistral Vibe plan (Pro, Student, Team: the plans that come with a
// month of Vibe) is used through a key of the plan's, which spends the
// plan's Vibe month before any pay as you go (mistral_plan.go). Mistral
// lets that key be used in any software (its team on r/MistralAI,
// "Using Vibe API key in other clients than Vibe": "You can use the Vibe
// API key with third-party software!"). magpie gets one the way Mistral's
// own Vibe CLI does at its browser sign-in (mistralai/mistral-vibe,
// vibe/setup/auth/browser_sign_in.py and http_browser_sign_in_gateway.py):
//
//  1. POST console.mistral.ai/api/vibe/sign-in {code_challenge,
//     code_challenge_method: S256} gives a process_id, the sign_in_url the
//     user signs in on in their own browser (on console.mistral.ai), the
//     poll_url (under console.mistral.ai/api) and when it expires_at;
//  2. GET poll_url answers {"status":"pending"} until the user has signed
//     in, then "completed" with an exchange_token; "expired", "denied" or
//     "error" (with a message) end it, as a 410 does;
//  3. POST /api/vibe/sign-in/{process_id}/exchange {exchange_token,
//     code_verifier} gives {"api_key": …}: a key of the workspace picked in
//     the browser, listed in the console under Code › Vibe CLI. A 403 with
//     error_code 9004 is a workspace that can't make one.
//
// The key is handed to the editor once, and is the provider's when the
// user saves it: from there it is a Mistral key like any pasted one.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/redact"
)

// mistralConsoleAPI is where the sign-in is asked; a test's fake console
// stands in for it.
var mistralConsoleAPI = "https://console.mistral.ai/api"

// mistralSignInPoll is how often the console is asked whether the user has
// signed in, Vibe's poll_interval; a test shortens it.
var mistralSignInPoll = 3 * time.Second

// a sign-in under way, by its own id
type mistralFlow struct {
	state  string // "pending", "done" or "failed"
	err    string
	key    string
	cancel context.CancelFunc
}

var mistralFlows = struct {
	sync.Mutex
	m map[string]*mistralFlow
}{m: map[string]*mistralFlow{}}

// mistralPKCE is a code verifier and its S256 challenge, as Vibe makes
// them (secrets.token_urlsafe(64)).
func mistralPKCE() (verifier, challenge string) {
	b := make([]byte, 64)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// mistralAsk sends a JSON body (or none, a GET) to the console, the reply
// read whole (up to 1MB).
func mistralAsk(ctx context.Context, method, u string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, b, err
}

// mistralUnder reports whether u is at base's origin and under its path,
// as Vibe checks the URLs the console gives back before it opens or asks
// them: a reply can't send the browser, or the poll, anywhere else.
func mistralUnder(u, base string) bool {
	a, err1 := url.Parse(u)
	b, err2 := url.Parse(base)
	if err1 != nil || err2 != nil || a.User != nil || !strings.EqualFold(a.Scheme, b.Scheme) || !strings.EqualFold(a.Host, b.Host) {
		return false
	}
	ap, _ := url.PathUnescape(a.EscapedPath())
	ap = path.Clean("/" + ap)
	bp := strings.TrimSuffix(path.Clean("/"+b.Path), "/")
	return bp == "" || ap == bp || strings.HasPrefix(ap, bp+"/")
}

// mistralBrowserBase is the console's own origin, where the sign-in page is.
func mistralBrowserBase() string {
	u, err := url.Parse(mistralConsoleAPI)
	if err != nil {
		return mistralConsoleAPI
	}
	return u.Scheme + "://" + u.Host
}

// StartMistralSignIn starts a sign-in to Mistral for a Vibe key: the id
// to ask MistralSignInState with, and the page the user signs in on, in
// their own browser. magpie waits on it itself until the page runs out.
func StartMistralSignIn(ctx context.Context) (id, page string, err error) {
	verifier, challenge := mistralPKCE()
	code, raw, err := mistralAsk(ctx, http.MethodPost, mistralConsoleAPI+"/vibe/sign-in",
		map[string]string{"code_challenge": challenge, "code_challenge_method": "S256"})
	if err != nil {
		return "", "", fmt.Errorf("couldn't reach Mistral: %w", err)
	}
	var r struct {
		ProcessID string `json:"process_id"`
		SignInURL string `json:"sign_in_url"`
		PollURL   string `json:"poll_url"`
		ExpiresAt string `json:"expires_at"`
	}
	if code >= 300 || json.Unmarshal(raw, &r) != nil || r.ProcessID == "" || r.SignInURL == "" || r.PollURL == "" {
		return "", "", fmt.Errorf("Mistral didn't start a sign-in: %d %s", code, volcHead(raw))
	}
	if !mistralUnder(r.SignInURL, mistralBrowserBase()) || !mistralUnder(r.PollURL, mistralConsoleAPI) {
		return "", "", errors.New("Mistral's sign-in pointed outside console.mistral.ai: magpie didn't open it")
	}
	life := 10 * time.Minute
	if t, err := time.Parse(time.RFC3339Nano, r.ExpiresAt); err == nil {
		if d := time.Until(t); d > 0 && d < 30*time.Minute {
			life = d
		}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id = base64.RawURLEncoding.EncodeToString(b)
	wait, cancel := context.WithTimeout(context.Background(), life)
	f := &mistralFlow{state: "pending", cancel: cancel}
	mistralFlows.Lock()
	mistralFlows.m[id] = f
	mistralFlows.Unlock()
	go func() {
		defer cancel()
		key, err := mistralWait(wait, r.ProcessID, r.PollURL, verifier)
		mistralFlows.Lock()
		defer mistralFlows.Unlock()
		if mistralFlows.m[id] != f {
			return // cancelled
		}
		if err != nil {
			f.state, f.err = "failed", redact.Scrub(err.Error())
			return
		}
		f.state, f.key = "done", key
	}()
	return id, r.SignInURL, nil
}

// MistralSignInState is how the sign-in id goes: "pending", "done" with
// the key, or "failed" with why; "" for one magpie doesn't have. The key
// is given once: a finished sign-in is forgotten when it is read.
func MistralSignInState(id string) (state, why, key string) {
	mistralFlows.Lock()
	defer mistralFlows.Unlock()
	f := mistralFlows.m[id]
	if f == nil {
		return "", "", ""
	}
	if f.state != "pending" {
		delete(mistralFlows.m, id)
	}
	return f.state, f.err, f.key
}

// CancelMistralSignIn stops waiting on the sign-in id.
func CancelMistralSignIn(id string) {
	mistralFlows.Lock()
	defer mistralFlows.Unlock()
	if f := mistralFlows.m[id]; f != nil {
		f.cancel()
		delete(mistralFlows.m, id)
	}
}

// mistralWait polls the sign-in until the user has signed in, then trades
// it for the key, as Vibe's _wait_for_completion and exchange do: three
// polls in a row that fail end it.
func mistralWait(ctx context.Context, process, poll, verifier string) (string, error) {
	t := time.NewTimer(mistralSignInPoll)
	defer t.Stop()
	failed := 0
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", errors.New("the sign-in page ran out before you signed in: sign in again")
			}
			return "", ctx.Err()
		case <-t.C:
		}
		t.Reset(mistralSignInPoll)
		code, raw, err := mistralAsk(ctx, http.MethodGet, poll, nil)
		if code == http.StatusGone {
			return "", errors.New("the sign-in page ran out before you signed in: sign in again")
		}
		var r struct {
			Status        string `json:"status"`
			ExchangeToken string `json:"exchange_token"`
			Message       string `json:"message"`
		}
		if err != nil || code >= 300 || json.Unmarshal(raw, &r) != nil {
			if ctx.Err() != nil {
				continue
			}
			if failed++; failed >= 3 {
				if err != nil {
					return "", fmt.Errorf("couldn't ask Mistral how the sign-in goes: %w", err)
				}
				return "", fmt.Errorf("Mistral didn't say how the sign-in goes: %d %s", code, volcHead(raw))
			}
			continue
		}
		failed = 0
		switch r.Status {
		case "pending":
			continue
		case "completed":
			if r.ExchangeToken == "" {
				return "", errors.New("you signed in, but Mistral gave nothing to finish it with: sign in again")
			}
			return mistralExchange(ctx, process, r.ExchangeToken, verifier)
		case "expired":
			return "", errors.New("the sign-in page ran out before you signed in: sign in again")
		case "denied":
			return "", errors.New("the sign-in was refused on Mistral's page")
		case "error":
			if m := strings.TrimSpace(r.Message); m != "" {
				return "", fmt.Errorf("Mistral: %s", m)
			}
			return "", errors.New("Mistral ended the sign-in with an error")
		default:
			return "", fmt.Errorf("Mistral answered the sign-in in a form magpie doesn't know: %s", volcHead(raw))
		}
	}
}

// mistralExchange trades a finished sign-in for its key.
func mistralExchange(ctx context.Context, process, token, verifier string) (string, error) {
	code, raw, err := mistralAsk(ctx, http.MethodPost, mistralConsoleAPI+"/vibe/sign-in/"+url.PathEscape(process)+"/exchange",
		map[string]string{"exchange_token": token, "code_verifier": verifier})
	if err != nil {
		return "", fmt.Errorf("couldn't reach Mistral to finish the sign-in: %w", err)
	}
	var r struct {
		APIKey    string `json:"api_key"`
		ErrorCode int    `json:"error_code"`
	}
	_ = json.Unmarshal(raw, &r)
	if code == http.StatusForbidden && r.ErrorCode == 9004 {
		return "", errors.New("this Mistral workspace can't make a Vibe key: sign in again and pick another workspace, or ask its admin")
	}
	if code >= 300 {
		return "", fmt.Errorf("Mistral didn't finish the sign-in: %d %s", code, volcHead(raw))
	}
	if strings.TrimSpace(r.APIKey) == "" {
		return "", errors.New("Mistral finished the sign-in without a key: sign in again")
	}
	return strings.TrimSpace(r.APIKey), nil
}
