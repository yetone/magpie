package davsync

import (
	"bytes"
	"context"
	"encoding/base64"
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
)

const githubMaxFile = 64 << 20

type github struct {
	repo, prefix, branch, token string
	ready                       bool
	empty                       bool
	client                      *http.Client
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
		branch: branch, token: c.Password, client: &client}, nil
}

func (g *github) send(ctx context.Context, method, apiPath string, body []byte, accept string, ref bool) (*http.Response, error) {
	u := &url.URL{Scheme: "https", Host: "api.github.com", Path: "/repos/" + g.repo}
	if apiPath != "" {
		u.Path += "/" + apiPath
	}
	if ref {
		q := url.Values{"ref": {g.branch}}
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
	res, err := g.send(ctx, http.MethodGet, "", nil, "application/vnd.github+json", false)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		defer res.Body.Close()
		return githubError(res)
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&repo)
	res.Body.Close()
	if err != nil {
		return err
	}
	if g.branch == "" {
		g.branch = repo.DefaultBranch
	}
	if g.branch == "" {
		return errors.New("the GitHub repository has no default branch: create an initial commit first")
	}
	res, err = g.send(ctx, http.MethodGet, "git/ref/heads/"+g.branch, nil, "application/vnd.github+json", false)
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
	res, err := g.send(ctx, http.MethodGet, "contents/"+g.at(name), nil, "application/vnd.github.object+json", true)
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
	res, err := g.send(ctx, http.MethodGet, "git/blobs/"+c.SHA, nil, "application/vnd.github.raw+json", false)
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
	return b, v, err
}

func (g *github) commit(ctx context.Context, name string, data []byte, sha string, remove bool) (version, error) {
	if len(data) > githubMaxFile {
		return version{}, errors.New("the GitHub sync file exceeds 64 MiB")
	}
	if err := g.prepare(ctx); err != nil {
		return version{}, err
	}
	in := map[string]string{"message": "magpie: sync " + name, "content": base64.StdEncoding.EncodeToString(data)}
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
	res, err := g.send(ctx, method, "contents/"+g.at(name), body, "application/vnd.github+json", false)
	if err != nil {
		return version{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict {
		return version{}, errChanged
	}
	if res.StatusCode == http.StatusUnprocessableEntity {
		// A first write raced another computer creating the same file.
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		if strings.Contains(string(body), `\"sha\" wasn't supplied`) {
			return version{}, errChanged
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
	res, err := g.send(ctx, http.MethodGet, "contents/"+g.at(usageFolder), nil, "application/vnd.github+json", true)
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
