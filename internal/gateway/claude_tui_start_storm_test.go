package gateway

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/testenv"
)

// Six conversations asked at once each started a full Claude Code, and on
// the reporter's Windows machine none of them showed its prompt in the 45s
// a start alone fits in: starts going at once slow each other past the
// wait (#5579, windows/amd64, 0.1.3057 and 0.1.2901). Every one was ended
// at the deadline and answered 502, and each caller's next try started the
// pile again — 531 interactive starts in an hour, 420 of them ending
// "showed no prompt in 45s", while one start at a time on the same machine
// and subscription answered normally.
//
// So the interactive mode's starts take turns: a run's Claude Code is
// started only once the one before it has shown its prompt (its helper has
// answered magpie's initialize request). The stand-in helper logs begin
// when it starts and end when it hands over to Claude Code; the starts
// must alternate — no begin while another is still starting — and every
// call is answered.
func TestClaudeTUIStartsTakeTurns(t *testing.T) {
	h := newReplyHarness(t, "ok")
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	log := filepath.Join(t.TempDir(), "starts")
	helper := filepath.Join(t.TempDir(), "magpie")
	testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
echo begin >> '`+log+`'
sleep 2
echo end >> '`+log+`'
shift; claude=$1; shift
exec "$claude" "$@"
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })

	const calls = 4
	var wg sync.WaitGroup
	codes := make([]int, calls)
	msgs := make([]string, calls)
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], msgs[i] = h.askCode("rules "+strconv.Itoa(i), "hi")
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("conversation %d: %d %s", i, c, msgs[i])
		}
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the helper's log: %v", err)
	}
	if n := strings.Count(string(b), "begin"); n != calls {
		t.Fatalf("the helper started %d times, want %d:\n%s", n, calls, b)
	}
	up := false
	for _, line := range strings.Split(string(b), "\n") {
		switch strings.TrimSpace(line) {
		case "begin":
			if up {
				t.Fatalf("a start began while another was still starting:\n%s", b)
			}
			up = true
		case "end":
			if !up {
				t.Fatalf("a start ended with none beginning:\n%s", b)
			}
			up = false
		}
	}
	if up {
		t.Fatalf("a start never handed over to Claude Code:\n%s", b)
	}
}
