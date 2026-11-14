// Package source chooses where magpie reads from, with a mirror as a
// fallback when the official address can't be reached. The official source
// stays first; a mirror is only asked when it fails. The Plugins page's
// 「国内镜像」 switch (China) puts a mirror in China first for the few
// addresses one serves byte for byte.
package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

const (
	npmMirror    = "https://registry.npmmirror.com"
	githubMirror = "https://gh-proxy.com"
	// with the 「国内镜像」 switch: Bun's releases at npmmirror, and the
	// plugin list at jsDelivr, both copies of the very files
	bunMirror  = "https://registry.npmmirror.com/-/binary/bun"
	listMirror = "https://cdn.jsdelivr.net/gh/magpie-community/plugins@"
	hfMirror   = "https://hf-mirror.com"
)

// NPMMirror is npm's registry in China, npmmirror.
const NPMMirror = npmMirror

// China is whether the Plugins page's 「国内镜像」 switch is on
// (settings.ChinaMirror); a var for tests.
var China = func() bool { return settings.Shared().ChinaMirror }

// chinaFirst is the mirror in China asked before raw with the switch on,
// "" when there is none. Only mirrors that serve the same bytes as the
// official address: npmmirror for npm's registry and Bun's releases (whose
// zips are checked against Bun's checksums all the same), jsDelivr for the
// community repo's plugin list, hf-mirror for the masking models. Never a proxy such as gh-proxy, which
// would hand magpie whatever it liked.
func chinaFirst(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	switch strings.ToLower(u.Hostname()) {
	case "registry.npmjs.org":
		return npmMirror + u.RequestURI()
	case "github.com":
		if rest, ok := strings.CutPrefix(u.EscapedPath(), "/oven-sh/bun/releases/download/"); ok && rest != "" {
			return bunMirror + "/" + rest
		}
	case "huggingface.co":
		return modelMirror(u)
	case "raw.githubusercontent.com":
		// /magpie-community/plugins/<branch>/<path>
		if rest, ok := strings.CutPrefix(u.EscapedPath(), "/magpie-community/plugins/"); ok {
			if ref, path, ok := strings.Cut(rest, "/"); ok && ref != "" && path != "" && u.RawQuery == "" {
				return listMirror + ref + "/" + path
			}
		}
	}
	return ""
}

// ChinaFirst is the mirror in China the 「国内镜像」 switch asks before raw,
// "" with the switch off or when no mirror serves raw.
func ChinaFirst(raw string) string {
	if !China() {
		return ""
	}
	return chinaFirst(raw)
}

// modelMirror is hf-mirror's copy of one of the local masking models'
// files (internal/pii), "" for anything else of Hugging Face's. Those
// files are fetched at the revision their sha256 were pinned at, and what
// hf-mirror serves is checked against those pins, so it is asked with the
// switch off too, after Hugging Face: where huggingface.co is blocked or
// poisoned (a *.facebook.com certificate, EOF, a handshake that times out;
// hh0592821 on Discord) the model still comes.
func modelMirror(u *url.URL) string {
	if u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "huggingface.co") || u.RawQuery != "" {
		return ""
	}
	for _, repo := range []string{"/LocalAI-io/privacy-filter-GGUF/resolve/", "/QuantumNous/astrlink-guard/resolve/"} {
		if strings.HasPrefix(u.EscapedPath(), repo) {
			return hfMirror + u.EscapedPath()
		}
	}
	return ""
}

// Enabled reports whether mirror fallback was turned on with
// MAGPIE_MIRRORS=on (or true, 1, cn).
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MAGPIE_MIRRORS"))) {
	case "on", "true", "1", "cn":
		return true
	}
	return false
}

func npmBase() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("MAGPIE_NPM_REGISTRY")), "/"); v != "" {
		return v
	}
	return npmMirror
}

func githubBase() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("MAGPIE_GITHUB_MIRROR")), "/"); v != "" {
		return v
	}
	return githubMirror
}

// URLs is the official address followed by its mirrors, or just the
// official one when there is no mirror for it; with the 「国内镜像」 switch
// on, a mirror in China comes before it. A masking model's file has
// hf-mirror after it with no switch at all (modelMirror); every other
// fallback mirror waits for MAGPIE_MIRRORS.
func URLs(raw string) []string {
	out := faithful(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return out
	}
	if m := modelMirror(u); m != "" {
		// pinned by sha256: no switch needed (modelMirror)
		return distinct(append(out, m)...)
	}
	if !Enabled() {
		return out
	}
	switch strings.ToLower(u.Hostname()) {
	case "registry.npmjs.org":
		return distinct(append(out, npmBase()+u.RequestURI())...)
	case "github.com", "api.github.com", "raw.githubusercontent.com", "codeload.github.com":
		return distinct(append(out, githubBase()+"/"+raw)...)
	}
	return out
}

// faithful is raw after its mirror in China when the switch is on, raw
// alone when not.
func faithful(raw string) []string {
	if China() {
		if m := chinaFirst(raw); m != "" {
			return []string{m, raw}
		}
	}
	return []string{raw}
}

