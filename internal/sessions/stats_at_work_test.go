package sessions

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// A session the list shows has to be one the range counted, too: the page
// keeps every session ever and narrows it to the range by Keys. A session
// that said something but spent nothing — a Claude Code chat whose first
// request never came back, a Cursor chat, which keeps no tokens and whose
// pauses are longer than idleGap — was left out of Keys, so it vanished
// from every range, the whole of time included, where the list is also
// where a session is found to be resumed. Counting a day the session spoke
// on as a day it was at work keeps it; a setting line or a fork's seed
// counts no message, so the session whose file only last wrote one of
// those still leaves the range it did not work in.
func TestStatsCountsSessionsThatSpentNothing(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)

	// one line typed, no reply: the first request failed
	solo := "aaaaaaaa-1111-1111-1111-111111111111"
	p := filepath.Join(claude, "projects", "-work-solo", solo+".jsonl")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(
		`{"type":"user","isSidechain":false,"message":{"role":"user","content":"hello, and then it died"},"uuid":"u1","timestamp":"2026-09-21T10:00:00.000Z","cwd":"/work/solo","sessionId":"`+solo+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	key := "claude:" + solo
	all := statsAt(0, statsNow)
	if _, ok := findSession(List(0), "claude", solo); !ok {
		t.Fatal("the single-line session is not listed at all")
	}
	if !slices.Contains(all.Keys, key) {
		t.Fatalf("a session that spoke but spent nothing is not counted: keys %v", all.Keys)
	}
	// the KPI, the footnote and the rows are the same set
	if o := all.Overview("", "", "", 5); o.Count != len(all.Sessions) || o.Count != len(all.Keys) {
		t.Fatalf("KPI %d, counted sessions %d, keys %d — they have to agree", o.Count, len(all.Sessions), len(all.Keys))
	}
	// a range the session did not speak in still leaves it out
	after := statsAt(1, time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC))
	if slices.Contains(after.Keys, key) {
		t.Fatalf("a session of the 21st is not in a range of the 22nd: %v", after.Keys)
	}
}

// The Cursor chats in TestCursor's fixture are the other shape of it: every
// one is listed but says nothing of tokens, so before they were counted only
// by the time between their lines the sessions page lost them in every range.
func TestStatsCountsCursorChats(t *testing.T) {
	inZone(t, 0)
	dir := cursorSetup(t)
	makeCursorChats(t, dir)
	ageCursor(dir)

	listed := List(0)
	if n := count(listed, "cursor"); n != 2 {
		t.Fatalf("cursor listed %d, want the two chats: %+v", n, listed)
	}
	all := statsAt(0, statsNow)
	for _, id := range []string{curMain, curOther} {
		if k := "cursor:" + id; !slices.Contains(all.Keys, k) {
			t.Fatalf("cursor %s is listed but not counted: keys %v", id, all.Keys)
		}
	}
	if o := all.Overview("cursor", "", "", 5); o.Count != 2 {
		t.Fatalf("cursor counted %d in the KPI, want 2", o.Count)
	}
	// and a session's Tokens stay empty: being counted for showing it does
	// not invent spend the file does not tell
	for _, s := range all.Sessions {
		if s.Agent == "cursor" && !s.Tokens.zero() {
			t.Fatalf("cursor %s invented tokens: %+v", s.ID, s.Tokens)
		}
	}
}
