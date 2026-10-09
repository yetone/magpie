package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"github.com/klauspost/compress/zstd"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func copyReasonixStore(t *testing.T, codec, location string) string {
	t.Helper()
	setup(t)
	root := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", root)
	dir := filepath.Join(root, location, "producer-capture")
	var copyTree func(string, string)
	copyTree = func(src, dst string) {
		es, e := os.ReadDir(src)
		if e != nil {
			t.Fatal(e)
		}
		os.MkdirAll(dst, 0700)
		for _, x := range es {
			a, b := filepath.Join(src, x.Name()), filepath.Join(dst, x.Name())
			if x.IsDir() {
				copyTree(a, b)
			} else {
				v, e := os.ReadFile(a)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(b, v, 0600); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	copyTree(filepath.Join("testdata/reasonix-stores", codec), dir)
	copyTree("testdata/reasonix-stores/.content-v1", filepath.Join(filepath.Dir(dir), ".content-v1"))
	return dir
}
func TestReasonixAllStoreCodecs(t *testing.T) {
	for _, c := range []struct{ codec, location string }{{"linear-v3", "sessions-v3"}, {"linear-v3.1", "projects/p/sessions-v3"}, {"events-v3", "sessions-v3"}, {"linear-v4", "projects/p/sessions-v4"}, {"linear-v4", "desktop-sessions-v5/by-id"}, {"linear-v4", "sessions-v99"}} {
		t.Run(c.codec+"/"+c.location, func(t *testing.T) {
			_ = copyReasonixStore(t, c.codec, c.location)
			s := reasonixOnly(t)
			if s.Title != "Native producer capture" || s.ID != "producer-capture" || !s.UsageIncomplete || s.Last.IsZero() {
				t.Fatalf("store not represented: %+v", s)
			}
			if c.location == "projects/p/sessions-v4" && !strings.Contains(s.Resume, shellQuote(s.ID)) {
				t.Fatalf("resume is not native identity: %s", s.Resume)
			}
			tr, e := TranscriptOf(s)
			if e != nil {
				t.Fatal(e)
			}
			if len(tr.Parts) != 4 || tr.Parts[0].Text != "你好" || tr.Parts[1].Kind != "thinking" || tr.Parts[3].Kind != "tool_result" || len(tr.Parts[3].Text) < 1000 {
				t.Fatalf("native content missing: parts=%d", len(tr.Parts))
			}
			if n := UnsupportedReasonixStores(); n != 0 {
				t.Fatalf("supported store reported unsupported: %d", n)
			}
			Reset()
			cold := reasonixOnly(t)
			if cold.Title != s.Title || !cold.Last.Equal(s.Last) {
				t.Fatal("cold summary differs")
			}
		})
	}
}

func appendReasonixBatch(t *testing.T, path string, first uint64, kind string, payload any) []byte {
	t.Helper()
	at := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	begin := map[string]any{"schemaVersion": 4, "codec": "reasonix.session.linear/v4", "recordType": "batch/begin", "commitId": fmtSeq(first), "operationId": fmtSeq(first), "operationHash": "test-operation", "firstSeq": first, "eventCount": 1, "writerGeneration": 1, "createdAt": at}
	b, _ := json.Marshal(payload)
	event := map[string]any{"schemaVersion": 4, "codec": "reasonix.session.linear/v4", "recordType": "batch/event", "event": map[string]any{"id": fmtSeq(first), "seq": first, "kind": kind, "payload": b}}
	digest := sha256.New()
	var out bytes.Buffer
	enc, e := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if e != nil {
		t.Fatal(e)
	}
	defer enc.Close()
	frame := func(v any, hash bool) {
		raw, _ := json.Marshal(v)
		if hash {
			digest.Write(raw)
			digest.Write([]byte{0})
		}
		compressed := enc.EncodeAll(raw, nil)
		h := make([]byte, 12)
		copy(h, "RX4F")
		binary.BigEndian.PutUint32(h[4:8], uint32(len(compressed)))
		binary.BigEndian.PutUint32(h[8:12], uint32(len(raw)))
		out.Write(h)
		out.Write(compressed)
	}
	frame(begin, true)
	frame(event, true)
	frame(map[string]any{"schemaVersion": 4, "codec": "reasonix.session.linear/v4", "recordType": "batch/end", "commitId": fmtSeq(first), "firstSeq": first, "eventCount": 1, "sha256": hex.EncodeToString(digest.Sum(nil))}, false)
	return out.Bytes()
}
func fmtSeq(n uint64) string { b, _ := json.Marshal(n); return "append-" + string(b) }
func TestReasonixStoreResumeTailAndRewrite(t *testing.T) {
	dir := copyReasonixStore(t, "linear-v4", "sessions-v4")
	path := filepath.Join(dir, "events.frames")
	s := reasonixOnly(t)
	original, _ := os.ReadFile(path)
	batch := appendReasonixBatch(t, path, 6, "message/complete", map[string]any{"message": map[string]any{"id": "followup", "role": "user", "content": "follow-up/resume", "createdAt": time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC).UnixMilli()}})
	os.WriteFile(path, append(append([]byte{}, original...), batch[:len(batch)-3]...), 0600)
	partial := reasonixOnly(t)
	if !partial.Last.Equal(s.Last) {
		t.Fatal("uncommitted tail became activity")
	}
	os.WriteFile(path, append(append([]byte{}, original...), batch...), 0600)
	resumed := reasonixOnly(t)
	if !resumed.Last.After(s.Last) {
		t.Fatal("resume append not refreshed")
	}
	tr, e := TranscriptOf(resumed)
	if e != nil || tr.Parts[len(tr.Parts)-1].Text != "follow-up/resume" {
		t.Fatalf("resume body: %v", e)
	}
	update := appendReasonixBatch(t, path, 7, "message/upsert", map[string]any{"message": map[string]any{"id": "followup", "role": "user", "content": "edited follow-up", "createdAt": time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC).UnixMilli()}})
	all := append(append(append([]byte{}, original...), batch...), update...)
	os.WriteFile(path, all, 0600)
	tr, e = TranscriptOf(reasonixOnly(t))
	if e != nil || tr.Parts[len(tr.Parts)-1].Text != "edited follow-up" {
		t.Fatal("upsert duplicated or ignored")
	}
	retract := appendReasonixBatch(t, path, 8, "message/retract", map[string]any{"messageIds": []string{"followup"}})
	os.WriteFile(path, append(all, retract...), 0600)
	tr, e = TranscriptOf(reasonixOnly(t))
	if e != nil || len(tr.Parts) != 4 {
		t.Fatal("retracted message visible")
	}
	Reset()
	tr, e = TranscriptOf(reasonixOnly(t))
	if e != nil || len(tr.Parts) != 4 {
		t.Fatal("cold replay differs")
	}
}
func TestReasonixStoreRejectsCorruptContentAndFrames(t *testing.T) {
	dir := copyReasonixStore(t, "linear-v4", "sessions-v4")
	path := filepath.Join(dir, "events.frames")
	b, _ := os.ReadFile(path)
	b[0] = 'X'
	os.WriteFile(path, b, 0600)
	if _, e := reasonixProjectStore(path, false); e == nil {
		t.Fatal("bad magic accepted")
	}
}

func TestReasonixStoreContextAndHistoryReplacement(t *testing.T) {
	dir := copyReasonixStore(t, "linear-v4", "sessions-v4")
	path := filepath.Join(dir, "events.frames")
	original, _ := os.ReadFile(path)
	context := appendReasonixBatch(t, path, 6, "model/context-replace", map[string]any{"messages": []any{map[string]any{"id": "context", "role": "user", "content": "provider context only"}}})
	os.WriteFile(path, append(append([]byte{}, original...), context...), 0600)
	tr, e := TranscriptOf(reasonixOnly(t))
	if e != nil || len(tr.Parts) != 4 || tr.Parts[0].Text != "你好" {
		t.Fatal("provider context replaced UI history")
	}
	history := appendReasonixBatch(t, path, 7, "history/replace", map[string]any{"messages": []any{map[string]any{"id": "rewritten", "role": "user", "content": "canonical replacement"}}})
	os.WriteFile(path, append(append(append([]byte{}, original...), context...), history...), 0600)
	tr, e = TranscriptOf(reasonixOnly(t))
	if e != nil || len(tr.Parts) != 1 || tr.Parts[0].Text != "canonical replacement" {
		t.Fatal("canonical history not replaced")
	}
}
func TestReasonixStoreCorruptObjectIsNotSilentlyEmpty(t *testing.T) {
	dir := copyReasonixStore(t, "linear-v4", "sessions-v4")
	objects := filepath.Join(filepath.Dir(dir), ".content-v1", "objects")
	filepath.WalkDir(objects, func(p string, d os.DirEntry, e error) error {
		if e == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			if len(b) > 0 {
				b[0] ^= 1
				os.WriteFile(p, b, 0600)
			}
		}
		return e
	})
	s := reasonixOnly(t)
	if !s.UsageIncomplete {
		t.Fatal("corrupt content called complete")
	}
	if _, e := TranscriptOf(s); e == nil {
		t.Fatal("corrupt referenced content accepted")
	}
	if UnsupportedReasonixStores() != 1 {
		t.Fatal("read failure missing from diagnostic")
	}
}
func TestReasonixStoreUnknownRequiredEvent(t *testing.T) {
	dir := copyReasonixStore(t, "linear-v4", "sessions-v4")
	path := filepath.Join(dir, "events.frames")
	b, _ := os.ReadFile(path)
	os.WriteFile(path, append(b, appendReasonixBatch(t, path, 6, "future/required", map[string]any{"value": true})...), 0600)
	if _, e := reasonixProjectStore(path, true); e == nil {
		t.Fatal("unknown required semantics ignored")
	}
}
func BenchmarkReasonixNativeStoreWarm(b *testing.B) {
	root := b.TempDir()
	b.Setenv("REASONIX_STATE_HOME", root)
	dir := filepath.Join(root, "sessions-v4", "producer-capture")
	os.MkdirAll(dir, 0700)
	for _, n := range []string{"manifest.json", "events.frames"} {
		v, _ := os.ReadFile(filepath.Join("testdata/reasonix-stores/linear-v4", n))
		os.WriteFile(filepath.Join(dir, n), v, 0600)
	}
	fs := reasonixFiles()
	if len(fs) == 0 {
		b.Fatal("no native source")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reasonixFiles()
	}
}

