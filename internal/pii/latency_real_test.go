package pii

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/pii/guard"
	"github.com/yetone/magpie/internal/redact"
)

// latencySession is a Messages request on turn n as Claude Code sends
// one: a long system prompt, tool definitions, and per turn the user's
// words with a reminder, the assistant's prose and a tool call, and its
// result, kb KB of it.
func latencySession(n, kb int) []byte {
	prose := "You are an interactive agent that helps users with software engineering tasks. Use the instructions below and the tools available to you to assist the user. Prefer editing existing files; keep changes small and verify them by running the tests. "
	var tools []any
	for i := 0; i < 24; i++ {
		tools = append(tools, map[string]any{"name": fmt.Sprintf("Tool%d", i), "description": strings.Repeat(fmt.Sprintf("Tool %d reads and writes files in the user's workspace. ", i), 25),
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": "The absolute path"}}}})
	}
	var msgs []any
	for i := 0; i < n; i++ {
		var out strings.Builder
		for j := 0; out.Len() < kb*1024; j++ {
			switch (i + j) % 4 {
			case 0:
				fmt.Fprintf(&out, "/home/ubuntu/projects/api/internal/store/store_%d.go:%d:\terr := db.QueryRowContext(ctx, \"SELECT id, name, email FROM users WHERE id = $1\", id).Scan(&u.ID, &u.Name, &u.Email)\n", j, 40+j)
			case 1:
				fmt.Fprintf(&out, "2026-11-13T12:%02d:%02d.%03dZ INFO request id=%08x method=POST path=/v1/orders/%d status=200 dur=%dms\n", j%60, i%60, j%1000, i*7919+j, j, 3+j%90)
			case 2:
				fmt.Fprintf(&out, "Author: Wei Zhang <wei.zhang@example.com>  commit %040x\n    fix: retry the payment webhook when the upstream answers 502 (#%d)\n", i*31337+j, 1000+j)
			case 3:
				fmt.Fprintf(&out, "    --- PASS: TestOrderFlow/case_%d (0.%02ds) created order %d for customer Maria Garcia, total 129.%02d EUR\n", j, j%100, 5000+j, j%100)
			}
		}
		msgs = append(msgs,
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": fmt.Sprintf("step %d: the order webhook still fails on retries for customer Maria Garcia, look at the logs and fix it", i)},
				map[string]any{"type": "text", "text": "<system-reminder>The user opened /home/ubuntu/projects/api/internal/store/store.go in the IDE.</system-reminder>"}}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": fmt.Sprintf("Turn %d. I'll look at the webhook handler and the retry logic, then run the store tests to see where the 502 comes from before changing anything.", i)},
				map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%024d", i), "name": "Bash", "input": map[string]any{"command": fmt.Sprintf("grep -rn webhook /home/ubuntu/projects/api | head -%d", 50+i)}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprintf("toolu_%024d", i), "content": out.String()}}},
		)
	}
	b, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "stream": true, "max_tokens": 32000,
		"system": []any{map[string]any{"type": "text", "text": strings.Repeat(prose, 100)}}, "tools": tools, "messages": msgs})
	return b
}

// TestRealLatencyOfALongSession times masking a long Claude Code
// session's requests turn after turn, with the rules alone and with
// AstrLink Guard, where GUARD_REAL_DIR names a folder holding its files.
func TestRealLatencyOfALongSession(t *testing.T) {
	d := os.Getenv("GUARD_REAL_DIR")
	if d == "" {
		t.Skip("GUARD_REAL_DIR names no AstrLink Guard files")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, guardRev[:12]), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range guardFiles {
		if err := os.Symlink(filepath.Join(d, filepath.Base(f.path)), filepath.Join(root, guardRev[:12], filepath.Base(f.path))); err != nil {
			t.Fatal(err)
		}
	}
	oldRoot := guardRoot
	guardRoot = func() string { return root }
	t.Cleanup(func() { guardRoot = oldRoot; guardUnload() })
	var read atomic.Int64
	oldClassify := guardBackend.classify
	guardBackend.classify = func(texts []string) ([][]redact.Found, error) {
		for _, s := range texts {
			read.Add(int64(len(s)))
		}
		return oldClassify(texts)
	}
	t.Cleanup(func() { guardBackend.classify = oldClassify })
	turns, kb, wait, steps := 60, 4, 15, 4
	fmt.Sscan(os.Getenv("LATENCY_TURNS"), &turns)
	fmt.Sscan(os.Getenv("LATENCY_KB"), &kb)
	fmt.Sscan(os.Getenv("LATENCY_WAIT"), &wait)
	fmt.Sscan(os.Getenv("LATENCY_STEPS"), &steps)
	model := os.Getenv("LATENCY_MODEL") != ""
	o := redact.Options{Secrets: true, Personal: true, Route: "anthropic"}
	if model {
		o.Model = func(texts []string) [][]redact.Found {
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(wait)*time.Second)
			defer cancel()
			got, err := guardBackend.find(ctx, texts)
			var p *Partial
			if err != nil && !errors.As(err, &p) {
				t.Fatal(err)
			}
			if len(got) != len(texts) {
				got = make([][]redact.Found, len(texts))
			}
			return got
		}
		o.MinScore = guard.MinScore
		o.NamesByModel = true
		guardClassify([]string{"warm the model"})
		read.Store(0)
	}
	if os.Getenv("LATENCY_WARM") != "" {
		// the history read before, as a session the model kept up with
		saved := wait
		wait = 3600
		redact.MaskJSON(latencySession(turns, kb), o)
		wait = saved
		turns++
	}
	for n := turns; n < turns+steps; n++ {
		body := latencySession(n, kb)
		read.Store(0)
		t0 := time.Now()
		redact.MaskJSON(body, o)
		t.Logf("turn %d: %d KB body, masked in %v, model read %d KB", n, len(body)>>10, time.Since(t0).Round(time.Millisecond), read.Load()>>10)
	}
}
