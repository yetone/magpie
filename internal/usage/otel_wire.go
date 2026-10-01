package usage

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"time"
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

// Only this allowlist leaves the machine: never record JSON, account/key
// names, session titles, raw errors, prompts, completions or tool arguments.
func otelAttributes(r Record) []otelAttribute {
	op := r.Operation
	if op == "" {
		op = "chat"
	}
	a := []otelAttribute{otelString("gen_ai.operation.name", op), otelString("gen_ai.provider.name", r.Provider), otelString("gen_ai.request.model", r.Model), otelString("magpie.agent", r.Agent)}
	if r.Served != "" {
		a = append(a, otelString("gen_ai.response.model", r.Served))
	}
	if r.Status >= 400 {
		a = append(a, otelString("error.type", strconv.Itoa(r.Status)))
	}
	return a
}

func otelEnvelope(signal string, data any) map[string]any {
	return map[string]any{"resource" + signal: []any{map[string]any{
		"resource":       map[string]any{"attributes": []otelAttribute{otelString("service.name", "magpie")}},
		"scope" + signal: []any{map[string]any{"scope": map[string]any{"name": "github.com/yetone/magpie"}, map[string]string{"Spans": "spans", "Metrics": "metrics"}[signal]: data}},
	}}}
}

func (e *otelExporter) traces(records []Record) any {
	spans := make([]any, 0, len(records))
	for _, r := range records {
		var traceID [16]byte
		if r.RouteID != 0 {
			copy(traceID[:8], e.salt[:])
			binary.BigEndian.PutUint64(traceID[8:], uint64(r.RouteID))
		} else {
			rand.Read(traceID[:])
		}
		var spanID [8]byte
		rand.Read(spanID[:])
		a := append(otelAttributes(r), otelInt("http.response.status_code", int64(r.Status)),
			otelInt("gen_ai.usage.input_tokens", int64(r.Input+r.CacheRead+r.CacheWrite)), otelInt("gen_ai.usage.output_tokens", int64(r.Output)),
			otelInt("gen_ai.usage.cache_read.input_tokens", int64(r.CacheRead)), otelInt("gen_ai.usage.cache_creation.input_tokens", int64(r.CacheWrite)),
			otelInt("gen_ai.usage.reasoning.output_tokens", int64(r.Reasoning)), otelInt("magpie.ttft_ms", r.TTFT), otelInt("magpie.first_text_ms", r.FirstText))
		if r.RouteID != 0 {
			a = append(a, otelInt("magpie.route.id", r.RouteID))
		}
		if r.Requested != "" {
			a = append(a, otelString("magpie.requested_model", r.Requested))
		}
		if r.Effort != "" {
			a = append(a, otelString("magpie.reasoning_effort", r.Effort))
		}
		status := 0
		if r.Status >= 400 {
			status = 2
		}
		op := r.Operation
		if op == "" {
			op = "chat"
		}
		spans = append(spans, map[string]any{"traceId": hex.EncodeToString(traceID[:]), "spanId": hex.EncodeToString(spanID[:]),
			"name": op + " " + r.Model, "kind": 3, "flags": 1,
			"startTimeUnixNano": strconv.FormatInt(r.Time.UnixNano(), 10), "endTimeUnixNano": strconv.FormatInt(r.Time.Add(time.Duration(r.Millis)*time.Millisecond).UnixNano(), 10),
			"attributes": a, "status": map[string]any{"code": status}})
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
