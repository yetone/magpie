package artifact

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/edit"
)

// Comments (docs/subsystems/artifacts.md, "Comments"): threads people
// leave on an artifact's page, each at a spot on it, as claude.ai's
// artifact shell keeps them. The viewer shell (shell.js) shows and
// writes them; a page that declares comments writes them too, through
// its runtime. A thread can be sent to an agent, which changes the page
// and answers in the thread. They are kept in comments.json, beside the
// artifact's meta.json, and go with it.

const (
	commentsName = "comments.json"
	// MaxCommentText is the most a comment's text holds, in bytes
	MaxCommentText = 4096
	// MaxThreads is the most threads an artifact keeps, MaxReplies the
	// most comments a thread does
	MaxThreads     = 500
	MaxReplies     = 200
	maxThreadLabel = 128
	maxDetail      = 512
	maxQuote       = 512
	maxAnchor      = 1024
)

// Anchor is where a thread sits on the page: an element, by the path the
// shell's click or the page's anchorFor makes, and a point in it (x, y,
// each 0 to 1 of the element's box). Name is a page's own name for the
// spot (customAnchors), when it places its pins itself.
type Anchor struct {
	Path string  `json:"path,omitempty"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Name string  `json:"name,omitempty"`
}

// Comment is one comment in a thread.
type Comment struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	// Agent, for a comment an agent wrote: who (claude, codex, …)
	Agent string    `json:"agent,omitempty"`
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
	// Sent: the comment was sent to an agent
	Sent bool `json:"sent,omitempty"`
}

// Run is an agent at work on a thread's request.
type Run struct {
	Agent string `json:"agent"`
	// State is working, done or failed
	State string    `json:"state"`
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at"`
	// Version is the version the run published, when it did
	Version int `json:"version,omitempty"`
}

// Thread is a comment and its replies, at one spot on the page.
type Thread struct {
	ID string `json:"id"`
	// Anchor is nil for a comment on the artifact as a whole
	Anchor *Anchor `json:"anchor,omitempty"`
	// Label names the spot in a few words; Detail says more, for the
	// agent; Quote is the text the person selected there
	Label  string `json:"label,omitempty"`
	Detail string `json:"detail,omitempty"`
	Quote  string `json:"quote,omitempty"`
	// Version is the version the thread was started on
	Version  int       `json:"version"`
	Author   string    `json:"author"`
	Created  time.Time `json:"created"`
	Resolved bool      `json:"resolved,omitempty"`
	Comments []Comment `json:"comments"`
	Run      *Run      `json:"run,omitempty"`
}

// CommentError is a comment call refused, with the contract's code.
type CommentError struct {
	Code, Message string
}

func (e *CommentError) Error() string { return e.Code + ": " + e.Message }

func commentInvalid(f string, a ...any) error {
	return &CommentError{Code: "invalid", Message: fmt.Sprintf(f, a...)}
}

// CheckCommentText is the contract's rule for a comment's text: plain
// text, not empty, at most 4 KiB, no control characters but newlines
// and tabs.
func CheckCommentText(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", commentInvalid("a comment has text")
	}
	if len(s) > MaxCommentText {
		return "", commentInvalid("a comment is at most %d bytes as UTF-8; this one is %d", MaxCommentText, len(s))
	}
	if !utf8.ValidString(s) {
		return "", commentInvalid("a comment is UTF-8 text")
	}
	for _, r := range s {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return "", commentInvalid("a comment has no control characters but newlines and tabs")
		}
	}
	return s, nil
}

