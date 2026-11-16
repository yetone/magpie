package claudetui

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// TestWholeIn: the words Claude Code records of a pasted message are taken
// as whole whatever it trims, wraps, numbers or takes out, and a message
// that lost a block of its words is not (#5205, which lost 1 KB blocks),
// nor one that lost an image.
func TestWholeIn(t *testing.T) {
	long := strings.Repeat("line of words 中文 ", 400)
	cases := []struct {
		name   string
		sent   string
		said   []string
		whole  bool
		images int        // sent
		rows   []heardRow // said, with its images
	}{
		{"as sent", "hello there", []string{"hello there"}, true, 0, nil},
		{"trimmed and wrapped", "\n/command and more\n", []string{"/command and more"}, true, 0, nil},
		{"a long paste's tags", long, []string{"\n<pasted_content id=\"a1\">\n" + long + "\n</pasted_content id=\"a1\">\n"}, true, 0, nil},
		{"image marks", "look at this\n\n", []string{"[Image #1] look at this"}, true, 0, nil},
		{"a zero-width space taken out", "a\u200bb", []string{"ab"}, true, 0, nil},
		{"tabs as spaces", "a\tb", []string{"a    b"}, true, 0, nil},
		{"the user's mark", userMark + "go on", []string{"[user message]\ngo on"}, true, 0, nil},
		{"a later row is its", "the message", []string{"another row", "the message"}, true, 0, nil},
		{"no row read", "the message", nil, true, 0, nil},
		{"a block lost", long, []string{long[:1024] + long[2048:]}, false, 0, nil},
		{"its end lost", long, []string{long[:len(long)/2]}, false, 0, nil},
		{"taken as many pastes", long, []string{"<pasted_content id=\"a1\">\n" + long[:990] + "\n</pasted_content id=\"a1\">\n<pasted_content id=\"a2\">\n" + long[990:] + "\n</pasted_content id=\"a2\">"}, false, 0, nil},
		{"other words", "the message", []string{"another row"}, false, 0, nil},
		{"its images", "look\n", nil, true, 2, []heardRow{{"[Image #1] [Image #2] look", 2}}},
		{"an image lost", "look\n", nil, false, 2, []heardRow{{"[Image #1] look", 1}}},
		{"an image alone", "", nil, true, 1, []heardRow{{"[Image #1]", 1}}},
		{"a queued row says no images", "look\n", nil, true, 1, []heardRow{{"look", -1}}},
		{"images, but a word lost", "look here\n", nil, false, 1, []heardRow{{"look", 1}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := c.rows
			for _, w := range c.said {
				rows = append(rows, heardRow{w, 0})
			}
			err := wholeIn(c.sent, c.images, rows, nil)
			if (err == nil) != c.whole {
				t.Fatalf("wholeIn = %v, want whole %v", err, c.whole)
			}
			var cut *messageCut
			if err != nil && !errors.As(err, &cut) {
				t.Fatalf("wholeIn = %#v, want a messageCut", err)
			}
		})
	}
}

// BenchmarkWholeIn: a 256 KB message checked against its record, a long
// paste's, read in place.
func BenchmarkWholeIn(b *testing.B) {
	sent := strings.Repeat("line of words 中文 and more\n", 256<<10/30)
	said := []heardRow{{"\n<pasted_content id=\"a1\">\n" + sent + "</pasted_content id=\"a1\">\n", 0}}
	b.SetBytes(int64(len(sent)))
	b.ReportAllocs()
	for b.Loop() {
		if wholeIn(sent, 0, said, nil) != nil {
			b.Fatal("not whole")
		}
	}
}

// TestWordsSkipRange: words reads U+3001 to U+FEFE as letters without
// asking unicode, as none of them is white space or a format character.
func TestWordsSkipRange(t *testing.T) {
	for r := rune(0x3001); r < 0xfeff; r++ {
		if unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			t.Fatalf("%U is white space or a format character", r)
		}
	}
}