func TestReasonixImportedSourceRetainsUsageOnce(t *testing.T) {
	dir := copyReasonixStore(t, "linear-v4", "sessions-v4")
	root := filepath.Dir(filepath.Dir(dir))
	legacy := filepath.Join(root, "sessions", "old.jsonl")
	os.MkdirAll(filepath.Dir(legacy), 0700)
	os.WriteFile(legacy, []byte(`{"role":"user","content":"old input","createdAt":1791244800000}`+"\n"), 0600)
	os.WriteFile(legacy+".meta", []byte(`{"id":"session-a","workspace_root":"/work"}`), 0600)
	ledger := strings.TrimSuffix(legacy, ".jsonl") + ".turns.jsonl"
	os.WriteFile(ledger, []byte(reasonixUsageFixture(1, "executor", "magpie/deepseek/deepseek-flash")), 0600)
	manifest := filepath.Join(dir, "manifest.json")
	b, _ := os.ReadFile(manifest)
	var m map[string]any
	json.Unmarshal(b, &m)
	m["source"] = map[string]any{"path": legacy, "legacyHeadId": "session-a"}
	b, _ = json.Marshal(m)
	os.WriteFile(manifest, b, 0600)
	s := reasonixOnly(t)
	if s.ID != "producer-capture" || s.Input != 40 || s.Output != 20 || s.CacheRead != 60 {
		t.Fatalf("import usage lost or separate session created: %+v", s)
	}
	Reset()
	s = reasonixOnly(t)
	if s.Input != 40 || s.Output != 20 {
		t.Fatal("cold import double counted usage")
	}
}
func TestReasonixNativeWorkspaceAuthority(t *testing.T) {
	body := "Host context\n\n## Workspace\nCurrent workspace: \"/work/native\""
	digest := sha256.Sum256([]byte(body))
	content := `<session-context version="1">` + "\n" + body + "\n\nDigest: sha256:" + hex.EncodeToString(digest[:]) + "\n</session-context>"
	m := reasonixMessage{Origin: "host", Content: content}
	if reasonixNativeWorkspace(m) != "/work/native" {
		t.Fatal("native host workspace lost")
	}
	m.Origin = "user"
	if reasonixNativeWorkspace(m) != "" {
		t.Fatal("human input became workspace metadata")
	}
	m.Origin = "host"
	m.Content = strings.Replace(content, "Host context", "damaged context", 1)
	if reasonixNativeWorkspace(m) != "" {
		t.Fatal("invalid context digest trusted")
	}
}

