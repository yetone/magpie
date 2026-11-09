// Package capre is a regular expression held to a length.
//
// Go's regexp unrolls a bounded repeat: [A-Za-z0-9.-]{0,254} is 254
// copies of the class, 89kB of program kept from start to exit for
// one settings check. The same class with * is a few states, and the
// bound is a length check. A pattern here is whole-string (^…$), every
// character of it of the repeated classes, so the length is its count
// of characters.
package capre

import (
	"regexp"
	"unicode/utf8"
)

type Regexp struct {
	re  *regexp.Regexp
	max int
}

// MustCompile is expr, unbounded, matching only a string of max
// characters at most.
func MustCompile(expr string, max int) *Regexp {
	return &Regexp{regexp.MustCompile(expr), max}
}

// MatchString is whether s, of max characters at most, matches.
func (r *Regexp) MatchString(s string) bool {
	return (len(s) <= r.max || utf8.RuneCountInString(s) <= r.max) && r.re.MatchString(s)
}
