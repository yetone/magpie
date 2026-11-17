package provider

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/plugin"
)

// OpenCode Zen's free tier serves OpenCode alone, and knows it by its
// connection, not its request: the same bytes OpenCode 2.3.0 sent, sent
// again from the same computer a minute apart, were answered 200 by Bun's
// fetch (1.4.2, magpie's, and 1.4.4, OpenCode's own) and 429
// FreeUsageLimitError ("Free usage limit reached. Free models work in
// OpenCode") by Go's net/http and by curl, over HTTP/1.1 and HTTP/2, with
// the public key or a user's (biubiubiuzone on X: "opencode zen 配置官方的key
// 后无法使用deepseek等 free模型，key可以在opencode app中正常使用"). Zen's
// limit for a free model is checked before its key is (zen/util/handler.ts:
// rateLimiter before authenticate), so a key doesn't lift it. So a request
// to the free tier's paths (OpenCodeFreeURL) goes out through the plugin
// host's fetch, which is Bun's, from wherever it is sent: the gateway, the
// Test button, an OpenCode Go request asked again at Zen.
func init() { netproxy.Elsewhere = zenFreeInBun }

// bunFetch is the plugin host's fetch (a var for tests).
var bunFetch = plugin.Fetch

// hasBun says Bun is on this computer (a var for tests).
var hasBun = plugin.HasBun

// zenBunProvider is the provider the host is told the request is for: no
// plugin's, so no plugin's sign-in or fetch takes it over.
const zenBunProvider = "magpie-opencode-free"

// zenFreeTier says req goes to Zen's free tier: an OpenCode host's
// /inference paths, where OpenCodeFreeURL sends its free models.
func zenFreeTier(req *http.Request) bool {
	return req.URL != nil && openCodeHost(req.URL.Hostname()) && strings.HasPrefix(req.URL.Path, "/inference/")
}

// zenFreeInBun sends a request to Zen's free tier through Bun's fetch.
// Where Bun can't be had (the download failed, offline), the request goes
// as before, through Go's own transport.
func zenFreeInBun(req *http.Request) (*http.Response, bool, error) {
	if !zenFreeTier(req) {
		return nil, false, nil
	}
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, true, err
		}
		body = b
	}
	h := map[string]string{}
	for k, vs := range req.Header {
		if len(vs) > 0 {
			h[strings.ToLower(k)] = vs[0]
		}
	}
	res, err := bunFetch(req.Context(), plugin.FetchRequest{
		Provider: zenBunProvider, URL: req.URL.String(), Method: req.Method, Headers: h, Body: body,
	})
	if err != nil && !hasBun() {
		req.Body = io.NopCloser(bytes.NewReader(body))
		return nil, false, nil
	}
	if res != nil {
		res.Request = req
	}
	return res, true, err
}
