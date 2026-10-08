package gateway

import (
	"math"
	"testing"
)

// devinSampling is the temperature and top_p a GetChatMessage request
// carries in its field 8.
func devinSampling(t *testing.T, r *Request) (temp, topP float64) {
	t.Helper()
	for _, f := range pbFields(buildDevin(r, "swe-2", "k")) {
		if f.num != 8 {
			continue
		}
		for _, g := range pbFields(f.data) {
			switch g.num {
			case 5:
				temp = math.Float64frombits(g.n)
			case 8:
				topP = math.Float64frombits(g.n)
			}
		}
	}
	return
}

// Devin answers 400 "an internal error occurred" to a temperature or
// top_p of exactly 0 (Hermes Agent's command approvals send temperature
// 0): a 0 goes as 1e-6, any other value as it came, none as Devin's
// defaults (plugins #50).
func TestDevinSamplingZero(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	say := []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}
	for _, c := range []struct {
		name            string
		temp, topP      *float64
		wantT, wantTopP float64
	}{
		{"none", nil, nil, 1, 0.95},
		{"temperature 0", f(0), nil, 1e-6, 0.95},
		{"top_p 0", nil, f(0), 1, 1e-6},
		{"both -0", f(math.Copysign(0, -1)), f(0), 1e-6, 1e-6},
		{"others as they came", f(0.2), f(1), 0.2, 1},
		{"1e-6 as it came", f(1e-6), nil, 1e-6, 0.95},
	} {
		temp, topP := devinSampling(t, &Request{Temp: c.temp, TopP: c.topP, Messages: say})
		if temp != c.wantT || topP != c.wantTopP {
			t.Errorf("%s: temperature %v top_p %v, want %v %v", c.name, temp, topP, c.wantT, c.wantTopP)
		}
	}
}
