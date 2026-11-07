package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// OpenAI's Decisions API (/v1/decisions) and System One (/v1/systemone)
// ask the same thing in two shapes, so a request at either reaches a
// model that answers only the other (wudi_boy on Discord): a Decisions
// request to Bailian's decision-model-preview, Ark's, a Jev, a Clef, a
// plugin's decision model or a group of them is asked as System One, and
// a System One request to gpt-6-luna as Decisions; each answer comes back
// in the shape it was asked in.
//
//	Decisions                       System One
//	input: text                     state: the text
//	input: messages                 state: {"messages":[{role, content}]}
//	input_json part                 its data, as JSON text
//	input_image part                images (a Clef's alone)
//	predicate                       noul (boolean at Vercel's)
//	choice, choices[].value         choice, criteria {value: description}
//	score, levels[].label           score, criteria [level, ...] least first
//	answers [{name, ...}] in order  answers {name: {...}}
//	usage.input/output_tokens       usage.input/output_tokens
//
// What has no counterpart is refused with a 400 that names it, never
// dropped: a tool call or its output, an image where the model takes
// none, an image in a question's instructions, a field the other API
// lacks. Prompt-cache hints and safety_identifier change no answer and
// System One has no place for them, so they alone go unsent.

// decisionQ is a Decisions question as it went to System One, for its
// answer to come back as Decisions gives one.
type decisionQ struct {
	name   *string // the agent's name for it; nil when it named none
	key    string  // its name in System One's questions
	kind   string  // predicate, choice or score
	order  []string
	values map[string]json.RawMessage // a choice's criteria, each its typed value
}

// untranslatable is a part of a request the other API has no counterpart
// for: the request is refused with a 400 that names it.
type untranslatable string

func (e untranslatable) Error() string { return string(e) }

func untranslated(format string, a ...any) error { return untranslatable(fmt.Sprintf(format, a...)) }

// decisionsUnsent are the Decisions fields that go unsent to System One:
// hints that change no answer.
var decisionsUnsent = []string{"prompt_cache_key", "prompt_cache_options", "safety_identifier"}

// systemOneTarget is a decision model a translated request may reach.
type systemOneTarget struct {
	p     provider.Provider
	model string
}

// takesImages is whether the model takes a System One request's images:
// a Clef on Workers AI does, and another magpie passes them on.
func (t systemOneTarget) takesImages() bool {
	if t.p.IsRemoteMagpie() {
		return true
	}
	return t.p.DecideVia() == provider.ViaCloudflare && provider.CloudflareClef(t.model)
}

func (t systemOneTarget) String() string { return t.p.ID + "/" + t.model }

// systemOneTargets are the decision models System One would ask for the
// model asked — a decision group's members, or the one RouteDecider
// finds — and whether there are any.
func systemOneTargets(asked string) ([]systemOneTarget, bool) {
	if _, ms, ok, _, err := decisionGroupOf(asked); err == nil && ok {
		var ts []systemOneTarget
		for _, m := range ms {
			ts = append(ts, systemOneTarget{m.Provider, m.Model})
		}
		return ts, len(ts) > 0
	}
	p, model, err := provider.RouteDecider(asked)
	if err != nil {
		return nil, false
	}
	return []systemOneTarget{{p, model}}, true
}

// decideBySystemOne answers a Decisions request whose model answers
// System One: asked there, its answer in Decisions' shape.
func (s *Server) decideBySystemOne(w http.ResponseWriter, r *http.Request, req map[string]json.RawMessage, asked string, ts []systemOneTarget) {
	body, qs, err := decisionsAsSystemOne(req, asked, ts)
	if err != nil {
		writeError(w, provider.Chat, http.StatusBadRequest, err.Error())
		return
	}
	rec := httptest.NewRecorder()
	s.serveSystemOneBody(rec, r, body)
	passTranslated(w, rec, func(b []byte) ([]byte, error) { return systemOneAsDecision(b, qs, asked) })
}

