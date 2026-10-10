package agent

import (
	"slices"
	"strings"
)

// notice is advice an agent gives after a change (Agent.Notice), as an
// English template whose {slots} hold only names, paths, versions and
// commands. Each one is made with newNotice, so Notices lists every one:
// the GUI shows a notice in the reader's language by matching the English
// it gets to these templates (NOTICES and tNotice in i18n.js), and
// TestEveryNoticeIsTranslated holds each to a translation in every
// language the GUI has (#1508). A notice of several sentences is notices
// joined with a space.
type notice string

var notices []notice

// newNotice is the template en, listed in Notices; called only to set a
// package var, never per call.
func newNotice(en string) notice {
	n := notice(en)
	notices = append(notices, n)
	return n
}

// say is the notice with its slots filled: say("agent", "Codex", …).
func (n notice) say(kv ...string) string {
	s := string(n)
	for i := 0; i+1 < len(kv); i += 2 {
		s = strings.ReplaceAll(s, "{"+kv[i]+"}", kv[i+1])
	}
	return s
}

// String is the notice as it reads with no slots to fill.
func (n notice) String() string { return string(n) }

// Notices are the templates of every notice magpie's own agents give.
func Notices() []string {
	out := make([]string, 0, len(notices))
	for _, n := range notices {
		out = append(out, string(n))
	}
	slices.Sort(out)
	return slices.Compact(out)
}
