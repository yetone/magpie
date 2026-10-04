package sessions

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCodexUsageBothCountersRestartAfterLegacyHistory(t *testing.T) {
	use := func(n int) string { return cxUse(n*100, n*20, 0, n*10, n*2) }
	for split := 0; split <= 6; split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"),
				tokenCountLine(1, use(100), use(1)),
				cxRecordLine(2, "first-runtime", use(1), use(1)), tokenCountLine(3, use(1), use(1)),
				cxRecordLine(4, "continues", use(2), use(1)), tokenCountLine(5, use(2), use(1)))
			path := codexUsageLog(t, lines[:split])
			Calls(time.Time{})
			List(0)
			Reset()
			lines = append(lines[split:],
				cxRecordLine(6, "second-runtime", use(3), use(1)), tokenCountLine(7, use(1), use(1)),
				cxRecordLine(8, "after-resume", use(4), use(1)), tokenCountLine(9, use(2), use(1)))
			appendText(t, path, strings.Join(lines, "\n")+"\n")
			if cs := Calls(time.Time{}); len(cs) != 5 {
				t.Fatalf("calls=%+v", cs)
			}
			if ss := List(0); len(ss) != 1 || ss[0].Tokens != (Tokens{400, 50, 100, 0}) {
				t.Fatalf("summary=%+v", ss)
			}
		})
	}
}

func TestCodexUsageLargerFirstRequestAfterLegacyReset(t *testing.T) {
	before := cxUse(91814, 46976, 0, 396, 123)
	after := cxUse(92225, 91008, 0, 885, 516)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), tokenCountLine(1, before, before))
	lines = append(lines, cxTurnLines(2, "t2", "gpt-6-astra", "high")...)
	lines = append(lines, tokenCountLine(3, after, after), tokenCountLine(4, after, after))
	codexUsageLog(t, lines)
	if cs := Calls(time.Time{}); len(cs) != 2 {
		t.Fatalf("new runtime's first request lost or replay counted: %+v", cs)
	}
	if ss := List(0); len(ss) != 1 || ss[0].Tokens != (Tokens{46055, 1281, 137984, 0}) {
		t.Fatalf("summary=%+v", ss)
	}
}
