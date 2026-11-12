package gateway

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestOptionalFieldValueErrorNotRetried(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Chat, provider.Responses} {
		for _, tt := range []struct {
			name, field, invalid, valid, reply string
			status                             int
		}{
			{"metadata-type", "metadata", `"invalid"`, `{"session":"synthetic"}`, `{"error":{"message":"Invalid value for \"metadata\": expected an object","type":"invalid_request_error"}}`, 400},
			{"metadata-dict", "metadata", `"invalid"`, `{"session":"synthetic"}`, `{"message":{"detail":[{"type":"dict_type","loc":["body","metadata"],"msg":"Input should be a valid dictionary","input":"invalid"}]}}`, 422},
			{"metadata-dict-status-prefix", "metadata", `"Unknown name 'metadata': Cannot find field."`, `{"session":"synthetic"}`, `[400] {"detail":[{"type":"dict_type","loc":["body","metadata"],"msg":"Input should be a valid dictionary","input":"Unknown name 'metadata': Cannot find field."}]}`, 400},
			{"metadata-dict-litellm", "metadata", `"invalid"`, `{"session":"synthetic"}`, optionalFieldProxyFault("litellm", `{"detail":[{"type":"dict_type","loc":["body","metadata"],"msg":"Input should be a valid dictionary","input":"invalid"}]}`), 400},
			{"metadata-dict-openrouter", "metadata", `"invalid"`, `{"session":"synthetic"}`, optionalFieldProxyFault("openrouter", `{"detail":[{"type":"dict_type","loc":["body","metadata"],"msg":"Input should be a valid dictionary","input":"invalid"}]}`), 400},
			{"tier-value", "service_tier", `"priority"`, `"default"`, `{"error":{"message":"Unsupported value: 'service_tier' does not support 'priority' with this model.","param":"service_tier","code":"unsupported_value"}}`, 400},
		} {
			t.Run(string(proto)+"/"+tt.name, func(t *testing.T) {
				f := &fake{t: t, reply: optionalFieldReply(proto)}
				f.refuse = func(b []byte) (int, string) {
					var v map[string]json.RawMessage
					json.Unmarshal(b, &v)
					if string(v[tt.field]) == tt.invalid {
						return tt.status, tt.reply
					}
					return 0, ""
				}
				setup(t, proto, f)
				srv := New()
				path, body := optionalFieldAsk(proto)
				code, reply := postTo(t, srv, path, string(withFields([]byte(body), map[string]any{tt.field: json.RawMessage(tt.invalid)})))
				if code != tt.status || f.calls != 1 {
					t.Errorf("invalid value: %d after %d calls, want %d after one: %s", code, f.calls, tt.status, reply)
				}
				calls := f.calls
				code, reply = postTo(t, srv, path, string(withFields([]byte(body), map[string]any{tt.field: json.RawMessage(tt.valid)})))
				if code != 200 || f.calls-calls != 1 {
					t.Fatalf("valid value: %d after %d calls: %s", code, f.calls-calls, reply)
				}
				var sent map[string]json.RawMessage
				json.Unmarshal(f.got, &sent)
				if string(sent[tt.field]) != tt.valid {
					t.Errorf("valid %s lost after the value error: %s", tt.field, f.got)
				}
			})
		}
	}
}

