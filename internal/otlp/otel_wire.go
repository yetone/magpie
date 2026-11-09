package otlp

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

type otelAttribute struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

func otelString(key, value string) otelAttribute {
	return otelAttribute{key, map[string]any{"stringValue": value}}
}
func otelInt(key string, value int64) otelAttribute {
	return otelAttribute{key, map[string]any{"intValue": strconv.FormatInt(value, 10)}}
}

// Only this allowlist leaves the machine: never record JSON, provider
// account/key names, session titles, raw errors, prompts, completions or
// tool arguments. The one exception is traces' input and output, sent only
// when the user turned bodies on (#538). The gateway key a call came with
// goes by the name the user gave it (gatewayKeyAttr), never its id or
// secret.
func otelAttributes(r Record) []otelAttribute {
	op := r.Operation
	if op == "" {
		op = "chat"
	}
	a := []otelAttribute{otelString("gen_ai.operation.name", op), otelString("gen_ai.provider.name", r.Provider), otelString("gen_ai.request.model", r.Model), otelString("magpie.agent", r.Agent)}
	a = gatewayKeyAttr(a, r)
	if r.Served != "" {
		a = append(a, otelString("gen_ai.response.model", r.Served))
	}
	if r.Failed() {
		errType := "stream_error"
		if r.Status >= 400 {
			errType = strconv.Itoa(r.Status)
		}
		a = append(a, otelString("error.type", errType))
	}
	return a
}

// gatewayKeyAttr adds the name of the gateway key a call came with
// (Record.CallerKeyName) as magpie.gateway_key.name; a call without one
// has no such attribute.
func gatewayKeyAttr(a []otelAttribute, r Record) []otelAttribute {
	if r.CallerKeyName == "" {
		return a
	}
	return append(a, otelString("magpie.gateway_key.name", r.CallerKeyName))
}

func otelEnvelope(signal string, data any) map[string]any {
	return map[string]any{"resource" + signal: []any{map[string]any{
		"resource":       map[string]any{"attributes": otelResourceAttributes()},
		"scope" + signal: []any{map[string]any{"scope": map[string]any{"name": "github.com/yetone/magpie"}, map[string]string{"Spans": "spans", "Metrics": "metrics"}[signal]: data}},
	}}}
}

// OTelSpan carries explicit IDs for gateway requests and agent-session observations.
type OTelSpan struct {
	TraceID, SpanID, ParentID        string
	Root                             bool
	End                              time.Time
	Name, Type, SessionID, TraceName string
	Session, Inferred, Update        bool
}

