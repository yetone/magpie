package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// OpenAI's Decisions API (POST /v1/decisions, gpt-6-luna) asks what
// System One asks in its own shape: a System One request goes there as
// SystemOneAsDecisions makes it, and its answer comes back as
// DecisionAsSystemOne reads it. magpie's /v1/systemone asks gpt-6-luna
// this way (gateway/decisions_systemone.go), and so does a routing
// rule's classifier set to an OpenAI key's gpt-6-luna (ViaOpenAI).

// systemOneFields are the fields of a System One request Decisions has a
// place for.
var systemOneFields = []string{"model", "state", "questions", "images"}

// SystemOneAsDecisions is a System One request as Decisions asks it, and
// its questions' names in order (Decisions answers in that order).
func SystemOneAsDecisions(body []byte) (map[string]json.RawMessage, []string, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, fmt.Errorf("not a System One request")
	}
	for _, k := range slices.Sorted(maps.Keys(req)) {
		if !slices.Contains(systemOneFields, k) {
			return nil, nil, fmt.Errorf("%q has no counterpart in OpenAI's Decisions API, which the model answers", k)
		}
	}
	var images []string
	if raw, ok := req["images"]; ok && json.Unmarshal(raw, &images) != nil {
		return nil, nil, fmt.Errorf("\"images\" is a list of image URLs")
	}
	if len(req["state"]) == 0 {
		return nil, nil, fmt.Errorf("a System One request with no state: Decisions asks its questions of an input")
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
		return nil, nil, fmt.Errorf("\"questions\" is an object of questions by name")
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
			return nil, nil, fmt.Errorf("questions.%s is not a question", k)
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
				return nil, nil, fmt.Errorf("questions.%s: %v", k, err)
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
				return nil, nil, fmt.Errorf("questions.%s: %v", k, err)
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
			return nil, nil, fmt.Errorf("questions.%s is a %q question, which OpenAI's Decisions API has no counterpart for (it asks predicate, choice and score)", k, q.Type)
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

// DecisionAsSystemOne is a Decisions answer as System One gives it: each
// answer under its question's name (keys, the questions in the order
// asked), its usage's input and output tokens.
func DecisionAsSystemOne(b []byte, keys []string) ([]byte, error) {
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
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &a); err != nil || a.Answers == nil {
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
