package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCallsReuseSavedIndexAndContinue(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "-p", "sess1.jsonl")
	writeLines(t, path, claudeMsg("m1", "claude-sonnet-5", 1, 2, 3, 4, 1))
	original := Calls(time.Time{})
	old := callCache[path]
	Saved()
	Reset()
	restoredCalls := Calls(time.Time{})
	restored := callCache[path]
	if !reflect.DeepEqual(original, restoredCalls) || restored.Off != old.Off {
		t.Fatal("per-call metadata/offsets did not survive restart")
	}
	if got := Calls(time.Time{}); !reflect.DeepEqual(got, original) || callCache[path] != restored {
		t.Fatal("unchanged file was parsed again after restart")
	}
	appendText(t, path, claudeMsg("m2", "claude-sonnet-5", 5, 6, 7, 8, 2)+"\n")
	if got := Calls(time.Time{}); len(got) != 2 || got[0].Input != 5 {
		t.Fatalf("continued parse %+v", got)
	}
	if len(restored.Calls) != 1 {
		t.Fatal("published snapshot mutated")
	}
	b, err := os.ReadFile(callCachePath(path))
	if err != nil || len(b) == 0 {
		t.Fatal("no saved request shard", err)
	}

}

func TestCodexSessionIdentitySurvivesIndexRestart(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
	writeLines(t, path,
		`{"timestamp":"`+stamp(0)+`","type":"session_meta","payload":{"id":"thread","model_provider":"custom","creator_account_id":"old-account","creator_user_id":"old-user"}}`,
		tokenCountLine(2, cxUse(10, 0, 0, 5, 0), cxUse(10, 0, 0, 5, 0)))
	check := func() {
		t.Helper()
		cs := Calls(time.Time{})
		if len(cs) != 1 || cs[0].Upstream != "custom" || cs[0].AccountID != "old-account" || cs[0].UserID != "old-user" {
			t.Fatalf("session identity %+v", cs)
		}
	}
	check()
	Saved()
	Reset()
	check()
	appendText(t, path, tokenCountLine(3, cxUse(20, 0, 0, 10, 0), cxUse(10, 0, 0, 5, 0))+"\n")
	cs := Calls(time.Time{})
	if len(cs) != 2 || cs[0].AccountID != "old-account" || cs[0].Upstream != "custom" {
		t.Fatal("incremental call lost session identity")
	}
}

func TestCallsDoNotInflateSessionIndex(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "-p", "sess1.jsonl")
	writeLines(t, path, claudeMsg("m1", "claude-sonnet-5", 1, 2, 3, 4, 1))
	List(0)
	kept := cache[path]
	if len(callCache) != 0 {
		t.Fatal("session list loaded request details")
	}
	Saved()
	before, _ := os.ReadFile(CachePath())
	Calls(time.Time{})
	Saved()
	after, _ := os.ReadFile(CachePath())
	if cache[path] != kept || !reflect.DeepEqual(before, after) {
		t.Fatal("request scan rewrote the summary index")
	}
	Reset()
	List(0)
	if len(callCache) != 0 {
		t.Fatal("restarting Sessions loaded request details")
	}
}

func TestCallsConcurrentWithSessionReaders(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "-p", "sess1.jsonl")
	writeLines(t, path, claudeMsg("m1", "claude-sonnet-5", 1, 2, 3, 4, 1))
	// OpenCode's DB connection must survive a concurrent call-file refresh.
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db := ocMakeEmpty(t, dbPath, "")
	t.Setenv("OPENCODE_DB", dbPath)
	if _, err := db.Exec(`INSERT INTO session(id,project_id,slug,directory,title,version,time_created,time_updated) VALUES('s','p','s','/work','db session','1',1,1)`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 9; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 3 {
			case 0:
				if len(Calls(time.Time{})) != 1 {
					t.Error("concurrent Calls lost a call")
				}
			case 1:
				if len(List(0)) != 2 {
					t.Error("concurrent List lost a session")
				}
			case 2:
				StatsFor(30)
			}
		}(i)
	}
	wg.Wait()
}

func TestRequestShardsAreBoundedAndIndependent(t *testing.T) {
	d := setupCalls(t)
	for i := 0; i < 40; i++ {
		var lines []string
		for j := 0; j < 150; j++ {
			lines = append(lines, strings.ReplaceAll(claudeMsg(fmt.Sprintf("%d-%d", i, j), "m", 1, 1, 0, 0, j), `"text":"hi"`, `"text":"PRIVATE REPLY MUST NOT BE CACHED"`))
		}
		writeLines(t, filepath.Join(d.claude, "projects", "-p", fmt.Sprintf("s-%02d.jsonl", i)), lines...)
	}
	if n := len(Calls(time.Time{})); n != 6000 {
		t.Fatal(n)
	}
	kept := 0
	for _, st := range callCache {
		kept += len(st.Calls)
	}
	if len(callCache) > maxKeptFiles || kept > maxKeptCalls {
		t.Fatalf("unbounded memory cache: %d files, %d calls", len(callCache), kept)
	}
	a := filepath.Join(d.claude, "projects", "-p", "s-00.jsonl")
	b := filepath.Join(d.claude, "projects", "-p", "s-01.jsonl")
	before, err := os.Stat(callCachePath(b))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(callCachePath(b))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "PRIVATE REPLY") {
		t.Fatal("conversation content persisted in request shard")
	}
	appendText(t, a, claudeMsg("new", "m", 2, 1, 0, 0, 200)+"\n")
	if n := len(Calls(time.Time{})); n != 6001 {
		t.Fatal(n)
	}
	after, err := os.Stat(callCachePath(b))
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("one changed session rewrote an unrelated shard")
	}
	resetCalls()
	if err := os.WriteFile(callCachePath(b), []byte("broken cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if n := len(Calls(time.Time{})); n != 6001 {
		t.Fatalf("corrupt shard was not rebuilt: %d", n)
	}
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	if n := len(Calls(time.Time{})); n != 5851 {
		t.Fatal(n)
	}
	if _, err := os.Stat(callCachePath(b)); !os.IsNotExist(err) {
		t.Fatal("removed session's shard remains")
	}
}
