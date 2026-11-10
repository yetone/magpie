package plugin

import (
	"context"
	"slices"
	"strings"
)

// Public is the plugins installed and on here that Discover shows to
// everyone, each by the key Discover gives it: a package magpie's list
// names, or a repository tagged Topic, by its Spec. A plugin from a
// private repository, a folder, or a package neither names is never in
// it: what the day's report and a like send of the user's plugins is
// this and nothing else (internal/stats).
func Public(ctx context.Context) []string {
	installed := Load().Plugins
	if len(installed) == 0 {
		return nil
	}
	listed := map[string]bool{}
	for _, l := range Market(ctx) {
		listed[l.Package] = true
	}
	// GitHub not answering keeps the list it gave last
	tagged, _ := TaggedRepos(ctx)
	return public(installed, listed, tagged)
}

func public(installed []Entry, listed map[string]bool, tagged []Tagged) []string {
	byRepo := map[string]string{}
	for _, t := range tagged {
		byRepo[strings.ToLower(t.Repo)] = t.Spec
		// one published on npm from its repository is installed by
		// that name too
		if t.Spec != "" && !IsGit(t.Spec) {
			listed[t.Spec] = true
		}
	}
	out := []string{}
	for _, e := range installed {
		if e.Off || IsPath(e.Spec) {
			continue
		}
		key := ""
		if IsGit(e.Spec) {
			// a repository by the repository it is, never by the package
			// name it gives, which another repository can give too
			key = byRepo[strings.ToLower(githubRepo(gitHubSpec(e.Spec)))]
		} else if n := Name(e.Spec); listed[n] {
			key = n
		}
		if key != "" && !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	return out
}

// gitHubSpec is spec as githubRepo reads it: owner/repo, GitHub's
// shorthand, as github:owner/repo.
func gitHubSpec(spec string) string {
	if !strings.Contains(spec, ":") {
		return "github:" + spec
	}
	return spec
}