// systemOneByDecisions answers a System One request whose model only
// OpenAI's Decisions API serves: asked there, its answer in System One's
// shape. false when no provider serves the model there, and the caller
// says so as it would.
func (s *Server) systemOneByDecisions(w http.ResponseWriter, r *http.Request, body []byte, asked string) bool {
	if _, _, ok := resolveRetrievalModel(asked, kindDecision); !ok {
		return false
	}
	req, keys, err := systemOneAsDecisions(body)
	if err != nil {
		writeError(w, provider.Chat, http.StatusBadRequest, err.Error())
		return true
	}
	rec := httptest.NewRecorder()
	s.serveRetrieval(rec, r, "/decisions", "decisions", time.Now(), asked, &decisionRequest{jsonRetrieval{req: req}})
	passTranslated(w, rec, func(b []byte) ([]byte, error) { return decisionAsSystemOne(b, keys) })
	return true
}

// passTranslated passes on the answer rec holds: a 2xx translated, an
// error as it was said.
func passTranslated(w http.ResponseWriter, rec *httptest.ResponseRecorder, translate func([]byte) ([]byte, error)) {
	b := rec.Body.Bytes()
	if rec.Code < 300 {
		out, err := translate(b)
		if err != nil {
			writeError(w, provider.Chat, http.StatusBadGateway, err.Error())
			return
		}
		b = out
	}
	h := w.Header()
	for k, vs := range rec.Header() {
		h[k] = vs
	}
	h.Del("Content-Length")
	if rec.Code < 300 {
		h.Set("Content-Type", "application/json")
	}
	w.WriteHeader(rec.Code)
	w.Write(b)
}

