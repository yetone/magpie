package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// POST /v1/magpie/usage/requests (#1874): a client that calls its vendor
// itself, not through magpie, tells magpie of its calls, so the Usage page
// counts them with the rest. It takes one record, or an array of them, in
// the shape usage.jsonl has, and writes each beside the gateway's own,
// marked source "reported": counted in the totals, and told apart, and
// filtered (?via=reported), on the page and over GET.
//
// A record's rid is required, and one already reported is skipped, after
// a restart too, since what was reported is read from the log itself. A
// record with no cost is priced at magpie's prices, as a gateway call is.
// A batch is all or nothing: one bad record answers 400, naming it and its
// field, and none is written.
//
// It writes into what this computer's usage says, so it is let in as
// GET /v1/magpie/usage is: this computer, or another with an enabled
// gateway key while magpie is shared. A request with a browser's marks
// needs an enabled gateway key even from this computer: corsGuard lets
// a page on localhost or 127.0.0.1 (any port) through keyless, and it
// shouldn't be able to write what the page counts.

const (
	// ingestMaxBody is the most a post may carry: a full batch of
	// ingestMaxRecords at a few hundred bytes each is a few MiB, so this
	// leaves room for records that carry an err text too.
	ingestMaxBody = 32 << 20
	// ingestMaxRecords is the most records one post may carry: a few
	// thousand, for a backfill of a client's history (#1874) sent in
	// batches, each answered record by record.
	ingestMaxRecords = 5000
	// ingestMaxTokens is the most of any token count one call may say:
	// more than any model's context, so a larger number is a mistake.
	ingestMaxTokens = 100_000_000
	// ingestMaxMillis is the longest a call may say it took: a day.
	ingestMaxMillis = int64(24 * time.Hour / time.Millisecond)
	// ingestMaxCost is the most one call may say it cost, in USD.
	ingestMaxCost = 10_000.0
	// ingestMaxText is the longest a name (agent, provider, model, rid…)
	// may be.
	ingestMaxText = 256
	// ingestFuture is how far ahead of this computer's clock a call's
	// time may be, for a client's clock that runs ahead.
	ingestFuture = time.Hour
)

// ingestEarliest is the earliest a call may be dated. A past time is taken
// as given, so a client's history can be backfilled and lands on its own
// day; this bound only stops a zero or mistyped year.
var ingestEarliest = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ingestFields are the fields a reported record may carry, of the ones
// usage.jsonl has: what a client can know of a call it made itself.
// first_text_ms and flow_ms (#2637) are what a reply's speed is told by
// (usage.DecodeOf): without first_text_ms a reply that reasoned tells
// none, and without flow_ms one its vendor held and let go in a burst
// isn't told apart from one that streamed. req and served (#3534) are
// the model the client asked for (an alias, a group) and the one its
// vendor's reply said answered: the Requested and Served columns. Each is
// kept only as sent, so a record without served says nothing of what
// answered; it is never taken from model.
var ingestFields = []string{"t", "agent", "provider", "model", "req", "served", "in", "out", "cache_read", "cache_write", "cache_write_1h", "reasoning", "ms", "ttft_ms", "first_text_ms", "flow_ms", "status", "rid", "cost", "effort", "session", "err"}

// ingestFault is what is wrong with one record of a post.
type ingestFault struct {
	index int
	field string
	msg   string
}

func (f *ingestFault) Error() string {
	return fmt.Sprintf("record %d: %s: %s", f.index, f.field, f.msg)
}

func (s *Server) usageIngest(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, usageRefused)
		return
	}
	who := access.Caller(r.Context())
	if fromBrowser(r) && who.KeyID == "" {
		// lanGuard identified only a remote or managed key; a page on this
		// computer with a key is looked up here
		for _, k := range callerKeys(r) {
			if id, ok := access.Authenticate(k); ok {
				who = id
				break
			}
		}
		if who.KeyID == "" {
			writeError(w, provider.Chat, http.StatusForbidden, "a browser page may report usage only with an enabled magpie gateway key (Authorization: Bearer <key>): a page has no say in what this computer's usage counts")
			return
		}
	}
	if heldKey(who) {
		writeError(w, provider.Chat, http.StatusForbidden, "this gateway key is held to a budget, models or accounts, so it may not report usage")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, provider.Chat, http.StatusUnsupportedMediaType, "send the records as JSON, with Content-Type: application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ingestMaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, provider.Chat, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is over %d bytes: send at most %d records a post", ingestMaxBody, ingestMaxRecords))
			return
		}
		writeError(w, provider.Chat, http.StatusBadRequest, "the body could not be read")
		return
	}
	recs, fault := parseIngest(body, time.Now())
	if fault != nil {
		v := map[string]any{"message": fault.Error(), "type": "invalid_request_error", "param": fault.field}
		if fault.index >= 0 {
			v["index"] = fault.index
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": v})
		return
	}
	took, err := usage.IngestReported(recs)
	if err != nil {
		writeError(w, provider.Chat, http.StatusInternalServerError, "the records could not be written: "+err.Error())
		return
	}
	type result struct {
		Index  int    `json:"index"`
		RID    string `json:"rid"`
		Status string `json:"status"`
	}
	out := struct {
		Accepted int      `json:"accepted"`
		Skipped  int      `json:"skipped"`
		Records  []result `json:"records"`
	}{Records: make([]result, len(recs))}
	for i, rec := range recs {
		st := "skipped"
		if took[i] {
			st = "accepted"
			out.Accepted++
		} else {
			out.Skipped++
		}
		out.Records[i] = result{i, rec.RequestID, st}
	}
	writeJSON(w, 200, out)
}

