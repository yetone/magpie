package gateway

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
)

// TestCallIDsSetNeverSkipsAnEdit holds callIDsSet to giveCallIDs: a body
// it calls set comes back from the decode byte for byte. The ids, roles
// and keys are drawn so both answers, and edits, come often.
func TestCallIDsSetNeverSkipsAnEdit(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	pick := func(xs ...string) string { return xs[r.IntN(len(xs))] }
	id := func() string {
		return pick(`"call_1"`, `"call_2"`, `""`, `null`, `7`, `"cx"`, `{}`)
	}
	set, unset, edited := 0, 0, 0
	for range 20000 {
		var msgs []string
		for range 1 + r.IntN(6) {
			switch r.IntN(5) {
			case 0:
				msgs = append(msgs, `{"role":"user","content":"hi"}`)
			case 1, 2:
				var calls []string
				for range r.IntN(3) {
					c := `{"type":"function","function":{"name":"f","arguments":"{}"}`
					if r.IntN(8) > 0 {
						c += `,` + pick(`"id"`, `"id"`, `"ID"`) + `:` + id()
					}
					calls = append(calls, c+`}`)
				}
				m := `{"role":` + pick(`"assistant"`, `"assistant"`, `"user"`) + `,"content":null`
				switch r.IntN(6) {
				case 0:
					m += `,"tool_calls":null`
				case 1:
					m += `,"tool_calls":` + pick(`{}`, `"x"`, `[1]`)
				default:
					m += `,"tool_calls":[` + strings.Join(calls, ",") + `]`
				}
				msgs = append(msgs, m+`}`)
			case 3:
				m := `{"role":` + pick(`"tool"`, `"tool"`) + `,"content":"ok"`
				if r.IntN(6) > 0 {
					m += `,` + pick(`"tool_call_id"`, `"tool_call_id"`) + `:` + id()
				}
				msgs = append(msgs, m+`}`)
			default:
				msgs = append(msgs, pick(`7`, `null`, `{"role":"tool"}`, `{"role":7,"tool_calls":[{"id":""}]}`))
			}
		}
		body := []byte(`{"model":"m","messages":[` + strings.Join(msgs, ",") + `]}`)
		if !json.Valid(body) {
			t.Fatalf("invalid fixture %s", body)
		}
		got := giveCallIDs(bytes.Clone(body))
		changed := !bytes.Equal(got, body)
		if changed {
			edited++
		}
		if callIDsSet(body) {
			set++
			if changed {
				t.Fatalf("callIDsSet(%s) but the decode gives\n%s", body, got)
			}
		} else {
			unset++
		}
		if out := chatCallIDs(bytes.Clone(body)); !bytes.Equal(out, got) {
			t.Fatalf("chatCallIDs(%s) = %s, want %s", body, out, got)
		}
	}
	if set < 1000 || unset < 1000 || edited < 1000 {
		t.Fatalf("set %d, unset %d, edited %d: the test reaches too little", set, unset, edited)
	}
	for _, b := range []string{`{"model":"m"}`, `{"messages":null}`, `{"messages":{}}`, `[]`} {
		if got := chatCallIDs([]byte(b)); string(got) != b {
			t.Errorf("chatCallIDs(%s) = %s", b, got)
		}
	}
}
