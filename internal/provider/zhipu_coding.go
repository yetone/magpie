package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// A GLM Coding Plan (Zhipu's, on open.bigmodel.cn, and Z.ai's, on
// api.z.ai) is served at its own OpenAI endpoint, /api/coding/paas/v4,
// while the pay-as-you-go API is /api/paas/v4. Both share the Anthropic
// endpoint (/api/anthropic), which bills the plan when the account has one,
// so a plan's key given the zhipu or zai preset at its first plan, Pay as
// you go, works in Claude Code and is refused in Codex: the pay-as-you-go
// endpoint answers it 429, code 1113 "余额不足或无可用资源包,请充值。" (the
// account has no balance), as @cleverpigo on X and kkgg on Discord found.
// Nothing in the key tells the two apart, so a 1113 at /api/paas/v4 is
// asked again at /api/coding/paas/v4: served there, the key is a plan's,
// and the provider is moved to the plan's endpoints (LearnZhipuCoding).
// Refused there too, the refusal says what each endpoint said and what to
// change. A pay-as-you-go key with a balance is never answered 1113 and
// stays where it is.

// zhipuPaygPath and zhipuCodingPath are the pay-as-you-go and the Coding
// Plan's OpenAI paths on both hosts.
const (
	zhipuPaygPath   = "/api/paas/v4"
	zhipuCodingPath = "/api/coding/paas/v4"
)

// zhipuHost is a host that serves a GLM Coding Plan beside pay as you go.
func zhipuHost(host string) bool {
	switch strings.ToLower(host) {
	case "open.bigmodel.cn", "api.z.ai":
		return true
	}
	return false
}

// ZhipuCodingBase is the GLM Coding Plan's OpenAI base for base, Zhipu's or
// Z.ai's pay-as-you-go one (https://open.bigmodel.cn/api/paas/v4 →
// https://open.bigmodel.cn/api/coding/paas/v4), and whether base is one.
func ZhipuCodingBase(base string) (string, bool) {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if !zhipuHost(HostOf(b)) || basePath(b) != zhipuPaygPath {
		return "", false
	}
	return strings.TrimSuffix(b, zhipuPaygPath) + zhipuCodingPath, true
}

// zhipuNoBalance is Zhipu's refusal of a key whose account has no balance
// for the API asked: 429 with code 1113.
func zhipuNoBalance(status int, b []byte) bool {
	return status == http.StatusTooManyRequests && ErrorType(b) == "1113"
}

