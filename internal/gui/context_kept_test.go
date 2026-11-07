package gui

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
)

// keptMux is the Context tab's routes, the gateway's trace left out.
func keptMux(t *testing.T) *http.ServeMux {
	prev := served.Swap(nil)
	t.Cleanup(func() { served.Store(prev) })
	mux := http.NewServeMux()
	contextRoutesAPI(mux)
	tuneRoutes(mux)
	return mux
}

func getJSON(t *testing.T, mux *http.ServeMux, url string, v any) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", url, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatal(err)
	}
}

// The Context tab asks again at every refresh of the Usage page (5s by
// default), and each ask read the week's routes from the history again,
// whole: a 1-core server with 962 MB went down with the tab open
// (Yayoi_no_yume on X). A day whose files are as they were is read once;
// a day that grew is read again, the others not; and each session's latest
// prompt is still there whole, its items too, when its latest request was
// on a day before.
func TestContextReadsADayOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := keptMux(t)
	ctxKept.Lock()
	ctxKept.days, ctxKept.key = map[string]ctxDay{}, ""
	ctxKept.Unlock()
	reads := map[string]int{}
	readingDay = func(day string) { reads[day]++ }
	t.Cleanup(func() { readingDay = nil })

	dir := gateway.HistoryDir()
	os.MkdirAll(dir, 0o700)
	now := time.Now()
	today, yesterday := now.Format("2006-01-02"), now.AddDate(0, 0, -1).Format("2006-01-02")
	at := func(day time.Time, id int64) gateway.Route {
		r := ctxRoute(id, "claude", "s"+fmt.Sprint(id%2), 30000, int(id)*1000, 200000, 20000)
		r.Time = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local).Add(time.Duration(id) * time.Minute)
		return r
	}
	lines := func(rs ...gateway.Route) []byte {
		var b bytes.Buffer
		for _, r := range rs {
			j, _ := json.Marshal(r)
			b.Write(append(j, '\n'))
		}
		return b.Bytes()
	}
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	z.Write(lines(at(now.AddDate(0, 0, -1), 1), at(now.AddDate(0, 0, -1), 2), at(now.AddDate(0, 0, -1), 3)))
	z.Close()
	os.WriteFile(filepath.Join(dir, yesterday+".jsonl.gz"), gz.Bytes(), 0o600)
	todayFile := filepath.Join(dir, today+".jsonl")
	os.WriteFile(todayFile, lines(at(now, 4)), 0o600)

	var first, again ctxJSON
	getJSON(t, mux, "/api/context?days=2", &first)
	if reads[today] != 1 || reads[yesterday] != 1 {
		t.Fatalf("first read %v", reads)
	}
	if len(first.Sessions) != 2 || first.Agents[0].Requests != 4 {
		t.Fatalf("first: %+v", first)
	}
	for _, s := range first.Sessions {
		if s.Latest == nil || len(s.Latest.Parts) != 3 || len(s.Latest.Parts[1].Items) != 1 {
			t.Fatalf("session %s's latest prompt: %+v", s.Key, s.Latest)
		}
	}
	for range 3 {
		getJSON(t, mux, "/api/context?days=2", &again)
	}
	if reads[today] != 1 || reads[yesterday] != 1 {
		t.Fatalf("read again with nothing changed: %v", reads)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("a refresh's answer changed:\n%+v\n%+v", first, again)
	}
	// today grows: only today is read again
	f, _ := os.OpenFile(todayFile, os.O_APPEND|os.O_WRONLY, 0)
	f.Write(lines(at(now, 5)))
	f.Close()
	getJSON(t, mux, "/api/context?days=2", &again)
	if reads[today] != 2 || reads[yesterday] != 1 {
		t.Fatalf("after today grew: %v", reads)
	}
	if again.Agents[0].Requests != 5 {
		t.Fatalf("after today grew: %d requests", again.Agents[0].Requests)
	}
	// the answer is the one worked out of every route at once
	_, rs1, _ := gateway.History(yesterday)
	_, rs2, _ := gateway.History(today)
	want := contextOf(append(rs1, rs2...), 2)
	wj, _ := json.Marshal(want)
	gj, _ := json.Marshal(again)
	if string(wj) != string(gj) {
		t.Fatalf("kept answer differs from the whole one:\n%s\n%s", gj, wj)
	}
}

