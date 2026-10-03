package gateway

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// isClaudeOAuth reports whether the request carries Claude Code's own OAuth
// bearer token (e.g. sk-ant-oat01-...).
func isClaudeOAuth(r *http.Request) bool {
	a := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(a) >= 7 && strings.EqualFold(a[:7], "Bearer ") {
		token := strings.TrimSpace(a[7:])
		return strings.HasPrefix(token, "sk-ant-oat")
	}
	return false
}

// isNativeAnthropicModel reports whether model names an Anthropic native model
// rather than a Magpie provider (provider/model) or routing group (group/...).
func isNativeAnthropicModel(model string) bool {
	return model != "" && !strings.Contains(model, "/")
}

// claudeUpstream relays a request as it came from Claude Code, carrying its own
// OAuth bearer token, to Anthropic's official API (provider.ClaudeBase).
func (s *Server) claudeUpstream(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	start := time.Now()
	usage.Saw(agentOf(r))
	if r.Method == http.MethodPost {
		var unmask func()
		w, body, unmask = redacted(w, body)
		defer unmask()
	}

	u := provider.ClaudeBase + path
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}

	model := modelOf(body)
	metadata := requestSessionMetadata(r.Header, body)
	kind := requestCallKind(r.Header, metadata)
	seat := Weighed{
		ID:       "claude",
		Provider: "anthropic",
		Name:     "Anthropic",
		Icon:     "claude-color",
		Who:      "Claude Code's own sign-in",
		Kind:     "account",
		Agent:    "claude",
		Model:    model,
	}

	tr := s.trace.begin(Route{
		Time:          start,
		Agent:         agentOf(r),
		Session:       sessionOf(r.Header),
		ParentSession: titleParentSession(r.Header, metadata, kind),
		Kind:          kind,
		Model:         model,
		Provider:      "anthropic",
		Order:         []Weighed{seat},
		Tries:         []Try{{ID: seat.ID, Model: model, Start: start}},
	})

	first := firstToken{start: start}
	served := ""
	var uu Usage

	end := func(status int, msg string, tokens, out int) {
		ms := time.Since(start).Milliseconds()
		ttft, text := first.ms()
		s.trace.update(tr, func(t *Route) {
			t.Tries[0].Done, t.Tries[0].Status, t.Tries[0].Millis, t.Tries[0].Error = true, status, ms, msg
			t.Tries[0].TTFT, t.Tries[0].FirstText = ttft, text
			t.Done, t.Status, t.Error, t.Millis, t.Tokens = true, status, msg, ms, tokens
			t.Output, t.TTFT, t.FirstText = out, ttft, text
			t.Usage = routeUsage("anthropic", model, uu)
			t.Tries[0].Served, t.Tries[0].Swapped = served, swapped(model, served)
			t.Served, t.Swapped = t.Tries[0].Served, t.Tries[0].Swapped
		})
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, u, bytes.NewReader(body))
	if err != nil {
		writeError(w, provider.Anthropic, http.StatusBadGateway, err.Error())
		end(http.StatusBadGateway, err.Error(), 0, 0)
		return
	}

	copyHeaders(req.Header, r.Header)
	req.Header.Del("Accept-Encoding")

	res, err := s.client.Do(req)
	if err != nil {
		msg := "Anthropic: " + err.Error()
		writeError(w, provider.Anthropic, http.StatusBadGateway, msg)
		end(http.StatusBadGateway, msg, 0, 0)
		return
	}
	defer res.Body.Close()

	for k, vs := range res.Header {
		if !hopHeader(k) {
			w.Header()[k] = vs
		}
	}
	w.WriteHeader(res.StatusCode)

	var sniff *usageSniffer
	ct := res.Header.Get("Content-Type")
	if strings.Contains(ct, "event-stream") || streamOf(body) {
		sniff = newSniffer(provider.Anthropic, "text/event-stream")
	} else if strings.Contains(ct, "json") {
		sniff = newSniffer(provider.Anthropic, "application/json")
	}

	f, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	var refusal []byte
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			if sniff != nil {
				sniff.write(buf[:n])
				first.see(buf[:n])
			}
			if res.StatusCode >= 400 && len(refusal) < 8<<10 {
				refusal = append(refusal, buf[:n]...)
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}

	if sniff != nil {
		uu.add(sniff.usage())
		served = uu.Served
	}

	call := Call{
		Time:     start,
		From:     provider.Anthropic,
		To:       provider.Anthropic,
		Model:    model,
		Provider: "anthropic",
		Agent:    agentOf(r),
		Kind:     kind,
		Status:   res.StatusCode,
		Millis:   time.Since(start).Milliseconds(),
		Usage:    uu,
	}
	call.TTFT, call.FirstText = first.ms()

	errType := ""
	if res.StatusCode >= 400 {
		call.Error = provider.APIError(refusal, res.Status)
		errType = provider.ErrorType(refusal)
	}
	end(call.Status, call.Error, uu.Input+uu.Output+uu.CacheRead+uu.CacheWrite, uu.Output)
	s.record(call)

	rec := usage.Record{
		RouteID:       tr.ID,
		Time:          start,
		Agent:         call.Agent,
		Provider:      "anthropic",
		Host:          provider.HostOf(provider.ClaudeBase),
		Model:         model,
		Requested:     model,
		Served:        served,
		Input:         uu.Input,
		Output:        uu.Output,
		CacheRead:     uu.CacheRead,
		CacheWrite:    uu.CacheWrite,
		Reasoning:     uu.Reasoning,
		Millis:        call.Millis,
		TTFT:          call.TTFT,
		FirstText:     call.FirstText,
		Status:        call.Status,
		Session:       sessionOf(r.Header),
		NativeSession: nativeSessionOf(r.Header),
		Kind:          call.Kind,
		RequestID:     requestID(res.Header),
		Endpoint:      path,
	}
	failedWith(&rec, call.Status, call.Error, errType)
	appendUsage(r, rec)
}
