// Package lazyre holds a regular expression that is compiled the first
// time it is used, not when its package starts.
//
// magpie is one binary, so every package's init runs on every launch,
// `magpie statusline` (run by Claude Code on each message) included.
// Hundreds of package-level regexp.MustCompile calls cost several MB and
// milliseconds there for patterns that launch never reaches. An RE has
// the methods of *regexp.Regexp that magpie calls; Regexp returns the
// compiled one. A bad pattern panics on first use, so TestAllCompile in
// each converted package compiles every one before release.
package lazyre

import (
	"regexp"
	"sync"
)

// RE is a regexp compiled on first use. Safe for concurrent use.
type RE struct {
	src  string
	once sync.Once
	re   *regexp.Regexp
}

var (
	allMu sync.Mutex
	all   []*RE
)

// New returns an RE for src. It does not compile it.
func New(src string) *RE {
	r := &RE{src: src}
	allMu.Lock()
	all = append(all, r)
	allMu.Unlock()
	return r
}

// Of wraps an expression already compiled, for a field that holds both
// kinds.
func Of(re *regexp.Regexp) *RE {
	r := &RE{src: re.String(), re: re}
	r.once.Do(func() {})
	return r
}

// MustAll compiles every RE made so far and returns how many there are,
// so a test fails on a bad pattern.
func MustAll() int {
	allMu.Lock()
	rs := append([]*RE(nil), all...)
	allMu.Unlock()
	for _, r := range rs {
		r.Regexp()
	}
	return len(rs)
}

// Regexp returns the compiled expression, compiling it the first time.
func (r *RE) Regexp() *regexp.Regexp {
	r.once.Do(func() { r.re = regexp.MustCompile(r.src) })
	return r.re
}

func (r *RE) String() string                   { return r.src }
func (r *RE) MatchString(s string) bool        { return r.Regexp().MatchString(s) }
func (r *RE) Match(b []byte) bool              { return r.Regexp().Match(b) }
func (r *RE) NumSubexp() int                   { return r.Regexp().NumSubexp() }
func (r *RE) SubexpNames() []string            { return r.Regexp().SubexpNames() }
func (r *RE) SubexpIndex(name string) int      { return r.Regexp().SubexpIndex(name) }
func (r *RE) LiteralPrefix() (string, bool)    { return r.Regexp().LiteralPrefix() }
func (r *RE) Find(b []byte) []byte             { return r.Regexp().Find(b) }
func (r *RE) FindIndex(b []byte) []int         { return r.Regexp().FindIndex(b) }
func (r *RE) FindSubmatch(b []byte) [][]byte   { return r.Regexp().FindSubmatch(b) }
func (r *RE) FindSubmatchIndex(b []byte) []int { return r.Regexp().FindSubmatchIndex(b) }
func (r *RE) FindString(s string) string       { return r.Regexp().FindString(s) }
func (r *RE) FindStringIndex(s string) []int   { return r.Regexp().FindStringIndex(s) }
func (r *RE) FindStringSubmatch(s string) []string {
	return r.Regexp().FindStringSubmatch(s)
}
func (r *RE) FindStringSubmatchIndex(s string) []int {
	return r.Regexp().FindStringSubmatchIndex(s)
}
func (r *RE) FindAll(b []byte, n int) [][]byte     { return r.Regexp().FindAll(b, n) }
func (r *RE) FindAllIndex(b []byte, n int) [][]int { return r.Regexp().FindAllIndex(b, n) }
func (r *RE) FindAllSubmatch(b []byte, n int) [][][]byte {
	return r.Regexp().FindAllSubmatch(b, n)
}
func (r *RE) FindAllSubmatchIndex(b []byte, n int) [][]int {
	return r.Regexp().FindAllSubmatchIndex(b, n)
}
func (r *RE) FindAllString(s string, n int) []string { return r.Regexp().FindAllString(s, n) }
func (r *RE) FindAllStringIndex(s string, n int) [][]int {
	return r.Regexp().FindAllStringIndex(s, n)
}
func (r *RE) FindAllStringSubmatch(s string, n int) [][]string {
	return r.Regexp().FindAllStringSubmatch(s, n)
}
func (r *RE) FindAllStringSubmatchIndex(s string, n int) [][]int {
	return r.Regexp().FindAllStringSubmatchIndex(s, n)
}
func (r *RE) ReplaceAll(src, repl []byte) []byte { return r.Regexp().ReplaceAll(src, repl) }
func (r *RE) ReplaceAllLiteral(src, repl []byte) []byte {
	return r.Regexp().ReplaceAllLiteral(src, repl)
}
func (r *RE) ReplaceAllFunc(src []byte, f func([]byte) []byte) []byte {
	return r.Regexp().ReplaceAllFunc(src, f)
}
func (r *RE) ReplaceAllString(src, repl string) string {
	return r.Regexp().ReplaceAllString(src, repl)
}
func (r *RE) ReplaceAllLiteralString(src, repl string) string {
	return r.Regexp().ReplaceAllLiteralString(src, repl)
}
func (r *RE) ReplaceAllStringFunc(src string, f func(string) string) string {
	return r.Regexp().ReplaceAllStringFunc(src, f)
}
func (r *RE) Split(s string, n int) []string { return r.Regexp().Split(s, n) }
func (r *RE) Expand(dst, template, src []byte, match []int) []byte {
	return r.Regexp().Expand(dst, template, src, match)
}
func (r *RE) ExpandString(dst []byte, template string, src string, match []int) []byte {
	return r.Regexp().ExpandString(dst, template, src, match)
}