func TestOptionalFieldUnsupportedRetried(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Chat, provider.Responses} {
		for _, tt := range []struct {
			name, reply string
			status      int
		}{
			{"mistral", `{"message":{"detail":[{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted","input":false}]}}`, 422},
			{"mistral-status-prefix", `[400] {"detail":[{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted","input":false}]}`, 400},
			{"mistral-litellm", optionalFieldProxyFault("litellm", `{"object":"error","message":{"detail":[{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted","input":false}]},"type":"invalid_request_error","param":null,"code":null}`), 400},
			{"mistral-openrouter", optionalFieldProxyFault("openrouter", `{"object":"error","message":{"detail":[{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted","input":false}]},"type":"invalid_request_error","param":null,"code":null}`), 400},
			{"gemini", `{"error":{"message":"Invalid JSON payload received. Unknown name \"store\": Cannot find field.","code":400}}`, 400},
			{"gemini-status-prefix", `{"error":{"message":"[400] Unknown name \"store\": Cannot find field.","code":400}}`, 400},
			{"plain-status-prefix", `[400] Unknown name "store": Cannot find field.`, 400},
			{"groq", `{"error":{"message":"The property 'store' is not supported","type":"invalid_request_error"}}`, 400},
			{"openai", `{"error":{"message":"Unsupported parameter: 'store' is not supported with this model.","param":"store","code":"unsupported_parameter"}}`, 400},
		} {
			t.Run(string(proto)+"/"+tt.name, func(t *testing.T) {
				f := &fake{t: t, reply: optionalFieldReply(proto)}
				f.refuse = func(b []byte) (int, string) {
					var v map[string]json.RawMessage
					json.Unmarshal(b, &v)
					if _, present := v["store"]; present {
						return tt.status, tt.reply
					}
					return 0, ""
				}
				setup(t, proto, f)
				srv := New()
				path, body := optionalFieldAsk(proto)
				request := string(withFields([]byte(body), map[string]any{"store": false, "metadata": map[string]string{"session": "synthetic"}}))
				for turn, want := range []int{2, 1} {
					calls := f.calls
					code, reply := postTo(t, srv, path, request)
					if code != 200 || f.calls-calls != want {
						t.Fatalf("turn %d: %d after %d calls, want %d: %s", turn+1, code, f.calls-calls, want, reply)
					}
					var sent map[string]json.RawMessage
					json.Unmarshal(f.got, &sent)
					if sent["store"] != nil || sent["metadata"] == nil {
						t.Errorf("turn %d: unrelated metadata removed or store retained: %s", turn+1, f.got)
					}
				}
			})
		}
	}
}

func TestOptionalFieldMixedValidation(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Chat, provider.Responses} {
		t.Run(string(proto), func(t *testing.T) {
			f := &fake{t: t, reply: optionalFieldReply(proto)}
			f.refuse = func(b []byte) (int, string) {
				var v map[string]json.RawMessage
				json.Unmarshal(b, &v)
				var detail []json.RawMessage
				if v["store"] != nil {
					detail = append(detail, json.RawMessage(`{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted","input":false}`))
				}
				if string(v["metadata"]) == `"invalid"` {
					detail = append(detail, json.RawMessage(`{"type":"dict_type","loc":["body","metadata"],"msg":"Input should be a valid dictionary","input":"invalid"}`))
				}
				if len(detail) != 0 {
					reply, _ := json.Marshal(map[string]any{"detail": detail})
					return 422, string(reply)
				}
				return 0, ""
			}
			setup(t, proto, f)
			srv := New()
			path, body := optionalFieldAsk(proto)
			for turn, tt := range []struct {
				metadata      any
				status, calls int
			}{
				{"invalid", 422, 2},
				{map[string]string{"session": "synthetic"}, 200, 2},
				{map[string]string{"session": "synthetic"}, 200, 1},
			} {
				calls := f.calls
				code, reply := postTo(t, srv, path, string(withFields([]byte(body), map[string]any{"store": false, "metadata": tt.metadata})))
				if code != tt.status || f.calls-calls != tt.calls {
					t.Fatalf("turn %d: %d after %d calls, want %d after %d: %s", turn+1, code, f.calls-calls, tt.status, tt.calls, reply)
				}
				var sent map[string]json.RawMessage
				json.Unmarshal(f.got, &sent)
				want, _ := json.Marshal(tt.metadata)
				if string(sent["metadata"]) != string(want) {
					t.Errorf("turn %d: metadata lost or changed: %s", turn+1, f.got)
				}
			}
		})
	}
}

