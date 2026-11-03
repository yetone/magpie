package claudetui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// testTap is a tap in a folder of the test's, telling l, and a func that
// writes lines to it as tap.js does.
func testTap(t *testing.T, l *lines) (*tap, func(...string)) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	tp, env := newTap(func(p string) string { return p })
	if tp == nil {
		t.Fatal("no tap")
	}
	t.Cleanup(tp.drop)
	tp.out, tp.sid = l, "sid"
	out := strings.TrimPrefix(env[1], tapEnv+"=")
	return tp, func(ls ...string) {
		f, err := os.OpenFile(out, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, s := range ls {
			_, _ = f.WriteString(s + "\n")
		}
	}
}

// a reply's stream as tap.js writes it, stream n, message id
func tapReply(n int, id string) []string {
	var ls []string
	for _, e := range []string{`{"type":"message_start","message":{"id":"` + id + `"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`{"type":"message_stop"}`} {
		ls = append(ls, fmt.Sprintf(`{"s":%d,"e":%s}`, n, e))
	}
	return append(ls, fmt.Sprintf(`{"s":%d,"end":"done"}`, n))
}

func whole(id string) []byte {
	return []byte(`{"type":"assistant","requestId":"req_1","message":{"id":"` + id + `","model":"claude-haiku","content":[{"type":"text","text":"hi"}]}}`)
}

// kinds is what l was told: a stream's events by their type
func kinds(l *lines) []string {
	var k []string
	for _, m := range l.out {
		if e, ok := m["event"].(map[string]any); ok && m["type"] == "stream_event" {
			k = append(k, e["type"].(string))
			continue
		}
		k = append(k, m["type"].(string))
	}
	return k
}

var streamedTurn = []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop", "assistant", "result"}

// A streamed reply is told as its stream goes, its whole message after,
// and the turn's end after both.
func TestTapStreamsBeforeWhole(t *testing.T) {
	l := &lines{}
	tp, write := testTap(t, l)
	tr := newTranslator(l)
	tr.tap = tp
	write(`{"tap":"on"}`, `{"s":1,"open":true}`)
	write(tapReply(1, "msg_1")...)
	tr.row(whole("msg_1"))
	tr.row([]byte(`{"type":"system","subtype":"turn_duration"}`))
	if got := kinds(l); !slices.Equal(got, streamedTurn) {
		t.Fatalf("told %v, want %v", got, streamedTurn)
	}
	if l.out[0]["session_id"] != "sid" {
		t.Fatalf("a stream_event without its session: %v", l.out[0])
	}
}

// A message the transcript has before its stream begins waits for it: a
// stream is open, and its message_start comes after.
func TestTapMessageWaitsForItsStream(t *testing.T) {
	l := &lines{}
	tp, write := testTap(t, l)
	tr := newTranslator(l)
	tr.tap = tp
	write(`{"tap":"on"}`, `{"s":1,"open":true}`)
	go func() {
		time.Sleep(100 * time.Millisecond)
		write(tapReply(1, "msg_1")...)
	}()
	tr.row(whole("msg_1"))
	tr.row([]byte(`{"type":"system","subtype":"turn_duration"}`))
	// the message is told once its stream has begun, the rest of the
	// stream perhaps still to come (the bridge reads its whole rows by id),
	// and the turn's end after all of it
	got := kinds(l)
	if i := slices.Index(got, "assistant"); i < 1 || got[0] != "message_start" {
		t.Fatalf("told %v: the message before its stream began", got)
	}
	if rest := slices.DeleteFunc(slices.Clone(got), func(k string) bool { return k == "assistant" }); !slices.Equal(rest, slices.DeleteFunc(slices.Clone(streamedTurn), func(k string) bool { return k == "assistant" })) {
		t.Fatalf("told %v, want the stream then the result", got)
	}
}

// A message no stream is being read for (Claude Code asked again without
// one) is told at once; one whose stream doesn't begin in tapWait is told
// whole, and its stream, come late, says nothing.
func TestTapMessageWithoutStream(t *testing.T) {
	old := tapWait
	tapWait = 200 * time.Millisecond
	t.Cleanup(func() { tapWait = old })
	l := &lines{}
	tp, write := testTap(t, l)
	tr := newTranslator(l)
	tr.tap = tp
	write(`{"tap":"on"}`)
	start := time.Now()
	tr.row(whole("msg_9"))
	if took := time.Since(start); took > tapWait/2 {
		t.Fatalf("a message with no stream open waited %s", took)
	}
	write(`{"s":1,"open":true}`)
	start = time.Now()
	tr.row(whole("msg_1"))
	if took := time.Since(start); took < tapWait {
		t.Fatalf("a message whose stream is open waited only %s", took)
	}
	write(tapReply(1, "msg_1")...)
	tr.row([]byte(`{"type":"system","subtype":"turn_duration"}`))
	if got, want := kinds(l), []string{"assistant", "assistant", "result"}; !slices.Equal(got, want) {
		t.Fatalf("told %v, want %v", got, want)
	}
}

// A stream cut short ends with a message_stop and no message_delta, as
// Claude Code ends one in -p; one still going at the turn's end ends there.
func TestTapCutStreamStops(t *testing.T) {
	old := tapWait
	tapWait = 100 * time.Millisecond
	t.Cleanup(func() { tapWait = old })
	l := &lines{}
	tp, write := testTap(t, l)
	tr := newTranslator(l)
	tr.tap = tp
	r := tapReply(1, "msg_1")
	write(`{"tap":"on"}`, `{"s":1,"open":true}`, r[0], r[2], `{"s":1,"end":"cut"}`)
	r = tapReply(2, "msg_2")
	write(`{"s":2,"open":true}`, r[0], r[2])
	tr.row(whole("msg_2"))
	tr.row([]byte(`{"type":"system","subtype":"turn_duration"}`))
	want := []string{"message_start", "content_block_delta", "message_stop", "message_start", "content_block_delta", "assistant", "message_stop", "result"}
	if got := kinds(l); !slices.Equal(got, want) {
		t.Fatalf("told %v, want %v", got, want)
	}
	// what comes after its end says nothing
	write(r[3:]...)
	tp.read()
	if got := kinds(l); len(got) != len(want) {
		t.Fatalf("an ended stream told more: %v", got)
	}
}

// A Claude Code that isn't Bun, or doesn't load the tap, writes nothing
// to it: its messages are told whole, at once.
func TestTapNotLoaded(t *testing.T) {
	for _, first := range []string{"", `{"tap":"none"}`} {
		l := &lines{}
		tp, write := testTap(t, l)
		tr := newTranslator(l)
		tr.tap = tp
		if first != "" {
			write(first)
		}
		write(`{"s":1,"open":true}`)
		start := time.Now()
		tr.row(whole("msg_1"))
		if took := time.Since(start); took > tapWait/2 {
			t.Fatalf("%q: a message waited %s", first, took)
		}
		if got := kinds(l); !slices.Equal(got, []string{"assistant"}) {
			t.Fatalf("%q: told %v", first, got)
		}
	}
	// and with no tap at all
	l := &lines{}
	tr := newTranslator(l)
	tr.row(whole("msg_1"))
	tr.row([]byte(`{"type":"system","subtype":"turn_duration"}`))
	if got := kinds(l); !slices.Equal(got, []string{"assistant", "result"}) {
		t.Fatalf("told %v", got)
	}
}

// Claude Code is given the tap with the user's own BUN_OPTIONS before it,
// but not a Windows one's in WSL; a path BUN_OPTIONS can't carry gives no
// tap.
func TestTapEnv(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	t.Setenv("BUN_OPTIONS", "--smol")
	tp, env := newTap(func(p string) string { return p })
	if tp == nil {
		t.Fatal("no tap")
	}
	defer tp.drop()
	js := filepath.Join(tp.dir, "tap.js")
	if runtime.GOOS == "windows" {
		js = filepath.ToSlash(shortPath(js))
	}
	if want := []string{"BUN_OPTIONS=--smol --preload " + js, tapEnv + "=" + filepath.Join(tp.dir, "tap.jsonl")}; !slices.Equal(env, want) {
		t.Fatalf("env %q, want %q", env, want)
	}
	if b, _ := os.ReadFile(filepath.Join(tp.dir, "tap.js")); string(b) != string(tapJS) {
		t.Fatal("tap.js isn't written")
	}
	t.Setenv(WSLEnv, `{"args":["-d","Ubuntu"],"projects":"x"}`)
	wsl, env := newTap(func(p string) string { return "/mnt/c/" + filepath.Base(p) })
	defer wsl.drop()
	if want := []string{"BUN_OPTIONS=--preload /mnt/c/tap.js", tapEnv + "=/mnt/c/tap.jsonl"}; !slices.Equal(env, want) {
		t.Fatalf("WSL env %q, want %q", env, want)
	}
	// the bridge's guard file, where Claude Code reads it (#4804)
	t.Setenv(GuardEnv, filepath.Join(tmp, "guard.json"))
	guarded, env := newTap(func(p string) string { return "/mnt/c/" + filepath.Base(p) })
	defer guarded.drop()
	if want := []string{"BUN_OPTIONS=--preload /mnt/c/tap.js", tapEnv + "=/mnt/c/tap.jsonl", GuardEnv + "=/mnt/c/guard.json"}; !slices.Equal(env, want) {
		t.Fatalf("guarded env %q, want %q", env, want)
	}
	if runtime.GOOS != "windows" {
		spaced := filepath.Join(tmp, "a b")
		_ = os.Mkdir(spaced, 0o700)
		t.Setenv("TMPDIR", spaced)
		if tp, env := newTap(func(p string) string { return p }); tp != nil || env != nil {
			t.Fatalf("a path with a space gave %q", env)
		}
		if left, _ := os.ReadDir(spaced); len(left) > 0 {
			t.Fatalf("a tap not used is left: %v", left)
		}
	}
}

// A Claude Code that loads the tap streams its reply before writing its
// message: the shim tells the stream, then the message, then the result.
func TestShimStreamsThroughTap(t *testing.T) {
	out := runShim(t, []string{"FAKE_TAP=1"}, append([]string{fakePath(t)}, pArgs...), []string{"hello"}, 10)
	var got []string
	for _, m := range out[1:] {
		if e, ok := m["event"].(map[string]any); ok {
			got = append(got, e["type"].(string))
			continue
		}
		got = append(got, m["type"].(string))
	}
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop", "assistant", "result"}
	if !slices.Equal(got, want) {
		t.Fatalf("told %v, want %v", got, want)
	}
	if d := out[4]["event"].(map[string]any)["delta"].(map[string]any)["text"]; d != "hello" {
		t.Fatalf("the stream said %v", d)
	}
}
