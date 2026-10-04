package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// codexTitlesTo is where a request Codex makes for a thread's title goes
// (#705): a hidden turn of its own (thread_title,
// thread_title_reconsideration), which Codex sends on its own model —
// gpt-5.6-luna when signed in to ChatGPT, the conversation's model when
// magpie is its provider. "" for a request that isn't one, or one left to
// go as Codex sent it; "off", or the model that writes the title:
// settings.CodexTitles, else the model Codex asked for when it is one of
// magpie's (ours: the request came to magpie's own API, where every model
// is). Codex reads a title only as the {"title": …} its text.format asks
// for, which a model behind the Chat or Anthropic API is never shown, so
// a title on one of magpie's models goes through codexTitle whoever picked
// it (#743: Codex as magpie's provider had none at all, the model's plain
// answer turned down).
func codexTitlesTo(h http.Header, body []byte, ours bool) string {
	if !isTitleKind(requestCallKind(h, requestSessionMetadata(h, body))) {
		return ""
	}
	if to := settings.Load().CodexTitles; to != "" {
		return to
	}
	if m := modelOf(body); m != "" && (ours || strings.Contains(m, "/")) {
		return m
	}
	return ""
}

// titleCheckKey holds, in a request's context, what serve asks of the
// reply it relayed before it records the call: the reason it fails its
// caller although the vendor answered, or "".
type titleCheckKey struct{}
type titleShapeKey struct{}

// replyTitleShape is the exact schema used by the title wrapper, or nil for a
// native reply. Inference must fingerprint the title the caller actually gets.
func replyTitleShape(ctx context.Context) *titleShape {
	shape, _ := ctx.Value(titleShapeKey{}).(*titleShape)
	return shape
}

// replyCheck is the check a request's context holds for serve, if any.
func replyCheck(ctx context.Context) func(reply string) string {
	f, _ := ctx.Value(titleCheckKey{}).(func(string) string)
	return f
}

// codexTitle answers a request for a thread's title as codexTitlesTo
// says: "off" here, with no title; a model's id by that model, its answer
// handed back as the {"title": …} Codex asked for. Either way the call is
// in the Usage and Routing views as the title request it is, and an answer
// with no title in it as the failure it is to Codex, which drops it
// without a word.
func (s *Server) codexTitle(w http.ResponseWriter, r *http.Request, body []byte, to string) {
	if to == "off" {
		s.codexTitleOff(w, r, body)
		return
	}
	shape := titleShapeOf(body)
	body, _ = codexInput(body, true)
	rec := &recorder{header: http.Header{}, status: 200}
	check := func(reply string) string {
		if res, err := compactReply([]byte(reply)); err == nil && titleJSON(messageText(res), shape) == "" {
			return noTitle(messageText(res))
		}
		return ""
	}
	ctx := context.WithValue(r.Context(), titleCheckKey{}, check)
	ctx = context.WithValue(ctx, titleShapeKey{}, &shape)
	s.serve(rec, r.WithContext(ctx), provider.Responses, withModel(body, to))
	if rec.status >= 400 {
		for k, vs := range rec.header {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.status)
		w.Write(rec.body.Bytes())
		return
	}
	res, err := compactReply(rec.body.Bytes())
	if err != nil {
		writeError(w, provider.Responses, 502, "title: "+err.Error())
		return
	}
	id := res.ID
	if id == "" {
		id = fmt.Sprintf("resp_magpie_%d", time.Now().UnixNano())
	}
	var out []any
	if t := titleJSON(messageText(res), shape); t != "" {
		out = append(out, map[string]any{"type": "message", "id": "msg_" + strings.TrimPrefix(id, "resp_"), "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": t, "annotations": []any{}}}})
	}
	writeTitleReply(w, id, out, res.Usage)
}

// messageText is the text of a reply's messages, its reasoning left out.
func messageText(res compactResult) string {
	var said strings.Builder
	for _, o := range res.Output {
		if o.Type != "message" {
			continue
		}
		for _, c := range o.Content {
			said.WriteString(c.Text)
		}
	}
	return said.String()
}

// noTitle is why an answer to a title request gave Codex no title: what
// the Routing and Usage views say of it, with the start of what the model
// said.
func noTitle(said string) string {
	said = strings.Join(strings.Fields(said), " ")
	if said == "" {
		return "title: the model answered with no text, so Codex got no title"
	}
	if r := []rune(said); len(r) > 80 {
		said = string(r[:80]) + "…"
	}
	return "title: no title in the model's answer, so Codex got none: " + said
}

