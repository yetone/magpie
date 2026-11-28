package davsync

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
)

const githubMaxFile = 64 << 20

// DefaultCommitName and DefaultCommitEmail are who GitHub sync's commits
// are by when the setup names no one (IceCodeNew, #4579). A commit sent
// with no author or committer is the token owner's, and GitHub counted
// every sync in their contribution graph, which goes by the author's
// email. This is GitHub Actions' bot user (id 41898282, type Bot, as
// api.github.com/users/github-actions%5Bbot%5D answers): an address of a
// bot no person can sign in as, so the commits are counted for no one,
// and one no other GitHub user can take, as a made-up
// <name>@users.noreply.github.com could be.
const (
	DefaultCommitName  = "github-actions[bot]"
	DefaultCommitEmail = "41898282+github-actions[bot]@users.noreply.github.com"
)

// commitEmailRe is an address's shape: something, @, a domain with a dot.
var commitEmailRe = lazyre.New(`^[^\s<>@]+@[^\s<>@.]+(\.[^\s<>@.]+)+$`)

// commitAs is who c's commits are by: its GitName and GitEmail, both or
// neither, none being the defaults. A GitHub App's bot is
// <slug>[bot] <<app id>+<slug>[bot]@users.noreply.github.com>; the token
// still does the writing, the App's key is never asked for.
func commitAs(c Config) (map[string]string, error) {
	name, email := strings.TrimSpace(c.GitName), strings.TrimSpace(c.GitEmail)
	switch {
	case name == "" && email == "":
		name, email = DefaultCommitName, DefaultCommitEmail
	case name == "" || email == "":
		return nil, errors.New("GitHub sync commits as a name and an email together: give both, or neither for " + DefaultCommitName)
	case strings.ContainsAny(name, "<>\x00\r\n"):
		return nil, errors.New("the name GitHub sync commits as can't have <, > or a line break in it")
	case !commitEmailRe.MatchString(email):
		return nil, fmt.Errorf("the email GitHub sync commits as must be an address, like %s, not %q", DefaultCommitEmail, email)
	}
	return map[string]string{"name": name, "email": email}, nil
}

// githubPart is the most of a backup one request sends. A blob goes up as
// base64 in a JSON body, a third bigger than the file, and GitHub refuses a
// body far under the 100 MB a blob can hold: a 50.8 MB backup (about 68 MB
// of body) got 422 "Sorry, your input was too large to process" (Zhang-Wei,
// Discord), while a 19 MB file's Contents PUT, about 25 MB of body, went
// through. GitHub documents no request size, so a part stays under the
// largest body seen accepted.
const githubPart = 16 << 20

// githubSplit names a backup sent in parts. Its file at the backup's path
// is a short list of the parts, each a blob in a folder beside it, all in
// the one commit; the list's own blob SHA is the version, and it names
// every part by its blob SHA, so a version pins one whole set. The list
// says it is format magpie-backup, version 2, with no key derivation, so a
// magpie before parts opens it as a newer magpie's backup and stops
// ("update magpie"), never as a damaged one (ErrCorrupt, ErrNotBackup) to
// rebuild or offer for replacing.
const githubSplit = "github-parts"

type githubParts struct {
	Format  string          `json:"format"`
	Version int             `json:"version"`
	Split   string          `json:"split"`
	Size    int64           `json:"size"`
	Parts   []githubPartRef `json:"parts"`
}