func TestRefusedOptionalNamesOnlyUnsupportedFields(t *testing.T) {
	request := []byte(`{"store":false,"metadata":{"session":"synthetic"},"service_tier":"default","prompt_cache_retention":"24h","prompt_cache_options":{"mode":"explicit"},"thinking":{"type":"enabled"}}`)
	for _, tt := range []struct {
		name, reply string
		status      int
		want        []string
	}{
		{"mixed-errors", `{"message":{"detail":[{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted","input":false},{"type":"dict_type","loc":["body","metadata"],"msg":"Input should be a valid dictionary","input":"invalid"}]}}`, 422, []string{"store"}},
		{"input-echo", `{"detail":[{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted","input":{"metadata":"Unknown name 'metadata': Cannot find field."}}]}`, 422, []string{"store"}},
		{"array-extra", `[{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted"}]`, 422, []string{"store"}},
		{"array-input-echo", `[{"type":"dict_type","loc":["body","metadata"],"msg":"Input should be a valid dictionary","input":"Unknown name 'metadata': Cannot find field."}]`, 422, nil},
		{"nested-array-input-echo", `[[{"type":"dict_type","loc":["body","metadata"],"msg":"Input should be a valid dictionary","input":"Unknown name 'metadata': Cannot find field."}]]`, 422, nil},
		{"mixed-array-input-echo", `[400,{"type":"dict_type","loc":["body","metadata"],"msg":"Input should be a valid dictionary","input":"Unknown name 'metadata': Cannot find field."}]`, 422, nil},
		{"status-prefixed-extra", `[400] {"detail":[{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted"}]}`, 400, []string{"store"}},
		{"status-prefixed-input-echo", `[400] {"detail":[{"type":"dict_type","loc":["body","metadata"],"msg":"Input should be a valid dictionary","input":"Unknown name 'metadata': Cannot find field."}]}`, 400, nil},
		{"text-prefixed-extra", `[HTTP 400] {"detail":[{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted"}]}`, 400, []string{"store"}},
		{"text-prefixed-input-echo", `[HTTP 400] {"detail":[{"type":"dict_type","loc":["body","metadata"],"msg":"Input should be a valid dictionary","input":"Unknown name 'metadata': Cannot find field."}]}`, 400, nil},
		{"nested-extra", `{"detail":[{"type":"extra_forbidden","loc":["body","metadata","store"],"msg":"Extra inputs are not permitted"}]}`, 422, nil},
		{"nested-unknown", `{"error":{"message":"Invalid JSON payload received. Unknown name \"store\" at 'metadata': Cannot find field."}}`, 400, nil},
		{"other-parameter", `{"error":{"message":"'metadata' is unsupported","param":"model","code":"unsupported_value"}}`, 400, nil},
		{"other-unsupported-parameter", `{"error":{"message":"Unsupported parameter: 'metadata'","param":"model","code":"unsupported_parameter"}}`, 400, nil},
		{"unsupported-value", `{"error":{"message":"Unsupported value: 'service_tier' is not supported with this model.","param":"service_tier","code":"unsupported_value"}}`, 400, nil},
		{"unsupported-value-no-code", `{"error":{"message":"Unsupported value: 'service_tier' is not supported with this model."}}`, 400, nil},
		// litellm's own, naming the value it turned away before the field (#3978)
		{"litellm-tier-value-first", `{"error":{"message":"litellm.BadRequestError: OpenAIException - 'priority' is not supported for 'service_tier' on this model. Received Model Group=gpt-6.1-sol\nAvailable Model Group Fallbacks=None","type":"invalid_request_error","param":null,"code":"400"}}`, 400, []string{"service_tier"}},
		// and the same refusal with the backend's JSON embedded in its message (0.1.1829, #3978)
		{"litellm-tier-json-embedded", `{"error":{"message":"litellm.BadRequestError: OpenAIException - {\"error\":{\"message\":\"'priority' is not supported for 'service_tier' on this model\",\"type\":\"invalid_request_error\",\"param\":null,\"code\":\"validation_error\"}}","type":"invalid_request_error","param":null,"code":"validation_error"}}`, 400, []string{"service_tier"}},
		{"missing-value", `{"detail":[{"type":"missing","loc":["body","metadata"],"msg":"Field required"}]}`, 422, nil},
		{"multiple-extras", `{"detail":[{"type":"extra_forbidden","loc":["body","thinking"],"msg":"Extra inputs are not permitted"},{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted"}]}`, 422, []string{"store", "thinking"}},
		{"root-location", `{"detail":[{"type":"extra_forbidden","loc":["store"],"msg":"Extra inputs are not permitted"}]}`, 422, []string{"store"}},
		{"unsupported-code", `{"error":{"message":"Not accepted here","code":"unsupported_parameter","param":"store"}}`, 400, []string{"store"}},
		{"codex-unquoted-unsupported-parameter", `{"code":null,"message":"Codex: Unsupported parameter: prompt_cache_retention","param":null,"type":"invalid_request_error"}`, 400, []string{"prompt_cache_retention"}},
		// the ChatGPT backend's, as it gave it on 2026-10-14 (#2154)
		{"openai-bare-name", `{"error":{"message":"prompt_cache_options is not supported on this model","type":"invalid_request_error","param":"prompt_cache_options","code":"invalid_parameter"}}`, 400, []string{"prompt_cache_options"}},
		// a relay's Chinese refusal (基元律动, ldl123292 on X)
		{"chinese-unknown-field", `{"error":{"message":"未知请求字段：prompt_cache_retention","type":"invalid_request_error"}}`, 400, []string{"prompt_cache_retention"}},
		{"chinese-unsupported-quoted", `{"error":{"message":"不支持的参数: 'prompt_cache_retention'"}}`, 400, []string{"prompt_cache_retention"}},
		{"chinese-other-field", `{"error":{"message":"未知请求字段：foo_bar"}}`, 400, nil},
		{"bare-name-not-leading", `{"error":{"message":"The input said metadata is not supported here"}}`, 400, nil},
		{"bare-name-other-field", `{"error":{"message":"reasoning is not supported on this model"}}`, 400, nil},
		{"plain-error", `Unknown name "store": Cannot find field.`, 400, []string{"store"}},
		{"status-prefix", `[400] Unknown name "store": Cannot find field.`, 400, []string{"store"}},
		{"text-status-prefix", `[HTTP 400] Unknown name "store": Cannot find field.`, 400, []string{"store"}},
		{"empty-array-prefix", `[] Unknown name "store": Cannot find field.`, 400, []string{"store"}},
		{"string-array-prefix", `["upstream"] Unknown name "store": Cannot find field.`, 400, []string{"store"}},
		{"nested-status-prefix", `[[400]] Unknown name "store": Cannot find field.`, 400, []string{"store"}},
		{"quoted-error", `{"error":{"message":"'store' is unsupported"}}`, 400, []string{"store"}},
		{"unrecognized-argument", `{"error":{"message":"Unrecognized request argument supplied: 'store'","param":null,"code":null}}`, 400, []string{"store"}},
		{"unknown-root", `{"error":{"message":"Unknown name \"store\" at '': Cannot find field."}}`, 400, []string{"store"}},
		{"model-error", `{"error":{"message":"The model 'metadata' is not supported."}}`, 400, nil},
		{"rate-limit", `{"error":{"message":"Unknown name \"store\": Cannot find field."}}`, 429, nil},
		{"server-error", `{"error":{"message":"Unknown name \"store\": Cannot find field."}}`, 500, nil},
		{"required-field", `{"error":{"message":"Unknown name \"messages\": Cannot find field."}}`, 400, nil},
	} {
		for _, proxy := range []string{"direct", "litellm", "openrouter"} {
			t.Run(tt.name+"/"+proxy, func(t *testing.T) {
				reply := tt.reply
				if proxy != "direct" {
					reply = optionalFieldProxyFault(proxy, reply)
				}
				if got := refusedOptional(tt.status, []byte(reply), request); !slices.Equal(got, tt.want) {
					t.Errorf("refused %v, want %v", got, tt.want)
				}
			})
		}
	}
}

