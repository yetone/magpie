package library

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// A plugin installed from a git source pinned to a release tag (#7549,
// jinpwng): Update fetches that same tag again, so a newer release never
// arrives by it. The pin and the repository's newer stable tags are shown
// (PluginTags), and UpgradePlugin moves the plugin to one of them: the
// same source with the new tag, installed under the plugin's own name, so
// its agents, projects and whether it is on stay, and the copies a
// project has are made again from the new files. An ordinary Update keeps
// the pin.

// stableTag is a release tag: v1, 1.2, v1.2.3, no pre-release or build
// part.
var stableTag = lazyre.New(`^v?\d+(\.\d+){0,3}$`)

// PluginPin is a plugin's release pin and the newer stable tags its
// repository has, newest first.
type PluginPin struct {
	Name  string   `json:"name"`
	Pin   string   `json:"pin"`
	Newer []string `json:"newer"`
}

// pinnedSource is input's repository and the stable tag it is pinned to;
// ok is false for a folder, a branch, a commit or no ref at all.
func pinnedSource(input string) (src Source, ok bool) {
	src, ok = githubSource(input)
	if !ok {
		var err error
		if src, ok, err = gitSource(input); err != nil || !ok {
			return Source{}, false
		}
	}
	// a ref of seven digits or more is read as a commit, as git does
	return src, src.Ref != "" && stableTag.MatchString(src.Ref) && !(len(src.Ref) >= 7 && hexRef.MatchString(src.Ref))
}

// tagParts are a stable tag's numbers, for comparing.
func tagParts(tag string) []int {
	var out []int
	for _, s := range strings.Split(strings.TrimPrefix(tag, "v"), ".") {
		n, _ := strconv.Atoi(s)
		out = append(out, n)
	}
	for len(out) < 4 {
		out = append(out, 0)
	}
	return out
}

// newerTags are the stable tags among tags that are newer than pin,
// newest first; of two that are the same release (v1.2 and 1.2.0) the one
// written as the pin is (its "v" or not) is kept.
func newerTags(pin string, tags []string) []string {
	base := tagParts(pin)
	v := strings.HasPrefix(pin, "v")
	seen := map[string]string{}
	for _, t := range tags {
		if !stableTag.MatchString(t) || slices.Compare(tagParts(t), base) <= 0 {
			continue
		}
		k := fmt.Sprint(tagParts(t))
		if o, had := seen[k]; had && strings.HasPrefix(o, "v") == v {
			continue
		}
		seen[k] = t
	}
	out := make([]string, 0, len(seen))
	for _, t := range seen {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b string) int { return slices.Compare(tagParts(b), tagParts(a)) })
	return out
}

// remoteTags are the tags of src's repository, by git ls-remote.
var remoteTags = func(src Source) ([]string, error) {
	git := gitPath()
	if git == "" {
		return nil, errNoGit()
	}
	repo := src.Repo
	if src.Kind == "github" {
		repo = "https://github.com/" + src.Repo + ".git"
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	out, err := runGit(ctx, git, gitEnv(git), os.TempDir(), src, "ls-remote", "--tags", "--refs", "--", repo)
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, l := range strings.Split(string(out), "\n") {
		if _, name, ok := strings.Cut(strings.TrimSpace(l), "\t"); ok {
			if t, ok := strings.CutPrefix(name, "refs/tags/"); ok {
				tags = append(tags, t)
			}
		}
	}
	return tags, nil
}

// PluginTags is the plugin's pin and the newer stable tags of its
// repository. A plugin whose source isn't pinned to a release tag has no
// pin: an error says so.
func PluginTags(name string) (*PluginPin, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	b := l.plugin(name)
	if b == nil {
		return nil, fmt.Errorf("no plugin called %s", name)
	}
	src, ok := pinnedSource(b.Source)
	if !ok {
		return nil, fmt.Errorf("%s isn't pinned to a release tag", name)
	}
	tags, err := remoteTags(src)
	if err != nil {
		return nil, err
	}
	return &PluginPin{Name: name, Pin: src.Ref, Newer: newerTags(src.Ref, tags)}, nil
}

// retagSource is input with its tag pin changed to tag: the ref's place in
// a GitHub /tree/ or /blob/ link, GitLab's /-/tree/ and Gitea's /src/tag/
// or /src/branch/.
func retagSource(input, pin, tag string) (string, error) {
	for _, sep := range []string{"/-/tree/", "/-/blob/", "/src/tag/", "/src/branch/", "/tree/", "/blob/"} {
		at := sep + pin
		i := strings.Index(input, at)
		if i < 0 {
			continue
		}
		if rest := input[i+len(at):]; rest != "" && rest[0] != '/' && rest[0] != '?' && rest[0] != '#' {
			continue
		}
		return input[:i] + sep + tag + input[i+len(at):], nil
	}
	return "", fmt.Errorf("%s names its tag %s where magpie can't change it", input, pin)
}

// UpgradePlugin moves a plugin pinned to a release tag to tag, a newer
// stable tag of its repository: installed from the source with that tag,
// under its own name, so its agents and projects keep it, and the source
// it is kept by is the new one from then on.
func UpgradePlugin(name, tag string) (*Result, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	b := l.plugin(name)
	if b == nil {
		return nil, fmt.Errorf("no plugin called %s", name)
	}
	src, ok := pinnedSource(b.Source)
	if !ok {
		return nil, fmt.Errorf("%s isn't pinned to a release tag", name)
	}
	if !stableTag.MatchString(tag) {
		return nil, fmt.Errorf("%q isn't a release tag", tag)
	}
	if tag == src.Ref {
		return UpdatePlugin(name)
	}
	to, err := retagSource(b.Source, src.Ref, tag)
	if err != nil {
		return nil, err
	}
	return updatePluginFrom(b, to)
}
