package provider

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// OpenCode's two gateways serve different free models. Zen's free tier
// (…/zen) serves its -free models, big-pickle and the rest models.dev's
// opencode prices at nothing, to anyone OpenCode is signed out as (the
// public key, OpenCodeAnonymousKey); Go (…/zen/go) serves a few of its
// own (beam-free and iceman-contributor-free on 2026-11-02) to a Go key
// alone. Asked a model it doesn't serve, Go answers 400 "Upstream request
// failed: Model is unavailable." before it looks at the key. So a free
// model of Zen's asked on an OpenCode Go provider — what a user who wants
// "OpenCode's free models", as OpenCode serves them signed out, ends up
// with when they add the provider named OpenCode Go (51bit.eth on Discord,
// in Claude Code: every request failed so, and a header of their own
// didn't help) — is asked again at Zen's free tier with the public key,
// which is where OpenCode itself asks it. Refused there too, the refusal
// says what each gateway said and where the model is served.

// openCodeUnavailable is whether b is the refusal OpenCode's gateway
// gives a model it doesn't serve.
func openCodeUnavailable(status int, b []byte) bool {
	return status == http.StatusBadRequest && strings.Contains(apiError(b, ""), "Model is unavailable")
}

// openCodeGoToZenFree is the URL at Zen's free tier of a request on proto
// that went to Go's gateway at u, "" when u isn't Go's: …/zen/go/v1/… and
// …/inference/go/v1/… are Zen's …/zen/v1/… asked where OpenCode 2 asks the
// free tier (OpenCodeFreeURL), and Go's inference URLs by API
// (…/inference/go/openai/v1/…) are Zen's beside them.
func openCodeGoToZenFree(u string, proto Protocol) string {
	pu, err := url.Parse(u)
	if err != nil || openCodeGateway(u) != "go" {
		return ""
	}
	seg := strings.SplitN(strings.TrimPrefix(pu.Path, "/"), "/", 3) // zen|inference, go, rest
	rest := ""
	if len(seg) == 3 {
		rest = "/" + seg[2]
	}
	if strings.HasPrefix(rest, "/openai/") || strings.HasPrefix(rest, "/anthropic/") {
		pu.Path = "/inference" + rest
		pu.RawPath = ""
		return pu.String()
	}
	pu.Path = "/zen" + rest
	pu.RawPath = ""
	return OpenCodeFreeURL(pu.String(), proto)
}

// RetryOpenCodeFreeOnZen asks Zen's free tier for a free model p's OpenCode
// Go gateway answered "Model is unavailable" to (req, res), with the public
// key in place of the user's, and returns what Zen answered when it served
// it. Otherwise res is returned, its message saying where the model is
// served. Any other request or answer is res as it was.
func RetryOpenCodeFreeOnZen(p Provider, proto Protocol, req *http.Request, res *http.Response, do func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if res == nil || res.StatusCode != http.StatusBadRequest || req.GetBody == nil || openCodeGateway(req.URL.String()) != "go" {
		return res, nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	if !openCodeUnavailable(res.StatusCode, b) {
		return res, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return res, nil
	}
	sent, _ := io.ReadAll(body)
	body.Close()
	model := gjson.GetBytes(sent, "model").String()
	hint := fmt.Sprintf("OpenCode Go doesn't serve %s: pick a model the OpenCode Go provider lists", model)
	zen := openCodeGoToZenFree(req.URL.String(), proto)
	if p.OpenCodeFree(model) && zen != "" && proto != Gemini {
		again := req.Clone(req.Context())
		again.Body = io.NopCloser(bytes.NewReader(sent))
		again.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(sent)), nil }
		again.ContentLength = int64(len(sent))
		if u, err := again.URL.Parse(zen); err == nil {
			again.URL = u
			again.Host = u.Host
			// the public key, never the user's Go key, as OpenCode asks
			// Zen's free tier signed out
			again.Header.Del("x-api-key")
			again.Header.Set("Authorization", "Bearer "+OpenCodeAnonymousKey)
			if proto == Anthropic {
				again.Header.Set("x-api-key", OpenCodeAnonymousKey)
			}
			got, err := do(again)
			said := ""
			switch {
			case err != nil:
				said = err.Error()
			case got.StatusCode < 300:
				res.Body.Close()
				return got, nil
			default:
				gb, _ := io.ReadAll(io.LimitReader(got.Body, 64<<10))
				got.Body.Close()
				said = fmt.Sprintf("%d %s", got.StatusCode, APIError(gb, got.Status))
			}
			hint = fmt.Sprintf("OpenCode Go doesn't serve %s, and OpenCode Zen's free tier didn't serve it either (%s). "+
				"Pick a model the OpenCode Go provider lists, or one of Zen's free models under the OpenCode Zen provider, which needs no key", model, said)
		}
	}
	b = withMessage(b, apiError(b, res.Status)+" — "+hint)
	res.Body = io.NopCloser(bytes.NewReader(b))
	res.ContentLength = int64(len(b))
	if res.Header == nil {
		res.Header = http.Header{}
	}
	res.Header.Set("Content-Length", strconv.Itoa(len(b)))
	return res, nil
}