func distinct(urls ...string) []string {
	out := []string{}
	for _, u := range urls {
		if u != "" && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out
}

// Do sends req as usual and, when the official answer fails in a way a
// mirror could answer, tries its mirrors in order. A request that carries
// credentials is never sent to a mirror with them: GitHub's, whose only
// one is the user's token, goes to a mirror without it (bare).
func Do(c *http.Client, req *http.Request) (*http.Response, error) {
	if !mirrorable(req) && !bare(req) {
		return do(c, req, []string{req.URL.String()})
	}
	return do(c, req, URLs(req.URL.String()))
}

// bare is whether req's one credential is an Authorization header to
// GitHub, whose answers come without it too, at the anonymous rate: a
// mirror is then asked without the header, and never sees the token. A
// GitHub token in Settings turned the mirror off for every request to
// GitHub's API (MYCF711, #3923).
func bare(req *http.Request) bool {
	if req.URL == nil || req.Header.Get("Authorization") == "" {
		return false
	}
	switch strings.ToLower(req.URL.Hostname()) {
	case "github.com", "api.github.com", "raw.githubusercontent.com", "codeload.github.com":
	default:
		return false
	}
	r := req.Clone(req.Context())
	r.Header.Del("Authorization")
	return mirrorable(r)
}

// mirrorable is whether req may be sent to a mirror. Credentials stay with
// the official source: a mirror must never see a token, cookie or key.
func mirrorable(req *http.Request) bool {
	if req.URL == nil || req.URL.User != nil {
		return false
	}
	for k := range req.URL.Query() {
		k = strings.ToLower(k)
		if strings.Contains(k, "token") || strings.Contains(k, "key") ||
			strings.Contains(k, "secret") || strings.Contains(k, "password") ||
			strings.Contains(k, "auth") {
			return false
		}
	}
	for _, h := range []string{
		"Authorization",
		"Proxy-Authorization",
		"Cookie",
		"X-Api-Key",
		"Api-Key",
		"Private-Token",
	} {
		if req.Header.Get(h) != "" {
			return false
		}
	}
	return true
}

// DoOfficial sends req to its official address alone. It is for a file
// whose checksum must come from the same trusted place as the file itself.
func DoOfficial(c *http.Client, req *http.Request) (*http.Response, error) {
	return do(c, req, []string{req.URL.String()})
}

// DoFaithful sends req to its official address, after its mirror in China
// when the 「国内镜像」 switch is on, but never to a fallback mirror: for a
// file nothing checks, which only a copy of the very file may stand in for.
func DoFaithful(c *http.Client, req *http.Request) (*http.Response, error) {
	if !mirrorable(req) && !bare(req) {
		return DoOfficial(c, req)
	}
	return do(c, req, faithful(req.URL.String()))
}

func do(c *http.Client, req *http.Request, urls []string) (*http.Response, error) {
	var last error
	// a mirror in China is asked before the official address: whatever
	// it can't answer (a version it hasn't copied yet, say) is asked of
	// the official one
	official := slices.Index(urls, req.URL.String())
	for i, raw := range urls {
		r := req.Clone(req.Context())
		if raw != req.URL.String() {
			u, err := url.Parse(raw)
			if err != nil {
				last = err
				continue
			}
			r.URL = u
			r.Host = ""
			// only a request bare allows has one here (Do)
			r.Header.Del("Authorization")
			if r.Body != nil {
				if r.GetBody == nil {
					break
				}
				body, err := r.GetBody()
				if err != nil {
					last = err
					break
				}
				r.Body = body
			}
		}
		var cancel context.CancelFunc
		var stop func() bool
		if len(urls) > 1 {
			ctx, cancelCandidate, stopCandidate := candidateContext(r.Context(), c, i < len(urls)-1)
			if ctx != r.Context() {
				r = r.WithContext(ctx)
				cancel = cancelCandidate
				stop = stopCandidate
			}
		}
		resp, err := c.Do(r)
		if err == nil && (!retryable(resp) && (i >= official || resp.StatusCode < 400) || i == len(urls)-1) {
			if stop != nil {
				stop()
			}
			return resp, nil
		}
		if cancel != nil {
			cancel()
		}
		if err == nil {
			resp.Body.Close()
			last = errors.Join(last, fmt.Errorf("%s: %s", raw, resp.Status))
			continue
		}
		last = errors.Join(last, fmt.Errorf("%s: %w", raw, err))
	}
	if last == nil {
		last = fmt.Errorf("no address for %s", req.URL)
	}
	return nil, last
}

// candidateContext bounds a source's try to half of the request's
// remaining budget, so an official source that hangs doesn't make the
// next one's try double the whole wait. The last source keeps all of
// the time left. The timer only guards the wait for response headers;
// stop leaves the response's context alive for its body.
func candidateContext(ctx context.Context, c *http.Client, more bool) (context.Context, context.CancelFunc, func() bool) {
	if !more {
		return ctx, func() {}, func() bool { return false }
	}
	var d time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		d = time.Until(deadline) / 2
	} else if c.Timeout > 0 {
		d = c.Timeout / 2
	}
	if d <= 0 {
		return ctx, func() {}, func() bool { return false }
	}
	ctx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(d, cancel)
	return ctx, cancel, timer.Stop
}

func retryable(resp *http.Response) bool {
	return resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= 500 ||
		(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0")
}
