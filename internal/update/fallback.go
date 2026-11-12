package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/source"
)

// A network that lets github.com through and nothing else (a company's
// allow list: ssharpcc on X) can't reach usemagpie.ai's feed. When the
// default feed fails, the newest release is read from GitHub itself:
// yetone/magpie-releases' API, or, when the API is rate-limited, the
// release page's redirect and the SHA256SUMS beside the files. Nothing
// is configured. The result goes through Release.verify like the feed's,
// and here a release must be signed whatever its version: the feed is the
// only thing that may carry an unsigned old release, and this isn't the
// feed. The files themselves are on github.com (Asset.GitHub), which
// download already takes, with its API route.
//
// The Stable channel is the site's choice (a release a week), which
// GitHub can't give: on Stable a failed feed stays a failed feed.

// releaseRepo is where releases are published.
const releaseRepo = "yetone/magpie-releases"

// feedWait is how long the feed has to answer when GitHub stands behind
// it: a network that drops usemagpie.ai's packets would otherwise hold
// the check for the client's ten minutes. A var for tests.
var feedWait = 20 * time.Second

// siteFeed is the default feed; a var for tests.
var siteFeed = Site + "/api/latest"

// maxSmall bounds SHA256SUMS and its signature.
const maxSmall = 1 << 20

// fallsBack says whether a failed feed goes on to GitHub: the default
// feed, not the Stable channel, and not an ask already cancelled.
func fallsBack(ctx context.Context) bool {
	return ctx.Err() == nil && Feed() == siteFeed && !Stable() && Source() == ""
}

// githubOnly says whether the release is read from GitHub alone, with no
// try at the feed: Settings' GitHub only, on the default feed and not on
// Stable (whose release only the site knows; its files still come from
// GitHub).
func githubOnly() bool {
	return Feed() == siteFeed && !Stable() && Source() == settings.UpdateSourceGitHub
}

// githubLatest is the newest release as GitHub gives it, verified.
func githubLatest(ctx context.Context, lang string) (*Release, error) {
	tag, notes, page, files, err := githubTag(ctx)
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("%s/%s/releases/download/%s/", githubWeb, releaseRepo, tag)
	sums, err := githubFile(ctx, base+"SHA256SUMS", files["SHA256SUMS"].api)
	if err != nil {
		return nil, err
	}
	sig, err := githubFile(ctx, base+"SHA256SUMS.sig", files["SHA256SUMS.sig"].api)
	if err != nil {
		return nil, fmt.Errorf("%s has no signature: %w", tag, err)
	}
	rel := &Release{
		Version: strings.TrimPrefix(tag, "v"),
		Notes:   InLang(notes, lang),
		URL:     page,
		Assets:  map[string]Asset{},
		Signed:  &Signed{Sums: sums, Sig: strings.TrimSpace(sig)},
	}
	if parse(rel.Version) == nil {
		return nil, fmt.Errorf("GitHub's release tag %q is no version", tag)
	}
	// the names come from the signed list; verify() drops any other and
	// gives each file its hash
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "*")
		if name == "" || strings.ContainsAny(name, `/\`) || name == "SHA256SUMS" || name == "SHA256SUMS.sig" {
			continue
		}
		u := base + url.PathEscape(name)
		rel.Assets[name] = Asset{URL: u, GitHub: u, API: files[name].api, Size: files[name].size}
	}
	if err := rel.verify(); err != nil {
		return nil, err
	}
	return rel, nil
}

type ghFile struct {
	api  string
	size int64
}

// githubTag is the newest release's tag, notes, page and the files the
// API lists. The API is asked first; limited or unreachable, the release
// page's redirect gives the tag alone.
func githubTag(ctx context.Context) (tag, notes, page string, files map[string]ghFile, err error) {
	files = map[string]ghFile{}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/repos/%s/releases/latest", githubAPI, releaseRepo), nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, aerr := source.Do(client, req)
	if aerr == nil {
		defer res.Body.Close()
		if res.StatusCode == 200 {
			var gh struct {
				Tag    string `json:"tag_name"`
				Body   string `json:"body"`
				Page   string `json:"html_url"`
				Assets []struct {
					Name string `json:"name"`
					URL  string `json:"url"`
					Size int64  `json:"size"`
				} `json:"assets"`
			}
			if derr := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&gh); derr == nil && gh.Tag != "" {
				for _, a := range gh.Assets {
					if strings.HasPrefix(a.URL, githubAPI+"/") {
						files[a.Name] = ghFile{api: a.URL, size: a.Size}
					}
				}
				return gh.Tag, gh.Body, gh.Page, files, nil
			}
		} else {
			aerr = fmt.Errorf("GitHub's API: %s", res.Status)
		}
	}
	// the release page: /releases/latest redirects to /releases/tag/<tag>
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	pr, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/%s/releases/latest", githubWeb, releaseRepo), nil)
	if err != nil {
		return
	}
	pres, perr := source.Do(&c, pr)
	if perr != nil {
		err = fmt.Errorf("GitHub: %w", errors.Join(aerr, perr))
		return
	}
	defer pres.Body.Close()
	loc, _ := url.Parse(pres.Header.Get("Location"))
	if loc != nil {
		if i := strings.LastIndex(loc.Path, "/releases/tag/"); i >= 0 {
			tag = strings.Trim(loc.Path[i+len("/releases/tag/"):], "/")
		}
	}
	if tag == "" || strings.Contains(tag, "/") {
		tag = ""
		err = fmt.Errorf("GitHub's release page named no release (%s)", pres.Status)
		if aerr != nil {
			err = fmt.Errorf("%w; the API: %v", err, aerr)
		}
		return
	}
	return tag, "", fmt.Sprintf("%s/%s/releases/tag/%s", githubWeb, releaseRepo, tag), files, nil
}

// githubFile reads a small release file from its github.com address (the
// mirror the user gave in front of it), else through the API route when
// that is known. Its bytes are trusted for nothing until the signature
// over them is checked.
func githubFile(ctx context.Context, raw, api string) (string, error) {
	var errs []error
	tries := []struct {
		u   string
		api bool
	}{{mirrored(mirrorOf(ctx), raw), false}, {api, true}}
	for _, try := range tries {
		if try.u == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, "GET", try.u, nil)
		if err != nil {
			return "", err
		}
		if try.api {
			req.Header.Set("Accept", "application/octet-stream")
		}
		res, err := source.Do(client, req)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		b, err := io.ReadAll(io.LimitReader(res.Body, maxSmall+1))
		res.Body.Close()
		switch {
		case res.StatusCode != 200:
			errs = append(errs, fmt.Errorf("%s: %s", hostOf(try.u), res.Status))
		case err != nil:
			errs = append(errs, err)
		case len(b) > maxSmall:
			errs = append(errs, fmt.Errorf("%s: too large", raw))
		default:
			return string(b), nil
		}
	}
	return "", errors.Join(errs...)
}