// codexTitleOff answers a title request with no title, sending it nowhere:
// a finished reply with nothing in it, which Codex takes as no title and
// leaves the thread named as it was (its thread_title.rs: no message, or
// no {"title": …} in it, sets none). The Routing and Usage views have it
// as the title request it was, answered by magpie and sent to no one.
func (s *Server) codexTitleOff(w http.ResponseWriter, r *http.Request, body []byte) {
	start := time.Now()
	usage.Saw(agentOf(r))
	metadata := requestSessionMetadata(r.Header, body)
	kind := requestCallKind(r.Header, metadata)
	model := modelOf(body)
	const why = "Codex titles are off in magpie's Settings: answered with no title, sent nowhere"
	seat := Weighed{ID: "magpie", Provider: "magpie", Name: "magpie", Who: "Titles off", Kind: "provider", Model: model}
	tr := s.trace.begin(Route{Time: start, Agent: agentOf(r), Session: sessionOf(r.Header), ParentSession: titleParentSession(r.Header, metadata, kind),
		Kind: kind, Model: model, Provider: "magpie", Order: []Weighed{seat}, Tries: []Try{{ID: seat.ID, Model: model, Start: start}}})
	writeTitleReply(w, fmt.Sprintf("resp_magpie_%d", start.UnixNano()), nil, nil)
	ms := time.Since(start).Milliseconds()
	s.trace.update(tr, func(t *Route) {
		t.Tries[0].Done, t.Tries[0].Status, t.Tries[0].Millis, t.Tries[0].Error = true, 200, ms, why
		t.Done, t.Status, t.Error, t.Millis = true, 200, why, ms
	})
	s.record(Call{Time: start, From: provider.Responses, To: provider.Responses, Model: model, Provider: "magpie",
		Agent: agentOf(r), Kind: kind, Status: 200, Millis: ms, Error: why})
	appendUsage(r, usage.Record{RouteID: tr.ID, Time: start, Agent: agentOf(r), Provider: "magpie", Model: model, Requested: model,
		Millis: ms, Status: 200, Rejected: true, Error: why, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header),
		Kind: kind, Endpoint: r.URL.Path})
}

// writeTitleReply streams a finished Responses reply with these output
// items, as the ChatGPT backend streams one to Codex.
func writeTitleReply(w http.ResponseWriter, id string, out []any, used json.RawMessage) {
	if out == nil {
		out = []any{}
	}
	done := map[string]any{"id": id, "object": "response", "status": "completed", "output": out}
	if len(used) > 0 {
		done["usage"] = used
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	evs := []map[string]any{{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "status": "in_progress", "output": []any{}}}}
	for i, it := range out {
		evs = append(evs, map[string]any{"type": "response.output_item.added", "output_index": i, "item": it},
			map[string]any{"type": "response.output_item.done", "output_index": i, "item": it})
	}
	evs = append(evs, map[string]any{"type": "response.completed", "response": done})
	for _, ev := range evs {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev["type"], b)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// titleShape is the object a title request's text.format asks for: its
// string properties' bounds and which are required. Codex 0.160's TUI asks
// for {"title"} alone; another of its builds for a description beside the
// title, both required and nothing else allowed (#743: magpie gave it the
// title alone, so it kept the first message as the thread's name).
type titleShape struct {
	Properties map[string]struct {
		Type      string `json:"type"`
		MaxLength int    `json:"maxLength"`
	} `json:"properties"`
	Required []string `json:"required"`
}

// titleShapeOf is the shape a Responses request's text.format.schema asks
// for, the zero shape (a title) when it names none.
func titleShapeOf(body []byte) titleShape {
	var q struct {
		Text struct {
			Format struct {
				Schema titleShape `json:"schema"`
			} `json:"format"`
		} `json:"text"`
	}
	json.Unmarshal(body, &q)
	return q.Text.Format.Schema
}

// titleJSON is a model's answer to a title request as Codex reads one:
// {"title": "…"}, with each other string its shape requires. Codex asks
// for that shape in the request's text.format, which a model behind
// another API never sees, and so may answer with the title alone, in
// quotes, or in a code fence. "" when it gave none.
func titleJSON(said string, shape titleShape) string {
	t := strings.TrimSpace(said)
	// a reasoning model's thoughts, where its API leaves them in the text
	if rest, ok := strings.CutPrefix(t, "<think>"); ok {
		if _, after, ok := strings.Cut(rest, "</think>"); ok {
			t = strings.TrimSpace(after)
		}
	}
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```")
		if i := strings.IndexByte(t, '\n'); i >= 0 && !strings.Contains(t[:i], "{") {
			t = t[i+1:]
		}
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
	}
	fields := map[string]any{}
	if strings.HasPrefix(t, "{") {
		if json.NewDecoder(bytes.NewReader([]byte(t))).Decode(&fields) == nil {
			if v, ok := fields["title"].(string); ok {
				t = v
			}
		}
	}
	// its first line, as a title is one
	for _, line := range strings.Split(t, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			t = line
			break
		}
	}
	if low := strings.ToLower(t); strings.HasPrefix(low, "title:") {
		t = t[len("title:"):]
	}
	t = strings.TrimSpace(strings.Trim(strings.TrimSpace(t), "\"'`*#“”‘’"))
	if t == "" || strings.HasPrefix(t, "{") {
		return ""
	}
	// each other string the schema requires, a description among them:
	// the model's own when its JSON gave one, else the title, every one
	// cut to the schema's length
	clip := func(name, v string) string {
		if n := shape.Properties[name].MaxLength; n > 0 {
			if r := []rune(v); len(r) > n {
				v = strings.TrimSpace(string(r[:n]))
			}
		}
		return v
	}
	out := map[string]string{"title": clip("title", t)}
	for _, name := range shape.Required {
		if _, done := out[name]; done {
			continue
		}
		if ty := shape.Properties[name].Type; ty != "" && ty != "string" {
			continue
		}
		v, _ := fields[name].(string)
		if v = strings.TrimSpace(v); v == "" {
			v = t
		}
		out[name] = clip(name, v)
	}
	b, _ := json.Marshal(out)
	return string(b)
}
