package sessions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// grower reads one session file as refresh does, each parse on from the
// one kept before, and checks every such parse against a whole read of the
// same file: a summary read on from where it was is the one a whole read
// makes. start is where the last parse began reading the file.
type grower struct {
	t     *testing.T
	files func() []file
	path  string
	old   *state
	start int64
}

func (g *grower) file() file {
	g.t.Helper()
	for _, f := range g.files() {
		if f.path == g.path {
			return f
		}
	}
	g.t.Fatalf("no file %s", g.path)
	return file{}
}

// read parses the file on from the last parse and says where it started.
func (g *grower) read() int64 {
	g.t.Helper()
	f := g.file()
	g.start = -1
	scanHook = func(p string, off int64) {
		if p == g.path && g.start < 0 {
			g.start = off
		}
	}
	defer func() { scanHook = nil }()
	s := parse(f, g.old)
	scanHook = nil
	whole := parse(f, nil)
	got, _ := json.Marshal(s)
	want, _ := json.Marshal(whole)
	if !bytes.Equal(got, want) {
		g.t.Fatalf("read on from %d:\n%s\nread whole:\n%s", g.start, got, want)
	}
	g.old = s
	return g.start
}

func (g *grower) size() int64 {
	g.t.Helper()
	fi, err := os.Stat(g.path)
	if err != nil {
		g.t.Fatal(err)
	}
	return fi.Size()
}

func (g *grower) append(s string) {
	g.t.Helper()
	f, err := os.OpenFile(g.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		g.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		g.t.Fatal(err)
	}
}