type githubPartRef struct {
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// partName is the backup's i-th part (from 0), beside it in magpie's folder.
func partName(i int) string { return file + ".parts/" + strconv.Itoa(i+1) }

// parseParts reads b as a list of parts: nil for a file that isn't one (a
// backup in one file), an error for a list that doesn't add up.
func parseParts(b []byte) (*githubParts, error) {
	if len(b) > 64<<10 {
		return nil, nil
	}
	var m githubParts
	if json.Unmarshal(b, &m) != nil || m.Split == "" {
		return nil, nil
	}
	if m.Format != "magpie-backup" || m.Version != 2 || m.Split != githubSplit {
		return nil, errors.New("the GitHub backup was sent in parts by a newer magpie; update magpie to read it")
	}
	total := int64(0)
	for _, p := range m.Parts {
		if len(p.SHA) != 40 || p.Size <= 0 || p.Size > githubPart {
			return nil, errors.New("the GitHub backup's list of parts is damaged")
		}
		total += p.Size
	}
	if len(m.Parts) < 2 || total != m.Size || total > githubMaxFile {
		return nil, errors.New("the GitHub backup's list of parts is damaged")
	}
	return &m, nil
}

type github struct {
	repo, prefix, branch, token string
	ready                       bool
	empty                       bool
	client                      *http.Client
	// who is every commit's author and committer (commitAs)
	who map[string]string
}

// newGitHub accepts github://owner/repo/prefix. Only GitHub's API receives
// the token; neither a download URL nor a configurable host is followed.
func newGitHub(c Config) (*github, error) {
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || !strings.EqualFold(u.Scheme, "github") || u.Host == "" ||
		u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("GitHub sync needs an address like github://owner/repo or github://owner/repo/folder")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	valid := func(s string) bool {
		if s == "" || s == "." || s == ".." {
			return false
		}
		for _, ch := range s {
			if ch != '-' && ch != '_' && ch != '.' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
				return false
			}
		}
		return true
	}
	if !valid(u.Host) || !valid(parts[0]) {
		return nil, errors.New("GitHub sync needs a repository in owner/repo form")
	}
	for _, p := range parts[1:] {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, "\\\x00\r\n") {
			return nil, errors.New("the GitHub folder must be a relative path without empty, . or .. components")
		}
	}
	branch := strings.TrimSpace(c.Branch)
	if strings.ContainsAny(branch, "\x00\r\n") || strings.Contains(branch, "..") {
		return nil, errors.New("the GitHub branch contains invalid characters")
	}
	if strings.ContainsAny(c.Password, " \t\r\n") {
		return nil, errors.New("the GitHub branch or token contains invalid whitespace")
	}
	who, err := commitAs(c)
	if err != nil {
		return nil, err
	}
	client := *syncClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.Host != "api.github.com" {
			return errors.New("GitHub sync refused a redirect outside api.github.com")
		}
		if len(via) >= 10 {
			return errors.New("GitHub sync stopped after too many redirects")
		}
		return nil
	}
	return &github{repo: u.Host + "/" + parts[0], prefix: strings.Join(parts[1:], "/"),
		branch: branch, token: c.Password, client: &client, who: who}, nil
}

// send calls the repository's API; a non-empty ref reads at that branch or
// commit.
func (g *github) send(ctx context.Context, method, apiPath string, body []byte, accept string, ref string) (*http.Response, error) {
	u := &url.URL{Scheme: "https", Host: "api.github.com", Path: "/repos/" + g.repo}
	if apiPath != "" {
		u.Path += "/" + apiPath
	}
	if ref != "" {
		q := url.Values{"ref": {ref}}
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "magpie-sync")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return g.client.Do(req)
}

func githubError(res *http.Response) error {
	if err := limited("GitHub", res.StatusCode, res.Header); err != nil {
		return err
	}
	if res.StatusCode == http.StatusForbidden && (res.Header.Get("X-RateLimit-Remaining") == "0" || res.Header.Get("Retry-After") != "") {
		after := retryAfter(res.Header.Get("Retry-After"))
		if after == 0 {
			if until, err := strconv.ParseInt(res.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				after = max(time.Until(time.Unix(until, 0)), 0)
			}
		}
		return &rateLimited{kind: "GitHub", status: res.StatusCode, after: after}
	}
	var e struct{ Message string }
	json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&e)
	switch res.StatusCode {
	case http.StatusUnauthorized:
		return errors.New("GitHub refused the sync token (HTTP 401): check that it has not expired or been revoked")
	case http.StatusForbidden:
		return fmt.Errorf("GitHub refused access (HTTP 403): the sync token needs Contents read and write permission; check repository rules too: %s", e.Message)
	case http.StatusNotFound:
		return errors.New("GitHub could not find the repository or branch (HTTP 404): check the name and the token's repository access")
	}
	return fmt.Errorf("GitHub sync: HTTP %d: %s", res.StatusCode, e.Message)
}