func TestReasonixNativeUnmeteredHistoryStillCounts(t *testing.T) {
	dir := copyReasonixStore(t, "linear-v4", "sessions-v4")
	path := filepath.Join(dir, "events.frames")
	original, _ := os.ReadFile(path)
	body := map[string]any{"messages": []map[string]any{
		{"id": "host", "role": "user", "content": `<session-context version="1">host snapshot</session-context>`},
		{"id": "user", "role": "user", "content": "real question", "createdAt": time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC).UnixMilli()},
		{"id": "answer", "role": "assistant", "content": "answer", "createdAt": time.Date(2026, 10, 8, 1, 0, 10, 0, time.UTC).UnixMilli()},
	}}
	batch := appendReasonixBatch(t, path, 6, "history/replace", body)
	os.WriteFile(path, append(append(original, batch...), appendReasonixBatch(t, path, 7, "session/title", map[string]any{"title": ""})...), 0600)
	st := StatsAt(7, time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	ov := st.Overview("reasonix", "", "", 10)
	if ov.Count != 1 {
		t.Fatalf("unmetered history count=%d", ov.Count)
	}
	for _, s := range st.Sessions {
		if s.Agent == "reasonix" {
			if s.Prompts != 1 || s.Replies != 1 || s.Input != 0 || !s.UsageIncomplete || s.Active != 10 {
				t.Fatalf("wrong known history: %+v", s)
			}
		}
	}
	s := reasonixOnly(t)
	if s.Title != "real question" {
		t.Fatalf("host snapshot became title: %q", s.Title)
	}
	Reset()
	if s := reasonixOnly(t); s.Title != "real question" {
		t.Fatal("cold title changed")
	}
}