// The tune advice beside it read every Claude Code and Codex file of the
// range whole at each ask: 300k calls were ~500 MB at once. A file is read
// again only when it changed, and the calls are the ones sessions.Calls
// has, a message a resumed session copied counted once.
func TestTuneReadsAFileOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cc := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cc)
	t.Setenv("CODEX_HOME", t.TempDir())
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	mux := keptMux(t)
	tuneKept.Lock()
	tuneKept.files = map[string]tuneFile{}
	tuneKept.Unlock()
	tuneForget()
	t.Cleanup(tuneForget)
	reads := map[string]int{}
	tuneRead = func(p string) { reads[filepath.Base(p)]++ }
	t.Cleanup(func() { tuneRead = nil })

	proj := filepath.Join(cc, "projects", "-work-app")
	os.MkdirAll(proj, 0o755)
	t0 := time.Now().Add(-2 * time.Hour)
	msg := func(sess string, id, i int) string {
		ts := t0.Add(time.Duration(i) * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
		return fmt.Sprintf(`{"type":"assistant","uuid":"u-%d","timestamp":"%s","message":{"id":"msg_%d","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"cache_creation_input_tokens":1000,"cache_read_input_tokens":%d,"output_tokens":200}},"requestId":"req_%d","sessionId":"%s","cwd":"/work/app","entrypoint":"cli"}`+"\n",
			id, ts, id, 20000+i*500, id, sess)
	}
	var a, b strings.Builder
	for i := range 40 {
		a.WriteString(msg("aaaaaaaa-0000-0000-0000-000000000000", i, i))
	}
	// resumed: the first file's last two messages copied, then its own
	b.WriteString(msg("bbbbbbbb-0000-0000-0000-000000000000", 38, 38))
	b.WriteString(msg("bbbbbbbb-0000-0000-0000-000000000000", 39, 39))
	for i := 40; i < 70; i++ {
		b.WriteString(msg("bbbbbbbb-0000-0000-0000-000000000000", i, i))
	}
	pa := filepath.Join(proj, "aaaaaaaa-0000-0000-0000-000000000000.jsonl")
	pb := filepath.Join(proj, "bbbbbbbb-0000-0000-0000-000000000000.jsonl")
	os.WriteFile(pa, []byte(a.String()), 0o644)
	os.Chtimes(pa, t0.Add(40*time.Minute), t0.Add(40*time.Minute))
	os.WriteFile(pb, []byte(b.String()), 0o644)

	var first, again tuneState
	getJSON(t, mux, "/api/tune?days=7", &first)
	if reads[filepath.Base(pa)] != 1 || reads[filepath.Base(pb)] != 1 {
		t.Fatalf("first read %v", reads)
	}
	since := time.Now().AddDate(0, 0, -7)
	byFile := tuneCallsSince(since)
	if n := len(byFile["claude"]); n != 70 {
		t.Fatalf("%d calls, want 70 (the two copied once)", n)
	}
	whole := tuneOf(sessions.Calls(since), 7, "")
	if len(first.Agents) != 1 || first.Agents[0].Calls != whole.Agents[0].Calls {
		t.Fatalf("calls %+v, sessions.Calls' %+v", first.Agents, whole.Agents)
	}
	for range 3 {
		getJSON(t, mux, "/api/tune?days=7", &again)
	}
	if reads[filepath.Base(pa)] != 1 || reads[filepath.Base(pb)] != 1 {
		t.Fatalf("read again with nothing changed: %v", reads)
	}
	f, _ := os.OpenFile(pb, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(msg("bbbbbbbb-0000-0000-0000-000000000000", 70, 70))
	f.Close()
	// within ctxAgain the advice given stands, nothing read
	getJSON(t, mux, "/api/tune?days=7", &again)
	if reads[filepath.Base(pb)] != 1 || again.Agents[0].Calls != 70 {
		t.Fatalf("read again within a minute: %v, %d calls", reads, again.Agents[0].Calls)
	}
	tuneForget()
	getJSON(t, mux, "/api/tune?days=7", &again)
	if reads[filepath.Base(pa)] != 1 || reads[filepath.Base(pb)] != 2 {
		t.Fatalf("after one grew: %v", reads)
	}
	if again.Agents[0].Calls != 71 {
		t.Fatalf("after one grew: %d calls", again.Agents[0].Calls)
	}
}

// An answer that left a call out of every session, its reply not read yet
// (ctxJSON.held), stands ctxWhileUnmatched, so the read after dsh writes the
// reply down names it; any other answer stands ctxAgain, as before #5542: a
// held set for a call no reply will ever name would re-read every session at
// each 5 s refresh.
func TestContextHeldAnswerStandsOnlyWhileAReplyIsAwaited(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := keptMux(t)
	ctxKept.Lock()
	ctxKept.days, ctxKept.key = map[string]ctxDay{}, ""
	ctxKept.Unlock()
	dir := gateway.HistoryDir()
	os.MkdirAll(dir, 0o700)
	r := ctxRoute(1, "claude", "s1", 30000, 1000, 200000, 0)
	r.Time = time.Now()
	j, _ := json.Marshal(r)
	os.WriteFile(filepath.Join(dir, time.Now().Format("2006-01-02")+".jsonl"), append(j, '\n'), 0o600)

	var out ctxJSON
	getJSON(t, mux, "/api/context?days=1", &out)
	if out.Days != 1 {
		t.Fatalf("first answer: %+v", out)
	}
	ask := func(held bool, age time.Duration) (kept bool) {
		ctxKept.Lock()
		ctxKept.out.Days, ctxKept.out.held, ctxKept.at = -1, held, time.Now().Add(-age)
		ctxKept.Unlock()
		var got ctxJSON
		getJSON(t, mux, "/api/context?days=1", &got)
		return got.Days == -1
	}
	if !ask(false, 6*time.Second) {
		t.Error("an answer that held no call out was worked out again after 6s, not ctxAgain")
	}
	if ask(true, 6*time.Second) {
		t.Error("an answer that held a call out stood past ctxWhileUnmatched")
	}
	if !ask(true, time.Second) {
		t.Error("an answer that held a call out was worked out again within ctxWhileUnmatched")
	}
}
