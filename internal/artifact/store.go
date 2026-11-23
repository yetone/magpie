// Package artifact keeps Magpie Artifacts: pages an agent makes for the
// user to look at or use (a report, a chart, a small app), kept on this
// computer with every version, and served by the gateway at /a/<id>/.
//
// Claude Code's own Artifact tool keeps its pages with the claude.ai
// account that made them, so a page made before magpie switched the
// Claude account doesn't open under the next one. These are magpie's,
// whichever account or agent made them.
//
// An artifact is a folder under Dir named by its id, a random,
// unguessable name: meta.json says what it is and lists its versions,
// and v<N>/ holds version N's files, the page as index.html beside the
// files it uses. A version is written whole into a folder of its own and
// never changed after; a publish that leaves a file out keeps the one
// before (linked, or copied where links can't be made).
package artifact

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/lazyre"
)

// Dir is the folder artifacts are kept in; a variable for tests.
var Dir = func() string { return appdir.Join(appdir.Config(), "artifacts") }

// Page is the name a version's page is kept and served under.
const Page = "index.html"

const (
	metaName = "meta.json"
	// MaxPage is the most a page may hold, MaxFile a file beside it, and
	// MaxVersion a version's files together (the page included)
	MaxPage    = 16 << 20
	MaxFile    = 16 << 20
	MaxVersion = 64 << 20
	// MaxFiles is the most files a version holds besides its page
	MaxFiles = 255
	// maxTitle and maxDescription are in characters
	maxTitle       = 200
	maxDescription = 1000
	maxLabel       = 60
)

// Artifact is one artifact as meta.json keeps it.
type Artifact struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	Versions    []Version `json:"versions"`
}

// Version is one publish of an artifact.
type Version struct {
	N     int       `json:"n"`
	At    time.Time `json:"at"`
	Label string    `json:"label,omitempty"`
	// Agent is who published it, as the gateway knew the caller
	Agent string `json:"agent,omitempty"`
	// Files are the version's files besides the page, by published path
	Files []string `json:"files,omitempty"`
	Size  int64    `json:"size"`
}

// Latest is the newest version.
func (a *Artifact) Latest() *Version {
	if len(a.Versions) == 0 {
		return nil
	}
	return &a.Versions[len(a.Versions)-1]
}

// Version is version n, nil when there is none; 0 is the newest.
func (a *Artifact) Version(n int) *Version {
	if n == 0 {
		return a.Latest()
	}
	for i := range a.Versions {
		if a.Versions[i].N == n {
			return &a.Versions[i]
		}
	}
	return nil
}

// Publish is what a publish asks for. With ID empty a new artifact is
// made; with it, the artifact gets a new version.
type Publish struct {
	ID          string
	Title       string // used when the page has no <title> of its own
	Description string
	Label       string
	Agent       string
	// Page is the page itself; on an update nil keeps the one before
	Page []byte
	// Files are the files beside the page, by published path; on an
	// update the files left out are kept, and a nil value removes one
	Files map[string][]byte
	// Base, when set, is the version the publisher last read: a publish
	// over a newer one is refused (ErrConflict)
	Base int
}

var (
	ErrNotFound = errors.New("no such artifact")
	// ErrConflict: the artifact has a version newer than the one the
	// publish was built on
	ErrConflict = errors.New("the artifact has a newer version than the one read")
)

var mu sync.Mutex

// lock holds the store against this process and another magpie.
func lock() func() {
	mu.Lock()
	unlock, _ := filelock.Lock(filepath.Join(Dir(), ".lock"), filelock.Wait)
	return func() { unlock(); mu.Unlock() }
}

var idOK = lazyre.New(`^[a-z2-7]{26}$`)

// ValidID is whether id is one an artifact can have.
func ValidID(id string) bool { return idOK.MatchString(id) }

// newID is 128 random bits, in lowercase base32.
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

func folder(id string) string { return filepath.Join(Dir(), id) }

func versionDir(id string, n int) string { return filepath.Join(folder(id), "v"+strconv.Itoa(n)) }

func readMeta(id string) (*Artifact, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(folder(id), metaName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a := &Artifact{}
	if err := json.Unmarshal(b, a); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(folder(id), metaName), err)
	}
	if a.ID != id || len(a.Versions) == 0 {
		return nil, fmt.Errorf("%s doesn't describe artifact %s", filepath.Join(folder(id), metaName), id)
	}
	return a, nil
}

func writeMeta(a *Artifact) error {
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(filepath.Join(folder(a.ID), metaName), b)
}

// Get is the artifact id.
func Get(id string) (*Artifact, error) { return readMeta(id) }