// prepare distinguishes a missing file from an inaccessible repository or
// branch before the sync considers creating its first backup.
func (g *github) prepare(ctx context.Context) error {
	if g.ready {
		return nil
	}
	res, err := g.send(ctx, http.MethodGet, "", nil, "application/vnd.github+json", "")
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		defer res.Body.Close()
		return githubError(res)
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
		Private       *bool  `json:"private"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&repo)
	res.Body.Close()
	if err != nil {
		return err
	}
	// A public repository hands every version of the sealed backup, its
	// whole commit history, to anyone, to guess the passphrase at leisure.
	if repo.Private == nil || !*repo.Private {
		return fmt.Errorf("the GitHub repository %s is public, or GitHub didn't say it is private: anyone could download every version of the sealed backup from its history. Make it private, or use a private repository", g.repo)
	}
	if g.branch == "" {
		g.branch = repo.DefaultBranch
	}
	if g.branch == "" {
		return errors.New("the GitHub repository has no default branch: create an initial commit first")
	}
	res, err = g.send(ctx, http.MethodGet, "git/ref/heads/"+g.branch, nil, "application/vnd.github+json", "")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict {
		body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		if err != nil {
			return err
		}
		var failure struct{ Message string }
		if json.Unmarshal(body, &failure) != nil || failure.Message != "Git Repository is empty." {
			res.Body = io.NopCloser(bytes.NewReader(body))
			return githubError(res)
		}
		if g.branch != repo.DefaultBranch {
			return errors.New("The GitHub repository is empty: leave Branch empty or use its default branch for the first sync")
		}
		g.empty = true
	} else if res.StatusCode != http.StatusOK {
		return githubError(res)
	}
	g.ready = true
	return nil
}

type githubContent struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	SHA      string `json:"sha"`
	Size     int64  `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

func (g *github) at(name string) string {
	return pathpkg.Join(g.prefix, folder, name)
}

func (g *github) content(ctx context.Context, name string) (*githubContent, error) {
	if err := g.prepare(ctx); err != nil {
		return nil, err
	}
	return g.contentAt(ctx, name, g.branch)
}

// contentAt reads a file's metadata at a branch or commit. The object media
// type answers for files up to 100 MB; over 1 MB its content is empty and
// bytes downloads the blob instead.
func (g *github) contentAt(ctx context.Context, name, ref string) (*githubContent, error) {
	res, err := g.send(ctx, http.MethodGet, "contents/"+g.at(name), nil, "application/vnd.github.object+json", ref)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, githubError(res)
	}
	var c githubContent
	if err := json.NewDecoder(io.LimitReader(res.Body, 90<<20)).Decode(&c); err != nil {
		return nil, err
	}
	if c.Type != "file" || c.SHA == "" || c.Size < 0 || c.Size > githubMaxFile {
		return nil, errors.New("GitHub sync needs a regular file no larger than 64 MiB")
	}
	g.empty = false
	return &c, nil
}

func (g *github) bytes(ctx context.Context, c *githubContent) ([]byte, error) {
	if c.Encoding == "base64" {
		b, err := base64.StdEncoding.DecodeString(c.Content)
		if err != nil {
			return nil, fmt.Errorf("decoding GitHub's file: %w", err)
		}
		if int64(len(b)) != c.Size {
			return nil, errors.New("GitHub returned an incomplete file")
		}
		return b, nil
	}
	if c.Encoding != "none" {
		return nil, fmt.Errorf("GitHub returned an unsupported file encoding %q", c.Encoding)
	}
	// The blob SHA pins the download to the metadata read above, even if
	// another computer commits a new backup while the download is starting.
	res, err := g.send(ctx, http.MethodGet, "git/blobs/"+c.SHA, nil, "application/vnd.github.raw+json", "")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, githubError(res)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, githubMaxFile+1))
	if err == nil && int64(len(b)) != c.Size {
		err = errors.New("GitHub returned an incomplete or oversized file")
	}
	return b, err
}

func (g *github) get(ctx context.Context, have version) ([]byte, version, error) {
	c, err := g.content(ctx, file)
	if err != nil || c == nil {
		return nil, version{}, err
	}
	v := version{ETag: c.SHA}
	if have.ETag == c.SHA {
		return nil, v, errNotModified
	}
	b, err := g.bytes(ctx, c)
	if err != nil {
		return nil, v, err
	}
	m, err := parseParts(b)
	if err != nil || m == nil {
		return b, v, err
	}
	// Each part by its blob SHA, the one the list names, so the parts are
	// the set this version holds even if a new backup is committed while
	// they download.
	whole := make([]byte, 0, m.Size)
	for i, p := range m.Parts {
		b, err := g.bytes(ctx, &githubContent{SHA: p.SHA, Size: p.Size, Encoding: "none"})
		if err != nil {
			return nil, v, fmt.Errorf("reading part %d of the GitHub backup: %w", i+1, err)
		}
		if gitBlobSHA(b) != p.SHA {
			return nil, v, fmt.Errorf("GitHub returned a different part %d of the backup than its list names", i+1)
		}
		whole = append(whole, b...)
	}
	return whole, v, nil
}

