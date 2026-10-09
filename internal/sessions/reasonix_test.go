package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reasonixFixture(t *testing.T) (string, string) {
	t.Helper()
	setup(t)
	root := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", root)
	dir := filepath.Join(root, "projects", "project", "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "2026-10-06.session.jsonl")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(p, `{"role":"user","content":"hello","createdAt":1791244800000}`+"\n"+`{"role":"assistant","content":"hello back","reasoning_content":"thinking","createdAt":1791244801000}`+"\n")
	write(p+".meta", `{"id":"session-a","custom_title":"Named session","workspace_root":"/work/project","created_at":"2026-10-06T00:00:00Z"}`)
	l := strings.TrimSuffix(p, ".jsonl") + ".turns.jsonl"
	write(l, reasonixUsageFixture(1, "executor", "magpie/deepseek/deepseek-flash"))
	// Global usage cannot be assigned to this session. Even a valid metadata
	// sidecar does not make a wire/events ledger into a second conversation.
	write(strings.TrimSuffix(p, ".jsonl")+".wire.jsonl", `{"kind":"turn_started","text":"not a transcript","modelRef":"fake/fake-model","msgIndex":0,"seq":1}`+"\n")
	write(strings.TrimSuffix(p, ".jsonl")+".wire.jsonl.meta", `{"id":"wire"}`)
	return p, l
}

func reasonixUsageFixture(seq int, source, model string) string {
	b, _ := json.Marshal(map[string]any{"schemaVersion": 1, "recordType": "event", "sessionId": "session-a", "turnId": "turn-a", "seq": seq, "kind": "usage", "createdAt": 1791244801000, "event": map[string]any{"usage": map[string]any{"promptTokens": 100, "completionTokens": 20, "cacheHitTokens": 60, "reasoningTokens": 10, "sessionCacheHitTokens": 99999, "contextPromptTokens": 900, "source": source, "model": model}}})
	return string(b) + "\n"
}

func reasonixOnly(t *testing.T) Session {
	t.Helper()
	var out []Session
	for _, s := range List(0) {
		if s.Agent == "reasonix" {
			out = append(out, s)
		}
	}
	if len(out) != 1 {
		t.Fatalf("want one Reasonix conversation, got %d", len(out))
	}
	return out[0]
}

func TestReasonixSessionUsageAndTranscript(t *testing.T) {
	p, l := reasonixFixture(t)
	s := reasonixOnly(t)
	if s.ID != "session-a" {
		t.Fatalf("native session identity lost: %q", s.ID)
	}
	if got, ok := Get("reasonix:session-a"); !ok || got.Path != p {
		t.Fatalf("session drill-down missing: %+v %v", got, ok)
	}
	if s.Title != "Named session" || s.Cwd != "/work/project" || s.Path != p || !s.Transcript {
		t.Fatalf("session metadata: %+v", s)
	}
	if s.Input != 40 || s.Output != 20 || s.CacheRead != 60 {
		t.Fatalf("billable buckets: %+v", s.Tokens)
	}
	tr, err := TranscriptOf(s)
	if err != nil || len(tr.Parts) != 3 || tr.Parts[0].Text != "hello" || tr.Parts[1].Kind != "thinking" {
		t.Fatalf("transcript: %+v, %v", tr, err)
	}
	f, err := os.OpenFile(l, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// A repeated event is not a new call; Planner and Executor count under
	// their actual models, not under the session's default model.
	_, err = f.WriteString(reasonixUsageFixture(1, "executor", "magpie/deepseek/deepseek-flash") + reasonixUsageFixture(2, "planner", "magpie/deepseek/deepseek-pro"))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	s = reasonixOnly(t)
	if s.Input != 80 || s.Output != 40 || s.CacheRead != 120 || len(s.Models) != 2 {
		t.Fatalf("append or duplicate accounting: %+v", s)
	}
	Reset() // cold aggregate cache must retain the ledger watermark
	s = reasonixOnly(t)
	if s.Input != 80 {
		t.Fatalf("cold cache duplicated usage: %+v", s)
	}
	if err := os.WriteFile(p+".meta", []byte(`{"id":"session-a","custom_title":"Renamed","workspace_root":"/work/other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s = reasonixOnly(t)
	if s.Title != "Renamed" || s.Cwd != "/work/other" {
		t.Fatalf("metadata-only change was stale: %+v", s)
	}
	var found bool
	for _, sum := range StatsFor(0).Sessions {
		if sum.Agent == "reasonix" {
			found = true
			if sum.Input != 80 {
				t.Fatalf("statistics: %+v", sum)
			}
		}
	}
	if !found {
		t.Fatal("Reasonix missing from session statistics")
	}
}

func TestReasonixLedgerRewriteAndPartialTail(t *testing.T) {
	_, l := reasonixFixture(t)
	reasonixOnly(t)
	line := reasonixUsageFixture(3, "executor", "deepseek/deepseek-flash")
	if err := os.WriteFile(l, []byte(line[:len(line)-1]), 0600); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); s.Input != 0 {
		t.Fatalf("partial event counted: %+v", s)
	}
	f, _ := os.OpenFile(l, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("\n")
	f.Close()
	if s := reasonixOnly(t); s.Input != 40 {
		t.Fatalf("completed tail not read: %+v", s)
	}
	if err := os.WriteFile(l, []byte(reasonixUsageFixture(1, "planner", "deepseek/deepseek-pro")), 0600); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); len(s.Models) != 1 || s.Models[0].Model != "deepseek/deepseek-pro" {
		t.Fatalf("replacement retained old usage: %+v", s)
	}
}

func TestReasonixMissingAndCompactedHistory(t *testing.T) {
	p, l := reasonixFixture(t)
	if err := os.Remove(l); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); !s.UsageIncomplete || s.Input != 0 {
		t.Fatalf("missing history: %+v", s)
	}
	checkpoint := `{"recordType":"checkpoint","sessionId":"session-a","compactedThroughSeq":200}` + "\n"
	if err := os.WriteFile(l, []byte(checkpoint+reasonixUsageFixture(201, "executor", "deepseek/deepseek-flash")), 0600); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); !s.UsageIncomplete || s.Input != 40 {
		t.Fatalf("compacted history: %+v", s)
	}
	if err := os.Remove(p + ".meta"); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); !s.UsageIncomplete || s.Title != "hello" {
		t.Fatalf("older metadata-free conversation: %+v", s)
	}
}

func TestReasonixCopiedSessionIsNotCountedTwice(t *testing.T) {
	p, l := reasonixFixture(t)
	dir := filepath.Join(ReasonixDir(), "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{p, p + ".meta", l} {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, filepath.Base(src)), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if s := reasonixOnly(t); s.Input != 40 {
		t.Fatalf("copied session double-counted: %+v", s)
	}
}

func BenchmarkReasonixLedgerAppend(b *testing.B) {
	dir := b.TempDir()
	p := filepath.Join(dir, "session.turns.jsonl")
	// Real ledger shape: text dominates the bytes; only a few usage events
	// are needed by statistics. The scanner must skip long streamed text.
	text := `{"recordType":"event","sessionId":"session-a","kind":"text","event":{"text":"` + strings.Repeat("x", 8192) + `"}}` + "\n"
	data := strings.Repeat(text, 2048) + reasonixUsageFixture(1, "executor", "deepseek/deepseek-flash")
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		b.Fatal(err)
	}
	f := file{agent: "reasonix", path: p, sid: "session-a"}
	stat(&f)
	s := parse(f, nil)
	writer, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		b.Fatal(err)
	}
	defer writer.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err = writer.WriteString(reasonixUsageFixture(i+2, "executor", "deepseek/deepseek-flash")); err != nil {
			b.Fatal(err)
		}
		stat(&f)
		s = parse(f, s)
	}
	if s.Models["deepseek/deepseek-flash"].Input != 40*(b.N+1) {
		b.Fatal("append usage lost or duplicated")
	}
}

// Fixtures captured from the published 2.29.0 serve host, not synthesized
// from Message's optional fields. See the fixture's provenance README.
func reasonix229Fixture(t *testing.T) (string, string) {
	t.Helper()
	p, l := reasonixFixture(t)
	if err := os.Remove(l); err != nil {
		t.Fatal(err)
	}
	wire := strings.TrimSuffix(p, ".jsonl") + ".wire.jsonl"
	for name, target := range map[string]string{"session.jsonl": p, "session.jsonl.meta": p + ".meta", "session.jsonl.telemetry.json": p + ".telemetry.json", "session.wire.jsonl": wire} {
		b, err := os.ReadFile(filepath.Join("testdata", "reasonix-2.29.0", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(target, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return p, wire
}

func TestReasonix229NativeUsageAndMessages(t *testing.T) {
	reasonix229Fixture(t)
	check := func() {
		t.Helper()
		s := reasonixOnly(t)
		if s.Title != "hello there" || s.Cwd != "/work/reasonix-229" || s.UsageIncomplete || s.Input != 468 || s.Output != 112 || s.CacheRead != 2000 || len(s.Models) != 1 || s.Models[0].Model != "fake/fake-model" {
			t.Fatalf("native 2.29.0 session: %+v", s)
		}
		tr, err := TranscriptOf(s)
		if err != nil || len(tr.Parts) != 4 || tr.Parts[0].Text != "hello there" || tr.Parts[2].Text != "follow up" {
			t.Fatalf("native user text: %+v %v", tr, err)
		}
		found := false
		for _, sum := range StatsFor(0).Sessions {
			if sum.Agent == "reasonix" {
				found = true
				if sum.Prompts != 2 || sum.Replies != 2 || len(sum.Models) != 1 || sum.Models[0] != "fake/fake-model" || sum.Input != 468 || sum.Output != 112 || sum.CacheRead != 2000 || sum.UsageIncomplete {
					t.Fatalf("native Usage Sessions: %+v", sum)
				}
			}
		}
		if !found {
			t.Fatal("native 2.x Usage Sessions absent")
		}
	}
	check()
	Reset()
	check()
}

func TestReasonix229WireCoverageAndResume(t *testing.T) {
	p, wire := reasonix229Fixture(t)
	reasonixOnly(t)
	original, err := os.ReadFile(wire)
	if err != nil {
		t.Fatal(err)
	}
	// Sequences restart after resume. Repeated seq/attempt IDs do not mean
	// duplicate billing; the append belongs to a new host process.
	f, err := os.OpenFile(wire, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(original); err != nil {
		t.Fatal(err)
	}
	f.Close()
	telemetry := `{"version":1,"usage":{"promptTokens":4936,"completionTokens":224,"cacheHitTokens":4000,"requestCount":4}}`
	if err = os.WriteFile(p+".telemetry.json", []byte(telemetry), 0600); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); s.Input != 936 || s.Output != 224 || s.CacheRead != 4000 || s.UsageIncomplete {
		t.Fatalf("resumed wire sequences: %+v", s)
	}
	// A retained prefix has only one request; global/session totals are not
	// assigned to a model or added to the retained usage a second time.
	lines := strings.Split(string(original), "\n")
	prefix := ""
	for _, line := range lines {
		prefix += line + "\n"
		var frame struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal([]byte(line), &frame)
		if frame.Kind == "usage" {
			break
		}
	}
	if err = os.WriteFile(wire, []byte(prefix), 0600); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); s.Input != 234 || s.Output != 56 || !s.UsageIncomplete {
		t.Fatalf("telemetry detects missing frames: %+v", s)
	}
	if err = os.Remove(p + ".telemetry.json"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(strings.TrimSuffix(wire, ".jsonl")+".meta.json", []byte(`{"truncated":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); !s.UsageIncomplete || s.Input != 234 {
		t.Fatalf("wire truncation marker: %+v", s)
	}
}

func TestReasonix229MessagesWithoutUsage(t *testing.T) {
	p, wire := reasonix229Fixture(t)
	if err := os.Remove(wire); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p + ".telemetry.json"); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); !s.UsageIncomplete || s.Input != 0 || len(s.Models) != 1 || s.Title != "hello there" {
		t.Fatalf("missing usage: %+v", s)
	}
	// Host-authored user messages must not change the native prompt/title.
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b = append([]byte(`{"role":"user","content":"host instruction","host_authored":true,"createdAt":1791340530000}`+"\n"), b...)
	if err = os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(p + ".meta"); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); s.Title != "hello there" {
		t.Fatalf("host-authored fallback: %+v", s)
	}
	for _, sum := range StatsFor(0).Sessions {
		if sum.Agent == "reasonix" && (sum.Prompts != 2 || sum.Replies != 2 || len(sum.Models) != 1) {
			t.Fatalf("timestamp-free assistant attribution: %+v", sum)
		}
	}
}

func TestReasonixDirsWithoutTranscripts(t *testing.T) {
	setup(t)
	root := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", root)
	for _, rel := range []string{"sessions", "projects/project/sessions"} {
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, d := range Dirs() {
			if d == root {
				found = true
			}
		}
		if !found {
			t.Fatalf("existing empty %s must be listed without reading transcripts", rel)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReasonixOldSummaryRebuildsModels(t *testing.T) {
	p, l := reasonixFixture(t)
	if err := os.Remove(l); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"role":"assistant","content":"reply","modelRef":"magpie/deepseek/deepseek-flash","createdAt":1791244801000}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reasonixOnly(t) // initialize the shared summary cache
	files := reasonixFiles()
	f := files[0]
	old := parse(f, nil)
	// An unchanged transcript already scanned by the previous adapter.
	old.Models = nil
	for _, d := range old.Days {
		d.Models = nil
	}
	old.DBRevision = strings.TrimPrefix(old.DBRevision, reasonixRevision)
	mu.Lock()
	cache[p] = old
	mu.Unlock()
	if s := reasonixOnly(t); len(s.Models) != 1 {
		t.Fatalf("old cached summary still hides models: %+v", s)
	}
}

func TestReasonix229RealResumeTelemetryEpoch(t *testing.T) {
	p, wire := reasonix229Fixture(t)
	for name, target := range map[string]string{"resumed.jsonl": p, "resumed.jsonl.meta": p + ".meta", "resumed.jsonl.telemetry.json": p + ".telemetry.json", "resumed.wire.jsonl": wire} {
		b, err := os.ReadFile(filepath.Join("testdata", "reasonix-2.29.0", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(target, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if s := reasonixOnly(t); s.Input != 702 || s.Output != 168 || s.CacheRead != 3000 || s.UsageIncomplete {
		t.Fatalf("real resume telemetry epoch: %+v", s)
	}
	for _, sum := range StatsFor(0).Sessions {
		if sum.Agent == "reasonix" && (sum.Prompts != 3 || sum.Replies != 3 || sum.UsageIncomplete) {
			t.Fatalf("real resumed activity: %+v", sum)
		}
	}
}

// Faults are applied to producer-captured frames. They must affect both the
// retained accounting and completeness; a green parser alone is not enough.
func TestReasonix229WireFaults(t *testing.T) {
	for _, tc := range []struct {
		name, old, new     string
		input, output, hit int
		partial            bool
	}{
		{"missing-user-index", `"msgIndex":1`, `"msgIndex":999`, 234, 56, 1000, true},
		{"missing-model", `"modelRef":"fake/fake-model"`, `"modelRef":""`, 0, 0, 0, true},
		{"estimated-usage", `"attemptId":"sa-1-1"`, `"attemptId":"sa-1-1","estimated":true`, 234, 56, 1000, true},
		{"invalid-cache", `"cacheHitTokens":1000,"cacheMissTokens":234`, `"cacheHitTokens":2000,"cacheMissTokens":234`, 0, 0, 0, true},
		{"executor-model-fallback", `"modelRef":"fake/fake-model","usageSource"`, `"modelRef":"","usageSource"`, 468, 112, 2000, false},
		{"auxiliary-quoted-model", `"source":"executor"`, `"source":"planner"`, 468, 112, 2000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, wire := reasonix229Fixture(t)
			b, err := os.ReadFile(wire)
			if err != nil {
				t.Fatal(err)
			}
			n := strings.ReplaceAll(string(b), tc.old, tc.new)
			if n == string(b) {
				t.Fatal("mutation did not match captured bytes")
			}
			if err = os.WriteFile(wire, []byte(n), 0600); err != nil {
				t.Fatal(err)
			}
			s := reasonixOnly(t)
			if s.Input != tc.input || s.Output != tc.output || s.CacheRead != tc.hit || s.UsageIncomplete != tc.partial {
				t.Fatalf("fault accounting: %+v", s)
			}
		})
	}
}

func TestReasonix229SidecarRefreshAndTail(t *testing.T) {
	p, wire := reasonix229Fixture(t)
	reasonixOnly(t) // warm cache
	marker := strings.TrimSuffix(wire, ".jsonl") + ".meta.json"
	if err := os.WriteFile(marker, []byte(`{"truncated":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); !s.UsageIncomplete || s.Input != 468 {
		t.Fatalf("marker-only update stale: %+v", s)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); s.UsageIncomplete {
		t.Fatalf("marker removal stale: %+v", s)
	}
	if err := os.WriteFile(p+".telemetry.json", []byte(`{"version":99,"usage":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); !s.UsageIncomplete || s.Input != 468 {
		t.Fatalf("unsupported telemetry version: %+v", s)
	}
	if err := os.Remove(p + ".telemetry.json"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(wire)
	if err != nil {
		t.Fatal(err)
	}
	tail := `{"kind":"usage","usage":{"promptTokens":10,"completionTokens":2,"cacheHitTokens":0,"source":"executor"},"seq":15}`
	if err = os.WriteFile(wire, append(b, []byte(tail)...), 0600); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); !s.UsageIncomplete || s.Input != 468 {
		t.Fatalf("partial line counted: %+v", s)
	}
	if err = os.WriteFile(wire, append(append(b, []byte(tail)...), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if s := reasonixOnly(t); s.UsageIncomplete || s.Input != 478 || s.Output != 114 {
		t.Fatalf("completed tail stale: %+v", s)
	}
}

func TestReasonixRawContentPresence(t *testing.T) {
	for _, tc := range []struct{ name, line, text, cwd string }{
		{"empty", `{"role":"user","raw_content":"","content":"injected text"}`, "", ""},
		{"legacy-literal-workspace", `{"role":"user","content":"<workspace>\nCurrent workspace: \"/literal\".\nhello"}`, "<workspace>\nCurrent workspace: \"/literal\".\nhello", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, l := reasonixFixture(t)
			_ = os.Remove(l)
			_ = os.Remove(p + ".meta")
			if err := os.WriteFile(p, []byte(tc.line+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			s := &state{}
			reasonixLine(s, []byte(tc.line), true)
			var parts []Part
			err := reasonixTranscript(p, func(_ bool, part Part) bool { parts = append(parts, part); return true })
			if err != nil || len(parts) != 1 || parts[0].Text != tc.text || s.ReasonixCwd != tc.cwd {
				t.Fatalf("presence semantics: %+v %+v %v", s, parts, err)
			}
		})
	}
}

func BenchmarkReasonix229WireLargeTranscriptTail(b *testing.B) {
	dir := b.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	wire := filepath.Join(dir, "session.wire.jsonl")
	for name, target := range map[string]string{"session.jsonl": p, "session.wire.jsonl": wire, "session.jsonl.telemetry.json": p + ".telemetry.json"} {
		data, err := os.ReadFile(filepath.Join("testdata", "reasonix-2.29.0", name))
		if err != nil {
			b.Fatal(err)
		}
		if name == "session.jsonl" {
			data = append(data, []byte(strings.Repeat(`{"role":"tool","content":"`+strings.Repeat("x", 8192)+`"}`+"\n", 2048))...)
		}
		if err = os.WriteFile(target, data, 0600); err != nil {
			b.Fatal(err)
		}
	}
	f := file{agent: "reasonix", path: wire, manifest: p, sid: "session-a"}
	stat(&f)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := parseReasonixWire(f)
		if s.Models["fake/fake-model"].Input != 468 || s.UsageIncomplete {
			b.Fatal("wire summary changed")
		}
	}
}
