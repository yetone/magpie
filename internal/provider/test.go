package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Result is what a probe of one endpoint came back with.
type Result struct {
	Protocol Protocol `json:"protocol"`
	OK       bool     `json:"ok"`
	Status   int      `json:"status,omitempty"`
	Millis   int64    `json:"ms"`
	Model    string   `json:"model,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Test sends the smallest possible request to each endpoint the vendor
// serves, signed with a key made for it and asking for a model that key
// sees, and reports what came back.
func (p Provider) Test(ctx context.Context) []Result {
	if p.Decides() {
		return p.testDecide(ctx)
	}
	p.Fetch(ctx)
	var out []Result
	for _, proto := range p.Speaks() {
		q, ok := p.keyFor(proto)
		model := p.testModel(q, proto)
		if !ok {
			out = append(out, Result{Protocol: proto, Model: model, Error: "no key is on for this endpoint"})
			continue
		}
		var url, body string
		switch proto {
		case Chat:
			url = q.Chat + "/chat/completions"
			body = fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, model)
		case Responses:
			url = q.Responses + "/responses"
			body = fmt.Sprintf(`{"model":%q,"input":"hi","max_output_tokens":16}`, model)
		case Anthropic:
			url = q.Anthropic + "/v1/messages"
			body = fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, model)
		}
		out = append(out, probe(ctx, q, proto, url, q.Prepare([]byte(body)), model))
	}
	return out
}

// keyFor is p using the first key on that works with proto: one made for
// it, else one made for any. A provider without keys is left as it is.
func (p Provider) keyFor(proto Protocol) (Provider, bool) {
	keys := p.KeysOn()
	if len(keys) == 0 {
		return p, true
	}
	for _, want := range []Protocol{proto, ""} {
		for _, k := range keys {
			if k.Protocol == want {
				return p.WithKey(k), true
			}
		}
	}
	return p, false
}

// testModel is the model a probe of proto's endpoint asks for: the first
// exposed one q's key sees, else the first it sees at all — preferring a
// Claude model on the Anthropic endpoint.
func (p Provider) testModel(q Provider, proto Protocol) string {
	k := q.first()
	var pools [][]catalog.Model
	if ms := p.Exposed(); len(ms) > 0 {
		pools = append(pools, ms)
	}
	pools = append(pools, p.Available())
	for _, want := range []func(string) bool{
		func(id string) bool {
			if apis := p.APIs(id); apis != nil {
				return slices.Contains(apis, proto)
			}
			return proto != Anthropic || isClaude(id)
		},
		func(string) bool { return true },
	} {
		for _, pool := range pools {
			for _, m := range pool {
				if want(m.ID) && (k.Key == "" || p.Serves(k, m.ID)) {
					return m.ID
				}
			}
		}
	}
	return ""
}

func isClaude(id string) bool {
	id = strings.ToLower(id)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	return strings.HasPrefix(id, "claude")
}

// AuthHeaders is how a request to the vendor proves who it is. Anthropic's
// own API wants x-api-key alone; compatible vendors take either, so both.
func AuthHeaders(p Provider, proto Protocol) map[string]string {
	if p.Key == "" {
		return map[string]string{}
	}
	if proto == Anthropic {
		if strings.HasSuffix(p.Host(), "anthropic.com") {
			return map[string]string{"x-api-key": p.Key}
		}
		return map[string]string{"x-api-key": p.Key, "Authorization": "Bearer " + p.Key}
	}
	return map[string]string{"Authorization": "Bearer " + p.Key}
}

func probe(ctx context.Context, p Provider, proto Protocol, url string, body []byte, model string) Result {
	r := Result{Protocol: proto, Model: model}
	if model == "" {
		r.Error = "no model to try: expose one, or refresh the model list"
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		r.Error = err.Error()
		return r
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if p.IsOpenCode() {
		req.Header.Set("x-opencode-session", "magpie-test-"+randomUUID())
	}
	if err := p.Sign(ctx, req, proto, body); err != nil {
		r.Error = err.Error()
		return r
	}
	start := time.Now()
	res, err := http.DefaultClient.Do(req)
	r.Millis = time.Since(start).Milliseconds()
	if err != nil {
		r.Error = strings.TrimPrefix(err.Error(), "Post \""+url+"\": ")
		return r
	}
	defer res.Body.Close()
	r.Status = res.StatusCode
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		r.OK = true
		return r
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	r.Error = APIError(b, res.Status)
	return r
}

// APIError pulls the human message out of an error body when there is one.
func APIError(b []byte, fallback string) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"` // FastAPI's (TypeSafe)
	}
	if json.Unmarshal(b, &v) == nil {
		if len(v.Detail) > 0 {
			v.Error = v.Detail
		}
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(v.Error, &e) == nil && e.Message != "" {
			return e.Message
		}
		var s string
		if json.Unmarshal(v.Error, &s) == nil && s != "" {
			return s
		}
		if v.Message != "" {
			return v.Message
		}
	}
	if s := strings.TrimSpace(string(b)); s != "" && len(s) < 200 && !strings.HasPrefix(s, "<") {
		return fallback + ": " + s
	}
	return fallback
}
