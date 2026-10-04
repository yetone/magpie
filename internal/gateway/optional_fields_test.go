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
	request := []byte(`{"store":false,"metadata":{"session":"synthetic"},"service_tier":"default","prompt_cache_retention":"24h","thinking":{"type":"enabled"}}`)
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
		{"missing-value", `{"detail":[{"type":"missing","loc":["body","metadata"],"msg":"Field required"}]}`, 422, nil},
		{"multiple-extras", `{"detail":[{"type":"extra_forbidden","loc":["body","thinking"],"msg":"Extra inputs are not permitted"},{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted"}]}`, 422, []string{"store", "thinking"}},
		{"root-location", `{"detail":[{"type":"extra_forbidden","loc":["store"],"msg":"Extra inputs are not permitted"}]}`, 422, []string{"store"}},
		{"unsupported-code", `{"error":{"message":"Not accepted here","code":"unsupported_parameter","param":"store"}}`, 400, []string{"store"}},
		{"codex-unquoted-unsupported-parameter", `{"code":null,"message":"Codex: Unsupported parameter: prompt_cache_retention","param":null,"type":"invalid_request_error"}`, 400, []string{"prompt_cache_retention"}},
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