func (e *otelExporter) traces(items []otelItem) any {
	spans := make([]any, 0, len(items))
	priceOf := usage.Pricer()
	for _, it := range items {
		r, sp := it.record, it.span
		var traceID [16]byte
		if sp == nil {
			if r.RouteID != 0 {
				copy(traceID[:8], e.salt[:])
				binary.BigEndian.PutUint64(traceID[8:], uint64(r.RouteID))
			} else {
				rand.Read(traceID[:])
			}
		}
		var spanID [8]byte
		if sp == nil {
			rand.Read(spanID[:])
		}
		a := append(otelAttributes(r), otelInt("http.response.status_code", int64(r.Status)),
			otelInt("gen_ai.usage.input_tokens", int64(r.Input+r.CacheRead+r.CacheWrite)), otelInt("gen_ai.usage.output_tokens", int64(r.Output)),
			otelInt("gen_ai.usage.cache_read.input_tokens", int64(r.CacheRead)), otelInt("gen_ai.usage.cache_creation.input_tokens", int64(r.CacheWrite)),
			otelInt("gen_ai.usage.reasoning.output_tokens", int64(r.Reasoning)), otelInt("magpie.ttft_ms", r.TTFT), otelInt("magpie.first_text_ms", r.FirstText))
		if r.FirstReasoning > 0 {
			a = append(a, otelInt("magpie.first_reasoning_ms", r.FirstReasoning))
		}
		if r.ReasoningHidden {
			a = append(a, otelInt("magpie.reasoning_hidden", 1))
		}
		if r.RouteID != 0 {
			a = append(a, otelInt("magpie.route.id", r.RouteID))
		}
		if r.Requested != "" {
			a = append(a, otelString("magpie.requested_model", r.Requested))
		}
		if r.Effort != "" {
			a = append(a, otelString("magpie.reasoning_effort", r.Effort))
		}
		if r.BodyIn != "" {
			a = append(a, otelString("langfuse.observation.input", r.BodyIn))
		}
		if r.BodyOut != "" {
			a = append(a, otelString("langfuse.observation.output", otelReplyText(r.BodyOut)))
		}
		status := 0
		if r.Failed() {
			status = 2
		}
		op := r.Operation
		if op == "" {
			op = "chat"
		}
		span := map[string]any{"traceId": hex.EncodeToString(traceID[:]), "spanId": hex.EncodeToString(spanID[:]),
			"name": op + " " + r.Model, "kind": 3, "flags": 1,
			"startTimeUnixNano": strconv.FormatInt(r.Time.UnixNano(), 10), "endTimeUnixNano": strconv.FormatInt(r.Time.Add(time.Duration(r.Millis)*time.Millisecond).UnixNano(), 10),
			"attributes": a, "status": map[string]any{"code": status}}
		if sp != nil {
			span["traceId"], span["spanId"] = sp.TraceID, sp.SpanID
			if !sp.End.IsZero() {
				span["endTimeUnixNano"] = strconv.FormatInt(sp.End.UnixNano(), 10)
			}
			if sp.ParentID != "" {
				span["parentSpanId"] = sp.ParentID
			}
			if sp.Root {
				span["name"], span["kind"] = "gateway "+r.Model, 2
				a = []otelAttribute{otelString("magpie.agent", r.Agent), otelString("magpie.requested_model", r.Model), otelInt("http.response.status_code", int64(r.Status))}
				a = gatewayKeyAttr(a, r)
				if r.RouteID != 0 {
					a = append(a, otelInt("magpie.route.id", r.RouteID))
				}
				// a request its agent canceled is no failure
				// (Record.Canceled): its status code alone says 499
				if r.Failed() {
					a = append(a, otelString("error.type", strconv.Itoa(r.Status)))
				}
				a = append(a, otelString("langfuse.observation.type", "span"))
			} else {
				a = append(a, otelString("langfuse.observation.type", "generation"))
			}
			if sp.Session {
				span["name"], span["kind"] = sp.Name, 1
				if sp.Type != "generation" {
					a = []otelAttribute{otelString("magpie.agent", r.Agent)}
					if r.Status >= 400 {
						a = append(a, otelString("error.type", "agent_operation_failed"))
					}
				}
				a = append(a, otelString("langfuse.session.id", sp.SessionID), otelString("langfuse.trace.name", sp.TraceName))
				// Replace the gateway-specific observation type, keeping the wire allowlist explicit.
				filtered := a[:0]
				for _, attr := range a {
					if attr.Key != "langfuse.observation.type" {
						filtered = append(filtered, attr)
					}
				}
				a = append(filtered, otelString("langfuse.observation.type", sp.Type))
				if sp.Inferred {
					a = append(a, otelString("magpie.timing.source", "inferred"))
				} else {
					a = append(a, otelString("magpie.timing.source", "recorded"))
				}
				if sp.Type != "generation" {
					if r.BodyIn != "" {
						a = append(a, otelString("langfuse.observation.input", r.BodyIn))
					}
					if r.BodyOut != "" {
						a = append(a, otelString("langfuse.observation.output", r.BodyOut))
					}
				}
			}
			span["attributes"] = a
		}
		// Price only model calls, never their gateway/conversation parents or
		// tools. Cache reads/writes belong to input cost; reasoning is already
		// included in output tokens. Use the ledger's effective tariff, including
		// explicit free models, and leave unknown prices for Langfuse to infer.
		generation := sp == nil || !sp.Root && (!sp.Session || sp.Type == "generation")
		if generation && !r.IsRejected() {
			if pr := priceOf(r); pr != nil {
				// the call's tier, by its whole input, for both halves
				f := pr.In(r.Time).At(r.Input + r.CacheRead + r.CacheWrite)
				cost, err := json.Marshal(map[string]float64{
					"input":  f.CostSplit(r.Input, 0, r.CacheRead, r.CacheWrite, r.CacheWrite1h),
					"output": f.Cost(0, r.Output, 0, 0),
					"total":  r.CostAt(*pr),
				})
				if err == nil {
					a = append(a, otelString("langfuse.observation.cost_details", string(cost)))
					span["attributes"] = a
				}
			}
		}
		spans = append(spans, span)
	}
	return otelEnvelope("Spans", spans)
}

