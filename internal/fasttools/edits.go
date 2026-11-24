package fasttools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxEdits = 100

type editReq struct {
	Path       string  `json:"path"`
	Old        string  `json:"old"`
	New        string  `json:"new"`
	ReplaceAll bool    `json:"replaceAll"`
	Content    *string `json:"content"`
}

// fileEdit is one file's edits, made in memory before any file is written.
type fileEdit struct {
	path    string
	name    string
	was     string // "" for a file that isn't there
	exists  bool
	mode    fs.FileMode
	now     string
	changes []string // what each edit did, for the result
	failed  []string
}

func applyEdits(raw json.RawMessage) (string, error) {
	var a struct {
		Cwd           string    `json:"cwd"`
		Edits         []editReq `json:"edits"`
		BestEffort    bool      `json:"bestEffort"`
		DryRun        bool      `json:"dryRun"`
		Verify        string    `json:"verify"`
		VerifyTimeout int       `json:"verifyTimeout"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	if len(a.Edits) == 0 {
		return "", errors.New("no edits")
	}
	if len(a.Edits) > maxEdits {
		return "", fmt.Errorf("%d edits: at most %d in one call", len(a.Edits), maxEdits)
	}
	dir, err := root(a.Cwd)
	if err != nil {
		return "", err
	}
	var order []*fileEdit
	byPath := map[string]*fileEdit{}
	failures := 0
	for i, e := range a.Edits {
		if e.Path == "" {
			return "", fmt.Errorf("edit %d has no path", i+1)
		}
		p := at(dir, e.Path)
		fe := byPath[p]
		if fe == nil {
			fe = &fileEdit{path: p, name: shown(dir, p), mode: 0o644}
			if fi, err := os.Stat(p); err == nil {
				if fi.IsDir() {
					return "", fmt.Errorf("%s is a folder", fe.name)
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return "", fmt.Errorf("%s: %w", fe.name, err)
				}
				fe.was, fe.exists, fe.mode = string(b), true, fi.Mode().Perm()
			} else if !errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("%s: %w", fe.name, err)
			}
			fe.now = fe.was
			byPath[p] = fe
			order = append(order, fe)
		}
		if why := fe.apply(i+1, e); why != "" {
			fe.failed = append(fe.failed, why)
			failures++
		}
	}
	var out strings.Builder
	if failures > 0 && !a.BestEffort {
		fmt.Fprintf(&out, "nothing written: %d of %d edits can't be made\n", failures, len(a.Edits))
		for _, fe := range order {
			for _, f := range fe.failed {
				fmt.Fprintf(&out, "%s: %s\n", fe.name, f)
			}
		}
		return strings.TrimRight(out.String(), "\n"), nil
	}
	if a.DryRun {
		out.WriteString("dry run, nothing written\n")
	}
	written := 0
	for _, fe := range order {
		for _, f := range fe.failed {
			fmt.Fprintf(&out, "%s: not made: %s\n", fe.name, f)
		}
		if fe.now == fe.was && fe.exists {
			if len(fe.changes) > 0 {
				fmt.Fprintf(&out, "%s: unchanged (the edits give the same text)\n", fe.name)
			}
			continue
		}
		if !a.DryRun {
			if err := writeFile(fe.path, fe.now, fe.mode); err != nil {
				fmt.Fprintf(&out, "%s: not written: %v\n", fe.name, err)
				continue
			}
			written++
		}
		verb := "edited"
		if !fe.exists {
			verb = "created"
		}
		fmt.Fprintf(&out, "%s: %s (%s)\n", fe.name, verb, strings.Join(fe.changes, "; "))
	}
	if a.DryRun || a.Verify == "" {
		return strings.TrimRight(out.String(), "\n"), nil
	}
	if written == 0 {
		out.WriteString("verify not run: nothing was written\n")
		return strings.TrimRight(out.String(), "\n"), nil
	}
	t := a.VerifyTimeout
	if t <= 0 {
		t = 120
	}
	r := runOne(dir, check{Cmd: a.Verify, Name: "verify"}, time.Duration(min(t, 900))*time.Second)
	out.WriteString(r.text(true))
	return strings.TrimRight(out.String(), "\n"), nil
}

// apply makes edit n on fe's text in memory, saying why it can't.
func (fe *fileEdit) apply(n int, e editReq) string {
	if e.Content != nil {
		c := *e.Content
		if fe.exists && strings.Contains(fe.was, "\r\n") && !strings.Contains(c, "\r\n") {
			c = strings.ReplaceAll(c, "\n", "\r\n")
		}
		fe.now = c
		fe.changes = append(fe.changes, lines(lineCount(c))+" written")
		return ""
	}
	if !fe.exists && fe.now == "" {
		return fmt.Sprintf("edit %d: the file isn't there (give content to create it)", n)
	}
	if e.Old == "" {
		return fmt.Sprintf("edit %d: old is empty (give content to write the whole file)", n)
	}
	old, nw := e.Old, e.New
	// the file's own line ends: an edit written with \n still matches a
	// CRLF file, and keeps it CRLF
	if strings.Contains(fe.now, "\r\n") && !strings.Contains(old, "\r\n") {
		old = strings.ReplaceAll(old, "\n", "\r\n")
		nw = strings.ReplaceAll(nw, "\n", "\r\n")
	}
	c := strings.Count(fe.now, old)
	switch {
	case c == 0:
		return fmt.Sprintf("edit %d: old isn't in the file%s", n, nearest(fe.now, e.Old))
	case c > 1 && !e.ReplaceAll:
		return fmt.Sprintf("edit %d: old is in the file %d times, at lines %s: give more lines around it, or replaceAll", n, c, lineList(fe.now, old, 8))
	}
	line := strings.Count(fe.now[:strings.Index(fe.now, old)], "\n") + 1
	if e.ReplaceAll {
		fe.now = strings.ReplaceAll(fe.now, old, nw)
		fe.changes = append(fe.changes, fmt.Sprintf("%d replaced from line %d", c, line))
	} else {
		fe.now = strings.Replace(fe.now, old, nw, 1)
		d := strings.Count(nw, "\n") - strings.Count(old, "\n")
		fe.changes = append(fe.changes, fmt.Sprintf("line %d%s", line, delta(d)))
	}
	return ""
}

// lineCount is how many lines s has: a last line's \n ends it, it doesn't
// start another.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

func delta(d int) string {
	switch {
	case d > 0:
		return " +" + lines(d)
	case d < 0:
		return " -" + lines(-d)
	}
	return ""
}

func lines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

// lineList is the lines s starts at in text, up to max of them.
func lineList(text, s string, most int) string {
	var at []string
	off := 0
	for len(at) < most {
		i := strings.Index(text[off:], s)
		if i < 0 {
			break
		}
		at = append(at, fmt.Sprint(strings.Count(text[:off+i], "\n")+1))
		off += i + max(len(s), 1)
	}
	return strings.Join(at, ", ")
}

// nearest says where old nearly is in text: the same lines with other
// spacing, or the line its first line is on.
func nearest(text, old string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	want := strings.Split(strings.ReplaceAll(old, "\r\n", "\n"), "\n")
	for len(want) > 0 && strings.TrimSpace(want[0]) == "" {
		want = want[1:]
	}
	for len(want) > 0 && strings.TrimSpace(want[len(want)-1]) == "" {
		want = want[:len(want)-1]
	}
	if len(want) == 0 {
		return ""
	}
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	// the same lines, other indenting or spacing
	for i := 0; i+len(want) <= len(lines); i++ {
		same := true
		for j, w := range want {
			if norm(lines[i+j]) != norm(w) {
				same = false
				break
			}
		}
		if same {
			return fmt.Sprintf("; it is at line %d with other spacing (or indenting): copy those lines as they are", i+1)
		}
	}
	// its first line, and how far the rest matches
	first := norm(want[0])
	if first == "" {
		return ""
	}
	best, bestN := -1, 0
	for i, l := range lines {
		if norm(l) != first {
			continue
		}
		n := 1
		for n < len(want) && i+n < len(lines) && norm(lines[i+n]) == norm(want[n]) {
			n++
		}
		if n > bestN {
			best, bestN = i, n
		}
	}
	if best < 0 {
		return "; nor is its first line: read the file again, it may have changed"
	}
	at := best + bestN
	got := ""
	if at < len(lines) {
		got = fmt.Sprintf(", where the file has %q", cutLine(strings.TrimSpace(lines[at])))
	}
	return fmt.Sprintf("; its first %d of %d lines are at line %d, then it differs at line %d%s", bestN, len(want), best+1, at+1, got)
}

// writeFile writes s to path through a file beside it renamed over it, so
// a reader never sees it half-written, keeping its mode.
func writeFile(path, s string, mode fs.FileMode) error {
	// a link's file is written, and the link stays one
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.WriteString(s)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