// plainLine is s cut to most bytes, on a rune, with control characters
// made spaces.
func plainLine(s string, most int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
	for len(s) > most {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// CheckAnchor is a's own copy, checked: a path or a name, and a point in
// the element's box.
func CheckAnchor(a *Anchor) (*Anchor, error) {
	if a == nil {
		return nil, nil
	}
	c := *a
	if c.Path == "" && c.Name == "" {
		return nil, commentInvalid("an anchor has the path anchorFor gave")
	}
	if len(c.Path) > maxAnchor || len(c.Name) > 256 {
		return nil, commentInvalid("an anchor's path is at most %d bytes, its name 256", maxAnchor)
	}
	for _, s := range []string{c.Path, c.Name} {
		if strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == '\u200b' || r == '\ufeff' }) >= 0 || !utf8.ValidString(s) {
			return nil, commentInvalid("an anchor has no control or invisible characters")
		}
	}
	for _, v := range []float64{c.X, c.Y} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, commentInvalid("an anchor's x and y are numbers")
		}
	}
	c.X, c.Y = min(max(c.X, 0), 1), min(max(c.Y, 0), 1)
	return &c, nil
}

// NewThread is what starts a thread.
type NewThread struct {
	Anchor               *Anchor
	Label, Detail, Quote string
	Version              int
	Author, Agent, Text  string
	Sent                 bool
}

// ShellToken is the token artifact id's viewer shell holds: it reads and
// writes the artifact's comments, and nothing else. The page holds its
// own (PageToken), which the shell's never is.
func ShellToken(id string) (string, error) {
	k, err := pageSecret()
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte("artifact-shell\x00" + id))
	return hex.EncodeToString(m.Sum(nil)), nil
}

// ShellTokenOK is whether t is artifact id's shell token.
func ShellTokenOK(id, t string) bool {
	want, err := ShellToken(id)
	return err == nil && t != "" && hmac.Equal([]byte(want), []byte(t))
}

func commentsPath(id string) string { return filepath.Join(folder(id), commentsName) }