type otelHistogram struct {
	Attributes []otelAttribute `json:"attributes"`
	Start      string          `json:"startTimeUnixNano"`
	Time       string          `json:"timeUnixNano"`
	Count      uint64          `json:"count,string"`
	Sum        float64         `json:"sum"`
	Bounds     []float64       `json:"explicitBounds"`
	Buckets    []uint64        `json:"bucketCounts"`
}

// OTLP uint64 bucket counts use decimal strings, like count and timestamps.
func (h otelHistogram) MarshalJSON() ([]byte, error) {
	type wire otelHistogram
	buckets := make([]string, len(h.Buckets))
	for i, n := range h.Buckets {
		buckets[i] = strconv.FormatUint(n, 10)
	}
	return json.Marshal(struct {
		wire
		Buckets []string `json:"bucketCounts"`
	}{wire(h), buckets})
}

func otelMetrics(records []Record, start, end time.Time) any {
	type metric struct {
		name, unit string
		bounds     []float64
		points     map[string]*otelHistogram
	}
	metrics := []metric{
		{"gen_ai.client.operation.duration", "s", []float64{.01, .02, .04, .08, .16, .32, .64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}, map[string]*otelHistogram{}},
		{"gen_ai.client.token.usage", "{token}", []float64{1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576}, map[string]*otelHistogram{}},
	}
	add := func(m *metric, attrs []otelAttribute, value float64) {
		key, _ := json.Marshal(attrs)
		h := m.points[string(key)]
		if h == nil {
			h = &otelHistogram{Attributes: attrs, Start: strconv.FormatInt(start.UnixNano(), 10), Time: strconv.FormatInt(end.UnixNano(), 10), Bounds: m.bounds, Buckets: make([]uint64, len(m.bounds)+1)}
			m.points[string(key)] = h
		}
		h.Count++
		h.Sum += value
		i := sort.SearchFloat64s(m.bounds, value)
		h.Buckets[i]++
	}
	for _, r := range records {
		a := otelAttributes(r)
		add(&metrics[0], a, float64(r.Millis)/1000)
		for _, token := range []struct {
			kind  string
			count int
		}{{"input", r.Input + r.CacheRead + r.CacheWrite}, {"output", r.Output}} {
			add(&metrics[1], append(append([]otelAttribute(nil), a...), otelString("gen_ai.token.type", token.kind)), float64(token.count))
		}
	}
	out := make([]any, 0, len(metrics))
	for _, m := range metrics {
		keys := make([]string, 0, len(m.points))
		for key := range m.points {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		points := make([]*otelHistogram, 0, len(keys))
		for _, key := range keys {
			points = append(points, m.points[key])
		}
		out = append(out, map[string]any{"name": m.name, "unit": m.unit, "histogram": map[string]any{"aggregationTemporality": 1, "dataPoints": points}})
	}
	return otelEnvelope("Metrics", out)
}

// otelReplyText is a streamed reply's text, put together from its events
// (Chat Completions' choices[].delta.content, Responses'
// response.output_text.delta, Anthropic's content_block_delta), so the
// trace shows what the model wrote rather than a few hundred SSE events; a
// reply that isn't a stream, or one with no text in it (only tool calls),
// goes as it came.
func otelReplyText(body string) string {
	if !strings.Contains(body, "data:") {
		return body
	}
	body, cut := strings.CutSuffix(body, usage.BodyCut)
	var text strings.Builder
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		var ev struct {
			Type    string          `json:"type"`
			Delta   json.RawMessage `json:"delta"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) != nil {
			continue
		}
		for _, c := range ev.Choices {
			text.WriteString(c.Delta.Content)
		}
		switch ev.Type {
		case "response.output_text.delta":
			var d string
			if json.Unmarshal(ev.Delta, &d) == nil {
				text.WriteString(d)
			}
		case "content_block_delta":
			var d struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(ev.Delta, &d) == nil {
				text.WriteString(d.Text)
			}
		}
	}
	if text.Len() == 0 {
		text.Reset()
		text.WriteString(body)
	}
	if cut {
		text.WriteString(usage.BodyCut)
	}
	return text.String()
}