// decisionsAsSystemOne is a Decisions request as System One asks it of
// the model asked (ts, what it may reach), and its questions in order.
func decisionsAsSystemOne(req map[string]json.RawMessage, asked string, ts []systemOneTarget) ([]byte, []decisionQ, error) {
	for _, k := range slices.Sorted(maps.Keys(req)) {
		if k != "model" && k != "input" && k != "questions" && !slices.Contains(decisionsUnsent, k) {
			return nil, nil, untranslated("%q has no counterpart in System One, which %s answers", k, asked)
		}
	}
	var images []string
	state, err := decisionsState(req["input"], ts, &images)
	if err != nil {
		return nil, nil, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(req["questions"], &raw); err != nil || len(raw) == 0 {
		return nil, nil, untranslated("\"questions\" is a list of 1 to 400 questions")
	}
	type question struct {
		Type         string          `json:"type"`
		Name         *string         `json:"name"`
		Instructions json.RawMessage `json:"instructions"`
		Choices      []struct {
			Value       json.RawMessage `json:"value"`
			Description *string         `json:"description"`
		} `json:"choices"`
		Levels []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"levels"`
	}
	asks := make([]question, len(raw))
	qs := make([]decisionQ, len(raw))
	named := map[string]bool{}
	for i, b := range raw {
		q := &asks[i]
		if err := json.Unmarshal(b, q); err != nil {
			return nil, nil, untranslated("questions[%d] is not a question: %v", i, err)
		}
		if q.Name != nil {
			if named[*q.Name] {
				return nil, nil, untranslated("questions[%d]: two questions are named %q, and System One asks each by its name", i, *q.Name)
			}
			named[*q.Name] = true
		}
		qs[i] = decisionQ{name: q.Name, kind: q.Type}
	}
	sq := map[string]any{}
	for i, q := range asks {
		d := &qs[i]
		if d.name != nil {
			d.key = *d.name
		} else {
			// an unnamed question is named for System One, as no named one is
			for n := i + 1; ; n++ {
				if d.key = "question_" + strconv.Itoa(n); !named[d.key] {
					named[d.key] = true
					break
				}
			}
		}
		instr, refusal, err := decisionsText(q.Instructions, fmt.Sprintf("questions[%d].instructions", i), nil)
		if err != nil {
			return nil, nil, err
		}
		if refusal != "" {
			return nil, nil, untranslated("questions[%d].instructions has a refusal part, which only an assistant's message has", i)
		}
		switch d.kind {
		case "predicate":
			sq[d.key] = map[string]any{"type": "noul", "instructions": instr}
		case "choice":
			criteria := map[string]any{}
			d.values = map[string]json.RawMessage{}
			for j, c := range q.Choices {
				var key string
				var on bool
				switch {
				case json.Unmarshal(c.Value, &key) == nil:
				case json.Unmarshal(c.Value, &on) == nil:
					key = strconv.FormatBool(on)
				default:
					return nil, nil, untranslated("questions[%d].choices[%d].value is neither a string nor a boolean", i, j)
				}
				if _, dup := d.values[key]; dup {
					return nil, nil, untranslated("questions[%d].choices[%d]: %q is there twice (as a string and as a boolean), and System One names a choice by its text alone", i, j, key)
				}
				d.values[key], d.order = c.Value, append(d.order, key)
				if c.Description != nil {
					criteria[key] = *c.Description
				} else {
					criteria[key] = nil
				}
			}
			sq[d.key] = map[string]any{"type": "choice", "instructions": instr, "criteria": criteria}
		case "score":
			levels := make([]string, len(q.Levels))
			for j, l := range q.Levels {
				d.order = append(d.order, l.Label)
				levels[j] = l.Label
				if l.Description != "" {
					levels[j] += ": " + l.Description
				}
			}
			sq[d.key] = map[string]any{"type": "score", "instructions": instr, "criteria": levels}
		default:
			return nil, nil, untranslated("questions[%d] is a %q question, which System One has no counterpart for (it asks predicate, choice and score)", i, d.kind)
		}
	}
	out := map[string]any{"model": asked, "state": state, "questions": sq}
	if len(images) > 0 {
		out["images"] = images
	}
	body, err := json.Marshal(out)
	return body, qs, err
}

// decisionsState is a Decisions input as System One's state: its text,
// or a conversation's messages, each one's parts as text. A message's
// images are put in images, where every model ts names takes them.
func decisionsState(input json.RawMessage, ts []systemOneTarget, images *[]string) (any, error) {
	var text string
	if json.Unmarshal(input, &text) == nil {
		return text, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, untranslated("\"input\" is neither text nor a list of messages")
	}
	msgs := make([]map[string]any, 0, len(items))
	for i, it := range items {
		var typ, role string
		json.Unmarshal(it["type"], &typ)
		json.Unmarshal(it["role"], &role)
		if typ != "" && typ != "message" {
			return nil, untranslated("input[%d] is a %s, which System One has no counterpart for: it is asked a state, not a conversation's tool calls and their outputs", i, typ)
		}
		if role == "" {
			return nil, untranslated("input[%d] is a message with no role", i)
		}
		content, refusal, err := decisionsText(it["content"], fmt.Sprintf("input[%d].content", i), func(where, url string) error {
			for _, t := range ts {
				if !t.takesImages() {
					return untranslated("%s is an image (input_image), which %s can't take: System One takes images only for Cloudflare's Clef", where, t)
				}
			}
			if !strings.HasPrefix(url, "data:") {
				return untranslated("%s is an image at a URL; System One's Clef takes an image only as a data: URL", where)
			}
			*images = append(*images, url)
			return nil
		})
		if err != nil {
			return nil, err
		}
		m := map[string]any{"role": role, "content": content}
		if refusal != "" { // an assistant's refusal, kept as what it is
			m["refusal"] = refusal
		}
		msgs = append(msgs, m)
	}
	return map[string]any{"messages": msgs}, nil
}

// decisionsText is a Decisions content (where it is: text, or a list of
// parts) as one text: its input_text and output_text parts as written,
// its input_json parts' data as JSON, and apart from them its refusal
// parts' text. An image goes to image, or, where there is none (a
// question's instructions), is refused.
func decisionsText(raw json.RawMessage, where string, image func(where, url string) error) (text, refusal string, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", "", nil
	}
	if json.Unmarshal(raw, &text) == nil {
		return text, "", nil
	}
	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Refusal  string          `json:"refusal"`
		Data     json.RawMessage `json:"data"`
		ImageURL string          `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", "", untranslated("%s is neither text nor a list of parts", where)
	}
	var out, refused []string
	for j, p := range parts {
		at := fmt.Sprintf("%s[%d]", where, j)
		switch p.Type {
		case "input_text", "output_text":
			out = append(out, p.Text)
		case "refusal":
			refused = append(refused, p.Refusal)
		case "input_json":
			var buf bytes.Buffer
			if err := json.Compact(&buf, p.Data); err != nil {
				return "", "", untranslated("%s is input_json with no JSON data", at)
			}
			out = append(out, buf.String())
		case "input_image":
			if image == nil {
				return "", "", untranslated("%s is an image (input_image), which System One has no counterpart for in a question's instructions", at)
			}
			if err := image(at, p.ImageURL); err != nil {
				return "", "", err
			}
		default:
			return "", "", untranslated("%s is a %q part, which System One has no counterpart for", at, p.Type)
		}
	}
	return strings.Join(out, "\n"), strings.Join(refused, "\n"), nil
}

// systemOneUsage is a System One answer's usage.
type systemOneUsage struct {
	Input  int `json:"input_tokens"`
	Output int `json:"output_tokens"`
}

// systemOneAsDecision is a System One answer as Decisions answers qs:
// each question's answer in their order, one System One left out a
// refusal, as Decisions says a question it didn't answer.
func systemOneAsDecision(b []byte, qs []decisionQ, asked string) ([]byte, error) {
	var a struct {
		Model   string                     `json:"model"`
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   systemOneUsage             `json:"usage"`
	}
	if err := decodeJSON(b, &a); err != nil || a.Answers == nil {
		return nil, fmt.Errorf("%s's answer is not a System One answer: %.200s", asked, b)
	}
	answers := make([]map[string]any, len(qs))
	for i, q := range qs {
		var x struct {
			Choice        json.RawMessage    `json:"choice"`
			Score         *float64           `json:"score"`
			Confidence    *float64           `json:"confidence"`
			Probabilities map[string]float64 `json:"probabilities"`
			Noul          *float64           `json:"noul"`
			Probability   *float64           `json:"probability"`
		}
		var name any
		if q.name != nil {
			name = *q.name
		}
		refused := map[string]any{"type": "refusal", "name": name}
		raw, ok := a.Answers[q.key]
		if !ok || json.Unmarshal(raw, &x) != nil {
			answers[i] = refused
			continue
		}
		confidence := func() float64 {
			if x.Confidence != nil {
				return *x.Confidence
			}
			ps := map[string]any{}
			for k, v := range x.Probabilities {
				ps[k] = v
			}
			return provider.Confidence(ps)
		}
		switch q.kind {
		case "predicate":
			p := x.Noul
			if p == nil {
				p = x.Probability
			}
			if p == nil {
				answers[i] = refused
				continue
			}
			answers[i] = map[string]any{"type": "predicate", "name": name, "probability": *p}
		case "choice":
			var said string
			if json.Unmarshal(x.Choice, &said) != nil {
				var on bool
				if json.Unmarshal(x.Choice, &on) != nil {
					answers[i] = refused
					continue
				}
				said = strconv.FormatBool(on)
			}
			value, ok := q.values[said]
			if !ok {
				return nil, fmt.Errorf("%s answered %s with %q, which is none of its choices", asked, q.key, said)
			}
			ps := []map[string]any{}
			for _, k := range q.order {
				if p, ok := x.Probabilities[k]; ok {
					ps = append(ps, map[string]any{"value": q.values[k], "probability": p})
				}
			}
			answers[i] = map[string]any{"type": "choice", "name": name, "choice": value, "confidence": confidence(), "probabilities": ps}
		case "score":
			if x.Score == nil {
				answers[i] = refused
				continue
			}
			ps := []map[string]any{}
			for k, label := range q.order {
				// keyed by the level's index, its label, or the label as asked
				for _, key := range []string{strconv.Itoa(k), label} {
					if p, ok := x.Probabilities[key]; ok {
						ps = append(ps, map[string]any{"label": label, "value": k, "probability": p})
						break
					}
				}
			}
			answers[i] = map[string]any{"type": "score", "name": name, "score": *x.Score, "confidence": confidence(), "probabilities": ps}
		}
	}
	model := a.Model
	if model == "" {
		model = asked
	}
	return json.Marshal(map[string]any{"model": model, "answers": answers, "usage": map[string]any{
		"input_tokens":          a.Usage.Input,
		"input_tokens_details":  map[string]int{"cached_tokens": 0, "cache_write_tokens": 0},
		"output_tokens":         a.Usage.Output,
		"output_tokens_details": map[string]int{"reasoning_tokens": 0},
		"total_tokens":          a.Usage.Input + a.Usage.Output,
	}})
}

// systemOneFields are the fields of a System One request Decisions has a
// place for.
var systemOneFields = []string{"model", "state", "questions", "images"}

// systemOneAsDecisions is a System One request as Decisions asks it, and
// its questions' names in order (Decisions answers in that order).
func systemOneAsDecisions(body []byte) (map[string]json.RawMessage, []string, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, untranslated("not a System One request")
	}
	for _, k := range slices.Sorted(maps.Keys(req)) {
		if !slices.Contains(systemOneFields, k) {
			return nil, nil, untranslated("%q has no counterpart in OpenAI's Decisions API, which the model answers", k)
		}
	}
	var images []string
	if raw, ok := req["images"]; ok && json.Unmarshal(raw, &images) != nil {
		return nil, nil, untranslated("\"images\" is a list of image URLs")
	}
	if len(req["state"]) == 0 {
		return nil, nil, untranslated("a System One request with no state: Decisions asks its questions of an input")
	}
	var parts []map[string]any
	var text string
	var data any
	switch {
	case json.Unmarshal(req["state"], &text) == nil:
		parts = append(parts, map[string]any{"type": "input_text", "text": text})
	case json.Unmarshal(req["state"], &data) == nil && isJSONObjects(data):
		parts = append(parts, map[string]any{"type": "input_json", "data": data})
	default:
		var buf bytes.Buffer
		json.Compact(&buf, req["state"])
		parts = append(parts, map[string]any{"type": "input_text", "text": buf.String()})
	}
	for _, im := range images {
		parts = append(parts, map[string]any{"type": "input_image", "image_url": im})
	}
	var input any = []map[string]any{{"type": "message", "role": "user", "content": parts}}
	if len(images) == 0 && text != "" {
		input = text
	}
	keys, qs, err := orderedObject(req["questions"])
	if err != nil || len(keys) == 0 {
		return nil, nil, untranslated("\"questions\" is an object of questions by name")
	}
	list := make([]map[string]any, len(keys))
	for i, k := range keys {
		var q struct {
			Type         string          `json:"type"`
			Instructions string          `json:"instructions"`
			Criteria     json.RawMessage `json:"criteria"`
			Choices      json.RawMessage `json:"choices"`
		}
		if err := json.Unmarshal(qs[i], &q); err != nil {
			return nil, nil, untranslated("questions.%s is not a question", k)
		}
		d := map[string]any{"name": k, "instructions": q.Instructions}
		options := q.Criteria
		if len(options) == 0 {
			options = q.Choices
		}
		switch q.Type {
		case "noul", "boolean":
			d["type"] = "predicate"
		case "choice":
			names, descs, err := criteriaOf(options)
			if err != nil {
				return nil, nil, untranslated("questions.%s: %v", k, err)
			}
			cs := make([]map[string]any, len(names))
			for j, n := range names {
				cs[j] = map[string]any{"value": n}
				if descs[j] != "" {
					cs[j]["description"] = descs[j]
				}
			}
			d["type"], d["choices"] = "choice", cs
		case "score":
			names, descs, err := criteriaOf(options)
			if err != nil {
				return nil, nil, untranslated("questions.%s: %v", k, err)
			}
			ls := make([]map[string]any, len(names))
			for j, n := range names {
				ls[j] = map[string]any{"label": n}
				if descs[j] != "" {
					ls[j]["description"] = descs[j]
				}
			}
			d["type"], d["levels"] = "score", ls
		default:
			return nil, nil, untranslated("questions.%s is a %q question, which OpenAI's Decisions API has no counterpart for (it asks predicate, choice and score)", k, q.Type)
		}
		list[i] = d
	}
	out := map[string]json.RawMessage{"model": req["model"]}
	out["input"], _ = json.Marshal(input)
	out["questions"], _ = json.Marshal(list)
	return out, keys, nil
}