// RetryZhipuCoding hands on res, the answer to req; or, when req went to
// Zhipu's or Z.ai's pay-as-you-go OpenAI API and was refused for want of a
// balance (1113), the answer to req asked again at the GLM Coding Plan's
// endpoint when that one serves it — remembering it for p
// (LearnZhipuCoding) — else res with what to change added to its message.
// do sends a request as req was sent.
func RetryZhipuCoding(p Provider, req *http.Request, res *http.Response, do func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if res == nil || res.StatusCode != http.StatusTooManyRequests || req.GetBody == nil || !zhipuHost(req.URL.Host) {
		return res, nil
	}
	rest, ok := strings.CutPrefix(req.URL.Path, zhipuPaygPath+"/")
	if !ok {
		return res, nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	if !zhipuNoBalance(res.StatusCode, b) {
		return res, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return res, nil
	}
	again := req.Clone(req.Context())
	again.Body = body
	again.URL.Path = zhipuCodingPath + "/" + rest
	again.URL.RawPath = ""
	from := req.URL.Scheme + "://" + req.URL.Host + zhipuPaygPath
	coding := req.URL.Scheme + "://" + req.URL.Host + zhipuCodingPath
	got, err := do(again)
	said := ""
	if err != nil {
		said = err.Error()
	} else if got.StatusCode < 300 {
		LearnZhipuCoding(p, from)
		return got, nil
	} else {
		gb, _ := io.ReadAll(io.LimitReader(got.Body, 64<<10))
		got.Body.Close()
		said = fmt.Sprintf("%d %s", got.StatusCode, APIError(gb, got.Status))
	}
	b = withMessage(b, apiError(b, res.Status)+" — "+ZhipuNoBalanceHint(from, coding, said))
	res.Body = io.NopCloser(bytes.NewReader(b))
	res.ContentLength = int64(len(b))
	if res.Header == nil {
		res.Header = http.Header{}
	}
	res.Header.Set("Content-Length", strconv.Itoa(len(b)))
	return res, nil
}

// ZhipuNoBalanceHint says what a 1113 at the pay-as-you-go endpoint from
// means when the Coding Plan's endpoint coding didn't serve the key either
// (said is what it answered), and what to change.
func ZhipuNoBalanceHint(from, coding, said string) string {
	site := "open.bigmodel.cn"
	if HostOf(from) == "api.z.ai" {
		site = "z.ai"
	}
	return fmt.Sprintf("this key's account has no balance for the pay-as-you-go API (%s), and the GLM Coding Plan's endpoint (%s) didn't serve it either (%s). "+
		"A GLM Coding Plan's key: set the provider's Plan to Coding Plan (its OpenAI URL %s), and check the plan is active at %s. "+
		"A pay-as-you-go key: top up the account's balance at %s",
		from, coding, said, coding, site, site)
}

// withMessage is the JSON error body b with its message set to msg, the
// rest (Zhipu's code 1113) as it was.
func withMessage(b []byte, msg string) []byte {
	var v map[string]any
	if json.Unmarshal(b, &v) != nil || v == nil {
		v = map[string]any{}
	}
	e, _ := v["error"].(map[string]any)
	if e == nil {
		e = map[string]any{}
		v["error"] = e
	}
	e["message"] = msg
	out, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return out
}

// LearnZhipuCoding moves provider p, whose key the pay-as-you-go endpoint
// from refused and the GLM Coding Plan's served, to the plan's endpoints:
// its OpenAI URL, and the plan's Responses URL when it has none (the zhipu
// and zai presets' Coding Plan region). Only a provider with that one key
// still at from is moved: one with other keys may hold pay-as-you-go keys
// too, and one the user has moved since is theirs. Each of its requests
// is then asked again at the plan's endpoint as this one was.
func LearnZhipuCoding(p Provider, from string) {
	coding, ok := ZhipuCodingBase(from)
	if !ok || p.ID == "" || p.Account != nil {
		return
	}
	learnMu.Lock()
	defer learnMu.Unlock()
	f, err := read()
	if err != nil {
		return
	}
	for i := range f.Providers {
		q := &f.Providers[i]
		if q.ID != p.ID {
			continue
		}
		if q.Account != nil || len(q.Keys) > 0 || q.Key != p.Key || strings.TrimRight(q.Chat, "/") != strings.TrimRight(from, "/") {
			return
		}
		q.Chat = coding
		if pr := Preset(q.Preset); pr != nil && q.Responses == "" {
			for _, r := range pr.Regions {
				if r.ID == "coding" && r.Chat == coding {
					q.Responses = r.Responses
				}
			}
		}
		_ = store(f)
		return
	}
}

// ZhipuSearchPath is where a GLM Coding Plan's web search MCP server is,
// on the plan's own host: Zhipu's Web Search MCP at
// open.bigmodel.cn/api/mcp/web_search_prime/mcp and Z.ai's at
// api.z.ai/api/mcp/web_search_prime/mcp, a Streamable HTTP server asked
// with the plan's key as a Bearer (docs.bigmodel.cn/cn/coding-plan/mcp/search-mcp-server,
// docs.z.ai/devpack/mcp/search-mcp-server).
const ZhipuSearchPath = "/api/mcp/web_search_prime/mcp"

// ZhipuCodingSearch is where p, a GLM Coding Plan with a key, searches the
// web (ZhipuSearchPath on its OpenAI endpoint's host), "" for a provider
// that isn't one. A plan is known by its OpenAI endpoint, the Coding Plan's
// /api/coding/paas/v4, on Zhipu's or Z.ai's host or at the zhipu or zai
// preset's: the Anthropic endpoint is the same for a plan and pay as you
// go, and the MCP server serves only a plan's key. A key of another plan
// than p's (KeyAccount.Plan) is asked at its own: see ZhipuSearchKeys.
func ZhipuCodingSearch(p Provider) string {
	if p.Account != nil || p.Key == "" {
		return ""
	}
	b := strings.TrimRight(strings.TrimSpace(p.Chat), "/")
	if basePath(b) != zhipuCodingPath || !zhipuHost(HostOf(b)) && p.Preset != "zhipu" && p.Preset != "zai" {
		return ""
	}
	return strings.TrimSuffix(b, zhipuCodingPath) + ZhipuSearchPath
}

// ZhipuSearches is whether a key of p in use is on a GLM Coding Plan
// (ZhipuSearchKeys has one), read from the keys themselves: the gateway
// asks it for every request, and KeysOn is counted per list.
func ZhipuSearches(p Provider) bool {
	if p.Key == "" || p.Account != nil {
		return false
	}
	if ZhipuCodingSearch(p) != "" {
		return true
	}
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" && ZhipuCodingSearch(p.WithKey(k)) != "" {
			return true
		}
	}
	return false
}

// ZhipuSearchKeys are p with each of its keys in use that is on a GLM
// Coding Plan (ZhipuCodingSearch of p with that key), in the order requests
// try them; none for a provider with no plan's key.
func ZhipuSearchKeys(p Provider) []Provider {
	var out []Provider
	for _, k := range p.KeysOn() {
		if v := p.WithKey(k); ZhipuCodingSearch(v) != "" {
			out = append(out, v)
		}
	}
	return out
}