// List is every artifact, the most recently updated first. One whose
// meta.json can't be read is left out, and said in the error beside the
// rest.
func List() ([]*Artifact, error) {
	ents, err := os.ReadDir(Dir())
	if errors.Is(err, fs.ErrNotExist) {
		return []*Artifact{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []*Artifact{}
	var bad []error
	for _, e := range ents {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		a, err := readMeta(e.Name())
		if errors.Is(err, ErrNotFound) {
			continue // a publish half made, or a delete half done
		}
		if err != nil {
			bad = append(bad, err)
			continue
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		return out[i].ID < out[j].ID
	})
	return out, errors.Join(bad...)
}

// Delete takes the artifact and every version of it away.
func Delete(id string) error {
	defer lock()()
	if _, err := readMeta(id); err != nil {
		return err
	}
	// meta.json goes first: a delete cut short leaves no artifact listed
	if err := os.Remove(filepath.Join(folder(id), metaName)); err != nil {
		return err
	}
	return os.RemoveAll(folder(id))
}

// CleanPath is a published path as kept: forward slashes, relative,
// nothing that climbs out or names the page; an error says why not.
func CleanPath(p string) (string, error) {
	q := strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if q == "" || strings.HasPrefix(q, "/") || strings.ContainsAny(q, "\x00\r\n") || !utf8.ValidString(q) ||
		(len(q) > 1 && q[1] == ':') {
		return "", fmt.Errorf("%q isn't a relative path", p)
	}
	c := path.Clean(q)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || len(c) > 512 {
		return "", fmt.Errorf("%q isn't a path inside the artifact", p)
	}
	for _, part := range strings.Split(c, "/") {
		if strings.HasPrefix(part, ".") && part != "." {
			return "", fmt.Errorf("%q: a hidden name isn't published", p)
		}
	}
	if c == Page {
		return "", fmt.Errorf("%s is the page itself: publish it as the page", Page)
	}
	return c, nil
}

// PublishPage writes a new artifact, or a new version of one.
func PublishPage(p Publish) (*Artifact, error) {
	if p.ID == "" && p.Page == nil {
		return nil, errors.New("a new artifact needs its page")
	}
	if len(p.Page) > MaxPage {
		return nil, fmt.Errorf("the page is %d bytes; the most is %d", len(p.Page), MaxPage)
	}
	if p.Page != nil && !utf8.Valid(p.Page) {
		return nil, errors.New("the page isn't UTF-8 text")
	}
	files := map[string][]byte{}
	removed := map[string]bool{}
	for k, v := range p.Files {
		c, err := CleanPath(k)
		if err != nil {
			return nil, err
		}
		if v == nil {
			removed[c] = true
			continue
		}
		if len(v) > MaxFile {
			return nil, fmt.Errorf("%s is %d bytes; the most a file may be is %d", c, len(v), MaxFile)
		}
		files[c] = v
	}
	label := oneLine(p.Label, maxLabel)
	desc := oneLine(p.Description, maxDescription)

	defer lock()()
	now := time.Now().UTC().Truncate(time.Millisecond)
	var a *Artifact
	var prev *Version
	if p.ID == "" {
		a = &Artifact{ID: newID(), Created: now}
	} else {
		var err error
		if a, err = readMeta(p.ID); err != nil {
			return nil, err
		}
		prev = a.Latest()
		if p.Base > 0 && p.Base != prev.N {
			return nil, fmt.Errorf("%w: version %d is the newest, and the publish was built on %d", ErrConflict, prev.N, p.Base)
		}
	}
	n := 1
	if prev != nil {
		n = prev.N + 1
	}
	// what the version holds: the files kept from the one before, then
	// those given now
	keep := []string{}
	if prev != nil {
		for _, f := range prev.Files {
			if _, given := files[f]; !given && !removed[f] {
				keep = append(keep, f)
			}
		}
	}
	all := append(slices.Clone(keep), mapKeys(files)...)
	slices.Sort(all)
	all = slices.Compact(all)
	if len(all) > MaxFiles {
		return nil, fmt.Errorf("a version holds at most %d files besides its page; this one would hold %d", MaxFiles, len(all))
	}
	// a file and a folder of the same name can't both be there
	for i := 1; i < len(all); i++ {
		if strings.HasPrefix(all[i], all[i-1]+"/") {
			return nil, fmt.Errorf("%s is a file, so %s can't be inside it", all[i-1], all[i])
		}
	}

	tmp := filepath.Join(folder(a.ID), fmt.Sprintf(".v%d-%s", n, newID()[:8]))
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	done := false
	defer func() {
		if !done {
			os.RemoveAll(tmp)
		}
	}()
	var size int64
	page := p.Page
	if page == nil {
		if err := carry(versionDir(a.ID, prev.N), tmp, Page); err != nil {
			return nil, fmt.Errorf("keeping the page of version %d: %w", prev.N, err)
		}
		fi, err := os.Stat(filepath.Join(tmp, Page))
		if err != nil {
			return nil, err
		}
		size += fi.Size()
		if page, err = os.ReadFile(filepath.Join(tmp, Page)); err != nil {
			return nil, err
		}
	} else {
		if err := os.WriteFile(filepath.Join(tmp, Page), page, 0o600); err != nil {
			return nil, err
		}
		size += int64(len(page))
	}
	for _, f := range keep {
		if err := carry(versionDir(a.ID, prev.N), tmp, f); err != nil {
			return nil, fmt.Errorf("keeping %s of version %d: %w", f, prev.N, err)
		}
		if fi, err := os.Stat(filepath.Join(tmp, filepath.FromSlash(f))); err == nil {
			size += fi.Size()
		}
	}
	for f, b := range files {
		dst := filepath.Join(tmp, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return nil, err
		}
		size += int64(len(b))
	}
	if size > MaxVersion {
		return nil, fmt.Errorf("the version would be %d bytes; the most is %d", size, MaxVersion)
	}
	if err := os.Rename(tmp, versionDir(a.ID, n)); err != nil {
		return nil, err
	}
	done = true

	title := PageTitle(page)
	if title == "" {
		title = oneLine(p.Title, maxTitle)
	}
	if title == "" && prev != nil {
		title = a.Title
	}
	if title == "" {
		title = "Untitled"
	}
	a.Title = title
	if desc != "" || prev == nil {
		a.Description = desc
	}
	a.Updated = now
	a.Versions = append(a.Versions, Version{N: n, At: now, Label: label, Agent: oneLine(p.Agent, 64), Files: all, Size: size})
	if err := writeMeta(a); err != nil {
		os.RemoveAll(versionDir(a.ID, n))
		return nil, err
	}
	return a, nil
}

// carry puts version src's file name into dst: a link where the file
// system makes one, a copy where it doesn't. A version's files never
// change, so a link is as good as a copy.
func carry(src, dst, name string) error {
	from := filepath.Join(src, filepath.FromSlash(name))
	to := filepath.Join(dst, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	if os.Link(from, to) == nil {
		return nil
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// File is where version n's (0: the newest) file name is on disk; name
// "" or Page is the page.
func File(id string, n int, name string) (string, *Artifact, error) {
	a, err := readMeta(id)
	if err != nil {
		return "", nil, err
	}
	v := a.Version(n)
	if v == nil {
		return "", a, fmt.Errorf("%w: artifact %s has no version %d", ErrNotFound, id, n)
	}
	if name == "" || name == Page {
		return filepath.Join(versionDir(id, v.N), Page), a, nil
	}
	c, err := CleanPath(name)
	if err != nil || !slices.Contains(v.Files, c) {
		return "", a, fmt.Errorf("%w: version %d of %s has no file %s", ErrNotFound, v.N, id, name)
	}
	return filepath.Join(versionDir(id, v.N), filepath.FromSlash(c)), a, nil
}

var titleRe = lazyre.New(`(?is)<title[^>]*>(.*?)</title>`)

// PageTitle is the text of the page's <title>, "" when it has none.
func PageTitle(page []byte) string {
	m := titleRe.FindSubmatch(page)
	if m == nil {
		return ""
	}
	return oneLine(html.UnescapeString(string(m[1])), maxTitle)
}

var doctypeRe = lazyre.New(`(?i)^\s*(<!--.*?-->\s*)*<!doctype\s`)

// Served is the page as a browser is given it: one written as a fragment,
// with no doctype, is put in a document of its own with a charset, a
// viewport and the base styles a page without them lacks, so it doesn't
// render in quirks mode with the browser's defaults. A page with its
// doctype is given as written.
func Served(page []byte) []byte {
	if doctypeRe.Match(page) {
		return page
	}
	low := bytes.ToLower(page)
	if bytes.Contains(low, []byte("<html")) {
		return append([]byte("<!doctype html>\n"), page...)
	}
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html><head><meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<style>:root{color-scheme:light dark}body{margin:0;padding:16px;font:16px/1.5 system-ui,-apple-system,\"Segoe UI\",sans-serif}</style>\n")
	if !bytes.Contains(low, []byte("<title")) {
		b.WriteString("<title>Artifact</title>\n")
	}
	b.WriteString("</head><body>\n")
	b.Write(page)
	b.WriteString("\n</body></html>\n")
	return b.Bytes()
}

// oneLine is s on one line, trimmed, cut to most characters.
func oneLine(s string, most int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > most {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:most-1])) + "…"
	}
	return s
}

func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