// isJSONObjects is whether v is what Decisions' input_json takes: an
// object, or a list of objects.
func isJSONObjects(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		return true
	case []any:
		return len(x) > 0 && !slices.ContainsFunc(x, func(e any) bool { _, ok := e.(map[string]any); return !ok })
	}
	return false
}

// criteriaOf is a System One question's criteria: a list of names, or
// an object of names to descriptions, in order.
func criteriaOf(raw json.RawMessage) ([]string, []string, error) {
	var list []string
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		return list, make([]string, len(list)), nil
	}
	keys, vals, err := orderedObject(raw)
	if err != nil || len(keys) == 0 {
		return nil, nil, fmt.Errorf("its criteria are neither a list of names nor an object of them")
	}
	descs := make([]string, len(keys))
	for i, v := range vals {
		if json.Unmarshal(v, &descs[i]) != nil && string(v) != "null" {
			var buf bytes.Buffer
			json.Compact(&buf, v)
			descs[i] = buf.String()
		}
	}
	return keys, descs, nil
}

// orderedObject is a JSON object's keys in the order written, and their
// values.
func orderedObject(raw json.RawMessage) ([]string, []json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, fmt.Errorf("not a JSON object")
	}
	var keys []string
	var vals []json.RawMessage
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return nil, nil, err
		}
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return nil, nil, err
		}
		keys, vals = append(keys, t.(string)), append(vals, v)
	}
	return keys, vals, nil
}