// parseIngest reads a post's records: one object, or an array of them.
// The first bad one is the answer, with its index (-1 for the body as a
// whole) and its field.
func parseIngest(body []byte, now time.Time) ([]usage.Record, *ingestFault) {
	body = bytes.TrimSpace(body)
	var raws []json.RawMessage
	switch {
	case len(body) > 0 && body[0] == '[':
		if err := json.Unmarshal(body, &raws); err != nil {
			return nil, &ingestFault{-1, "body", "not a JSON array of records: " + err.Error()}
		}
		if len(raws) == 0 {
			return nil, &ingestFault{-1, "body", "the array has no records"}
		}
		if len(raws) > ingestMaxRecords {
			return nil, &ingestFault{-1, "body", fmt.Sprintf("%d records: send at most %d a post", len(raws), ingestMaxRecords)}
		}
	case len(body) > 0 && body[0] == '{':
		raws = []json.RawMessage{body}
	default:
		return nil, &ingestFault{-1, "body", "send a JSON record, or an array of them"}
	}
	out := make([]usage.Record, 0, len(raws))
	for i, raw := range raws {
		rec, fault := parseReported(i, raw, now)
		if fault != nil {
			return nil, fault
		}
		out = append(out, rec)
	}
	return out, nil
}

// parseReported reads one record, field by field, so what is wrong with it
// is said by the field's name.
func parseReported(i int, raw json.RawMessage, now time.Time) (usage.Record, *ingestFault) {
	var rec usage.Record
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return rec, &ingestFault{i, "record", "not a JSON object"}
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys) // the same field is named for the same body every time
	for _, k := range keys {
		if !slices.Contains(ingestFields, k) {
			return rec, &ingestFault{i, k, "not a field a reported record takes (" + strings.Join(ingestFields, ", ") + ")"}
		}
	}
	text := func(k string, need bool) (string, *ingestFault) {
		v, ok := fields[k]
		if !ok || string(v) == "null" {
			if need {
				return "", &ingestFault{i, k, "required"}
			}
			return "", nil
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			return "", &ingestFault{i, k, "must be a string"}
		}
		s = strings.TrimSpace(s)
		switch {
		case need && s == "":
			return "", &ingestFault{i, k, "required"}
		case utf8.RuneCountInString(s) > ingestMaxText && k != "err":
			return "", &ingestFault{i, k, fmt.Sprintf("longer than %d characters", ingestMaxText)}
		case strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) && k != "err":
			return "", &ingestFault{i, k, "has a control character"}
		}
		return s, nil
	}
	whole := func(k string, max int64, need bool) (int64, *ingestFault) {
		v, ok := fields[k]
		if !ok || string(v) == "null" {
			if need {
				return 0, &ingestFault{i, k, "required"}
			}
			return 0, nil
		}
		var n json.Number
		// a Number takes a quoted one too, which is a string here
		if v[0] == '"' || json.Unmarshal(v, &n) != nil {
			return 0, &ingestFault{i, k, "must be a number"}
		}
		x, err := n.Int64()
		if err != nil {
			return 0, &ingestFault{i, k, "must be a whole number"}
		}
		if x < 0 {
			return 0, &ingestFault{i, k, "must not be negative"}
		}
		if x > max {
			return 0, &ingestFault{i, k, fmt.Sprintf("over %d, more than one call can be", max)}
		}
		return x, nil
	}
	var f *ingestFault
	if rec.RequestID, f = text("rid", true); f != nil {
		return rec, f
	}
	if rec.Agent, f = text("agent", true); f != nil {
		return rec, f
	}
	if rec.Provider, f = text("provider", true); f != nil {
		return rec, f
	}
	if rec.Model, f = text("model", true); f != nil {
		return rec, f
	}
	if rec.Requested, f = text("req", false); f != nil {
		return rec, f
	}
	if rec.Served, f = text("served", false); f != nil {
		return rec, f
	}
	if rec.Effort, f = text("effort", false); f != nil {
		return rec, f
	}
	if rec.Session, f = text("session", false); f != nil {
		return rec, f
	}
	if rec.Error, f = text("err", false); f != nil {
		return rec, f
	}
	if len(rec.Error) > 4096 {
		rec.Error = strings.ToValidUTF8(rec.Error[:4096], "")
	}
	t, ok := fields["t"]
	if !ok || string(t) == "null" {
		return rec, &ingestFault{i, "t", "required"}
	}
	var ts string
	if json.Unmarshal(t, &ts) != nil {
		return rec, &ingestFault{i, "t", "must be an RFC 3339 time, as a string"}
	}
	when, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return rec, &ingestFault{i, "t", "must be an RFC 3339 time (2026-10-13T08:00:00Z)"}
	}
	if when.After(now.Add(ingestFuture)) {
		return rec, &ingestFault{i, "t", "is in the future"}
	}
	if when.Before(ingestEarliest) {
		return rec, &ingestFault{i, "t", "is before 2000-01-01T00:00:00Z, the earliest a call may be dated"}
	}
	rec.Time = when
	counts := []struct {
		k  string
		to *int
	}{{"in", &rec.Input}, {"out", &rec.Output}, {"cache_read", &rec.CacheRead}, {"cache_write", &rec.CacheWrite}, {"cache_write_1h", &rec.CacheWrite1h}, {"reasoning", &rec.Reasoning}}
	for _, c := range counts {
		n, f := whole(c.k, ingestMaxTokens, false)
		if f != nil {
			return rec, f
		}
		*c.to = int(n)
	}
	if rec.CacheWrite1h > rec.CacheWrite {
		return rec, &ingestFault{i, "cache_write_1h", "more than cache_write, which counts it"}
	}
	if rec.Millis, f = whole("ms", ingestMaxMillis, false); f != nil {
		return rec, f
	}
	if rec.TTFT, f = whole("ttft_ms", ingestMaxMillis, false); f != nil {
		return rec, f
	}
	// first_text_ms is when the reply's first text came, counted as
	// ttft_ms is: within the call, and never before its first content,
	// which ttft_ms is (text, reasoning or a tool call). One before it
	// would read the reasoning as written in no time, and ThinkPace would
	// never tell an answer let go in a burst.
	if rec.FirstText, f = whole("first_text_ms", ingestMaxMillis, false); f != nil {
		return rec, f
	}
	hasMs := fields["ms"] != nil && string(fields["ms"]) != "null"
	if hasMs && rec.FirstText > rec.Millis {
		return rec, &ingestFault{i, "first_text_ms", "after ms, the end of the call it is in"}
	}
	if rec.TTFT > 0 && rec.FirstText > 0 && rec.FirstText < rec.TTFT {
		return rec, &ingestFault{i, "first_text_ms", "before ttft_ms: the first text is content too, so it can't come before the first content"}
	}
	// flow_ms is how long the content took to come, from where the speed
	// is counted (the first text when it reasoned, else the first
	// content): the time from a tenth of it to nine tenths, scaled to the
	// whole (×10/8), as the gateway measures it. Scaled so, a reply that
	// came evenly over nearly all of the call can read a little over ms,
	// so the bound is ms scaled the same way.
	if rec.Flow, f = whole("flow_ms", ingestMaxMillis, false); f != nil {
		return rec, f
	}
	if hasMs && rec.Flow > rec.Millis*10/8+1 {
		return rec, &ingestFault{i, "flow_ms", "over ms×10/8: the content took longer to come than the call took"}
	}
	st, f := whole("status", 599, true)
	if f != nil {
		return rec, f
	}
	if st < 100 {
		return rec, &ingestFault{i, "status", "must be an HTTP status, 100 to 599"}
	}
	rec.Status = int(st)
	if v, ok := fields["cost"]; ok && string(v) != "null" {
		var c float64
		if json.Unmarshal(v, &c) != nil {
			return rec, &ingestFault{i, "cost", "must be a number of USD"}
		}
		if math.IsNaN(c) || math.IsInf(c, 0) || c < 0 {
			return rec, &ingestFault{i, "cost", "must not be negative"}
		}
		if c > ingestMaxCost {
			return rec, &ingestFault{i, "cost", fmt.Sprintf("over %g USD, more than one call can cost", ingestMaxCost)}
		}
		rec.OwnCost = &c
	}
	return rec, nil
}