// errTooLarge is the Contents API refusing a file it won't process, which
// it does well under the 100 MB a blob can hold (a 19 MB backup passed and a
// larger one didn't, on Discord).
var errTooLarge = errors.New("GitHub's Contents API refused the file as too large")

// errBehind is a ref update that is no longer a fast-forward: another commit
// reached the branch after its head was read.
var errBehind = errors.New("the GitHub branch moved meanwhile")

// githubWrite is one blob a commit puts at path.
type githubWrite struct {
	path, sha string
	data      []byte
}

// commit writes or removes one file in one commit. A repository with
// commits goes through the Git Data API (blob, tree, commit, ref), which
// takes files up to 100 MB, where the Contents API refuses large ones (HTTP
// 422 "the file is too large to be processed"). A backup over githubPart
// goes up in parts, with their list at its path, in that one commit. An
// empty repository has no branch to build on, so its first write goes
// through the Contents API, which creates the default branch.
func (g *github) commit(ctx context.Context, name string, data []byte, sha string, remove bool) (version, error) {
	if len(data) > githubMaxFile {
		return version{}, errors.New("the GitHub sync file exceeds 64 MiB")
	}
	if err := g.prepare(ctx); err != nil {
		return version{}, err
	}
	var writes []githubWrite
	if !remove && name == file && len(data) > githubPart {
		m := githubParts{Format: "magpie-backup", Version: 2, Split: githubSplit, Size: int64(len(data))}
		for i, off := 0, 0; off < len(data); i, off = i+1, off+githubPart {
			p := data[off:min(off+githubPart, len(data))]
			writes = append(writes, githubWrite{g.at(partName(i)), gitBlobSHA(p), p})
			m.Parts = append(m.Parts, githubPartRef{gitBlobSHA(p), int64(len(p))})
		}
		list, err := json.Marshal(m)
		if err != nil {
			return version{}, err
		}
		writes = append(writes, githubWrite{g.at(name), gitBlobSHA(list), list})
	} else if !remove {
		writes = []githubWrite{{g.at(name), gitBlobSHA(data), data}}
	}
	if g.empty {
		// One file is tried through the Contents API first; parts can't go
		// that way, so they start on a first commit of the note's.
		if len(writes) <= 1 {
			v, err := g.contents(ctx, name, data, sha, remove)
			if !errors.Is(err, errTooLarge) {
				return v, err
			}
		}
		// Too large for the Contents API: give the repository a first
		// commit of its own, then build the file's commit on it.
		if err := g.initialize(ctx); err != nil {
			return version{}, err
		}
	}
	sent := map[string]bool{}
	for try := 0; ; try++ {
		v, err := g.gitCommit(ctx, name, writes, sha, sent)
		if !errors.Is(err, errBehind) {
			return v, err
		}
		// A commit that touched other files (another computer's usage)
		// is built on again; gitCommit returns errChanged if it touched
		// this file.
		if try == 3 {
			return version{}, errChanged
		}
	}
}

// githubJSON sends in as JSON and decodes a 2xx reply into out. Any other
// status is returned as the response, its body readable, for the caller to
// judge.
func (g *github) githubJSON(ctx context.Context, method, apiPath string, in, out any) (*http.Response, error) {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return nil, err
		}
	}
	res, err := g.send(ctx, method, apiPath, body, "application/vnd.github+json", "")
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		b, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		res.Body = io.NopCloser(bytes.NewReader(b))
		return res, nil
	}
	defer res.Body.Close()
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out); err != nil {
		return nil, fmt.Errorf("reading GitHub's reply to %s %s: %w", method, apiPath, err)
	}
	return nil, nil
}

