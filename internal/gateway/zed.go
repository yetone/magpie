package gateway

// PLUGIN-SERVED (see AGENTS.md): Zed ("zed") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-zed-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/zed) and raise the mover's
// min in internal/provider/migrate_zed.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/zed"
)

// A Zed subscription is served through cloud.zed.dev/completions, the way the
// Zed editor asks it: the request is written in the API of the model's own
// provider — Anthropic's Messages, OpenAI's Responses, xAI's chat
// completions, Gemini's generateContent — wrapped in Zed's envelope, and the
// reply is those providers' own stream events, a JSON line each, which the
// matching decoder reads. The protocol lives in internal/zed.

// zedToken, zedProviderOf and zedBase stand in for the account, so tests
// can point them elsewhere.
var (
	zedToken      = provider.ZedToken
	zedProviderOf = provider.ZedProviderOf
	zedBase       = provider.ZedCloud
)

// serveZed answers a request through Zed's API.
func (s *Server) serveZed(w http.ResponseWriter, r *http.Request, from provider.Protocol, p provider.Provider, model string, body []byte, usage *Usage) (int, string) {
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	// the vendor's own API is told the answer's format in words
	req = req.inSystem()
	req.Model = model
	ask := s.askZed(p.Account.User, model)
	if req.WebSearch && !searching(r.Context()) {
		if canSearch() {
			return s.searchReply(w, r, from, "Zed", req, usage, ask)
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events, status, msg := ask(ctx, req)
	if events == nil {
		return writeError(w, from, status, msg), msg
	}
	// Zed's failures keep their statuses (a 429 or 402 is another account's
	// turn), and a reply cut short is an error, not a shorter answer
	return relayStatus(w, from, "Zed", req, events, usage, cancel)
}

// zedRequest is the provider's own request for req, and the protocol its
// stream comes back in.
func zedRequest(req *Request, vendor, model string) (json.RawMessage, provider.Protocol, error) {
	q := *req
	q.Stream = true
	switch zed.Wire(vendor) {
	case "anthropic":
		// Zed's Anthropic request has no stream field: the cloud always streams
		var m map[string]json.RawMessage
		if err := json.Unmarshal(buildAnthropic(&q, model), &m); err != nil {
			return nil, "", err
		}
		delete(m, "stream")
		if msgs, ok := m["messages"]; ok {
			withErr, err := zedToolResults(msgs)
			if err != nil {
				return nil, "", err
			}
			m["messages"] = withErr
		}
		b, err := json.Marshal(m)
		return b, provider.Anthropic, err
	case "responses":
		// fitted to the Responses types Zed's cloud reads (a web_search
		// tool, or include's web_search_call.action.sources, would have it
		// turn the request away)
		return provider.ZedBody(buildResponses(&q, model, "api.openai.com", false)), provider.Responses, nil
	case "chat":
		var m map[string]json.RawMessage
		if err := json.Unmarshal(buildChat(&q, model, "api.x.ai", false), &m); err != nil {
			return nil, "", err
		}
		// as Zed writes an xAI request
		if v, ok := m["max_tokens"]; ok {
			m["max_completion_tokens"] = v
			delete(m, "max_tokens")
		}
		b, err := json.Marshal(m)
		return b, provider.Chat, err
	case "gemini":
		// Code Assist's envelope holds Gemini's own request; Zed's is that
		// request with the model named in it
		inner := gjson.GetBytes(buildCodeAssist(&q, model, "gemini"), "request")
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(inner.Raw), &m); err != nil {
			return nil, "", err
		}
		for _, k := range []string{"session_id", "sessionId"} {
			delete(m, k)
		}
		m["model"], _ = json.Marshal("models/" + model)
		b, err := json.Marshal(m)
		return b, provider.CodeAssist, err
	}
	return nil, "", fmt.Errorf("magpie can't ask Zed's %q models", vendor)
}

// zedToolResults says is_error on every tool result, false as well as true:
// Zed's cloud refuses a tool result without it ("failed to parse Anthropic
// request: missing field `is_error`"), which Anthropic's own API leaves out
// when it is false, so every turn after a tool call failed.
func zedToolResults(msgs json.RawMessage) (json.RawMessage, error) {
	var ms []map[string]any
	d := json.NewDecoder(bytes.NewReader(msgs))
	d.UseNumber() // a tool's input keeps its numbers as they were
	if err := d.Decode(&ms); err != nil {
		return nil, err
	}
	for _, m := range ms {
		parts, _ := m["content"].([]any)
		for _, p := range parts {
			if b, ok := p.(map[string]any); ok && b["type"] == "tool_result" {
				if _, ok := b["is_error"]; !ok {
					b["is_error"] = false
				}
			}
		}
	}
	return json.Marshal(ms)
}

// askZed is a round for Zed's API.
func (s *Server) askZed(user, model string) round {
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		vendor := zedProviderOf(user, model)
		raw, proto, err := zedRequest(req, vendor, model)
		if err != nil {
			return nil, 400, "Zed: " + err.Error()
		}
		body := zed.CompletionBody(vendor, model, "", raw)
		var res *http.Response
		for try := 0; ; try++ {
			tok, err := zedToken(ctx, user, try > 0)
			if err != nil {
				status := 502
				if errors.Is(err, provider.ErrZedSignIn) {
					status = 401
				}
				return nil, status, "Zed: " + err.Error()
			}
			hr, err := http.NewRequestWithContext(ctx, http.MethodPost, zed.CompletionURL(zedBase()), bytes.NewReader(body))
			if err != nil {
				return nil, 500, "Zed: " + err.Error()
			}
			zed.CompletionHeaders(hr.Header, tok)
			if res, err = s.client.Do(hr); err != nil {
				return nil, 502, "Zed: " + err.Error()
			}
			// a model token Zed calls stale is minted again, once
			if try == 0 && zed.TokenStale(res.StatusCode, res.Header) {
				io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
				res.Body.Close()
				continue
			}
			break
		}
		res = notAnAPIReply(res, "") // a web page served 200 (#1012)
		if res.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			status, msg := zedFailure(res.StatusCode, b)
			return nil, status, msg
		}
		wrapped := res.Header.Get("x-zed-server-supports-status-messages") != ""
		events := make(chan Event, 16)
		go func() {
			defer close(events)
			defer res.Body.Close()
			dec := decoder(proto)
			send := func(ev Event) error {
				select {
				case events <- ev:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			var failed error
			ended, stop := false, errors.New("stop")
			err := zed.ReadLines(res.Body, wrapped, func(l zed.Line) error {
				switch {
				case l.Ended:
					ended = true
				case l.Failed != nil:
					send(Event{Kind: KError, Status: l.Failed.Status(), Text: "Zed: " + l.Failed.Error()})
					return stop
				case len(l.Event) > 0:
					if err := dec(string(l.Event), func(ev Event) {
						if failed == nil {
							failed = send(ev)
						}
					}); err != nil {
						return err
					}
				}
				return failed
			})
			switch {
			case errors.Is(err, stop) || failed != nil || ctx.Err() != nil:
			case err != nil:
				send(Event{Kind: KError, Text: "Zed: " + err.Error()})
			case wrapped && !ended:
				// Zed ends every whole reply with stream_ended
				send(Event{Kind: KError, Text: "Zed: the reply ended before it was complete"})
			}
		}()
		return events, 0, ""
	}
}

// zedFailure is the status and message for a refused request: Zed's
// {code, message, upstream_status}, a 402 as the plan it is.
func zedFailure(status int, b []byte) (int, string) {
	if status == http.StatusPaymentRequired {
		return 402, "Zed: payment required — this account's plan doesn't include Zed's hosted models, or its allowance is used up (see zed.dev/account)"
	}
	if status == http.StatusUnauthorized {
		return 401, "Zed: the sign-in was refused — sign in again"
	}
	code := status
	if up := gjson.GetBytes(b, "upstream_status").Int(); up >= 400 && up <= 599 {
		code = int(up)
	} else if c := gjson.GetBytes(b, "code").String(); c != "" {
		if n := (&zed.Failed{Code: c}).Status(); n != 502 {
			code = n
		}
	}
	if code < 400 || code > 599 {
		code = 502
	}
	msg := zed.Failure(status, b)
	if ra := gjson.GetBytes(b, "retry_after"); ra.Exists() && ra.Float() > 0 {
		msg += " (retry after " + strconv.FormatFloat(ra.Float(), 'f', -1, 64) + "s)"
	}
	return code, "Zed: " + msg
}
