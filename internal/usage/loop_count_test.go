package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Usage counts, per model, the replies magpie ended because the model
// was stuck in a loop (#1858): they are among its failed calls, and the
// model's row says how many of them were loops, apart from the vendor's
// own errors. The Overview's model groups and the Requests ranking both
// count them; a handed-on try is no request of its own.
func TestLoopsCountedPerModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)) // midnight never falls between a call and Today (#6486)
	at := func(m int) string { return now.Add(time.Duration(m-10) * time.Second).Format(time.RFC3339Nano) }
	lines := []string{
		// two replies of kimi ended for looping, as the gateway logs them:
		// the agent had its 200, with the stream's error
		`{"t":"` + at(1) + `","agent":"claude","provider":"p","model":"kimi","in":10,"out":900,"ms":5,"status":200,"err":"p: stopped a reply stuck repeating itself","err_type":"` + LoopErrType + `","route_id":1}`,
		`{"t":"` + at(2) + `","agent":"claude","provider":"p","model":"kimi","in":10,"out":900,"ms":5,"status":200,"err":"p: stopped a reply stuck repeating itself","err_type":"` + LoopErrType + `","route_id":2}`,
		// a vendor's own error, and an answer
		`{"t":"` + at(3) + `","agent":"claude","provider":"p","model":"kimi","ms":5,"status":429,"err":"rate limited","err_type":"rate_limit_error","route_id":3}`,
		`{"t":"` + at(4) + `","agent":"claude","provider":"p","model":"kimi","in":10,"out":5,"ms":5,"status":200,"route_id":4}`,
		// another model's loop, on a try handed on: no request of its own
		`{"t":"` + at(5) + `","agent":"claude","provider":"p","model":"glm","in":10,"out":900,"ms":5,"status":200,"err":"p: stopped a reply stuck repeating itself","err_type":"` + LoopErrType + `","route_id":5,"handed_on":true}`,
		`{"t":"` + at(6) + `","agent":"claude","provider":"q","model":"glm","in":10,"out":5,"ms":5,"status":200,"route_id":5,"tries":2}`,
	}
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	if err := os.WriteFile(Path(), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	want := map[string][2]int{"p/kimi": {3, 2}, "p/glm": {0, 0}, "q/glm": {0, 0}} // errors, loops
	s := Summarize(Today)
	seen := 0
	for _, g := range s.Models {
		w, ok := want[g.ID]
		if !ok {
			continue
		}
		seen++
		if g.Errors != w[0] || g.Loops != w[1] {
			t.Errorf("overview %s: errors %d, loops %d; want %d, %d", g.ID, g.Errors, g.Loops, w[0], w[1])
		}
	}
	if seen != 3 {
		t.Fatalf("overview models %+v", s.Models)
	}
	if s.Loops != 2 {
		t.Errorf("overview total loops %d, want 2", s.Loops)
	}

	rows, sum, _ := Ledger(Today, Filter{})
	if sum.Loops != 2 || sum.Errors != 3 {
		t.Errorf("ledger sum: errors %d, loops %d", sum.Errors, sum.Loops)
	}
	byModel := map[string][2]int{"kimi": {3, 2}, "glm": {0, 0}}
	seen = 0
	for _, sh := range Breakdown(rows, "model") {
		w, ok := byModel[sh.ID]
		if !ok {
			continue
		}
		seen++
		if sh.Errors != w[0] || sh.Loops != w[1] {
			t.Errorf("requests %s: errors %d, loops %d; want %d, %d", sh.ID, sh.Errors, sh.Loops, w[0], w[1])
		}
	}
	if seen != 2 {
		t.Errorf("requests ranked %d of kimi and glm", seen)
	}
}