// gitCommit builds one commit on the branch's head that puts writes (the
// file, or its parts and their list; none removes it) and moves the branch
// to it. The file must still be at sha in that head, so another computer's
// backup is never written over; the ref update without force refuses a
// head that moved after it was read. Parts the file at the head lists and
// writes no longer has are removed with it. sent holds the blobs already
// uploaded, across retries.
func (g *github) gitCommit(ctx context.Context, name string, writes []githubWrite, sha string, sent map[string]bool) (version, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if res, err := g.githubJSON(ctx, http.MethodGet, "git/ref/heads/"+g.branch, nil, &ref); err != nil {
		return version{}, err
	} else if res != nil {
		return version{}, githubError(res)
	}
	head := ref.Object.SHA
	if head == "" {
		return version{}, errors.New("GitHub did not return the branch's head commit")
	}
	c, err := g.contentAt(ctx, name, head)
	if err != nil {
		return version{}, err
	}
	if c != nil && c.SHA != sha || c == nil && sha != "" {
		return version{}, errChanged
	}
	// The parts the file at the head lists. A list that doesn't read
	// leaves its parts where they are: removing only what is known to be
	// there never fails a tree on a path that isn't.
	had := 0
	if c != nil && name == file && c.Encoding == "base64" {
		if b, err := g.bytes(ctx, c); err == nil {
			if m, err := parseParts(b); err == nil && m != nil {
				had = len(m.Parts)
			}
		}
	}
	for _, w := range writes {
		if sent[w.sha] {
			continue
		}
		var out struct {
			SHA string `json:"sha"`
		}
		in := map[string]string{"content": base64.StdEncoding.EncodeToString(w.data), "encoding": "base64"}
		if res, err := g.githubJSON(ctx, http.MethodPost, "git/blobs", in, &out); err != nil {
			return version{}, err
		} else if res != nil {
			return version{}, githubError(res)
		}
		if out.SHA != w.sha {
			return version{}, errors.New("GitHub stored a different blob than the one sent")
		}
		sent[w.sha] = true
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if res, err := g.githubJSON(ctx, http.MethodGet, "git/commits/"+head, nil, &commit); err != nil {
		return version{}, err
	} else if res != nil {
		return version{}, githubError(res)
	}
	if commit.Tree.SHA == "" {
		return version{}, errors.New("GitHub did not return the head commit's tree")
	}
	// A null sha removes the path from the tree.
	type entry struct {
		Path string  `json:"path"`
		Mode string  `json:"mode"`
		Type string  `json:"type"`
		SHA  *string `json:"sha"`
	}
	var entries []entry
	for _, w := range writes {
		entries = append(entries, entry{Path: w.path, Mode: "100644", Type: "blob", SHA: &w.sha})
	}
	message := "magpie: sync " + name
	if len(writes) == 0 {
		entries = append(entries, entry{Path: g.at(name), Mode: "100644", Type: "blob"})
		message = "magpie: remove " + name
	}
	parts := max(len(writes)-1, 0)
	for i := parts; i < had; i++ {
		entries = append(entries, entry{Path: g.at(partName(i)), Mode: "100644", Type: "blob"})
	}
	var tree, made struct {
		SHA string `json:"sha"`
	}
	if res, err := g.githubJSON(ctx, http.MethodPost, "git/trees", map[string]any{"base_tree": commit.Tree.SHA, "tree": entries}, &tree); err != nil {
		return version{}, err
	} else if res != nil {
		return version{}, githubError(res)
	}
	if res, err := g.githubJSON(ctx, http.MethodPost, "git/commits", map[string]any{"message": message, "tree": tree.SHA, "parents": []string{head},
		"author": g.who, "committer": g.who}, &made); err != nil {
		return version{}, err
	} else if res != nil {
		return version{}, githubError(res)
	}
	if made.SHA == "" {
		return version{}, errors.New("GitHub did not return the new commit")
	}
	var moved json.RawMessage
	res, err := g.githubJSON(ctx, http.MethodPatch, "git/refs/heads/"+g.branch, map[string]any{"sha": made.SHA, "force": false}, &moved)
	if err != nil {
		return version{}, err
	}
	if res != nil {
		if res.StatusCode == http.StatusUnprocessableEntity {
			b, _ := io.ReadAll(res.Body)
			if strings.Contains(strings.ToLower(string(b)), "fast forward") {
				return version{}, errBehind
			}
			res.Body = io.NopCloser(bytes.NewReader(b))
		}
		return version{}, githubError(res)
	}
	g.empty = false
	if len(writes) == 0 {
		return version{}, nil
	}
	return version{ETag: writes[len(writes)-1].sha}, nil
}

// gitBlobSHA is the SHA git names a blob holding b by: the version GitHub
// reports for the file.
func gitBlobSHA(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// initialize gives an empty repository its first commit, a short note in
// magpie's folder, so a backup too large for the Contents API can be
// committed on top of it through the Git Data API. Another computer that
// initialized it first is just as good.
func (g *github) initialize(ctx context.Context) error {
	note := []byte("This folder holds magpie's encrypted sync files.\n")
	if _, err := g.contents(ctx, "README.md", note, "", false); err != nil && !errors.Is(err, errChanged) {
		return err
	}
	g.empty = false
	return nil
}

// contents writes through the Contents API, which only an empty repository's
// first write uses.
func (g *github) contents(ctx context.Context, name string, data []byte, sha string, remove bool) (version, error) {
	in := map[string]any{"message": "magpie: sync " + name, "content": base64.StdEncoding.EncodeToString(data),
		"author": g.who, "committer": g.who}
	// Omitting branch lets the first backup initialize an empty repository.
	if !g.empty {
		in["branch"] = g.branch
	}
	if sha != "" {
		in["sha"] = sha
	}
	method := http.MethodPut
	if remove {
		method = http.MethodDelete
		in["message"] = "magpie: remove " + name
		delete(in, "content")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return version{}, err
	}
	res, err := g.send(ctx, method, "contents/"+g.at(name), body, "application/vnd.github+json", "")
	if err != nil {
		return version{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict {
		return version{}, errChanged
	}
	if res.StatusCode == http.StatusRequestEntityTooLarge {
		return version{}, errTooLarge
	}
	if res.StatusCode == http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		// A first write raced another computer creating the same file.
		if strings.Contains(string(body), `\"sha\" wasn't supplied`) {
			return version{}, errChanged
		}
		var e struct{ Message string }
		if json.Unmarshal(body, &e) == nil && strings.Contains(strings.ToLower(e.Message), "too large") {
			return version{}, errTooLarge
		}
		res.Body = io.NopCloser(bytes.NewReader(body))
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return version{}, githubError(res)
	}
	var out struct{ Content *githubContent }
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return version{}, err
	}
	if remove {
		return version{}, nil
	}
	if out.Content == nil || out.Content.SHA == "" {
		return version{}, errors.New("GitHub did not return the version of the committed file")
	}
	g.empty = false
	return version{ETag: out.Content.SHA}, nil
}

func (g *github) put(ctx context.Context, data []byte, sha string) (version, error) {
	return g.commit(ctx, file, data, sha, false)
}

func (g *github) list(ctx context.Context) (map[string]string, error) {
	if err := g.prepare(ctx); err != nil {
		return nil, err
	}
	res, err := g.send(ctx, http.MethodGet, "contents/"+g.at(usageFolder), nil, "application/vnd.github+json", g.branch)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return map[string]string{}, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, githubError(res)
	}
	var entries []githubContent
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&entries); err != nil {
		return nil, err
	}
	if len(entries) >= 1000 {
		return nil, errors.New("the GitHub usage folder reached the Contents API's 1,000-file listing limit")
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.Type == "file" && pathpkg.Base(e.Name) == e.Name &&
			(strings.HasSuffix(e.Name, usageExt) || strings.HasSuffix(e.Name, quotasExt)) {
			if e.SHA == "" {
				return nil, errors.New("GitHub returned a usage file without its version")
			}
			out[e.Name] = e.SHA
		}
	}
	return out, nil
}

func (g *github) read(ctx context.Context, name string) ([]byte, error) {
	c, err := g.content(ctx, pathpkg.Join(usageFolder, name))
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("the GitHub usage file %s is missing", name)
	}
	return g.bytes(ctx, c)
}

func (g *github) write(ctx context.Context, name string, data []byte) error {
	at := pathpkg.Join(usageFolder, name)
	c, err := g.content(ctx, at)
	if err != nil {
		return err
	}
	sha := ""
	if c != nil {
		sha = c.SHA
	}
	_, err = g.commit(ctx, at, data, sha, false)
	return err
}

func (g *github) remove(ctx context.Context, name string) error {
	at := pathpkg.Join(usageFolder, name)
	c, err := g.content(ctx, at)
	if err != nil || c == nil {
		return err
	}
	_, err = g.commit(ctx, at, nil, c.SHA, true)
	return err
}