// grows runs the cases every reader of an appended file meets: a line
// appended is the only thing read; half a line waits for the rest and is
// counted once; a file cut short, or replaced by one as long, is read whole
// again. line is a whole line the file may take, with its newline.
func checkGrows(t *testing.T, g *grower, line string) {
	t.Helper()
	orig, err := os.ReadFile(g.path)
	if err != nil {
		t.Fatal(err)
	}
	if at := g.read(); at != 0 {
		t.Fatalf("first read started at %d", at)
	}
	// a line appended: only it is read
	was := g.size()
	g.append(line)
	if at := g.read(); at != was {
		t.Fatalf("after a line appended to %d bytes, read from %d", was, at)
	}
	// half a line, still being written: it waits for the rest
	was = g.size()
	half := len(line) / 2
	g.append(line[:half])
	if at := g.read(); at != was {
		t.Fatalf("with half a line at %d, read from %d", was, at)
	}
	g.append(line[half:])
	if at := g.read(); at != was {
		t.Fatalf("the half line finished at %d, read from %d", was, at)
	}
	// cut short: read whole again
	if err := os.WriteFile(g.path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if at := g.read(); at != 0 {
		t.Fatalf("cut short, read from %d", at)
	}
	// replaced by another as long, its first line changed: read whole
	g.append(line)
	g.read()
	b, _ := os.ReadFile(g.path)
	other := append([]byte(nil), b...)
	other[len(orig)-2] ^= 1 // a line it had, changed in place
	if bytes.Equal(other, b) {
		t.Fatal("not changed")
	}
	if err := os.WriteFile(g.path, other, 0o644); err != nil {
		t.Fatal(err)
	}
	if at := g.read(); at != 0 {
		t.Fatalf("rewritten in place, read from %d", at)
	}
}

// Grok Build appends to a session's updates.jsonl: a turn that ended is
// read alone, not the session again.
func TestGrokReadsOnlyWhatWasAppended(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	dir := filepath.Join(home, ".grok", "sessions", "%2Fwork%2Fgrok", grokMain)
	g := &grower{t: t, files: grokFiles, path: filepath.Join(dir, "updates.jsonl")}
	turn := `{"timestamp":1790496140,"method":"_x.ai/session/update","params":{"sessionId":"` + grokMain + `","update":{"sessionUpdate":"turn_completed","usage":{"inputTokens":200,"outputTokens":6,"cachedReadTokens":100,"cacheCreationTokens":0,"modelUsage":{"grok-4.7-build":{"inputTokens":200,"outputTokens":6,"cachedReadTokens":100,"cacheCreationTokens":0}}}}}}` + "\n"
	checkGrows(t, g, turn)

	// its summary rewritten, a title given: nothing of the updates read
	g.read()
	summary := filepath.Join(dir, "summary.json")
	b, _ := os.ReadFile(summary)
	os.WriteFile(summary, bytes.Replace(b, []byte(`"generated_title": "Parser port"`), []byte(`"generated_title": "Port of the parser"`), 1), 0o644)
	if at, end := g.read(), g.size(); at != end || g.old.more().Named != "Port of the parser" {
		t.Fatalf("summary changed: read from %d of %d, named %q", at, end, g.old.more().Named)
	}
	// the model its turns are counted under changed: read whole
	b, _ = os.ReadFile(summary)
	os.WriteFile(summary, bytes.Replace(b, []byte(`"current_model_id": "grok-4.7"`), []byte(`"current_model_id": "grok-5"`), 1), 0o644)
	if at := g.read(); at != 0 {
		t.Fatalf("model changed: read from %d", at)
	}
}

// A first prompt still coming in chunks when a read ends goes on in the
// next one.
func TestGrokPromptAcrossReads(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	dir := filepath.Join(home, ".grok", "sessions", "%2Fwork%2Fgrok", "0190aaaa-0000-7000-8000-0000000000ee")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"info":{"id":"0190aaaa-0000-7000-8000-0000000000ee","cwd":"/w"},"created_at":"2026-09-27T08:00:00Z"}`), 0o644)
	chunk := func(ts int, text string) string {
		return fmt.Sprintf(`{"timestamp":%d,"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":%q}}}}`+"\n", ts, text)
	}
	p := filepath.Join(dir, "updates.jsonl")
	os.WriteFile(p, []byte(chunk(1790496001, "Port the ")), 0o644)
	g := &grower{t: t, files: grokFiles, path: p}
	g.read()
	g.append(chunk(1790496002, "parser"))
	g.read()
	g.append(`{"timestamp":1790496003,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"ok"}}}}` + "\n")
	g.read()
	g.append(chunk(1790496004, "and more"))
	if g.read(); g.old.Title != "Port the parser" {
		t.Fatalf("title %q", g.old.Title)
	}
}

// Droid appends to a transcript and keeps a running total in its
// settings: a message appended is read alone, and the total, counted on
// the day of the last message, moves with it instead of being counted twice.
func TestDroidReadsOnlyWhatWasAppended(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	putDroid(t, home)
	dir := filepath.Join(home, ".factory", "sessions", "-work-droid")
	g := &grower{t: t, files: droidFiles, path: filepath.Join(dir, droidMain+".jsonl")}
	msg := `{"type":"message","id":"m5","timestamp":"2026-09-27T08:03:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Fixed"},{"type":"tool_use","id":"t2","name":"Edit","input":{}}]}}` + "\n"
	checkGrows(t, g, msg)

	// a day later, the total grown: counted once, on the new day
	g.read()
	was := g.size()
	g.append(`{"type":"message","id":"m6","timestamp":"2026-09-28T09:00:00.000Z","message":{"role":"user","content":"and the logout one"}}` + "\n")
	os.WriteFile(filepath.Join(dir, droidMain+".settings.json"), []byte(`{"model":"claude-opus-4-5","tokenUsage":{"inputTokens":300,"outputTokens":80,"cacheCreationTokens":10,"cacheReadTokens":2000}}`), 0o644)
	if at := g.read(); at != was {
		t.Fatalf("read from %d, not %d", at, was)
	}
	if d := g.old.Days.of("2026-09-27"); d == nil || len(d.Models) != 0 {
		t.Fatalf("the total is still on the day before: %+v", d)
	}
	if m := g.old.Days.of("2026-09-28").Models.of("claude-opus-4-5"); m.Input != 300 {
		t.Fatalf("the total on its day: %+v", g.old.Days.of("2026-09-28"))
	}
	// only the settings changed: nothing of the transcript read
	os.WriteFile(filepath.Join(dir, droidMain+".settings.json"), []byte(`{"model":"claude-opus-4-5","tokenUsage":{"inputTokens":400,"outputTokens":90}}`), 0o644)
	if at, end := g.read(), g.size(); at != end {
		t.Fatalf("settings changed: read from %d of %d", at, end)
	}
}

// A fork's copied lines that end in a later read are still passed over.
func TestDroidForkAcrossReads(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	putDroid(t, home)
	p := filepath.Join(home, ".factory", "sessions", "-work-droid", droidFork+".jsonl")
	b, _ := os.ReadFile(p)
	lines := bytes.SplitAfter(b, []byte("\n"))
	os.WriteFile(p, bytes.Join(lines[:3], nil), 0o644)
	g := &grower{t: t, files: droidFiles, path: p}
	g.read()
	for _, l := range lines[3:] {
		g.append(string(l))
		g.read()
	}
	if g.old.Title != "Try another way" {
		t.Fatalf("title %q", g.old.Title)
	}
}

// Kimi Code appends to an agent's wire.jsonl: a record appended is read
// alone, and a state.json rewritten reads nothing of the wire.
func TestKimiReadsOnlyWhatWasAppended(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "home", ".kimi-code")
	kimiSessionsIn(t, dir)
	sessions := filepath.Join(dir, "sessions")
	slug := "wd_v14-cancel_000000000003"
	g := &grower{t: t, files: kimiFiles, path: filepath.Join(sessions, slug, kimiCancel, "agents", "main", "wire.jsonl")}
	rec := `{"type":"usage.record","model":"kimi-code/k3","usage":{"inputOther":40,"output":5,"inputCacheRead":900,"inputCacheCreation":0},"usageScope":"turn","time":1784687340000}` + "\n"
	checkGrows(t, g, rec)

	g.read()
	kimiSetState(t, sessions, slug, kimiCancel, map[string]any{"title": "Cancelled twice", "isCustomTitle": true})
	if at, end := g.read(), g.size(); at != end || g.old.more().Custom != "Cancelled twice" {
		t.Fatalf("state.json changed: read from %d of %d, custom %q", at, end, g.old.more().Custom)
	}
	// the session now says it was forked: what it counted is read again
	kimiSetState(t, sessions, slug, kimiCancel, map[string]any{"forkedFrom": kimiJune})
	if at := g.read(); at != 0 {
		t.Fatalf("made a fork: read from %d", at)
	}
}

// A fork's marker that comes in a later read still ends its copy of its
// source: what was counted before it is taken back.
func TestKimiForkMarkerAcrossReads(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "home", ".kimi-code")
	kimiSessionsIn(t, dir)
	sessions := filepath.Join(dir, "sessions")
	slug := "wd_v14-cancel_000000000003"
	src, err := os.ReadFile(filepath.Join(sessions, slug, kimiCancel, "agents", "main", "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	id := "session_f0000000-0000-4000-8000-0000000000aa"
	w := filepath.Join(sessions, slug, id, "agents", "main", "wire.jsonl")
	os.MkdirAll(filepath.Dir(w), 0o755)
	os.WriteFile(w, src, 0o644)
	kimiSetState(t, sessions, slug, id, map[string]any{"cwd": "/w", "createdAt": int64(1784687000000), "title": "Again", "forkedFrom": kimiCancel})
	g := &grower{t: t, files: kimiFiles, path: w}
	g.read()
	g.append(`{"type":"forked","agentId":"main","time":1791700000000}` + "\n")
	g.read()
	g.append(`{"type":"usage.record","model":"kimi-code/k3","usage":{"inputOther":7,"output":3,"inputCacheRead":0,"inputCacheCreation":0},"usageScope":"turn","time":1791700005000}` + "\n")
	g.read()
	if g.old.Models.of("kimi-code/k3") != (Tokens{Input: 7, Output: 3}) || len(g.old.Models) != 1 {
		t.Fatalf("the fork's own: %+v", g.old.Models)
	}
}

const dshReply = `{"type":"assistant/message","seq":6,"time":1790503290000,"data":{"message":{"source":{"model":"deepseek-v4-flash"}},"usage":{"inputTokens":1,"outputTokens":1,"cacheReadTokens":0}}}` + "\n"

// dsh appends to a session file a line at a time: a reply is read alone.
func TestDshReadsOnlyWhatWasAppended(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	p := filepath.Join(home, ".dsh", "sessions", "work-dsh-3f2a", "dsh-a", "session.v3.jsonl")
	checkGrows(t, &grower{t: t, files: dshFiles, path: p}, dshReply)
}

// Packed, dsh appends a frame of lines at a time: a frame appended is read
// alone, from where it starts; a frame not yet written whole waits, and so
// does a line a frame ends inside of, read on from the frame it starts in.
func TestDshPackedReadsOnlyWhatWasAppended(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	plain := filepath.Join(home, ".dsh", "sessions", "work-dsh-3f2a", "dsh-a", "session.v3.jsonl")
	b, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(plain)
	enc, _ := zstd.NewWriter(nil)
	defer enc.Close()
	frame := func(s string) []byte { return enc.EncodeAll([]byte(s), nil) }
	p := plain + ".zstd"
	half := bytes.IndexByte(b[len(b)/2:], '\n') + len(b)/2 + 1
	orig := append(frame(string(b[:half])), frame(string(b[half:]))...)
	os.WriteFile(p, orig, 0o644)
	g := &grower{t: t, files: dshFiles, path: p}
	if at := g.read(); at != 0 {
		t.Fatalf("first read started at %d", at)
	}
	reply := func(seq int) string {
		return fmt.Sprintf(`{"type":"assistant/message","seq":%d,"time":%d,"data":{"message":{"source":{"model":"deepseek-v4-flash"}},"usage":{"inputTokens":%d,"outputTokens":1,"cacheReadTokens":0}}}`+"\n", seq, 1790503290000+int64(seq)*1000, seq)
	}
	// a frame appended: only it is read
	was := g.size()
	g.append(string(frame(reply(6))))
	if at := g.read(); at != was {
		t.Fatalf("after a frame appended to %d bytes, read from %d", was, at)
	}
	// half a frame, still being written: it waits for the rest
	was = g.size()
	f := frame(reply(7))
	g.append(string(f[:len(f)/2]))
	if at := g.read(); at != was {
		t.Fatalf("with half a frame at %d, read from %d", was, at)
	}
	g.append(string(f[len(f)/2:]))
	if at := g.read(); at != was {
		t.Fatalf("the half frame finished at %d, read from %d", was, at)
	}
	// a frame that ends inside a line: the line goes on in the next
	// frame, and the next read starts at the frame it starts in, past
	// what of that frame it counted
	was = g.size()
	r9 := reply(9)
	g.append(string(frame(reply(8) + r9[:20])))
	if at := g.read(); at != was {
		t.Fatalf("a frame ending in a line at %d, read from %d", was, at)
	}
	g.append(string(frame(r9[20:])))
	if at := g.read(); at != was {
		t.Fatalf("the line finished in the next frame, read from %d, not its frame's %d", at, was)
	}
	if m := g.old.Models.of("deepseek-v4-flash"); m.Input != 10+6+7+8+9 {
		t.Fatalf("flash, each reply once: %+v", m)
	}
	// cut short: read whole again
	os.WriteFile(p, orig, 0o644)
	if at := g.read(); at != 0 {
		t.Fatalf("cut short, read from %d", at)
	}
	// replaced by one longer, its first frame changed: read whole
	g.append(string(frame(reply(6))))
	g.read()
	other := append(frame(string(bytes.Replace(b[:half], []byte("house rules"), []byte("house_rules"), 1))), frame(string(b[half:]))...)
	for len(other) <= int(g.size()) {
		other = append(other, frame(reply(6))...)
	}
	os.WriteFile(p, other, 0o644)
	if at := g.read(); at != 0 {
		t.Fatalf("replaced, read from %d", at)
	}
}

// A fork's end-seed that comes in a later read still ends what it was
// seeded with: what was counted before it is its source's.
func TestDshEndSeedAcrossReads(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	p := filepath.Join(home, ".dsh", "sessions", "work-dsh-3f2a", "dsh-c", "session.v3.jsonl")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	cut := bytes.Index(b, []byte(`{"type":"session/end-seed"`))
	rest := bytes.IndexByte(b[cut:], '\n') + cut + 1
	os.WriteFile(p, b[:cut], 0o644)
	g := &grower{t: t, files: dshFiles, path: p}
	g.read()
	g.append(string(b[cut:rest]))
	g.read()
	g.append(string(b[rest:]))
	g.read()
	if m := g.old.Models.of("deepseek-v4-pro"); m.Input != 50 || len(g.old.Models) != 1 || g.old.Title != "Tidy the build script" {
		t.Fatalf("the fork's own: %+v, title %q", g.old.Models, g.old.Title)
	}
}

// Cline rewrites a session's manifest on its own (a title given, the
// session touched), and its size and time are the session file's: when only
// it changed, the messages are not read again. When they changed, or the
// manifest moved the start or the fork their counts were made under, they
// are.
func TestClineReadsMessagesOnlyWhenTheyChange(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	dir := filepath.Join(home, ".cline", "data", "sessions", clineMain)
	path := filepath.Join(dir, clineMain+".messages.json")
	manifest := filepath.Join(dir, clineMain+".json")
	reads := 0
	clineHook = func(p string) {
		if p == path {
			reads++
		}
	}
	t.Cleanup(func() { clineHook = nil })
	at := time.Now()
	touch := func(p string) {
		at = at.Add(time.Minute)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	edit := func(p, from, to string) {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(b, []byte(from)) {
			t.Fatalf("no %q in %s", from, p)
		}
		os.WriteFile(p, bytes.Replace(b, []byte(from), []byte(to), 1), 0o644)
		touch(p)
	}
	var old *state
	// read reads the session on from old and says whether it read its
	// messages, checking it against a whole read
	read := func() bool {
		t.Helper()
		var f file
		for _, c := range clineFiles() {
			if c.path == path {
				f = c
			}
		}
		was := reads
		s := parse(f, old)
		read := reads > was
		whole := parse(f, nil)
		got, _ := json.Marshal(s)
		want, _ := json.Marshal(whole)
		if !bytes.Equal(got, want) {
			t.Fatalf("read on:\n%s\nread whole:\n%s", got, want)
		}
		old = s
		return read
	}
	// no title of its own, so the first prompt names it
	edit(manifest, `"title": "Importer speed-up"`, `"status": "x"`)
	edit(manifest, `"prompt": "<user_input mode=\"act\">Speed up   the\nimporter</user_input>",`, ``)
	if !read() {
		t.Fatal("first read: messages not read")
	}
	if old.Title != "Speed up the importer" {
		t.Fatalf("title %q", old.Title)
	}
	// the manifest touched and given a title: the messages are not read
	edit(manifest, `"status": "completed"`, `"status": "running"`)
	if read() {
		t.Fatal("manifest touched: messages read again")
	}
	edit(manifest, `"status": "x"`, `"title": "Faster importer"`)
	if read() || old.more().Named != "Faster importer" || old.Title != "Speed up the importer" {
		t.Fatalf("manifest titled: named %q, title %q", old.more().Named, old.Title)
	}
	// a message more: read again
	edit(path, `"content": "done",`, `"content": "done, all of it",`)
	if !read() {
		t.Fatal("messages changed: not read")
	}
	// the same size, a later time: read again
	touch(path)
	if !read() {
		t.Fatal("messages touched: not read")
	}
	// made a fork after its first reply: what came before is its source's
	edit(manifest, `"metadata": {`, `"metadata": {"fork": {"forkedAt": "2026-09-27T10:01:00.000Z"},`)
	if !read() || old.Models.of("claude-opus-5-5") != (Tokens{}) {
		t.Fatalf("forked: %+v", old.Models)
	}
	// started at another time: read again
	edit(manifest, `"started_at": "2026-09-27T10:00:00.000Z"`, `"started_at": "2026-09-27T10:05:00.000Z"`)
	if !read() {
		t.Fatal("start moved: not read")
	}
}
