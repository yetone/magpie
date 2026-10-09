package provider

import "testing"

// A limit on requests a minute (coeo91 on Discord) is kept with the
// provider, within 0–MaxRPMLimit, and 0 takes it off; a subscription's is
// kept with its picks and is every account's of it.
func TestSetRPM(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "or", Name: "OpenRouter", Key: "k", Models: []string{"m"}, Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := SetRPM("or", 20); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("or"); err != nil || p.MaxRPM != 20 || p.RPMLimit() != 20 {
		t.Fatalf("after SetRPM 20: %+v %v", p, err)
	}
	for _, n := range []int{-1, MaxRPMLimit + 1} {
		if err := SetRPM("or", n); err == nil {
			t.Fatalf("SetRPM %d was taken", n)
		}
	}
	if p, _ := Find("or"); p.MaxRPM != 20 {
		t.Fatalf("a refused limit changed it: %d", p.MaxRPM)
	}
	p, _ := Find("or")
	p.MaxRPM = 50000
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("or"); p.MaxRPM != MaxRPMLimit {
		t.Fatalf("saved past the bound: %d", p.MaxRPM)
	}
	if err := SetRPM("or", 0); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("or"); p.MaxRPM != 0 || p.RPMLimit() != 0 {
		t.Fatalf("off: %d", p.MaxRPM)
	}

	if err := SetRPM("codex", 30); err != nil {
		t.Fatal(err)
	}
	if s, ok := storedPicks("codex"); !ok || s.MaxRPM != 30 {
		t.Fatalf("codex's picks: %+v", s)
	}
	n := 0
	for _, a := range All() {
		if a.ID == "codex" && a.Account != nil {
			n++
			if a.RPMLimit() != 30 {
				t.Fatalf("codex account %s: limit %d", a.Account.User, a.RPMLimit())
			}
		}
	}
	if n == 0 {
		t.Fatal("no codex account listed")
	}
}
