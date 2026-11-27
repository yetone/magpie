package sessions

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// wholeSession is a Claude session whose third message is longer than
// partMax: "turn 0", "turn 1", then 20,000 characters ending in
// TAIL-WORDS, then "turn 3".
func wholeSession(t *testing.T) (Session, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "whole.jsonl")
	long := "LONG-START " + strings.Repeat("y", 20_000-len("LONG-START  TAIL-WORDS")) + " TAIL-WORDS"
	writeLines(t, file,
		claudeUser(1, `"turn 0"`),
		claudeBlocks("m1", 2, `{"type":"text","text":"turn 1"}`, `{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"`+strings.Repeat("z", 600)+` TOOL-END"}}`),
		claudeUser(3, fmt.Sprintf("%q", long)),
		claudeUser(4, `"turn 3"`),
	)
	return Session{Agent: "claude", ID: "whole", Path: file}, long
}

// A read with Whole gives each message as it was said, where a read
// otherwise cuts it at partMax and says how much was left off; the
// parts are numbered the same either way (hdhfhf_hdh219 on X: show cut
// every message at 8000 characters while search matched words past it).
func TestReadTranscriptWhole(t *testing.T) {
	s, long := wholeSession(t)
	cut, err := ReadTranscript(s, ReadAsk{})
	if err != nil {
		t.Fatal(err)
	}
	whole, err := ReadTranscript(s, ReadAsk{Whole: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(cut.Parts) != len(whole.Parts) || len(cut.Parts) != 5 {
		t.Fatalf("%d parts cut, %d whole, want 5 each", len(cut.Parts), len(whole.Parts))
	}
	p := cut.Parts[3]
	if len([]rune(p.Text)) != partMax || p.Cut != len([]rune(long))-partMax || strings.Contains(p.Text, "TAIL-WORDS") {
		t.Fatalf("by default the long message is cut at %d: %d runes, cut %d", partMax, len([]rune(p.Text)), p.Cut)
	}
	if w := whole.Parts[3]; w.Text != long || w.Cut != 0 {
		t.Fatalf("whole: %d runes, cut %d", len([]rune(w.Text)), w.Cut)
	}
	// from a message on, whole too
	after, err := ReadTranscript(s, ReadAsk{From: 3, Whole: true})
	if err != nil || after.From != 3 || len(after.Parts) != 2 || after.Parts[0].Text != long || after.Next() != 0 {
		t.Fatalf("from 3 whole: %v from %d, %d parts, next %d", err, after.From, len(after.Parts), after.Next())
	}
	// at the words past the cut
	at, err := ReadTranscript(s, ReadAsk{Find: "tail-words", Whole: true})
	if err != nil || len(at.Parts) != 5 || at.Parts[3].Text != long {
		t.Fatalf("find whole: %v, %d parts", err, len(at.Parts))
	}
}

// SessionText says when a message was cut and how to read it whole, and
// with Whole shows it, tool calls too.
func TestSessionTextWhole(t *testing.T) {
	s, long := wholeSession(t)
	how := TextHow{
		From:  func(_ Session, n int) string { return fmt.Sprintf("from %d", n) },
		Whole: func(_ Session, n int) string { return fmt.Sprintf("whole from %d", n) },
	}
	text, err := SessionText(s, ReadAsk{}, how)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("(cut: %d more characters of this message; whole from 3)", len(long)-partMax)
	if !strings.Contains(text, want) || strings.Contains(text, "TAIL-WORDS") || strings.Contains(text, "TOOL-END") {
		t.Fatalf("by default, want %q and nothing past the cut:\n%.600s", want, text)
	}
	if text, err = SessionText(s, ReadAsk{Whole: true, MaxChars: TextReadMost}, how); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "[3] user:\n"+long+"\n") || !strings.Contains(text, "TOOL-END") || strings.Contains(text, "(cut:") {
		t.Fatalf("whole:\n%.600s…%s", text, text[max(0, len(text)-300):])
	}
	// a page smaller than the message still shows it whole, and goes on after it
	if text, err = SessionText(s, ReadAsk{From: 3, Whole: true, MaxChars: 1000}, how); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, long) || !strings.Contains(text, "(more: from 4)") {
		t.Fatalf("whole, a small page:\n%.300s…%s", text, text[max(0, len(text)-300):])
	}
}

// ReadTranscript with MaxChars is a page of SessionText's size, and Next
// says where the next one starts: --json now reads on as the text does.
func TestReadTranscriptPages(t *testing.T) {
	s, n := longSession(t)
	var all []Part
	for from, reads := 0, 0; ; reads++ {
		if reads > n {
			t.Fatal("the pages never end")
		}
		tr, err := ReadTranscript(s, ReadAsk{From: from, MaxChars: 30_000})
		if err != nil {
			t.Fatal(err)
		}
		if tr.From != from || len(tr.Parts) == 0 {
			t.Fatalf("page from %d: from %d, %d parts", from, tr.From, len(tr.Parts))
		}
		size := 0
		for _, p := range tr.Parts {
			size += len(p.Text)
		}
		if size > 30_000 {
			t.Fatalf("page from %d holds %d characters", from, size)
		}
		all = append(all, tr.Parts...)
		if from = tr.Next(); from == 0 {
			break
		}
	}
	if len(all) != n || all[n-1].Text != "LAST-WORDS" {
		t.Fatalf("%d parts in all of %d, last %.20q", len(all), n, all[len(all)-1].Text)
	}
}