// Proxies carry the vendor's JSON error as text, sometimes with prose after it.
func optionalFieldProxyFault(proxy, raw string) string {
	if proxy == "litellm" {
		return string(mustJSON(map[string]any{"error": map[string]any{
			"message": "litellm.BadRequestError: MistralException - " + raw + "\nSee provider docs.",
			"type":    "invalid_request_error",
		}}))
	}
	return string(mustJSON(map[string]any{"error": map[string]any{
		"message": "Provider returned error",
		"code":    400,
		"metadata": map[string]any{
			"provider_name": "synthetic",
			"raw":           raw,
		},
	}}))
}

func optionalFieldAsk(proto provider.Protocol) (string, string) {
	if proto == provider.Responses {
		return "/v1/responses", `{"model":"m1","input":"hi","stream":true}`
	}
	return "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}],"stream":true}`
}

func optionalFieldReply(proto provider.Protocol) string {
	if proto == provider.Responses {
		return sse(
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"ok"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)
	}
	return sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)
}

// #2154 (neavo): pi-ai sends prompt_cache_options, which OpenAI's Responses
// API turns away by its bare name: asked again without it, and not sent it
// again, while the cache key stays.
func TestPromptCacheOptionsRefusedRetried(t *testing.T) {
	f := &fake{t: t, reply: optionalFieldReply(provider.Responses)}
	f.refuse = func(b []byte) (int, string) {
		var v map[string]json.RawMessage
		json.Unmarshal(b, &v)
		if _, present := v["prompt_cache_options"]; present {
			return 400, `{"error":{"message":"prompt_cache_options is not supported on this model","type":"invalid_request_error","param":"prompt_cache_options","code":"invalid_parameter"}}`
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	srv := New()
	path, body := optionalFieldAsk(provider.Responses)
	request := string(withFields([]byte(body), map[string]any{"prompt_cache_options": map[string]string{"mode": "explicit"}, "prompt_cache_key": "k1"}))
	for turn, want := range []int{2, 1} {
		calls := f.calls
		code, reply := postTo(t, srv, path, request)
		if code != 200 || f.calls-calls != want {
			t.Fatalf("turn %d: %d after %d calls, want %d: %s", turn+1, code, f.calls-calls, want, reply)
		}
		var sent map[string]json.RawMessage
		json.Unmarshal(f.got, &sent)
		if sent["prompt_cache_options"] != nil || sent["prompt_cache_key"] == nil {
			t.Errorf("turn %d: options kept or the key lost: %s", turn+1, f.got)
		}
	}
}

// A litellm relay in front of a model that serves no tier names the value
// it turned away before the field: "'priority' is not supported for
// 'service_tier' on this model" (#3978). Asked again without the tier,
// and not sent it again.
func TestServiceTierRefusedForTheValueRetried(t *testing.T) {
	f := &fake{t: t, reply: optionalFieldReply(provider.Responses)}
	f.refuse = func(b []byte) (int, string) {
		var v map[string]json.RawMessage
		json.Unmarshal(b, &v)
		if _, present := v["service_tier"]; present {
			return 400, `{"error":{"message":"litellm.BadRequestError: OpenAIException - 'priority' is not supported for 'service_tier' on this model. Received Model Group=gpt-6.1-sol\nAvailable Model Group Fallbacks=None","type":"invalid_request_error","param":null,"code":"400"}}`
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	srv := New()
	path, body := optionalFieldAsk(provider.Responses)
	request := string(withFields([]byte(body), map[string]any{"service_tier": "priority", "metadata": map[string]string{"session": "synthetic"}}))
	for turn, want := range []int{2, 1} {
		calls := f.calls
		code, reply := postTo(t, srv, path, request)
		if code != 200 || f.calls-calls != want {
			t.Fatalf("turn %d: %d after %d calls, want %d: %s", turn+1, code, f.calls-calls, want, reply)
		}
		var sent map[string]json.RawMessage
		json.Unmarshal(f.got, &sent)
		if sent["service_tier"] != nil || sent["metadata"] == nil {
			t.Errorf("turn %d: the tier kept or unrelated metadata lost: %s", turn+1, f.got)
		}
	}
	if srv.fits("fake", tierField, provider.Responses) {
		t.Errorf("the tier not remembered unfit for the provider")
	}
}
