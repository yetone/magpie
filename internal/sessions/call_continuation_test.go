package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCallSourceKeepsAppendContinuation(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			d := setupCalls(t)
			var path, extra string
			if agent == "claude" {
				path = filepath.Join(d.claude, "projects", "fixture", "session.jsonl")
				writeLines(t, path, claudeMsg("one", "claude-opus-5-5", 10, 2, 30, 4, 1))
				extra = claudeMsg("two", "claude-opus-5-5", 10, 2, 30, 4, 2)
			} else {
				path = filepath.Join(d.codex, "sessions", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
				writeLines(t, path, append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), tokenCountLine(1, cxUse(100, 40, 0, 10, 2), cxUse(100, 40, 0, 10, 2)))...)
				extra = tokenCountLine(2, cxUse(200, 80, 0, 20, 4), cxUse(100, 40, 0, 10, 2))
			}
			read := func() []Call {
				t.Helper()
				for _, source := range CallSources() {
					if source.Path == path {
						return ReadCallSource(source)
					}
				}
				t.Fatal("source missing")
				return nil
			}
			if cs := read(); len(cs) != 1 {
				t.Fatal(cs)
			}
			if entry := callContinuations[path]; len(entry.state.Calls) != 0 {
				t.Fatal("expanded calls retained")
			}
			before, err := os.Stat(callCachePath(path))
			if err != nil {
				t.Fatal(err)
			}
			appendText(t, path, extra+"\n")
			if cs := read(); len(cs) != 2 {
				t.Fatal(cs)
			}
			after, err := os.Stat(callCachePath(path))
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("live append rebuilt the entire shard")
			}
		})
	}
}