func TestReasonixTitleSkipsInjectedBlocks(t *testing.T) {
	for _, tag := range strings.Fields("response-language reasoning-language memory-update background-jobs active-goal autoresearch-runtime hook-context capability-route interrupted-turn-recovery execution-policy") {
		t.Run(tag, func(t *testing.T) {
			block := "<" + tag + ` event="SessionStart">` + strings.Repeat("context ", 40) + "</" + tag + ">"
			if got := reasonixTitleText(reasonixMessage{Role: "user", Content: block + "\nreal question"}); got != "real question" {
				t.Fatalf("preview=%q", got)
			}
			if got := reasonixTitleText(reasonixMessage{Role: "user", Content: block}); got != "" {
				t.Fatal("pure host block has title")
			}
		})
	}
	summary := "<compaction-summary>earlier conversation</compaction-summary>"
	if got := reasonixTitleText(reasonixMessage{Role: "user", Content: summary}); got != "" {
		t.Fatal("summary became title")
	}
	if got := reasonixTitleText(reasonixMessage{Role: "user", Origin: "user", Content: summary}); got != summary {
		t.Fatal("explicit user text lost")
	}
	raw := "<hook-context>literal user text</hook-context>"
	if got := reasonixTitleText(reasonixMessage{Role: "user", RawContent: &raw}); got != raw {
		t.Fatal("raw user text lost")
	}
}
