package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/masking"
	"github.com/yetone/magpie/internal/maskrec"
	"github.com/yetone/magpie/internal/settings"
)

// previewMax is how much text the preview masks.
const previewMax = 64 << 10

// valuesMax is how many placeholders one /api/masking/values asks for.
const valuesMax = 2000

// maskedRecord is a record as the page shows it: each value says whether
// the masker still knows what it stood for, so the page offers to show it
// only where it can.
type maskedRecord struct {
	maskrec.Record
	// Key is a string, as gateway.Masked's: a JS number rounds it
	Key    int64         `json:"key,string"`
	Values []maskedValue `json:"values,omitempty"`
	// Hits are the placeholders whose values the search found, for the
	// row to say which of its values it was listed for
	Hits []string `json:"hits,omitempty"`
}

type maskedValue struct {
	maskrec.Value
	Known bool `json:"known,omitempty"`
}

// teller is the served masker, where it can say what a placeholder stood
// for.
func teller() (gwmw.Teller, bool) {
	m, ok := gwmw.Served()
	if !ok {
		return nil, false
	}
	t, ok := m.(gwmw.Teller)
	return t, ok
}

func shownRecord(r maskrec.Record, t gwmw.Teller) maskedRecord {
	out := maskedRecord{Record: r, Key: r.Key}
	for _, v := range r.Values {
		mv := maskedValue{Value: v}
		if t != nil {
			_, mv.Known = t.Value(v.Placeholder)
		}
		out.Values = append(out.Values, mv)
	}
	return out
}

// searchMax is the longest search the records take, in characters.
const searchMax = 200

// maskSearcher is a search of the records for a word: what each value
// it looks at stood for comes from the masker's memory, in this process,
// and goes nowhere else.
type maskSearcher struct {
	q, p    string // q lower-cased, and as typed for a placeholder
	t       gwmw.Teller
	hit     map[string]bool
	unknown bool
}

// maskSearch is the search for q, trimmed and cut to searchMax
// characters; nil when there is nothing to search for.
func maskSearch(q string, t gwmw.Teller) *maskSearcher {
	q = strings.TrimSpace(q)
	if r := []rune(q); len(r) > searchMax {
		q = string(r[:searchMax])
	}
	if q == "" {
		return nil
	}
	return &maskSearcher{q: strings.ToLower(q), p: q, t: t, hit: map[string]bool{}}
}

// takes says whether v's value has the search in it, whatever its case,
// or its placeholder is the search; a value the masker doesn't know is
// counted as unknown.
func (s *maskSearcher) takes(v maskrec.Value) bool {
	if hit, seen := s.hit[v.Placeholder]; seen {
		return hit
	}
	hit := v.Placeholder == s.p
	if !hit {
		val, ok := "", false
		if s.t != nil {
			val, ok = s.t.Value(v.Placeholder)
		}
		if !ok {
			s.unknown = true
			return false
		}
		hit = strings.Contains(strings.ToLower(val), s.q)
	}
	s.hit[v.Placeholder] = hit
	return hit
}

