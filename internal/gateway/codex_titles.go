package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// codexTitlesTo is where settings.CodexTitles sends a request Codex makes
// for a thread's title (#705): a hidden turn of its own (thread_title,
// thread_title_reconsideration), which Codex sends on its own model —
// gpt-5.6-luna when signed in to ChatGPT — and so through its ChatGPT
// sign-in even while the conversation is on one of magpie's models. ""
// for a request that isn't one, or when the setting leaves them as Codex
// sends them; "off", or the model that writes the title.
func codexTitlesTo(h http.Header, body []byte) string {
	if !isTitleKind(requestCallKind(h, requestSessionMetadata(h, body))) {
		return ""
	}
	return settings.Load().CodexTitles
}

// codexTitle answers a request for a thread's title as settings.CodexTitles
// says: "off" here, with no title; a model's id by that model, its answer
// handed back as the {"title": …} Codex asked for. Either way the call is
// in the Usage and Routing views as the title request it is.
func (s *Server) codexTitle(w http.ResponseWriter, r *http.Request, body []byte, to string) {
	if to == "off" {
		s.codexTitleOff(w, r, body)
		return
	}
	body, _ = codexInput(body, true)
	rec := &recorder{header: http.Header{}, status: 200}
	s.serve(rec, r, provider.Responses, withModel(body, to))
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
	var said strings.Builder
	for _, o := range res.Output {
		if o.Type != "message" {
			continue
		}
		for _, c := range o.Content {
			said.WriteString(c.Text)
		}
	}
	id := res.ID
	if id == "" {
		id = fmt.Sprintf("resp_magpie_%d", time.Now().UnixNano())
	}
	var out []any
	if t := titleJSON(said.String()); t != "" {
		out = append(out, map[string]any{"type": "message", "id": "msg_" + strings.TrimPrefix(id, "resp_"), "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": t, "annotations": []any{}}}})
	}
	writeTitleReply(w, id, out, res.Usage)
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

// titleJSON is a model's answer to a title request as Codex reads one:
// {"title": "…"}. Codex asks for that shape in the request's text.format,
// which a model behind another API never sees, and so may answer with the
// title alone, in quotes, or in a code fence. "" when it gave none.
func titleJSON(said string) string {
	t := strings.TrimSpace(said)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```")
		if i := strings.IndexByte(t, '\n'); i >= 0 && !strings.Contains(t[:i], "{") {
			t = t[i+1:]
		}
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
	}
	if strings.HasPrefix(t, "{") {
		var v struct {
			Title *string `json:"title"`
		}
		if json.NewDecoder(bytes.NewReader([]byte(t))).Decode(&v) == nil && v.Title != nil {
			t = *v.Title
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
	b, _ := json.Marshal(map[string]string{"title": t})
	return string(b)
}
