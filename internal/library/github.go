package library

import (
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// ---- asking GitHub's API with the user's token ----------------------------
//
// GitHub's REST API answers an address 60 requests an hour without a token,
// which a check of a few repositories' skills, or a network shared with
// others, uses up; with a token it answers 5,000 an hour. The token is the
// one set in Settings (GitHubToken), else GITHUB_TOKEN or GH_TOKEN from the
// environment. It goes to GitHub's API alone (githubAPI's host), never to
// codeload or anywhere else, and is never written to a log or an error.

// GitHubTokenEnv names the environment variables a token is taken from when
// Settings has none, in the order they are looked at.
var GitHubTokenEnv = []string{"GITHUB_TOKEN", "GH_TOKEN"}

// GitHubToken is the token requests to GitHub's API carry, and where it
// came from: "settings", the environment variable's name, or "" for none.
func GitHubToken() (token, from string) {
	if t := strings.TrimSpace(settings.Load().GitHubToken); t != "" {
		return t, "settings"
	}
	for _, k := range GitHubTokenEnv {
		if t := strings.TrimSpace(os.Getenv(k)); t != "" {
			return t, k
		}
	}
	return "", ""
}

// githubAPIHost is the host a token may be sent to: GitHub's API's.
func githubAPIHost() string {
	u, err := url.Parse(githubAPI)
	if err != nil {
		return ""
	}
	return u.Host
}

// withGitHubToken has req carry the token, if there is one and req goes to
// GitHub's API; it says whether it did.
func withGitHubToken(req *http.Request) bool {
	if req.URL == nil || req.URL.Host == "" || !strings.EqualFold(req.URL.Host, githubAPIHost()) {
		return false
	}
	t, _ := GitHubToken()
	if t == "" {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+t)
	return true
}

// errLimited is GitHub turning requests away for a while: the API allows
// an address 60 an hour without a token, 5,000 with one.
type errLimited struct {
	until time.Time
	token bool // the requests carried a token
}

func (e errLimited) Error() string {
	when := "for a while"
	if !e.until.IsZero() {
		when = "until " + e.until.Local().Format("15:04")
	}
	if e.token {
		return "GitHub's rate limit for your GitHub token is used up " + when + "; try again then"
	}
	return "GitHub's rate limit for requests without a token (60 an hour) is used up " + when +
		". Add a GitHub token in Settings → Network and sharing to raise it to 5,000 an hour"
}

// limitedBy is the errLimited a refusal of GitHub's says, or false when it
// isn't one: 429, or 403 with the limit spent or a secondary limit's
// Retry-After or message.
func limitedBy(resp *http.Response, body []byte, token bool) (errLimited, bool) {
	msg := strings.ToLower(string(body))
	if resp.StatusCode != 429 && (resp.StatusCode != 403 || resp.Header.Get("X-RateLimit-Remaining") != "0" &&
		resp.Header.Get("Retry-After") == "" && !strings.Contains(msg, "rate limit")) {
		return errLimited{}, false
	}
	e := errLimited{token: token}
	if n, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && n > 0 {
		e.until = time.Unix(n, 0)
	} else if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		e.until = time.Now().Add(time.Duration(s) * time.Second)
	}
	return e, true
}

// etags are GitHub's answers kept by URL, for the next request for one to
// ask if it changed: a 304 doesn't count against the rate limit.
var etags = struct {
	sync.Mutex
	m map[string]etagged
}{m: map[string]etagged{}}

type etagged struct {
	etag string
	body []byte
}
