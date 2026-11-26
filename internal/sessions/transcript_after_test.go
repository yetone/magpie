package sessions

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// longSession is a Claude session longer than one transcript holds, each
// turn numbered, and its last turn says LAST-WORDS.
func longSession(t *testing.T) (Session, int) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "long.jsonl")
	n := transcriptMax/7000 + 40
	var lines []string
	for i := range n {
		text := fmt.Sprintf("turn %d %s", i, strings.Repeat("x", 7000))
		if i == n-1 {
			text = "LAST-WORDS"
		}
		lines = append(lines, claudeUser(i+1, fmt.Sprintf("%q", text)))
	}
	writeLines(t, file, lines...)
	return Session{Agent: "claude", ID: "long", Path: file}, n
}

// A long session's sheet reads its first transcriptMax characters, then
// the rest when asked, until it has every part (usrname1833 on Discord:
// the sheet stopped before a long session's last turns).
func TestTranscriptAfterReadsTheRest(t *testing.T) {
	s, n := longSession(t)
	first, err := TranscriptOf(s)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Cut || len(first.Parts) >= n {
		t.Fatalf("first read: cut %v, %d of %d parts", first.Cut, len(first.Parts), n)
	}
	all := first.Parts
	for reads := 0; ; reads++ {
		if reads > n {
			t.Fatal("the rest never ends")
		}
		more, err := TranscriptAfter(s, len(all))
		if err != nil {
			t.Fatal(err)
		}
		if more.From != len(all) {
			t.Fatalf("from %d, asked after %d", more.From, len(all))
		}
		all = append(all, more.Parts...)
		if !more.Cut {
			break
		}
		if len(more.Parts) == 0 {
			t.Fatal("cut with nothing read")
		}
	}
	if len(all) != n {
		t.Fatalf("%d parts read in all, the session has %d", len(all), n)
	}
	for i, p := range all[:n-1] {
		if !strings.HasPrefix(p.Text, fmt.Sprintf("turn %d ", i)) {
			t.Fatalf("part %d is %.20q", i, p.Text)
		}
	}
	if all[n-1].Text != "LAST-WORDS" {
		t.Fatalf("last part %.20q", all[n-1].Text)
	}
	if tr, _ := TranscriptAfter(s, n); len(tr.Parts) != 0 || tr.Cut {
		t.Fatalf("after the end: %d parts, cut %v", len(tr.Parts), tr.Cut)
	}
}

// SessionText's "more" past the first transcriptMax characters reads on
// from there: before, it read the same cut start again and never reached
// the end, for magpie sessions show and the sessions tool alike.
func TestSessionTextReadsPastTheCut(t *testing.T) {
	s, n := longSession(t)
	how := TextHow{From: func(_ Session, i int) string { return fmt.Sprintf("from %d", i) }}
	text, err := SessionText(s, ReadAsk{From: n - 2, MaxChars: TextReadMost}, how)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "LAST-WORDS") || !strings.Contains(text, fmt.Sprintf("turn %d ", n-2)) {
		t.Fatalf("from %d: %.400q", n-2, text)
	}
	if strings.Contains(text, "longer than is read at once") {
		t.Fatalf("the end still said to be cut: %.400q", text)
	}
}

// A long session's turns are counted in its whole file, not in the part a
// sheet has read (usrname1833 on Discord: the sheet's "turn i / N" counted
// only what was read, about 18 turns of a real session): a turn is the
// user's message, as the sheet counts them, and a read that starts past
// From knows the turns before it.
func TestCountedCountsTheWholeFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "turns.jsonl")
	const turns = 120
	var lines []string
	for i := range turns {
		sec := i * 10
		id := fmt.Sprintf("msg_%d", i)
		lines = append(lines,
			claudeUser(sec, fmt.Sprintf("%q", fmt.Sprintf("ask %d", i))),
			claudeBlocks(id+"a", sec+1, `{"type":"thinking","thinking":"think first"}`, fmt.Sprintf(`{"type":"tool_use","id":"tu_%d","name":"Bash","input":{"command":"ls"}}`, i)),
			claudeUser(sec+2, fmt.Sprintf(`[{"type":"tool_result","tool_use_id":"tu_%d","content":%q}]`, i, strings.Repeat("out ", 2000))),
			claudeBlocks(id+"b", sec+3, fmt.Sprintf(`{"type":"text","text":"answer %d"}`, i)),
		)
	}
	writeLines(t, file, lines...)
	s := Session{Agent: "claude", ID: "turns", Path: file}

	first, err := TranscriptOf(s)
	if err != nil {
		t.Fatal(err)
	}
	read := 0
	for _, p := range first.Parts {
		if turn(p) {
			read++
		}
	}
	if !first.Cut || read >= turns || read == 0 {
		t.Fatalf("the first read should hold some turns, not all: cut %v, %d of %d", first.Cut, read, turns)
	}
	got, err := Counted(s, first)
	if err != nil {
		t.Fatal(err)
	}
	if got.Turns != turns || got.TurnsBefore != 0 {
		t.Fatalf("first read counts %d turns, %d before; the file has %d", got.Turns, got.TurnsBefore, turns)
	}
	if len(got.Parts) != len(first.Parts) {
		t.Fatal("counting changed what was read")
	}

	// a read from part n, as a search's or the rest's, knows the turns before n
	n := len(first.Parts) + 3
	later, err := TranscriptAfter(s, n)
	if err != nil {
		t.Fatal(err)
	}
	got, err = Counted(s, later)
	if err != nil {
		t.Fatal(err)
	}
	// the turns among the first n parts, from the first read and the next
	rest, err := TranscriptAfter(s, len(first.Parts))
	if err != nil {
		t.Fatal(err)
	}
	before := 0
	for _, p := range append(append([]Part{}, first.Parts...), rest.Parts...)[:n] {
		if turn(p) {
			before++
		}
	}
	if got.Turns != turns || got.TurnsBefore != before || before == 0 {
		t.Fatalf("from %d: %d turns, %d before; want %d, %d", n, got.Turns, got.TurnsBefore, turns, before)
	}

	// a whole session's turns are its own: nothing more is read
	short := claudeFile(t, claudeUser(0, `"hi"`), claudeBlocks("m", 1, `{"type":"text","text":"hello"}`))
	whole, _ := TranscriptOf(short)
	if got, _ := Counted(short, whole); got.Turns != 0 || got.TurnsBefore != 0 {
		t.Fatalf("a whole session was counted again: %+v", got)
	}
}