// readThreads is the artifact's threads; none when it has no file yet.
func readThreads(id string) ([]Thread, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(commentsPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(folder(id), metaName)); err != nil {
			return nil, ErrNotFound
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Threads []Thread `json:"threads"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		// unknown, not empty: a write over it would lose every thread
		return nil, fmt.Errorf("%s: %w", commentsPath(id), err)
	}
	return f.Threads, nil
}

func writeThreads(id string, ts []Thread) error {
	if ts == nil {
		ts = []Thread{}
	}
	b, err := json.MarshalIndent(map[string]any{"threads": ts}, "", "  ")
	if err != nil {
		return err
	}
	if err := edit.WriteAtomic(commentsPath(id), b); err != nil {
		return err
	}
	os.Chmod(commentsPath(id), 0o600)
	tell(Change{Artifact: id, Comments: true})
	return nil
}

// Threads is the artifact's threads, the oldest first.
func Threads(id string) ([]Thread, error) {
	return readThreads(id)
}

// CommentsStamp moves whenever the artifact's comments are written, by
// this process or another: their file's time and size.
func CommentsStamp(id string) string {
	fi, err := os.Stat(commentsPath(id))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.ModTime().UnixNano(), fi.Size())
}

// changeThreads reads the threads, lets fn change them and writes them,
// under the store's lock.
func changeThreads(id string, fn func([]Thread) ([]Thread, error)) error {
	defer lock()()
	if _, err := readMeta(id); err != nil {
		return err
	}
	ts, err := readThreads(id)
	if err != nil {
		return err
	}
	if ts, err = fn(ts); err != nil {
		return err
	}
	return writeThreads(id, ts)
}

func threadAt(ts []Thread, tid string) int {
	return slices.IndexFunc(ts, func(t Thread) bool { return t.ID == tid })
}

func notFound(what string) error {
	return &CommentError{Code: "not_found", Message: "no such " + what}
}

// StartThread starts a thread on artifact id with its first comment.
func StartThread(id string, n NewThread) (Thread, error) {
	text, err := CheckCommentText(n.Text)
	if err != nil {
		return Thread{}, err
	}
	anchor, err := CheckAnchor(n.Anchor)
	if err != nil {
		return Thread{}, err
	}
	now := time.Now().UTC()
	t := Thread{
		ID: "t_" + randomID(), Anchor: anchor, Version: n.Version, Author: n.Author, Created: now,
		Label: plainLine(n.Label, maxThreadLabel), Detail: plainLine(n.Detail, maxDetail), Quote: plainLine(n.Quote, maxQuote),
		Comments: []Comment{{ID: "c_" + randomID(), Author: n.Author, Agent: n.Agent, Text: text, At: now, Sent: n.Sent}},
	}
	err = changeThreads(id, func(ts []Thread) ([]Thread, error) {
		if len(ts) >= MaxThreads {
			return nil, &CommentError{Code: "upstream_error", Message: fmt.Sprintf("an artifact keeps at most %d threads; resolve or delete some", MaxThreads)}
		}
		return append(ts, t), nil
	})
	return t, err
}

// Reply adds a comment to thread tid; agent names the agent that wrote
// it, if one did.
func Reply(id, tid, author, agent, text string, sent bool) (Comment, error) {
	text, err := CheckCommentText(text)
	if err != nil {
		return Comment{}, err
	}
	c := Comment{ID: "c_" + randomID(), Author: author, Agent: agent, Text: text, At: time.Now().UTC(), Sent: sent}
	err = changeThreads(id, func(ts []Thread) ([]Thread, error) {
		i := threadAt(ts, tid)
		if i < 0 {
			return nil, notFound("thread")
		}
		if len(ts[i].Comments) >= MaxReplies {
			return nil, &CommentError{Code: "upstream_error", Message: fmt.Sprintf("a thread keeps at most %d comments", MaxReplies)}
		}
		ts[i].Comments = append(ts[i].Comments, c)
		if agent == "" && ts[i].Resolved {
			// a person writing in a resolved thread opens it again
			ts[i].Resolved = false
		}
		return ts, nil
	})
	return c, err
}

// Resolve marks thread tid resolved, or open again.
func Resolve(id, tid string, resolved bool) error {
	return changeThreads(id, func(ts []Thread) ([]Thread, error) {
		i := threadAt(ts, tid)
		if i < 0 {
			return nil, notFound("thread")
		}
		ts[i].Resolved = resolved
		return ts, nil
	})
}

// DeleteThread deletes thread tid and every comment in it.
func DeleteThread(id, tid string) error {
	return changeThreads(id, func(ts []Thread) ([]Thread, error) {
		i := threadAt(ts, tid)
		if i < 0 {
			return nil, notFound("thread")
		}
		return slices.Delete(ts, i, i+1), nil
	})
}

// DeleteComment deletes one comment of thread tid; the thread goes with
// its last.
func DeleteComment(id, tid, cid string) error {
	return changeThreads(id, func(ts []Thread) ([]Thread, error) {
		i := threadAt(ts, tid)
		if i < 0 {
			return nil, notFound("thread")
		}
		j := slices.IndexFunc(ts[i].Comments, func(c Comment) bool { return c.ID == cid })
		if j < 0 {
			return nil, notFound("comment")
		}
		ts[i].Comments = slices.Delete(ts[i].Comments, j, j+1)
		if len(ts[i].Comments) == 0 {
			return slices.Delete(ts, i, i+1), nil
		}
		return ts, nil
	})
}

// SetRun records an agent's run on thread tid; nil clears it.
func SetRun(id, tid string, r *Run) error {
	return changeThreads(id, func(ts []Thread) ([]Thread, error) {
		i := threadAt(ts, tid)
		if i < 0 {
			return nil, notFound("thread")
		}
		ts[i].Run = r
		return ts, nil
	})
}

// GetThread is thread tid.
func GetThread(id, tid string) (Thread, error) {
	ts, err := readThreads(id)
	if err != nil {
		return Thread{}, err
	}
	i := threadAt(ts, tid)
	if i < 0 {
		return Thread{}, notFound("thread")
	}
	return ts[i], nil
}

// randomID is 80 random bits in lowercase base32.
func randomID() string { return newID()[:16] }
