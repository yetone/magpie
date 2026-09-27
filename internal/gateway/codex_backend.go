package gateway

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// CodexPath is where Codex's built-in OpenAI provider reaches magpie, as its
// `openai_base_url`. Codex keeps its own sign-in and sends what it would send
// the ChatGPT backend: a request for one of its own models goes on there as
// it came, one for a magpie model (a catalog id, provider/model) is served
// like any other, and the model list is OpenAI's with magpie's added. Ending
// in /backend-api/codex keeps Codex treating it as that backend.
const CodexPath = "/backend-api/codex"

// codexAPIBase is where a Codex signed in with an API key sends its requests;
// its model list still comes from the ChatGPT backend. A var so tests can
// point it elsewhere.
var codexAPIBase = "https://api.openai.com/v1"

// magpieCompaction marks a compaction item magpie made: its summary, which
// only magpie reads back.
const magpieCompaction = "magpie1:"

// codexCompactPrompt and codexSummaryPrefix are Codex's own (Apache-2.0,
// openai/codex, prompts/templates/compact).
const codexCompactPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.`

const codexSummaryPrefix = `Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:`

func (s *Server) codexBackend(w http.ResponseWriter, r *http.Request) {
	// Responses over a WebSocket: 426 sends Codex to plain HTTP at once
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "magpie speaks HTTP", http.StatusUpgradeRequired)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, CodexPath)
	body, err := codexBody(r)
	if err != nil {
		writeError(w, provider.Responses, 400, err.Error())
		return
	}
	switch {
	case r.Method == http.MethodGet && rest == "/models":
		s.codexModels(w, r)
		return
	case r.Method == http.MethodPost && (rest == "/responses" || rest == "/responses/compact"):
		var model string
		body, model, err = requestModel(body)
		if err != nil {
			writeError(w, provider.Responses, 400, err.Error())
			return
		}
		// The namespace owns the route even if a model is not in the catalog.
		// Unknown providers/groups must fail locally, never fall through to OpenAI.
		if strings.Contains(model, "/") {
			if rest == "/responses/compact" {
				writeError(w, provider.Responses, 400, "/responses/compact is not supported for Magpie models; use a compaction_trigger on /responses")
				return
			}
			body, compact := codexInput(body, true)
			if compact {
				s.codexCompact(w, r, body)
				return
			}
			s.serve(w, r, provider.Responses, body)
			return
		}
		if rest == "/responses/compact" {
			break // preserve native compaction's existing passthrough
		}
		body, _ = codexInput(body, false)
		if id, ok := codexAccounts(r.Header, model); ok {
			s.serve(w, r, provider.Responses, withModel(body, id))
			return
		}
	}
	s.codexUpstream(w, r, rest, body)
}

// codexBody reads a request's body as it was before Codex compressed it
// (zstd, for the ChatGPT backend), so it can be read and passed on plain.
func codexBody(r *http.Request) ([]byte, error) {
	var rd io.Reader = r.Body
	switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "zstd":
		d, err := zstd.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		rd = d
	case "gzip":
		g, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		rd = g
	default:
		return nil, fmt.Errorf("magpie can't read a %s body", enc)
	}
	r.Header.Del("Content-Encoding")
	return io.ReadAll(rd)
}

// codexAccounts is what a request for one of Codex's own models is served
// as when Codex is signed in to ChatGPT and more of its accounts are on in
// magpie: the codex subscription's model (codex/<model>), which goes to the
// account Codex is signed in to and, when that one is out of its allowance
// or rate limited, on to the next — as it can't when relayed as it came.
func codexAccounts(h http.Header, model string) (string, bool) {
	if model == "" || strings.Contains(model, "/") || apiKey(h) {
		return "", false
	}
	id := "codex/" + model
	p, _, ok := provider.Resolve(id)
	if !ok || p.Account == nil || p.Account.Agent != "codex" || len(p.AlsoOn()) == 0 {
		return "", false
	}
	return id, true
}

// withModel is a request body asking for another model.
func withModel(body []byte, model string) []byte {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	q["model"], _ = json.Marshal(model)
	b, err := json.Marshal(q)
	if err != nil {
		return body
	}
	return b
}

// codexUpstream relays a request as it came, the sign-in included, to where
// Codex would have sent it.
func (s *Server) codexUpstream(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	start := time.Now()
	usage.Saw(agentOf(r))
	if r.Method == http.MethodPost {
		var unmask func()
		w, body, unmask = redacted(w, body)
		defer unmask()
	}
	base := provider.CodexBase
	if apiKey(r.Header) && rest != "/models" {
		base = codexAPIBase
	}
	u := base + rest
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	// a turn goes in the Routing view's live trace as the others do, to
	// the one it can go to: Codex's own sign-in, or its key
	var tr *Route
	end := func(status int, msg string, tokens int) {}
	if rest == "/responses" {
		who := "Codex's own sign-in"
		if base == codexAPIBase {
			who = "Codex's API key"
		}
		model := modelOf(body)
		seat := Weighed{ID: "codex", Provider: "openai", Name: "OpenAI", Icon: "openai", Who: who, Kind: "account", Agent: "codex", Model: model}
		tr = s.trace.begin(Route{Time: start, Agent: agentOf(r), Model: model, Provider: "openai",
			Order: []Weighed{seat}, Tries: []Try{{ID: seat.ID, Model: model, Start: start}}})
		end = func(status int, msg string, tokens int) {
			ms := time.Since(start).Milliseconds()
			s.trace.update(tr, func(t *Route) {
				t.Tries[0].Done, t.Tries[0].Status, t.Tries[0].Millis, t.Tries[0].Error = true, status, ms, msg
				t.Done, t.Status, t.Error, t.Millis, t.Tokens = true, status, msg, ms, tokens
			})
		}
	}
	var res *http.Response
	for tries := 0; ; tries++ {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, u, bytes.NewReader(body))
		if err != nil {
			writeError(w, provider.Responses, 502, err.Error())
			end(502, err.Error(), 0)
			return
		}
		copyHeaders(req.Header, r.Header)
		// left to the transport, the reply comes back plain for the usage in it
		req.Header.Del("Accept-Encoding")
		if res, err = s.client.Do(req); err != nil {
			writeError(w, provider.Responses, 502, "OpenAI: "+err.Error())
			end(502, "OpenAI: "+err.Error(), 0)
			return
		}
		if rest != "/responses" || tries >= 3 || (res.StatusCode != 400 && res.StatusCode != 404) {
			break
		}
		// an item OpenAI can't take — sealed by another account, or
		// another vendor's that it looks up and doesn't have — is taken
		// out and the rest asked again, rather than the conversation
		// stuck on it for good
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(msg))
		b, ok := withoutUnreadable(body, msg)
		if !ok {
			break
		}
		body = b
	}
	defer res.Body.Close()
	if base == codexAPIBase && res.StatusCode == http.StatusUnauthorized {
		// Codex signed in with an API key OpenAI refuses — often one a
		// relay issued, left in ~/.codex/auth.json — and one of Codex's own
		// models was picked, which goes out with Codex's own sign-in
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		writeError(w, provider.Responses, res.StatusCode, codexKeyRefused(msg))
		s.record(Call{Time: start, From: provider.Responses, To: provider.Responses, Model: modelOf(body),
			Provider: "openai", Agent: agentOf(r), Status: res.StatusCode,
			Millis: time.Since(start).Milliseconds(), Error: res.Status})
		end(res.StatusCode, res.Status, 0)
		return
	}
	for k, vs := range res.Header {
		if !hopHeader(k) {
			w.Header()[k] = vs
		}
	}
	modelsEtag(w.Header())
	w.WriteHeader(res.StatusCode)
	var sniff *usageSniffer
	if rest == "/responses" {
		ct := res.Header.Get("Content-Type")
		if ct == "" && streamOf(body) { // the ChatGPT backend streams without saying so
			ct = "text/event-stream"
		}
		sniff = newSniffer(provider.Responses, ct)
	}
	f, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			if sniff != nil {
				sniff.write(buf[:n])
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
	if sniff == nil {
		return
	}
	call := Call{Time: start, From: provider.Responses, To: provider.Responses, Model: modelOf(body),
		Provider: "openai", Agent: agentOf(r), Status: res.StatusCode,
		Millis: time.Since(start).Milliseconds()}
	var uu Usage
	uu.add(sniff.usage())
	call.Usage = uu
	if res.StatusCode >= 400 {
		call.Error = res.Status
	}
	end(call.Status, call.Error, uu.Input+uu.Output+uu.CacheRead+uu.CacheWrite)
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: call.Provider, Host: provider.HostOf(base), Model: call.Model,
		Input: uu.Input, Output: uu.Output, CacheRead: uu.CacheRead, CacheWrite: uu.CacheWrite,
		Reasoning: uu.Reasoning, Millis: call.Millis, Status: call.Status})
}

// unreadableItem is the item OpenAI's refusal names: sealed content it
// can't verify, or an id it doesn't have ("Item with id 'rs_…' not found").
var unreadableItem = regexp.MustCompile(`(?i)(?:encrypted content for item|item with id) '?([A-Za-z]+_[A-Za-z0-9_-]+)'?`)

// withoutUnreadable is body without what OpenAI's refusal msg says it
// can't read: the item it names, or the reasoning another account sealed.
func withoutUnreadable(body, msg []byte) ([]byte, bool) {
	if !foreignReasoning.Match(msg) && !bytes.Contains(bytes.ToLower(msg), []byte("not found")) {
		return nil, false
	}
	if m := unreadableItem.FindSubmatch(msg); m != nil {
		if b, ok := withoutItem(body, string(m[1])); ok {
			return b, true
		}
	}
	if foreignReasoning.Match(msg) {
		return withoutReasoning(body)
	}
	return nil, false
}

// withoutItem is a Responses request without the input item of this id.
func withoutItem(body []byte, id string) ([]byte, bool) {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return nil, false
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return nil, false
	}
	kept := items[:0:0]
	for _, it := range items {
		var t struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(it, &t) == nil && t.ID == id {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(items) {
		return nil, false
	}
	q["input"], _ = json.Marshal(kept)
	b, err := json.Marshal(q)
	return b, err == nil
}

// apiKey reports whether Codex signed in with an API key rather than a
// ChatGPT account.
// codexKeyRefused says why a model of Codex's own failed with 401: what
// OpenAI said, and what to do about it.
func codexKeyRefused(msg []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	said := strings.TrimSpace(string(msg))
	if json.Unmarshal(msg, &e) == nil && e.Error.Message != "" {
		said = e.Error.Message
	}
	return "OpenAI refused the API key Codex is signed in with (" + said + "). " +
		"This is one of Codex's own models, which goes to OpenAI with Codex's own sign-in: " +
		"pick one of magpie's models in Codex (provider/model, or set it in magpie's Agents view), " +
		"or sign Codex in with ChatGPT, or with an OpenAI API key"
}

func apiKey(h http.Header) bool {
	return strings.HasPrefix(strings.TrimPrefix(h.Get("Authorization"), "Bearer "), "sk-")
}

func hopHeader(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Connection", "Keep-Alive", "Proxy-Connection", "Transfer-Encoding", "Upgrade", "Te", "Trailer", "Content-Length":
		return true
	}
	return false
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if !hopHeader(k) && http.CanonicalHeaderKey(k) != "Host" {
			dst[k] = vs
		}
	}
}

// codexModels is the ChatGPT backend's model list for this sign-in, with
// magpie's models after it. Should the backend not answer, Codex's last
// list of its own stands in.
func (s *Server) codexModels(w http.ResponseWriter, r *http.Request) {
	var own []any
	etag := ""
	u := provider.CodexBase + "/models"
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	if req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil); err == nil {
		copyHeaders(req.Header, r.Header)
		req.Header.Del("Accept-Encoding")
		if res, err := s.client.Do(req); err == nil {
			var list struct {
				Models []any `json:"models"`
			}
			b, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
			res.Body.Close()
			if res.StatusCode < 300 && json.Unmarshal(b, &list) == nil {
				own, etag = list.Models, res.Header.Get("ETag")
			} else if res.StatusCode == 401 || res.StatusCode == 403 {
				// a sign-in to renew is Codex's to see
				w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
				w.WriteHeader(res.StatusCode)
				w.Write(b)
				return
			}
		}
	}
	if own == nil {
		for _, e := range codexcat.CacheEntries() {
			own = append(own, e)
		}
	}
	ms := provider.CodexListed()
	// the list is the backend's and magpie's, and so is its ETag
	w.Header().Set("ETag", codexcat.WithTag(etag, codexcat.Tag(ms)))
	writeJSON(w, 200, map[string]any{"models": append(own, codexcat.Entries(ms, len(own)+100)...)})
}

// modelsEtag is the X-Models-Etag of a backend reply as Codex should read
// it: with magpie's models in it, as the list's own ETag has them, so a
// change to either has Codex ask for the list again.
func modelsEtag(h http.Header) {
	if v := h.Get("X-Models-Etag"); v != "" {
		h.Set("X-Models-Etag", codexcat.WithTag(v, codexcat.Tag(provider.CodexListed())))
	}
}

// codexInput restores summaries magpie made. For a magpie model, it also
// replaces Codex's compaction trigger with a request to summarise the input.
// OpenAI's own models keep the trigger for their backend to handle.
func codexInput(body []byte, magpieModel bool) (_ []byte, compact bool) {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body, false
	}
	var items []map[string]any
	if json.Unmarshal(q["input"], &items) != nil {
		return body, false
	}
	changed := false
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		switch it["type"] {
		case "compaction", "compaction_summary":
			enc, _ := it["encrypted_content"].(string)
			sum, ok := strings.CutPrefix(enc, magpieCompaction)
			if !ok {
				out = append(out, it) // OpenAI's own, for OpenAI
				continue
			}
			changed = true
			if b, err := base64.StdEncoding.DecodeString(sum); err == nil {
				out = append(out, userMessage(codexSummaryPrefix+"\n"+string(b)))
			}
		case "compaction_trigger":
			if !magpieModel {
				out = append(out, it)
				continue
			}
			changed, compact = true, true
			out = append(out, userMessage(codexCompactPrompt))
		case "reasoning":
			// OpenAI accepts no reasoning content parts in replayed input.
			// Drop the item so its encrypted_content cannot fail there too.
			if !magpieModel {
				if content, present := it["content"]; present && content != nil {
					parts, ok := content.([]any)
					if !ok || len(parts) > 0 {
						changed = true
						continue
					}
				}
				// Nor reasoning with nothing sealed in it — another vendor's,
				// only an id (rs_…): OpenAI, which keeps nothing (store is
				// false), looks the id up and answers 404 "Item … not found".
				if enc, _ := it["encrypted_content"].(string); enc == "" {
					changed = true
					continue
				}
			}
			out = append(out, it)
		default:
			out = append(out, it)
		}
	}
	if !changed {
		return body, false
	}
	b, _ := json.Marshal(out)
	q["input"] = b
	if compact {
		// the summary is text; a tool call would be no summary
		delete(q, "tools")
		delete(q, "tool_choice")
		delete(q, "parallel_tool_calls")
		q["stream"] = json.RawMessage("false")
	}
	nb, err := json.Marshal(q)
	if err != nil {
		return body, false
	}
	return nb, compact
}

func userMessage(text string) map[string]any {
	return map[string]any{"type": "message", "role": "user",
		"content": []map[string]any{{"type": "input_text", "text": text}}}
}

// codexCompact compacts a conversation held by a magpie model: the model
// summarises it, and the summary goes back to Codex as the compaction item
// the backend would have made, marked as magpie's so magpie reads it back
// in later requests.
func (s *Server) codexCompact(w http.ResponseWriter, r *http.Request, body []byte) {
	rec := &recorder{header: http.Header{}, status: 200}
	s.serve(rec, r, provider.Responses, body)
	if rec.status >= 400 {
		for k, vs := range rec.header {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.status)
		w.Write(rec.body.Bytes())
		return
	}
	var res struct {
		ID     string `json:"id"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(rec.body.Bytes(), &res); err != nil {
		writeError(w, provider.Responses, 502, "compaction: "+err.Error())
		return
	}
	var sum strings.Builder
	for _, o := range res.Output {
		if o.Type != "message" {
			continue
		}
		for _, c := range o.Content {
			sum.WriteString(c.Text)
		}
	}
	if strings.TrimSpace(sum.String()) == "" {
		writeError(w, provider.Responses, 502, "compaction: the model wrote no summary")
		return
	}
	id := res.ID
	if id == "" {
		id = fmt.Sprintf("resp_magpie_%d", time.Now().UnixNano())
	}
	item := map[string]any{"type": "compaction", "id": "cmp_" + strings.TrimPrefix(id, "resp_"),
		"encrypted_content": magpieCompaction + base64.StdEncoding.EncodeToString([]byte(sum.String()))}
	done := map[string]any{"id": id, "object": "response", "status": "completed", "output": []any{item}}
	if len(res.Usage) > 0 {
		done["usage"] = res.Usage
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	for _, ev := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "status": "in_progress", "output": []any{}}},
		{"type": "response.output_item.added", "output_index": 0, "item": item},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": done},
	} {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev["type"], b)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// recorder keeps a reply for a second look.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(status int)      { r.status = status }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