// TestHold: what Claude Code answers while its message is checked is let
// out after the check when the message was whole, and never when it
// wasn't, the check's error alone told.
func TestHold(t *testing.T) {
	var b strings.Builder
	s := &shim{out: &b, sid: "s1"}
	s.hold()
	s.emit(map[string]any{"type": "stream_event"})
	if b.Len() > 0 {
		t.Fatal("an answer got out while its message was checked")
	}
	if s.unhold(nil) != nil || !strings.Contains(b.String(), "stream_event") {
		t.Fatalf("a whole message's answer wasn't let out: %q", b.String())
	}
	s.emit(map[string]any{"type": "assistant"})
	if !strings.Contains(b.String(), "assistant") {
		t.Fatal("held after the check")
	}
	b.Reset()
	s.hold()
	s.emit(map[string]any{"type": "stream_event"})
	err := s.unhold(&messageCut{got: 1, sent: 2})
	s.emit(map[string]any{"type": "result", "subtype": "success"})
	s.tell(map[string]any{"type": "result", "is_error": true, "result": Unpastable + err.Error()})
	if strings.Contains(b.String(), "stream_event") || strings.Contains(b.String(), "success") ||
		!strings.Contains(b.String(), Unpastable) {
		t.Fatalf("a cut message's answer got out: %q", b.String())
	}
}

// TestWholeInTaken: the image files' paths Claude Code says it took as
// images are left out of the words pasted, where it recorded the message
// without them, and their images out of the count; any other words lost
// still fail it (planner2's screenshots, a worker's summary of them,
// 2.1.319).
func TestWholeInTaken(t *testing.T) {
	a, b := `C:\s\a.png`, `C:\s\b.png`
	long := strings.Repeat("line of words 中文\n", 400)
	cases := []struct {
		name   string
		sent   string
		images int
		row    heardRow
		taken  []string
		whole  bool
	}{
		{"lines taken", "fix this\n" + a + "\nand this\n" + b + "\n", 0,
			heardRow{"[Image #1] [Image #2]\n\n<pasted_content id=\"x\">\nfix this\nand this\n</pasted_content id=\"x\">\n", 2}, []string{a, b}, true},
		{"two on a line", a + " " + b + "\nlook", 0, heardRow{"[Image #1] [Image #2]look", 2}, []string{a, b}, true},
		{"quoted, with a space", `"C:\s b\a.png"` + "\nlook", 0, heardRow{"[Image #1]look", 1}, []string{`C:\s b\a.png`}, true},
		{"one left as words", a + "\n" + `C:\s\missing.png` + "\nmore", 0, heardRow{"[Image #1]" + `C:\s\missing.png` + "\nmore", 1}, []string{a}, true},
		{"more on its line stays", a + "|\n" + a + "\n", 0, heardRow{"[Image #1]" + a + "|", 1}, []string{a}, true},
		{"the same path left in words before", "see " + a + " here\n" + a + "\n", 0, heardRow{"[Image #1]see " + a + " here", 1}, []string{a}, true},
		{"beside an image pasted", "look\n" + a, 1, heardRow{"[Image #1] [Image #2]look", 2}, []string{a}, true},
		{"one pasted as an image, its path not in the words", "look", 1, heardRow{"[Image #1] look", 1}, []string{`C:\tmp\paste.png`}, true},
		{"a long one with paths, whole", long + a + "\n" + long, 0, heardRow{"[Image #1]" + long + long, 1}, []string{a}, true},
		{"paths taken, a word lost", a + "\nlook here\n", 0, heardRow{"[Image #1]look", 1}, []string{a}, false},
		{"a long one with paths, a block lost", long + a + "\n" + long, 0, heardRow{"[Image #1]" + long[:1024] + long[2048:] + long, 1}, []string{a}, false},
		{"a path taken, an image pasted lost", "look\n" + a, 1, heardRow{"[Image #1]look", 1}, []string{a}, false},
		{"a path at the start and the end", a + "\nlook\n" + b, 0, heardRow{"[Image #1] [Image #2]look", 2}, []string{a, b}, true},
		{"one path taken twice", a + "\nlook\n" + a + "\n", 0, heardRow{"[Image #1] [Image #2]look", 2}, []string{a, a}, true},
		{"one path twice, taken once", a + "\nlook\n" + a + "\n", 0, heardRow{"[Image #1]look\n" + a, 1}, []string{a}, true},
		{"after a CJK word", "看图\n" + a + "\n中文", 0, heardRow{"[Image #1]看图\n中文", 1}, []string{a}, true},
		{"cut in the middle of a path", "look\n" + a + "\nmore", 0, heardRow{"[Image #1]look\nC:\\s", 1}, []string{a}, false},
		{"a word lost between paths", a + "\nlook\n" + b + "\nmore", 0, heardRow{"[Image #1] [Image #2]look", 2}, []string{a, b}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := wholeIn(c.sent, c.images, []heardRow{c.row}, c.taken)
			if (err == nil) != c.whole {
				t.Fatalf("wholeIn = %v, want whole %v", err, c.whole)
			}
			var cut *messageCut
			if err != nil && !errors.As(err, &cut) {
				t.Fatalf("wholeIn = %#v, want a messageCut", err)
			}
		})
	}
}

