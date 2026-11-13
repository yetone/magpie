package gateway

import (
	"encoding/json"

	"github.com/yetone/magpie/internal/provider"
)

// arkHost is Volcengine Ark's (火山方舟) API host. Its Responses API runs
// the web_search tool itself, on pay-as-you-go (/api/v3) and the plans
// (/api/coding/v3, /api/plan/v3) alike: Ark's 联网搜索工具 page
// (docs.volcengine.com/docs/82379/1756990) asks it on
// ark.cn-beijing.volces.com/api/v3/responses with {"type":"web_search"},
// and its Web Search page marks the tool 支持 on the Responses API and 不支持
// on the Chat API (axelburks on Discord: Codex's web_search was taken from
// its body, and the model said it had no such tool). BytePlus ModelArk's
// docs name no such tool, so its host isn't counted.
const arkHost = "ark.cn-beijing.volces.com"

// arkSearchFields are the members of a web_search tool Ark's Responses API
// documents: type, sources (["doubao"] for 豆包搜索 Custom, else
// search_engine with toutiao, douyin or moji), and the content plugin's
// limit, max_keyword and user_location.
var arkSearchFields = map[string]bool{"type": true, "sources": true, "limit": true, "max_keyword": true, "user_location": true}

// arkLocationFields are the members of Ark's user_location: the city,
// region and country, beside OpenAI's type. OpenAI's timezone isn't one.
var arkLocationFields = map[string]bool{"type": true, "country": true, "region": true, "city": true}

// arkIncludes are the include values Ark's Responses API takes. Its
// 创建 Response page (docs.volcengine.com/docs/82379/1569618) names the
// parameter but no values; asked with a real key (2026-11-01, model
// deepseek-v4-1-flash-260910), it took reasoning.encrypted_content and
// refused the whole request over each of OpenAI's others with 400 "The
// parameter `include` specified in the request are not valid: unknown
// type: <value>": web_search_call.action.sources, web_search_call.results,
// file_search_call.results, message.input_image.image_url,
// computer_call_output.output.image_url, code_interpreter_call.outputs and
// message.output_text.logprobs (axelburks on Discord: every search on Ark
// failed over the sources magpie asked for).
var arkIncludes = map[string]bool{"reasoning.encrypted_content": true}

// includeTaken is whether a Responses request built for host may ask for
// the include value v: Ark's only what arkIncludes names, any other host's
// all of them.
func includeTaken(host, v string) bool {
	return host != arkHost || arkIncludes[v]
}

// forArkSearch gives Ark's Responses API the web_search tool in the shape
// its docs give: OpenAI's members Ark doesn't name (Codex's
// external_web_access, search_context_size, filters, a location's
// timezone) are left out, and web_search_preview is web_search. An
// include value Ark refuses (arkIncludes) is left out too: the AI SDK's
// OpenAI provider (OpenCode's) adds web_search_call.action.sources to
// every request offering web_search. Any other provider's body, or one
// with neither, goes as it came.
func forArkSearch(p provider.Provider, body []byte) []byte {
	if p.Account != nil || provider.HostOf(p.Base(provider.Responses)) != arkHost ||
		!bodyHas(body, "web_search") && !bodyHas(body, `"include"`) {
		return body
	}
	q, err := rawObject(body)
	if err != nil {
		return body
	}
	changed := false
	if raw, ok := q["include"]; ok {
		var inc []string
		if json.Unmarshal(raw, &inc) == nil {
			kept := make([]string, 0, len(inc))
			for _, v := range inc {
				if arkIncludes[v] {
					kept = append(kept, v)
				}
			}
			if len(kept) < len(inc) {
				if len(kept) == 0 {
					delete(q, "include")
				} else if q["include"], err = marshalPlain(kept); err != nil {
					return body
				}
				changed = true
			}
		}
	}
	tools, _ := rawArray(q["tools"])
	toolsChanged := false
	for i, raw := range tools {
		t, err := rawObject(raw)
		if err != nil {
			continue
		}
		var typ string
		if json.Unmarshal(t["type"], &typ) != nil || typ != "web_search" && typ != "web_search_preview" {
			continue
		}
		out := map[string]json.RawMessage{"type": json.RawMessage(`"web_search"`)}
		drop := typ != "web_search" // a member left out, or the type renamed
		for k, v := range t {
			if k == "type" {
				continue
			}
			if !arkSearchFields[k] {
				drop = true
				continue
			}
			if k == "user_location" {
				loc, err := rawObject(v)
				if err != nil {
					drop = true
					continue
				}
				for lk := range loc {
					if !arkLocationFields[lk] {
						delete(loc, lk)
						drop = true
					}
				}
				if v, err = marshalPlain(loc); err != nil {
					continue
				}
			}
			out[k] = v
		}
		if !drop {
			continue
		}
		b, err := marshalPlain(out)
		if err != nil {
			continue
		}
		tools[i], toolsChanged = b, true
	}
	if !changed && !toolsChanged {
		return body
	}
	if toolsChanged {
		if q["tools"], err = marshalPlain(tools); err != nil {
			return body
		}
	}
	b, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return b
}
