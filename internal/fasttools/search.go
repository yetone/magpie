package fasttools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

const (
	maxQueries = 20
	// a file bigger than this isn't searched: a bundle, a lock file, data
	maxSearchFile = 2 << 20
	// files listed at most: a home folder given as cwd isn't walked whole
	maxListed = 200000
	lineCut   = 240
)

type query struct {
	Kind       string `json:"kind"`
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignoreCase"`
	Context    int    `json:"context"`
	Max        int    `json:"max"`
}

func batchSearch(raw json.RawMessage) (string, error) {
	var a struct {
		Cwd     string  `json:"cwd"`
		Queries []query `json:"queries"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	if len(a.Queries) == 0 {
		return "", fmt.Errorf("no queries")
	}
	if len(a.Queries) > maxQueries {
		return "", fmt.Errorf("%d queries: at most %d in one call", len(a.Queries), maxQueries)
	}
	dir, err := root(a.Cwd)
	if err != nil {
		return "", err
	}
	// the files are listed once, for every query that needs them
	var files []string
	var listErr error
	var listOnce sync.Once
	list := func() ([]string, error) {
		listOnce.Do(func() { files, listErr = listFiles(dir) })
		return files, listErr
	}
	out := make([]string, len(a.Queries))
	var wg sync.WaitGroup
	for i, q := range a.Queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := runQuery(dir, q, list)
			head := fmt.Sprintf("## %d. %s %s", i+1, q.Kind, q.Pattern)
			if q.Path != "" {
				head += " in " + q.Path
			}
			if q.Glob != "" {
				head += " (" + q.Glob + ")"
			}
			if err != nil {
				out[i] = head + "\nerror: " + err.Error()
				return
			}
			out[i] = head + "\n" + res
		}()
	}
	wg.Wait()
	// each query gets a fair part of the room, so the first one's many
	// matches don't push out the rest
	share := outCap / len(out)
	for i, s := range out {
		if len(s) > share && share > 200 {
			out[i] = capTo(s, share)
		}
	}
	return strings.Join(out, "\n\n"), nil
}

// capTo cuts s to about n bytes at a line's end, saying how many lines were left.
func capTo(s string, n int) string {
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	if i := strings.LastIndexByte(s[:cut], '\n'); i > 0 {
		cut = i
	}
	rest := strings.Count(s[cut:], "\n")
	return s[:cut] + fmt.Sprintf("\n… %d more lines (narrow it with path, glob or max)", rest)
}

func runQuery(dir string, q query, list func() ([]string, error)) (string, error) {
	most := q.Max
	if most <= 0 {
		most = 40
	}
	most = min(most, 200)
	switch q.Kind {
	case "git":
		return gitQuery(dir, q.Pattern)
	case "glob":
		files, err := list()
		if err != nil {
			return "", err
		}
		re, err := globRE(q.Pattern)
		if err != nil {
			return "", err
		}
		var hit []string
		n := 0
		for _, f := range scoped(files, q) {
			if re.MatchString(f) || !strings.Contains(q.Pattern, "/") && re.MatchString(f[strings.LastIndexByte(f, '/')+1:]) {
				n++
				if len(hit) < most {
					hit = append(hit, f)
				}
			}
		}
		if n == 0 {
			return "no files", nil
		}
		s := strings.Join(hit, "\n")
		if n > len(hit) {
			s += fmt.Sprintf("\n… %d files in all", n)
		}
		return s, nil
	case "text", "regex":
		pat := q.Pattern
		if q.Kind == "text" {
			pat = regexp.QuoteMeta(pat)
		}
		if q.IgnoreCase {
			pat = "(?i)" + pat
		}
		// ^ and $ are a line's: a file is first matched whole, so it is
		// skipped only when no line of it can match
		re, err := regexp.Compile("(?m)" + pat)
		if err != nil {
			return "", fmt.Errorf("regex: %w", err)
		}
		files, err := list()
		if err != nil {
			return "", err
		}
		return grep(dir, scoped(files, q), re, most, min(max(q.Context, 0), 5)), nil
	}
	return "", fmt.Errorf("kind %q: text, regex, glob or git", q.Kind)
}

// scoped is the files under q's path and matching q's glob.
func scoped(files []string, q query) []string {
	p := strings.Trim(filepath.ToSlash(q.Path), "/")
	p = strings.TrimPrefix(p, "./")
	var g *regexp.Regexp
	if q.Glob != "" && q.Kind != "glob" {
		g, _ = globRE(q.Glob)
	}
	if (p == "" || p == ".") && g == nil {
		return files
	}
	var out []string
	for _, f := range files {
		if p != "" && p != "." && f != p && !strings.HasPrefix(f, p+"/") {
			continue
		}
		if g != nil && !g.MatchString(f) && (strings.Contains(q.Glob, "/") || !g.MatchString(f[strings.LastIndexByte(f, '/')+1:])) {
			continue
		}
		out = append(out, f)
	}
	return out
}

type hit struct {
	file  string
	lines []string // already formatted
	n     int      // matches in the file
}

// grep searches files side by side, keeping them in their listed order.
func grep(dir string, files []string, re *regexp.Regexp, most, ctx int) string {
	hits := make([]*hit, len(files))
	var next int
	var mu sync.Mutex
	var found int
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if next >= len(files) || found >= most*4 {
					mu.Unlock()
					return
				}
				i := next
				next++
				mu.Unlock()
				h := grepFile(dir, files[i], re, ctx)
				if h != nil {
					mu.Lock()
					hits[i] = h
					found += h.n
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	var b strings.Builder
	shownN, total, inFiles := 0, 0, 0
	for _, h := range hits {
		if h == nil {
			continue
		}
		total += h.n
		inFiles++
		for _, l := range h.lines {
			if shownN >= most {
				break
			}
			b.WriteString(l)
			b.WriteByte('\n')
			if !strings.HasPrefix(l, "  ") && l != "--" {
				shownN++
			}
		}
	}
	if total == 0 {
		return "no matches"
	}
	if total > shownN {
		fmt.Fprintf(&b, "… %d matches in %d files", total, inFiles)
		if next < len(files) {
			b.WriteString(" (search stopped early)")
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func grepFile(dir, rel string, re *regexp.Regexp, ctx int) *hit {
	p := filepath.Join(dir, filepath.FromSlash(rel))
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxSearchFile {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil || bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		return nil
	}
	// a CRLF line's $ is before its \r
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	if !re.Match(b) {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	h := &hit{file: rel}
	last := -1
	for i, l := range lines {
		if !re.MatchString(l) {
			continue
		}
		h.n++
		if ctx > 0 {
			from := max(i-ctx, last+1)
			if last >= 0 && from > last+1 {
				h.lines = append(h.lines, "--")
			}
			for j := from; j < i; j++ {
				h.lines = append(h.lines, fmt.Sprintf("  %s:%d- %s", rel, j+1, cutLine(lines[j])))
			}
		}
		h.lines = append(h.lines, fmt.Sprintf("%s:%d: %s", rel, i+1, cutLine(l)))
		last = i
		if ctx > 0 {
			for j := i + 1; j <= min(i+ctx, len(lines)-1); j++ {
				if re.MatchString(lines[j]) {
					break
				}
				h.lines = append(h.lines, fmt.Sprintf("  %s:%d- %s", rel, j+1, cutLine(lines[j])))
				last = j
			}
		}
	}
	return h
}

func cutLine(l string) string {
	l = strings.TrimRight(l, "\r")
	if len(l) <= lineCut {
		return l
	}
	cut := lineCut
	for cut > 0 && !utf8Start(l[cut]) {
		cut--
	}
	return l[:cut] + "…"
}

// skipDirs aren't walked when the folder isn't a git checkout.
var skipDirs = []string{".git", "node_modules", ".hg", ".svn", ".venv", "venv", "__pycache__", ".next", ".turbo", ".cache", "target", "dist", "build", ".gradle", ".idea", "Pods", "DerivedData"}

// listFiles is the files under dir, slash-separated and relative: what git
// tracks and doesn't ignore in a checkout, the folder walked otherwise.
func listFiles(dir string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := proc.CommandContext(ctx, "git", "-C", dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Env = gitEnv()
	if out, err := cmd.Output(); err == nil {
		var files []string
		for _, f := range bytes.Split(out, []byte{0}) {
			if len(f) > 0 {
				files = append(files, string(f))
			}
			if len(files) >= maxListed {
				break
			}
		}
		// a file git still lists but that's deleted isn't searched
		return slices.Compact(files), nil
	}
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if p != dir && slices.Contains(skipDirs, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		r, _ := filepath.Rel(dir, p)
		files = append(files, filepath.ToSlash(r))
		if len(files) >= maxListed {
			return fs.SkipAll
		}
		return nil
	})
	return files, err
}

// globRE is glob as a regexp over a slash-separated path: * and ? within a
// name, ** across folders, {a,b} for either.
func globRE(glob string) (*regexp.Regexp, error) {
	glob = strings.TrimPrefix(filepath.ToSlash(glob), "./")
	var b strings.Builder
	b.WriteString("^")
	inBrace := false
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*' && i+1 < len(glob) && glob[i+1] == '*':
			i++
			if i+1 < len(glob) && glob[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '{':
			inBrace = true
			b.WriteString("(?:")
		case c == '}' && inBrace:
			inBrace = false
			b.WriteString(")")
		case c == ',' && inBrace:
			b.WriteString("|")
		case c == '[':
			j := strings.IndexByte(glob[i:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			cls := glob[i+1 : i+j]
			if strings.HasPrefix(cls, "!") {
				cls = "^" + cls[1:]
			}
			b.WriteString("[" + cls + "]")
			i += j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("glob %q: %w", glob, err)
	}
	return re, nil
}

// gitRead are the git commands a git query runs: ones that only read.
var gitRead = []string{"status", "diff", "log", "show", "blame", "ls-files", "grep", "branch", "rev-parse", "shortlog", "describe", "ls-tree", "cat-file", "merge-base", "reflog", "tag"}

// gitNever are options that would have a read-only command write a file or
// run a program: a pager, an external diff, a text conversion.
var gitNever = []string{"--output", "-O", "--open-files-in-pager", "--ext-diff", "--textconv", "--filters", "--exec", "--upload-pack"}

// gitListing are the options branch and tag take only to list; a word that
// isn't an option names a branch or tag to make, unless one of listWith
// says it is a pattern or a commit to list by.
var (
	gitListing = map[string][]string{
		"branch": {"-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "-l", "--list", "--contains", "--no-contains", "--merged", "--no-merged", "--points-at", "--show-current", "--sort", "--format", "--no-color", "--column", "--no-column", "-i", "--ignore-case"},
		"tag":    {"-l", "--list", "-n", "--contains", "--no-contains", "--merged", "--no-merged", "--points-at", "--sort", "--format", "--no-color", "--column", "--no-column", "-i", "--ignore-case"},
	}
	listWith = []string{"-l", "--list", "--contains", "--no-contains", "--merged", "--no-merged", "--points-at", "--sort", "--format"}
)

// onlyListing says why git sub args would change something, for branch,
// tag and reflog, which list but can also make, move and delete.
func onlyListing(sub string, args []string) error {
	if sub == "reflog" {
		if len(args) > 0 && (args[0] == "expire" || args[0] == "delete") {
			return fmt.Errorf("git reflog %s: only listing", args[0])
		}
		return nil
	}
	allowed, ok := gitListing[sub]
	if !ok {
		return nil
	}
	pattern := false
	for _, w := range args {
		k, _, _ := strings.Cut(w, "=")
		if slices.Contains(listWith, k) {
			pattern = true
		}
	}
	for _, w := range args {
		if !strings.HasPrefix(w, "-") {
			if !pattern {
				return fmt.Errorf("git %s %s: only listing (add --list to list by a pattern)", sub, w)
			}
			continue
		}
		k, _, _ := strings.Cut(w, "=")
		if !slices.Contains(allowed, k) && !(sub == "tag" && strings.HasPrefix(w, "-n")) {
			return fmt.Errorf("git %s %s: only listing", sub, w)
		}
	}
	return nil
}

func gitQuery(dir, args string) (string, error) {
	words, err := splitWords(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), "git ")))
	if err != nil {
		return "", err
	}
	if len(words) == 0 {
		return "", fmt.Errorf("no git command")
	}
	sub := words[0]
	if !slices.Contains(gitRead, sub) {
		return "", fmt.Errorf("git %s: only read-only commands (%s)", sub, strings.Join(gitRead, ", "))
	}
	for _, w := range words[1:] {
		if w == "--" {
			break
		}
		for _, n := range gitNever {
			if w == n || strings.HasPrefix(w, n+"=") || len(n) == 2 && strings.HasPrefix(w, n) && !strings.HasPrefix(w, "--") {
				return "", fmt.Errorf("git %s %s: not in a read-only search", sub, w)
			}
		}
	}
	if err := onlyListing(sub, words[1:]); err != nil {
		return "", err
	}
	full := []string{"--no-pager", "-C", dir, sub}
	if sub == "diff" || sub == "log" || sub == "show" {
		full = append(full, "--no-ext-diff", "--no-textconv", "--no-color")
	}
	if sub == "grep" {
		full = append(full, "-n", "--no-color")
	}
	full = append(full, words[1:]...)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := proc.CommandContext(ctx, "git", full...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	s := strings.TrimRight(string(out), "\n")
	if err != nil {
		if s == "" {
			return "", err
		}
		if sub == "grep" && strings.TrimSpace(s) == "" {
			return "no matches", nil
		}
		return "", fmt.Errorf("%v: %s", err, s)
	}
	if s == "" {
		return "(nothing)", nil
	}
	return s, nil
}

// gitEnv is git's environment with nothing that would run a program.
func gitEnv() []string {
	env := []string{"GIT_PAGER=cat", "PAGER=cat", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "GIT_PAGER", "PAGER", "GIT_EXTERNAL_DIFF", "GIT_TERMINAL_PROMPT", "GIT_OPTIONAL_LOCKS", "GIT_EDITOR", "GIT_SSH_COMMAND":
			continue
		}
		env = append(env, e)
	}
	return env
}

// splitWords reads s as a shell would its words: '…' and "…" quote, a
// backslash escapes the next character. Nothing is expanded.
func splitWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	in := false
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else if c == '\\' && q == '"' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			q = c
			in = true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			in = true
		case c == ' ' || c == '\t' || c == '\n':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		case strings.IndexByte("|;&<>`$", c) >= 0:
			return nil, fmt.Errorf("git arguments are words only, not shell (%q)", string(c))
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if q != 0 {
		return nil, fmt.Errorf("an unclosed quote")
	}
	if in {
		words = append(words, cur.String())
	}
	return words, nil
}
