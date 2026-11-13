package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// decodedToolsPaired is the answer toolPairsOut's decoding path gives:
// ok false where it returns the body for a shape it can't read.
func decodedToolsPaired(body []byte) (paired, ok bool) {
	var q map[string]json.RawMessage
	if decodeJSON(body, &q) != nil {
		return false, false
	}
	dec := json.NewDecoder(bytes.NewReader(q["messages"]))
	var msgs []map[string]any
	if dec.Decode(&msgs) != nil {
		return false, false
	}
	for _, m := range msgs {
		if m == nil {
			return false, false
		}
		raw, has := m["tool_calls"]
		if !has || raw == nil {
			continue
		}
		list, isList := raw.([]any)
		if !isList {
			return false, false
		}
		calls := []map[string]any{}
		for _, c := range list {
			cm, isObj := c.(map[string]any)
			if !isObj {
				return false, false
			}
			calls = append(calls, cm)
		}
		m["tool_calls"] = calls
	}
	return toolsPaired(msgs), true
}

// TestRawToolsPairedAsDecoded holds rawToolsPaired to the decoding path
// on random conversations: calls answered, answered twice, out of order,
// left open, ids repeated or missing, and odd shapes.
func TestRawToolsPairedAsDecoded(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	ids := []string{`"a"`, `"b"`, `"c"`, `""`, `1`, `null`, `"ab"`}
	roles := []string{`"user"`, `"assistant"`, `"tool"`, `"system"`, `null`, `"tool"`}
	yes, no := 0, 0
	for range 20000 {
		var ms []string
		for range r.IntN(8) {
			var f []string
			if r.IntN(10) > 0 {
				f = append(f, `"role":`+roles[r.IntN(len(roles))])
			}
			if r.IntN(3) == 0 {
				f = append(f, `"tool_call_id":`+ids[r.IntN(len(ids))])
			}
			switch r.IntN(6) {
			case 0, 1:
				var cs []string
				for range r.IntN(4) {
					if r.IntN(15) == 0 {
						cs = append(cs, `"x"`)
						continue
					}
					cs = append(cs, `{"id":`+ids[r.IntN(len(ids))]+`,"type":"function"}`)
				}
				f = append(f, `"tool_calls":[`+strings.Join(cs, ",")+`]`)
			case 2:
				f = append(f, `"tool_calls":null`)
			case 3:
				if r.IntN(5) == 0 {
					f = append(f, `"tool_calls":{}`)
				}
			}
			if r.IntN(8) == 0 && len(f) > 0 {
				f = append(f, f[0]) // a key named twice
			}
			r.Shuffle(len(f), func(i, j int) { f[i], f[j] = f[j], f[i] })
			m := "{" + strings.Join(f, ",") + "}"
			if r.IntN(40) == 0 {
				m = "null"
			}
			ms = append(ms, m)
		}
		body := []byte(fmt.Sprintf(`{"model":"m","tools":[],"messages":[%s]}`, strings.Join(ms, ",")))
		got, gotOK := rawToolsPaired(body)
		want, wantOK := decodedToolsPaired(body)
		if !gotOK {
			continue // the decoding path answers these
		}
		if !wantOK || got != want {
			t.Fatalf("rawToolsPaired = %v, %v; decoded %v, %v for %s", got, gotOK, want, wantOK, body)
		}
		if got {
			yes++
		} else {
			no++
		}
	}
	if yes < 1000 || no < 1000 {
		t.Fatalf("paired %d, unpaired %d: the test reaches too little", yes, no)
	}
}