// maskingRoutes are Settings → Privacy's record of what masking swapped:
//   - GET /api/masking/summary?days= is maskrec.Sum;
//   - GET /api/masking/records?before=&limit=&kind=&by=&agent=&provider=&blocked=&q=
//     a page of records, newest first (kind may list several, split by
//     commas; blocked=1 keeps the requests held back; q keeps those with a
//     value that has q in it, whatever its case, or whose placeholder is
//     q, as maskSearch reads them: r3inbowari on X asked to find whether
//     a word was masked);
//   - GET /api/masking/record?key= one record;
//   - GET /api/masking/value?p= what a placeholder stood for, while this
//     magpie's masker remembers it (404 after a restart: it is never on
//     disk);
//   - GET /api/masking/sent?key= the body a record's request went to its
//     vendor with, masked, while this magpie keeps it (the latest
//     maskrec keeps; 404 after a restart);
//   - POST /api/masking/values {ps} what each placeholder stood for, of
//     those the masker still remembers, for the page to show the request
//     as the agent sent it;
//   - POST /api/masking/clear deletes every record kept (maskrec.Clear),
//     at once rather than after 30 days (maxyou2090 on Discord);
//   - POST /api/masking/preview {text} the text as a vendor would get it
//     under the settings now, and what was found, each told by its kind
//     and hint, never its value; nothing of it is remembered (#1977);
//   - POST /api/masking/allow {p} adds what a placeholder stood for to
//     Never mask (settings' RedactAllow, which redact-allow sets whole),
//     so a value masked by mistake goes as written from the next request
//     on (yew_fence on Discord). The masker is asked for the value, so
//     only one it masked is added; 404 once it no longer knows it.
func maskingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/masking/allow", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			P string `json:"p"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		t, ok := teller()
		if !ok {
			http.NotFound(rw, r)
			return
		}
		v, ok := t.Value(in.P)
		if !ok {
			http.NotFound(rw, r)
			return
		}
		// a settings file that can't be read is unknown, not empty: the
		// list it holds is never written over with this one value
		s, err := settings.LoadChecked()
		if err != nil {
			fail(rw, err)
			return
		}
		// Save trims the list and keeps each value once, whatever its case
		s.RedactAllow = append(s.RedactAllow, v)
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	mux.HandleFunc("POST /api/masking/preview", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4*previewMax)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if len(in.Text) > previewMax {
			in.Text = strings.ToValidUTF8(in.Text[:previewMax], "")
		}
		text, hits, off := masking.Preview(in.Text)
		type hit struct {
			Kind        string `json:"kind"`
			By          string `json:"by"`
			Action      string `json:"action,omitempty"`
			Placeholder string `json:"placeholder"`
			Head        string `json:"head,omitempty"`
			Len         int    `json:"len"`
		}
		out := []hit{}
		for _, h := range hits {
			out = append(out, hit{h.Kind, h.By, h.Action, h.Placeholder, h.Head, h.Len})
		}
		writeJSON(rw, map[string]any{"text": text, "hits": out, "off": off})
	})
	mux.HandleFunc("POST /api/masking/clear", func(rw http.ResponseWriter, r *http.Request) {
		if err := maskrec.Clear(); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]bool{"cleared": true})
	})
	mux.HandleFunc("GET /api/masking/summary", func(rw http.ResponseWriter, r *http.Request) {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		writeJSON(rw, maskrec.Sum(min(max(days, 1), 30)))
	})
	mux.HandleFunc("GET /api/masking/records", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		limit, _ := strconv.Atoi(q.Get("limit"))
		f := maskrec.Filter{Kind: q.Get("kind"), By: q.Get("by"), Agent: q.Get("agent"), Provider: q.Get("provider"), Blocked: q.Get("blocked") == "1"}
		t, _ := teller()
		s := maskSearch(q.Get("q"), t)
		if s != nil {
			f.Value = s.takes
		}
		rs, more := maskrec.List(before, min(max(limit, 1), 200), f)
		out := []maskedRecord{}
		for _, rec := range rs {
			mr := shownRecord(rec, t)
			if s != nil {
				for _, v := range rec.Values {
					if s.takes(v) {
						mr.Hits = append(mr.Hits, v.Placeholder)
					}
				}
			}
			out = append(out, mr)
		}
		res := map[string]any{"records": out, "more": more}
		if s != nil {
			// some value looked at that the masker no longer knows: masked
			// before magpie last started, so the search can't tell it
			res["unknown"] = s.unknown
		}
		writeJSON(rw, res)
	})
	mux.HandleFunc("GET /api/masking/record", func(rw http.ResponseWriter, r *http.Request) {
		key, _ := strconv.ParseInt(r.URL.Query().Get("key"), 10, 64)
		rec, ok := maskrec.Get(key)
		if !ok {
			http.NotFound(rw, r)
			return
		}
		t, _ := teller()
		writeJSON(rw, shownRecord(rec, t))
	})
	mux.HandleFunc("GET /api/masking/value", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-store")
		t, ok := teller()
		if !ok {
			http.NotFound(rw, r)
			return
		}
		v, ok := t.Value(r.URL.Query().Get("p"))
		if !ok {
			http.NotFound(rw, r)
			return
		}
		writeJSON(rw, map[string]string{"value": v})
	})
	mux.HandleFunc("GET /api/masking/sent", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-store")
		key, _ := strconv.ParseInt(r.URL.Query().Get("key"), 10, 64)
		body, ok := maskrec.SentOf(key)
		if !ok {
			http.NotFound(rw, r)
			return
		}
		writeJSON(rw, map[string]string{"body": body})
	})
	mux.HandleFunc("POST /api/masking/values", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-store")
		var in struct {
			Ps []string `json:"ps"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		out := map[string]string{}
		if t, ok := teller(); ok {
			for _, p := range in.Ps[:min(len(in.Ps), valuesMax)] {
				if v, ok := t.Value(p); ok {
					out[p] = v
				}
			}
		}
		writeJSON(rw, map[string]any{"values": out})
	})
}
