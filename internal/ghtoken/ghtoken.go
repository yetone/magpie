// Package ghtoken has magpie's requests to GitHub's REST API carry the
// user's GitHub token, and says plainly when GitHub turns them away.
//
// GitHub's API answers an address 60 requests an hour without a token,
// which a check of a few repositories, or a network shared with others,
// uses up; with a token it answers 5,000 an hour. The token is the one set
// in Settings (GitHubToken), else GITHUB_TOKEN or GH_TOKEN from the
// environment. It goes to GitHub's API's host alone, never to codeload, raw
// files or a mirror (source.Do sends no request with Authorization to one),
// and is never written to a log or an error. The library's skills and the
// Plugins page's git plugins both ask GitHub through here (#1942).
//
// A token GitHub refuses (401) is one that no longer reads: the request is
// asked again without it, and the token isn't sent again while magpie runs
// (till on Discord). For a token from the environment that is the whole of
// it. The token set in Settings is the user's own choice, so its refusal is
// also said (SettingsRefused): the checks go on without a token, not with
// the environment's, rather than failing whole, and Settings › Network and
// sharing says that token was refused and needs replacing (#3926).
package ghtoken

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// Env names the environment variables a token is taken from when Settings
// has none, in the order they are looked at.
var Env = []string{"GITHUB_TOKEN", "GH_TOKEN"}

// Token is the token requests to GitHub's API carry, and where it came
// from: "settings", the environment variable's name, or "" for none.
// A token GitHub refused isn't one, wherever it came from. After an
// environment variable's, the next variable's is, or none; after the one
// set in Settings, none: the user chose that one over the environment's,
// and the page says requests go without a token till it is replaced.
func Token() (token, from string) {
	if t := strings.TrimSpace(settings.Shared().GitHubToken); t != "" {
		if isRefused(t) {
			return "", ""
		}
		return t, "settings"
	}
	for _, k := range Env {
		if t := strings.TrimSpace(os.Getenv(k)); t != "" && !isRefused(t) {
			return t, k
		}
	}
	return "", ""
}

// refused holds the tokens GitHub answered 401, Settings' included, for as
// long as magpie runs: keyed by the token, so a variable or a Settings
// field set to another one is tried afresh.
var refused = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// settingsRefused is the token set in Settings that GitHub refused, and
// when, for Settings to say that token needs replacing while the checks
// go on without one.
var settingsRefused struct {
	sync.Mutex
	t  string
	at time.Time
}

func isRefused(t string) bool {
	refused.Lock()
	defer refused.Unlock()
	return refused.m[t]
}

func markRefused(t string) {
	refused.Lock()
	refused.m[t] = true
	refused.Unlock()
}

func markSettingsRefused(t string) {
	settingsRefused.Lock()
	settingsRefused.t, settingsRefused.at = t, time.Now()
	settingsRefused.Unlock()
}

// SettingsRefused is when GitHub refused the token set in Settings, and
// whether it did. From then on the checks ask GitHub without a token until
// that field is changed or magpie restarts, so a stale token costs the user
// the 5,000 an hour it was for and not every check (#3926).
func SettingsRefused() (time.Time, bool) {
	now := strings.TrimSpace(settings.Shared().GitHubToken)
	settingsRefused.Lock()
	defer settingsRefused.Unlock()
	if settingsRefused.t == "" || now != settingsRefused.t {
		return time.Time{}, false
	}
	return settingsRefused.at, true
}

// Do sends req with send, carrying the token as Authorize does, and says
// whether the answer given back is to a request that carried it. When
// GitHub refuses (401) a token, that token is dropped for the rest of the
// run and req is sent again without it: the anonymous answer is the one
// given back, so a check goes on with the 60 an hour rather than failing
// whole. A refusal of the token set in Settings is kept too
// (SettingsRefused), for the page to say which token to replace.
func Do(req *http.Request, api string, send func(*http.Request) (*http.Response, error)) (*http.Response, bool, error) {
	t, from := authorize(req, api)
	resp, err := send(req)
	// a mirror answers a request sent it without the token
	// (source.Do), and its 401 isn't GitHub refusing the token
	if t != "" && err == nil && resp.Request != nil && resp.Request.Header.Get("Authorization") == "" {
		t = ""
	}
	if err != nil || t == "" || resp.StatusCode != http.StatusUnauthorized {
		return resp, t != "" && err == nil, err
	}
	again := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return resp, true, nil
		}
		b, err := req.GetBody()
		if err != nil {
			return resp, true, nil
		}
		again.Body = b
	}
	markRefused(t)
	if from == "settings" {
		markSettingsRefused(t)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	again.Header.Del("Authorization")
	resp, err = send(again)
	return resp, false, err
}

// Authorize has req carry the token, if there is one and req goes to the
// host of api (GitHub's API's base URL, which tests move); it says whether
// it did.
//
// Do is Authorize with an environment token GitHub refuses dropped for an
// anonymous request; a caller sends through it rather than this.
func Authorize(req *http.Request, api string) bool {
	t, _ := authorize(req, api)
	return t != ""
}

// authorize is Authorize, saying which token req carries and where it came
// from, or "" for none.
func authorize(req *http.Request, api string) (token, from string) {
	u, err := url.Parse(api)
	if err != nil || u.Host == "" || req.URL == nil || req.URL.Host == "" || !strings.EqualFold(req.URL.Host, u.Host) {
		return "", ""
	}
	t, from := Token()
	if t == "" {
		return "", ""
	}
	req.Header.Set("Authorization", "Bearer "+t)
	return t, from
}

// Refused is the error for a 401 from GitHub to a request that carried the
// token: which token it was, never the token itself.
func Refused() error {
	_, from := Token()
	where := "the GitHub token in Settings → Network and sharing"
	if _, ok := SettingsRefused(); !ok && from != "settings" {
		where = from + " (no GitHub token is set in Settings → Network and sharing)"
	}
	return fmt.Errorf("GitHub refused %s: it may have expired or been revoked", where)
}

// Limited is GitHub turning requests away for a while: until when (zero
// when GitHub didn't say), and whether the requests carried a token.
type Limited struct {
	Until time.Time
	Token bool
}

func (e Limited) Error() string {
	when := "for a while"
	if !e.Until.IsZero() {
		when = "until " + e.Until.Local().Format("15:04")
	}
	if e.Token {
		return "GitHub's rate limit for your GitHub token is used up " + when + "; try again then"
	}
	return "GitHub's rate limit for requests without a token (60 an hour) is used up " + when +
		". Add a GitHub token in Settings → Network and sharing to raise it to 5,000 an hour"
}

// LimitedBy is the Limited a refusal of GitHub's says, or false when it
// isn't one: 429, or 403 with the limit spent or a secondary limit's
// Retry-After or message. A 403 for anything else (a token without access)
// isn't one.
func LimitedBy(resp *http.Response, body []byte, token bool) (Limited, bool) {
	msg := strings.ToLower(string(body))
	if resp.StatusCode != 429 && (resp.StatusCode != 403 || resp.Header.Get("X-RateLimit-Remaining") != "0" &&
		resp.Header.Get("Retry-After") == "" && !strings.Contains(msg, "rate limit")) {
		return Limited{}, false
	}
	e := Limited{Token: token}
	if n, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && n > 0 {
		e.Until = time.Unix(n, 0)
	} else if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		e.Until = time.Now().Add(time.Duration(s) * time.Second)
	}
	return e, true
}
