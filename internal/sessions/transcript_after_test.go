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
