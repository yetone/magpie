package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/source"
)

// GitHub's API route to a release's file. github.com's download address
// (…/releases/download/<tag>/<file>) can answer 504 on every try while
// the same file is served through the API: on 2026-11-08 github.com gave
// 504 for every magpie release's file for hours, while
// api.github.com/repos/<owner>/<repo>/releases/assets/<id>, asked with
// Accept: application/octet-stream, redirected to the file at once. A
// download whose github.com address is busy goes on there (download): the
// same bytes, checked against the feed's hash, and on the Mac against the
// installed app's team, exactly as before. A mirror the user gave keeps
// precedence: through one, no API route is tried. A file the feed names
// on the site's R2 goes on to its github.com address (Asset.GitHub) first.

// githubWeb and githubAPI are GitHub's addresses; vars for tests.
var (
	githubWeb = "https://github.com"
	githubAPI = "https://api.github.com"
)

// apiRoute is the API address of a's file: the feed's (Asset.API), else
// the one GitHub's API lists for the release a.GitHub (or a.URL) names. "" with a nil
// error when a's file isn't on GitHub (a test's server, a feed pointed
// elsewhere).
func apiRoute(ctx context.Context, a Asset) (string, error) {
	if a.API != "" {
		return a.API, nil
	}
	src := a.URL
	if a.GitHub != "" {
		src = a.GitHub
	}
	rest, ok := strings.CutPrefix(src, githubWeb+"/")
	if !ok {
		return "", nil
	}
	// <owner>/<repo>/releases/download/<tag>/<file>
	p := strings.Split(rest, "/")
	if len(p) != 6 || p[2] != "releases" || p[3] != "download" {
		return "", nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", githubAPI, p[0], p[1], p[4]), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := source.Do(client, req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("GitHub's API about %s: %s", p[4], res.Status)
	}
	var rel struct {
		Assets []struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("GitHub's API about %s: %w", p[4], err)
	}
	for _, x := range rel.Assets {
		if x.Name == p[5] && strings.HasPrefix(x.URL, githubAPI+"/") {
			return x.URL, nil
		}
	}
	return "", fmt.Errorf("GitHub's API lists no %s in %s", p[5], p[4])
}

// busyErr reports whether err is a host's busy answer (busyStatus), not
// a connection that broke.
func busyErr(err error) bool {
	var s statusError
	return errors.As(err, &s)
}

// viaAPIKey marks a try made through GitHub's API route: no mirror, and
// the file asked for as bytes (Accept: application/octet-stream) rather
// than its description.
type viaAPIKey struct{}

func withAPIRoute(ctx context.Context) context.Context {
	return context.WithValue(ctx, viaAPIKey{}, true)
}

func onAPIRoute(ctx context.Context) bool {
	v, _ := ctx.Value(viaAPIKey{}).(bool)
	return v
}