// TestWholeInUnexplained: a row with more images than were pasted, for
// which Claude Code hasn't said it took as many of the paste's paths
// (not yet, not at all, or a path made its own, as in WSL), is not judged
// rather than failed each time; a cut is still one where it has said.
func TestWholeInUnexplained(t *testing.T) {
	a := `C:\s\a.png`
	for _, c := range []struct {
		name  string
		sent  string
		row   heardRow
		taken []string
	}{
		{"not yet said", a + "\nlook", heardRow{"[Image #1]look", 1}, nil},
		{"said otherwise", a + "\nlook", heardRow{"[Image #1]look", 1}, []string{"/mnt/c/s/a.png"}},
		{"fewer said than taken", a + "\n" + `C:\s\b.png` + "\nlook", heardRow{"[Image #1] [Image #2]look", 2}, []string{a}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := wholeIn(c.sent, 0, []heardRow{c.row}, c.taken); !errors.Is(err, errUnexplained) {
				t.Fatalf("wholeIn = %v, want errUnexplained", err)
			}
		})
	}
	// a later row of the message, whole, is still taken
	if err := wholeIn("look", 0, []heardRow{{"[Image #1]", 1}, {"look", 0}}, nil); err != nil {
		t.Fatalf("wholeIn = %v, want whole", err)
	}
}

// TestHeardTaken: the paths Claude Code says it took are kept only while a
// message is checked, have it checked again, and end with it.
func TestHeardTaken(t *testing.T) {
	row := json.RawMessage(`{"role":"user","content":[{"type":"text","text":"[Image: source: C:\\s\\a.png]"}]}`)
	s := &shim{rowsCh: make(chan struct{}, 1)}
	s.heardTaken(row)
	if s.taken != nil || len(s.rowsCh) > 0 {
		t.Fatal("kept with no message checked")
	}
	s.said = []heardRow{}
	s.heardTaken(row)
	if !slices.Equal(s.taken, []string{`C:\s\a.png`}) || len(s.rowsCh) != 1 {
		t.Fatalf("taken %q, %d signals", s.taken, len(s.rowsCh))
	}
	s.heardAll()
	if s.said != nil || s.taken != nil {
		t.Fatal("kept after the message")
	}
}

// TestImageSources: the meta row Claude Code records after a message for
// the paths it took as images (2.1.319), and rows that aren't it.
func TestImageSources(t *testing.T) {
	row := `{"role":"user","content":[{"type":"text","text":"[Image: source: C:\\s\\red.png]"},{"type":"text","text":"[Image: source: C:\\s b\\blue.png]"}]}`
	if got := imageSources(json.RawMessage(row)); !slices.Equal(got, []string{`C:\s\red.png`, `C:\s b\blue.png`}) {
		t.Fatalf("imageSources = %q", got)
	}
	for _, r := range []string{`{"role":"user","content":"[Image: source: C:\\x.png]"}`, `{"role":"user","content":[{"type":"text","text":"caveat"}]}`} {
		if got := imageSources(json.RawMessage(r)); got != nil {
			t.Fatalf("imageSources(%s) = %q", r, got)
		}
	}
}
