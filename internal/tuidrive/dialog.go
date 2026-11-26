package tuidrive

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/lazyre"
)

// Dialog is how far up the screen's words a dialog is looked for: a
// dialog an agent asks is at the foot of its screen, and the same words
// further up are history (a resumed conversation that talks of the
// dialog), not the dialog.
type Dialog struct {
	// Foot is the most the dialog draws under its option rows: its footer
	// ("Enter to confirm · Esc to cancel"), in characters.
	Foot int
	// Body is the most it draws under its heading: its options, and a
	// preview of the choice.
	Body int
}

// TailAfter is how many characters of words follow byte offset end.
func TailAfter(words string, end int) int {
	return utf8.RuneCountInString(words[end:])
}

// Options is whether words end on a question: each of its option rows
// (rows, matched by their words, as the unselected one has no mark), with
// only the dialog's footer under them, and its words ask at most Body
// before the rows or in them. The question's words alone are not it: a
// resumed conversation's history can hold them (@alisenray: Claude Code's
// "Bypass Permissions mode" in the history failed every --resume at 45s
// with the prompt box showing). Nor do the words come first: Claude Code
// (2.1.327, 2.1.329) heads its trust question "Quick safety check", and
// "trust this folder" is in its Yes row alone, under the No row. A check
// that wanted the words above both rows never saw the question, and every
// run of a saved account in a caller's folder waited out its 45s on it
// (@ahz2ygnhjmIJ0E0 on X).
func (d Dialog) Options(words, ask string, rows ...*lazyre.RE) bool {
	start, end := len(words), -1
	for _, re := range rows {
		m := re.FindAllStringIndex(words, -1)
		if len(m) == 0 {
			return false
		}
		start = min(start, m[len(m)-1][0])
		end = max(end, m[len(m)-1][1])
	}
	if TailAfter(words, end) > d.Foot {
		return false
	}
	// the words can end in the footer's reach: Claude Code's Yes row "Yes,
	// I trust" holds "trust this folder", past what the row matched
	h := strings.LastIndex(words, ask)
	return h >= 0 && (h >= start || TailAfter(words[:start], h) <= d.Body)
}

// Heading is whether words end on a screen drawn under heading ask (an
// onboarding screen, an offer, sign-in): the heading is in the bottom of
// the screen, not further up in history.
func (d Dialog) Heading(words, ask string) bool {
	h := strings.LastIndex(words, ask)
	return h >= 0 && TailAfter(words, h+len(ask)) <= d.Body
}

// LastMatchNear is whether re matches in words with at most n characters
// after its last match.
func LastMatchNear(re *lazyre.RE, words string, n int) bool {
	m := re.FindAllStringIndex(words, -1)
	return len(m) > 0 && TailAfter(words, m[len(m)-1][1]) <= n
}

// LastAt is where re's last match in p starts, -1 with none.
func LastAt(re *lazyre.RE, p string) int {
	m := re.FindAllStringIndex(p, -1)
	if len(m) == 0 {
		return -1
	}
	return m[len(m)-1][0]
}

// Deadline reads a deadline given as Unix milliseconds: when the
// driver's caller stops waiting.
func Deadline(v string) (time.Time, bool) {
	ms, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}
