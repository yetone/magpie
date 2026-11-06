package provider

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strconv"
	"time"
)

// quotaHistBuf is what the history was last written from, under
// quotaHistMu, kept for the next write: the file is written again on
// each new reading of any account (a few times a minute with a dozen
// accounts), and json.Marshal made the whole of it anew each time, with
// three times for each point (~750 KB, ~300k allocations a minute on a
// live magpie, a quarter of all it allocated idle).
var quotaHistBuf []byte

// appendQuotaHist is json.Marshal(h) appended to b, byte for byte, made
// without a value of its own for each point; ok is false for what
// json.Marshal would refuse (a NaN, a year past 9999), which the caller
// leaves to it.
func appendQuotaHist(b []byte, h quotaHist) ([]byte, bool) {
	if h == nil {
		return append(b, "null"...), true
	}
	b = append(b, '{')
	for i, key := range slices.Sorted(maps.Keys(h)) {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendJSONString(b, key)
		b = append(b, ':')
		ws := h[key]
		if ws == nil {
			b = append(b, "null"...)
			continue
		}
		b = append(b, '{')
		for j, name := range slices.Sorted(maps.Keys(ws)) {
			if j > 0 {
				b = append(b, ',')
			}
			b = appendJSONString(b, name)
			b = append(b, ':')
			pts := ws[name]
			if pts == nil {
				b = append(b, "null"...)
				continue
			}
			b = append(b, '[')
			for k, p := range pts {
				if k > 0 {
					b = append(b, ',')
				}
				var ok bool
				if b, ok = appendQuotaPoint(b, p); !ok {
					return b, false
				}
			}
			b = append(b, ']')
		}
		b = append(b, '}')
	}
	return append(b, '}'), true
}

func appendQuotaPoint(b []byte, p QuotaPoint) ([]byte, bool) {
	var ok bool
	b = append(b, `{"at":`...)
	if b, ok = appendJSONTime(b, p.At); !ok {
		return b, false
	}
	b = append(b, `,"left":`...)
	if b, ok = appendJSONFloat(b, p.Left); !ok {
		return b, false
	}
	if p.Start != nil {
		b = append(b, `,"start":`...)
		if b, ok = appendJSONTime(b, *p.Start); !ok {
			return b, false
		}
	}
	if p.ResetsAt != nil {
		b = append(b, `,"resetsAt":`...)
		if b, ok = appendJSONTime(b, *p.ResetsAt); !ok {
			return b, false
		}
	}
	return append(b, '}'), true
}

// appendJSONTime is time.Time's MarshalJSON appended to b: RFC 3339 with
// its fraction, for a year json.Marshal takes.
func appendJSONTime(b []byte, t time.Time) ([]byte, bool) {
	if y := t.Year(); y < 0 || y > 9999 {
		return b, false
	}
	if _, off := t.Zone(); off%60 != 0 {
		return b, false // MarshalJSON refuses an offset with seconds in it
	}
	b = append(b, '"')
	b = t.AppendFormat(b, time.RFC3339Nano)
	return append(b, '"'), true
}

// appendJSONFloat is a float64 as encoding/json writes one.
func appendJSONFloat(b []byte, f float64) ([]byte, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return b, false
	}
	format := byte('f')
	if a := math.Abs(f); a != 0 && (a < 1e-6 || a >= 1e21) {
		format = 'e'
	}
	b = strconv.AppendFloat(b, f, format, -1, 64)
	if format == 'e' {
		// e-09 to e-9, as encoding/json does
		if n := len(b); n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b, true
}

// appendJSONString is a string as json.Marshal writes it: as it is when
// nothing in it is escaped there.
func appendJSONString(b []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			q, _ := json.Marshal(s) // escaped as json.Marshal does
			return append(b, q...)
		}
	}
	b = append(b, '"')
	b = append(b, s...)
	return append(b, '"')
}

// marshalQuotaHist is h as the file holds it, in quotaHistBuf; under
// quotaHistMu. What it gives is overwritten by the next call.
func marshalQuotaHist(h quotaHist) ([]byte, error) {
	b, ok := appendQuotaHist(quotaHistBuf[:0], h)
	if !ok {
		return json.Marshal(h)
	}
	quotaHistBuf = b
	return b, nil
}
