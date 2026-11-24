package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
)

// Sharing (docs/subsystems/artifacts.md, "Sharing"): the user, never an
// agent, shares an artifact on usemagpie.ai (site/shares.js), where
// anyone with its link opens it. The link shows the version shared, and
// each new version published after (ShareLatest), until the user stops
// sharing (Unshare) or deletes the artifact, which takes the link down
// first.
//
// share.json, beside meta.json and readable by the user alone, keeps the
// link and the token that updates it or takes it down: usemagpie.ai keeps
// the token only hashed, so a lost share.json leaves the link up for
// good, short of a report.

const shareName = "share.json"

// MaxShared is the most a shared version's files hold together, as
// usemagpie.ai takes (site/shares.js MAX_BYTES).
const MaxShared = 32 << 20

// ShareSite is where artifacts are shared. MAGPIE_SHARE_SITE moves it,
// and a test names its own: a test never shares on the real site.
var ShareSite = func() string {
	if s := os.Getenv("MAGPIE_SHARE_SITE"); s != "" {
		return strings.TrimRight(s, "/")
	}
	if testing.Testing() {
		return ""
	}
	return "https://usemagpie.ai"
}

// ErrSharedGone is a shared link usemagpie.ai took down on its side (a
// page reported): the artifact isn't shared any more.
var ErrSharedGone = errors.New("its shared link was taken down on usemagpie.ai")

// Share is an artifact's shared link, as share.json keeps it.
type Share struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Token string `json:"token"`
	// Version is the artifact's version the link shows
	Version int       `json:"version"`
	At      time.Time `json:"at"`
}

// shareMu is held over each share's talk with usemagpie.ai, so two at
// once (the user's click and an agent's publish) never share an artifact
// twice; nothing the store's own lock guards waits on it.
var shareMu sync.Mutex

// Shared is the artifact id's shared link, or nil when it isn't shared.
func Shared(id string) (*Share, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(folder(id), shareName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := &Share{}
	if err := json.Unmarshal(b, s); err != nil || s.Token == "" || s.URL == "" {
		// unreadable is unknown: never taken for "not shared", which
		// would share it again under a second link
		return nil, fmt.Errorf("%s doesn't read: %v", filepath.Join(folder(id), shareName), err)
	}
	return s, nil
}

func writeShare(id string, s *Share) error {
	defer lock()()
	if _, err := readMeta(id); err != nil {
		return err
	}
	if s == nil {
		err := os.Remove(filepath.Join(folder(id), shareName))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(folder(id), shareName)
	if err := edit.WriteAtomic(p, b); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// ShareLatest shares the artifact's newest version: at a new link, or,
// shared already, at its link in place of the version it showed.
func ShareLatest(ctx context.Context, id string) (*Share, error) {
	site := ShareSite()
	if site == "" {
		return nil, errors.New("sharing is off here")
	}
	shareMu.Lock()
	defer shareMu.Unlock()
	a, err := readMeta(id)
	if err != nil {
		return nil, err
	}
	old, err := Shared(id)
	if err != nil {
		return nil, err
	}
	v := a.Latest()
	if old != nil && old.Version == v.N {
		return old, nil
	}
	body, ctype, err := shareBody(a, v)
	if err != nil {
		return nil, err
	}
	method, u := http.MethodPost, site+"/api/shares"
	if old != nil {
		method, u = http.MethodPut, site+"/api/shares/"+old.ID
	}
	var got struct {
		ID      string `json:"id"`
		URL     string `json:"url"`
		Token   string `json:"token"`
		Version int    `json:"version"`
	}
	status, err := shareAsk(ctx, method, u, old, body, ctype, &got)
	if status == http.StatusNotFound && old != nil {
		// taken down there: not shared again behind the user's back
		if err := writeShare(id, nil); err != nil {
			return nil, err
		}
		return nil, ErrSharedGone
	}
	if err != nil {
		return nil, err
	}
	s := &Share{ID: got.ID, URL: got.URL, Token: got.Token, Version: v.N, At: time.Now().UTC()}
	if old != nil {
		s.ID, s.URL, s.Token = old.ID, old.URL, old.Token
	}
	if !validShareID(s.ID) || !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, site) || s.Token == "" {
		return nil, fmt.Errorf("usemagpie.ai's answer doesn't read: %+v", got)
	}
	if err := writeShare(id, s); err != nil {
		return nil, err
	}
	return s, nil
}

// Unshare takes the artifact's shared link down; one not shared, or taken
// down on usemagpie.ai already, is done.
func Unshare(ctx context.Context, id string) error {
	shareMu.Lock()
	defer shareMu.Unlock()
	return unshare(ctx, id)
}

func unshare(ctx context.Context, id string) error {
	s, err := Shared(id)
	if err != nil || s == nil {
		return err
	}
	site := ShareSite()
	if site == "" {
		return errors.New("sharing is off here")
	}
	status, err := shareAsk(ctx, http.MethodDelete, site+"/api/shares/"+s.ID, s, nil, "", nil)
	if err != nil && status != http.StatusNotFound {
		return err
	}
	return writeShare(id, nil)
}

func validShareID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

// shareBody is version v of a as site/shares.js reads it: a meta part,
// then part i holding files[i], the page first.
func shareBody(a *Artifact, v *Version) ([]byte, string, error) {
	if v.Size > MaxShared {
		return nil, "", fmt.Errorf("%s is %.1f MB with its files; a shared page holds %d MB at most", a.Title, float64(v.Size)/(1<<20), MaxShared>>20)
	}
	names := append([]string{Page}, v.Files...)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	meta, _ := json.Marshal(map[string]any{"title": a.Title, "description": a.Description, "files": names})
	if err := w.WriteField("meta", string(meta)); err != nil {
		return nil, "", err
	}
	for i, name := range names {
		b, err := os.ReadFile(filepath.Join(versionDir(a.ID, v.N), filepath.FromSlash(name)))
		if err != nil {
			return nil, "", err
		}
		// its pages run window.claude there too, with nothing served
		if name == Page {
			b = WithRuntime(Served(b), SharedRuntime())
		} else if htmlName(name) {
			b = WithRuntime(b, SharedRuntime())
		}
		part, err := w.CreateFormFile(fmt.Sprint(i), "file")
		if err != nil {
			return nil, "", err
		}
		part.Write(b)
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// shareAsk asks usemagpie.ai, with s's token when s is given, and reads
// its JSON answer into out. Its error says what the site said.
func shareAsk(ctx context.Context, method, u string, s *Share, body []byte, ctype string, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if s != nil {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("usemagpie.ai didn't answer: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return res.StatusCode, fmt.Errorf("usemagpie.ai: %s", e.Error)
		}
		return res.StatusCode, fmt.Errorf("usemagpie.ai answered %s", res.Status)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return res.StatusCode, fmt.Errorf("usemagpie.ai's answer doesn't read: %v", err)
		}
	}
	return res.StatusCode, nil
}

// htmlName is whether a file is served as a page (text/html).
func htmlName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".html") || strings.HasSuffix(n, ".htm")
}
