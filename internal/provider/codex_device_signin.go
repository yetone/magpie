package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A ChatGPT sign-in by device code, as `codex login --device-auth` does it
// (codex-rs login/src/device_code_auth.rs): magpie asks for a code, the
// user enters it on ChatGPT's device page in any browser, and magpie asks
// until the page is done. Nothing comes back to localhost:1455, so magpie
// on a server or in Docker, its window in a browser elsewhere, signs in
// with no address to copy back (curtinp118 on Discord).

// codexIssuer is where Codex's device sign-in is; a var so tests can point
// it elsewhere.
var codexIssuer = "https://auth.openai.com"

// CodexDeviceSite is the "site" of a ChatGPT sign-in by device code
// (StartSignInWith): the window asks for it when it is opened from
// another computer.
const CodexDeviceSite = "device"

// codexDeviceWait is how long Codex keeps a device code: 15 minutes.
var codexDeviceWait = 15 * time.Minute

// codexDevicePollMisses is how many failed polls in a row end the sign-in.
var codexDevicePollMisses = 5

// errCodexDeviceOff is the vendor turning device codes away for Codex's client.
var errCodexDeviceOff = errors.New("ChatGPT doesn't take a device code for this sign-in now; use the browser sign-in")

// codexDevicePost posts body as JSON with Codex's own headers. It answers
// the status and the reply.
func codexDevicePost(ctx context.Context, u string, body any) (int, []byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	codexClientHeaders(req.Header, codexVersion())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, out, nil
}

// startCodexDeviceSignIn asks ChatGPT for a device code and waits, in the
// background, for the user to enter it.
func startCodexDeviceSignIn(s *signInFlow) error {
	base := strings.TrimSuffix(codexIssuer, "/")
	ctx, cancel := context.WithCancel(s.via(context.Background()))
	code, b, err := codexDevicePost(ctx, base+"/api/accounts/deviceauth/usercode", map[string]string{"client_id": codexClientID})
	if err != nil {
		cancel()
		return fmt.Errorf("couldn't reach ChatGPT for a sign-in code: %w", err)
	}
	if code == http.StatusNotFound {
		cancel()
		return errCodexDeviceOff
	}
	if code != http.StatusOK {
		cancel()
		return fmt.Errorf("ChatGPT gave no sign-in code (%d): %s", code, APIError(b, http.StatusText(code)))
	}
	var uc struct {
		ID       string `json:"device_auth_id"`
		Code     string `json:"user_code"`
		Code2    string `json:"usercode"`
		Interval any    `json:"interval"` // a string of seconds, "5"
	}
	if err := json.Unmarshal(b, &uc); err != nil {
		cancel()
		return fmt.Errorf("ChatGPT's sign-in code didn't read: %w", err)
	}
	uc.Code = firstNonEmpty(uc.Code, uc.Code2)
	if uc.ID == "" || uc.Code == "" {
		cancel()
		return errors.New("ChatGPT gave no sign-in code")
	}
	secs := 0
	switch v := uc.Interval.(type) {
	case string:
		secs, _ = strconv.Atoi(strings.TrimSpace(v))
	case float64:
		secs = int(v)
	}
	s.mu.Lock()
	s.st.URL, s.st.Code = base+"/codex/device", uc.Code
	s.stop = cancel
	s.mu.Unlock()
	interval := devicePoll(secs)
	go func() {
		fail := func(msg string) {
			log.Printf("codex device sign-in failed: %s", msg)
			s.finish(SignInState{State: "failed", Error: msg})
		}
		deadline := time.Now().Add(codexDeviceWait)
		misses := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			if time.Now().After(deadline) {
				fail("the code expired; start again")
				return
			}
			code, b, err := codexDevicePost(ctx, base+"/api/accounts/deviceauth/token",
				map[string]string{"device_auth_id": uc.ID, "user_code": uc.Code})
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				// a hiccup: ask again, unless it keeps failing
				if misses++; misses >= codexDevicePollMisses {
					fail("couldn't ask ChatGPT whether the code was entered: " + err.Error())
					return
				}
				continue
			case code == http.StatusForbidden || code == http.StatusNotFound:
				// not entered yet
				misses = 0
				continue
			case code != http.StatusOK:
				fail(fmt.Sprintf("ChatGPT refused the sign-in (%d): %s", code, APIError(b, http.StatusText(code))))
				return
			}
			var ok struct {
				Code     string `json:"authorization_code"`
				Verifier string `json:"code_verifier"`
			}
			if json.Unmarshal(b, &ok) != nil || ok.Code == "" || ok.Verifier == "" {
				fail("ChatGPT finished the sign-in but sent no code")
				return
			}
			ectx, ecancel := context.WithTimeout(ctx, 30*time.Second)
			l, err := codexExchange(ectx, ok.Code, ok.Verifier, base+"/deviceauth/callback")
			if err == nil {
				var using bool
				err = keepLogin(ectx, func() (err error) { using, err = addLogin(l); return err })
				if err == nil {
					ecancel()
					s.finish(SignInState{State: "done", User: l.User, Plan: l.Plan, Using: using})
					return
				}
			}
			ecancel()
			if ctx.Err() != nil {
				return
			}
			fail(err.Error())
			return
		}
	}()
	return nil
}