// decisionAsSystemOne is a Decisions answer as System One gives it: each
// answer under its question's name (keys, the questions in the order
// asked), its usage's input and output tokens.
func decisionAsSystemOne(b []byte, keys []string) ([]byte, error) {
	var a struct {
		Model   string `json:"model"`
		Answers []struct {
			Type          string          `json:"type"`
			Name          *string         `json:"name"`
			Probability   float64         `json:"probability"`
			Choice        json.RawMessage `json:"choice"`
			Score         float64         `json:"score"`
			Confidence    float64         `json:"confidence"`
			Probabilities []struct {
				Value       json.RawMessage `json:"value"`
				Label       string          `json:"label"`
				Probability float64         `json:"probability"`
			} `json:"probabilities"`
		} `json:"answers"`
		Usage systemOneUsage `json:"usage"`
	}
	if err := decodeJSON(b, &a); err != nil || a.Answers == nil {
		return nil, fmt.Errorf("the answer is not a Decisions answer: %.200s", b)
	}
	text := func(v json.RawMessage) string {
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s
		}
		return string(v) // true, false
	}
	answers := map[string]any{}
	for i, x := range a.Answers {
		name := ""
		if x.Name != nil {
			name = *x.Name
		} else if i < len(keys) {
			name = keys[i]
		}
		switch x.Type {
		case "predicate":
			answers[name] = map[string]any{"type": "noul", "noul": x.Probability}
		case "choice":
			ps := map[string]float64{}
			for _, p := range x.Probabilities {
				ps[text(p.Value)] = p.Probability
			}
			answers[name] = map[string]any{"type": "choice", "choice": text(x.Choice), "confidence": x.Confidence, "probabilities": ps}
		case "score":
			ps := map[string]float64{}
			for _, p := range x.Probabilities {
				ps[p.Label] = p.Probability
			}
			answers[name] = map[string]any{"type": "score", "score": x.Score, "confidence": x.Confidence, "probabilities": ps}
		default: // a refusal: the model declined this one
			answers[name] = map[string]any{"type": x.Type}
		}
	}
	return json.Marshal(map[string]any{"model": a.Model, "answers": answers, "usage": a.Usage})
}
