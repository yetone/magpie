package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// TestSessionsListMakesOnlyTheGatewayRowsItShows: while a session grew,
// each Sessions poll copied every gateway session the natives don't hold
// three times and made each a row, ~60 MB of a poll's ~150 MB over ~10k
// of them, though the list shows its limit at most. The rows are still
// the ones the whole merge gave, in its order, a hidden one left out and
// a native's id never doubled; and a grown session file is read.
func TestSessionsListMakesOnlyTheGatewayRowsItShows(t *testing.T) {
	h := sandboxHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, ".claude"))
	proj := filepath.Join(h, ".claude", "projects", "-work")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	line := func(id string, at time.Time, text string) string {
		ts := at.UTC().Format("2006-01-02T15:04:05.000Z")
		return `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"` + text + `"},"uuid":"u` + text + `","timestamp":"` + ts + `","cwd":"/work","sessionId":"` + id + `"}` + "\n" +
			`{"parentUuid":"u` + text + `","isSidechain":false,"message":{"model":"claude-sonnet-5","id":"msg_` + id + text + `","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":2}},"requestId":"req_` + text + `","type":"assistant","uuid":"a` + text + `","timestamp":"` + ts + `","cwd":"/work","sessionId":"` + id + `"}` + "\n"
	}
	for i, mins := range []int{1, 7, 30} {
		id := fmt.Sprintf("%08d-1111-2222-3333-444444444444", i)
		at := now.Add(-time.Duration(mins) * time.Minute)
		p := filepath.Join(proj, id+".jsonl")
		if err := os.WriteFile(p, []byte(line(id, at, "hi")), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, at, at)
	}
	const gateway = 6000
	if err := os.MkdirAll(filepath.Dir(usage.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(usage.Path())
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for i := range gateway {
		// pairs at one moment, so a tie's order is the merge's to keep
		at := now.Add(-time.Duration(i/2) * time.Minute)
		enc.Encode(usage.Record{Time: at, Agent: "opencode", Provider: "fake", Model: "m", Session: fmt.Sprint("g", i), Input: 10, Status: 200})
	}
	// a native's own id, which is the native's row only
	enc.Encode(usage.Record{Time: now, Agent: "claude", Provider: "fake", Model: "m", Session: "00000001-1111-2222-3333-444444444444", Input: 10, Status: 200})
	f.Close()
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	if _, err := sessions.DeleteGateway("opencode", "g3", "", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	ask := func(path string) ([]sessions.Session, uint64) {
		t.Helper()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		runtime.ReadMemStats(&after)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var out struct{ Sessions []sessions.Session }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Sessions, after.TotalAlloc - before.TotalAlloc
	}
	// what the whole merge gives, as the list made it before
	want := func(n int) []sessions.Session {
		list := sessions.List(sessions.All)
		keys := map[string]bool{}
		for _, s := range list {
			for _, k := range sessions.GatewayKeys(s) {
				keys[k] = true
			}
		}
		var ext []sessions.ExternalSession
		title := sessions.GatewayTitler()
		for _, g := range listedGateway(usage.GatewaySessions(time.Time{}, keys)) {
			ext = append(ext, gatewayListed(g, title))
		}
		all := sessions.MergeExternal(list, ext)
		all = all[:min(n, len(all))]
		// as the answer has them
		b, _ := json.Marshal(all)
		var out []sessions.Session
		json.Unmarshal(b, &out)
		for i := range out {
			out[i].Path = tilde(out[i].Path)
		}
		return out
	}
	same := func(what string, got, want []sessions.Session) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %d rows, want %d", what, len(got), len(want))
		}
		for i := range want {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Fatalf("%s: row %d\n got %+v\nwant %+v", what, i, got[i], want[i])
			}
		}
	}
	for _, n := range []int{5, 40, gateway + 10} {
		got, _ := ask(fmt.Sprint("/api/sessions?limit=", n))
		same(fmt.Sprint("limit ", n), got, want(n))
	}
	got, _ := ask("/api/sessions")
	same("no limit", got, want(sessions.All))
	for _, s := range got {
		if s.Gateway && (s.ID == "g3" || s.ID == "00000001-1111-2222-3333-444444444444") {
			t.Fatalf("listed %s|%s from the gateway", s.Agent, s.ID)
		}
	}

	// a session grows, as one being worked in does
	id := "00000002-1111-2222-3333-444444444444"
	p := filepath.Join(proj, id+".jsonl")
	grown := time.Now().Add(-time.Second).Truncate(time.Second)
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString(line(id, grown, "more"))
	fh.Close()
	os.Chtimes(p, grown, grown)
	got, alloc := ask("/api/sessions?limit=20")
	same("after a session grew", got, want(20))
	if got[0].ID != id || !got[0].Last.Equal(grown) {
		t.Fatalf("the grown session isn't first at %v: %+v", grown, got[0])
	}
	// a row of each gateway session is ~3 MB here, and it was ~20 MB;
	// what doesn't grow with them is ~1 MB
	rows := uint64(gateway) * uint64(unsafe.Sizeof(sessions.Session{}))
	if alloc > rows {
		t.Fatalf("a poll of 20 rows after a session grew allocated %d bytes; %d gateway sessions' rows are %d", alloc, gateway, rows)
	}
}
