package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// Note is one release's notes, as the app shows them after an update.
type Note struct {
	Version string `json:"version"` // no v
	Notes   string `json:"notes"`   // markdown, its Install section taken out
	URL     string `json:"url,omitempty"`
	// Pending: a release after the one running, up to the update waiting
	Pending bool `json:"pending,omitempty"`
}

// NotesFeed is where the notes of past releases are listed: beside the
// update feed, on magpie's site. MAGPIE_NOTES_FEED points it elsewhere.
func NotesFeed() string {
	if f := os.Getenv("MAGPIE_NOTES_FEED"); f != "" {
		return f
	}
	return Site + "/api/notes"
}

// ReleasePage is version's page on GitHub, where its notes are when the
// site can't give them (#661).
func ReleasePage(version string) string {
	return "https://github.com/yetone/magpie-releases/releases/tag/v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
}

// NotesBetween asks the site for the notes of every release after after
// (none when "") up to and including upto, newest first, in lang (see
// InLang).
func NotesBetween(ctx context.Context, after, upto, lang string) ([]Note, error) {
	notes, _, err := NotesBetweenTotal(ctx, after, upto, lang)
	return notes, err
}

// NotesBetweenTotal is NotesBetween with how many releases there are in
// all before MostNotes keeps the newest, so a list cut short can say so
// (sujingshen on X: 156 releases behind read "40 releases").
func NotesBetweenTotal(ctx context.Context, after, upto, lang string) ([]Note, int, error) {
	q := url.Values{"upto": {upto}}
	if after != "" {
		q.Set("after", after)
	}
	notes, err := askNotes(ctx, q, lang)
	if err != nil {
		return nil, 0, err
	}
	all := between(notes, after, upto)
	return capNotes(all), len(all), nil
}

// BackCounts are the ranges Settings' What's new offers besides the notes
// since the last update: this version and that many before it (Linka on
// Discord).
var BackCounts = []int{10, 20, 50, 100}

// NotesBack asks the site for the notes of upto and the n releases before
// it, newest first, in lang. The site is asked for those alone (limit); one
// from before it took a limit sends them all, and Back keeps n+1.
func NotesBack(ctx context.Context, upto string, n int, lang string) ([]Note, error) {
	notes, err := askNotes(ctx, url.Values{"upto": {upto}, "limit": {strconv.Itoa(n + 1)}}, lang)
	if err != nil {
		return nil, err
	}
	return Back(notes, upto, n), nil
}

// askNotes is the site's list for q, each release's notes in lang.
func askNotes(ctx context.Context, q url.Values, lang string) ([]Note, error) {
	if lang != "" {
		q.Set("lang", lang)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", NotesFeed()+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("release notes: %s", res.Status)
	}
	var out struct {
		Releases []Note `json:"releases"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("release notes: %w", err)
	}
	for i := range out.Releases {
		out.Releases[i].Notes = InLang(out.Releases[i].Notes, lang)
	}
	return out.Releases, nil
}

// zhMarker is where a release's notes turn from English to Chinese: the
// release workflow translates them and puts the Chinese below it.
const zhMarker = "<!-- lang:zh -->"

// InLang is a release's notes in lang, as the site cuts them (a site from
// before it did, or a feed elsewhere, hands them whole): "zh" the Chinese
// where there is some, else the English, everything above the marker.
// Notes from before the marker are English alone, in any language.
func InLang(md, lang string) string {
	i := strings.Index(md, zhMarker)
	if i < 0 {
		return md
	}
	if lang == "zh" {
		if zh := strings.TrimSpace(md[i+len(zhMarker):]); zh != "" {
			return zh
		}
	}
	return strings.TrimSpace(md[:i])
}

// withLang adds lang to a feed's address, as ?lang= (or &lang=).
func withLang(feed, lang string) string {
	if lang == "" {
		return feed
	}
	u, err := url.Parse(feed)
	if err != nil {
		return feed
	}
	q := u.Query()
	q.Set("lang", lang)
	u.RawQuery = q.Encode()
	return u.String()
}

// MaxNotes is how many releases' notes are shown at most: magpie releases
// often, and a long-unopened one would have pages of them.
const MaxNotes = 40

// StableMaxNotes is MaxNotes on the Stable channel, where an update is a
// week of releases and its notes are read as one list (ssharpcc on X):
// every one of them, as far as the site keeps them.
const StableMaxNotes = 1500

// MostNotes is how many releases' notes are shown at most on the channel
// Settings name.
func MostNotes() int {
	if Stable() {
		return StableMaxNotes
	}
	return MaxNotes
}

// Stable is whether Settings' Update channel is Stable: the update feed is
// asked for a version a week, the newest that had been out three days as
// the week began (Monday 00:00 UTC), or an urgent one (a security hole, a
// ban risk, lost data) as soon as it is out. usemagpie.ai chooses it from
// the release list (site/worker.js, "The Stable channel"); a release is
// urgent when a line of its notes starts with "**Urgent release:**".
func Stable() bool { return settings.Load().UpdateChannel == settings.UpdateStable }

// withChannel is feed asking for Stable's release on the Stable channel,
// and feed as it is on every version.
func withChannel(feed string) string {
	if !Stable() {
		return feed
	}
	u, err := url.Parse(feed)
	if err != nil {
		return feed
	}
	q := u.Query()
	q.Set("channel", settings.UpdateStable)
	u.RawQuery = q.Encode()
	return u.String()
}

// Between keeps the notes of the releases after after up to and including
// upto, newest first, at most MostNotes; with after "" only upto's. Each
// one's Install section is taken out, and one left empty is dropped.
func Between(notes []Note, after, upto string) []Note {
	return capNotes(between(notes, after, upto))
}

// capNotes keeps the newest MostNotes of notes, newest first.
func capNotes(notes []Note) []Note {
	if most := MostNotes(); len(notes) > most {
		return notes[:most]
	}
	return notes
}

// between is Between with every release kept.
func between(notes []Note, after, upto string) []Note {
	var out []Note
	seen := map[string]bool{}
	for _, n := range notes {
		v := strings.TrimPrefix(strings.TrimSpace(n.Version), "v")
		if parse(v) == nil || seen[v] || Newer(v, upto) {
			continue
		}
		if after == "" {
			if Newer(upto, v) {
				continue
			}
		} else if !Newer(v, after) {
			continue
		}
		n.Version, n.Notes = v, StripInstall(n.Notes)
		if strings.TrimSpace(n.Notes) == "" {
			continue
		}
		seen[v] = true
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool { return Newer(out[i].Version, out[j].Version) })
	return out
}

// Back keeps upto and the n releases before it, newest first: n+1
// releases counted before any is dropped, so a release whose notes are
// empty once its Install section is out still counts as one. A release
// newer than upto, or one listed twice, isn't counted.
func Back(notes []Note, upto string, n int) []Note {
	var keep []Note
	seen := map[string]bool{}
	for _, r := range notes {
		v := strings.TrimPrefix(strings.TrimSpace(r.Version), "v")
		if parse(v) == nil || seen[v] || Newer(v, upto) {
			continue
		}
		seen[v] = true
		r.Version = v
		keep = append(keep, r)
	}
	sort.SliceStable(keep, func(i, j int) bool { return Newer(keep[i].Version, keep[j].Version) })
	if len(keep) > n+1 {
		keep = keep[:n+1]
	}
	out := []Note{}
	for _, r := range keep {
		if r.Notes = StripInstall(r.Notes); strings.TrimSpace(r.Notes) != "" {
			out = append(out, r)
		}
	}
	return out
}

var heading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)

// installHeadings are the Install section's heading in each language the
// notes come in: the Chinese notes call it 安装, and every release's own
// section would otherwise repeat in What's new.
var installHeadings = []string{"Install", "安装", "安裝", "インストール"}

// StripInstall takes a release's "Install" section (in any language) out of
// its notes: the download links, which mean nothing to a magpie that just
// updated itself.
// The section runs to the next heading of its level or above.
func StripInstall(md string) string {
	var out []string
	skip := 0 // the level of the Install heading being skipped, 0 when not
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		if m := heading.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			if skip > 0 && level <= skip {
				skip = 0
			}
			if slices.ContainsFunc(installHeadings, func(h string) bool { return strings.EqualFold(m[2], h) }) {
				skip = level
			}
		}
		if skip == 0 {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ShowNotes decides, as magpie starts on current having last run as last
// ("" when it never said), whether to show what changed since, and from
// which version on (exclusive). Only an upgrade between releases shows:
// not a fresh install, the same version again, a downgrade, or a build
// from source. A magpie from before versions were kept has no last; when
// it was already in use (existed) its own version's notes are shown, from
// "", which Between takes as upto's alone.
func ShowNotes(last, current string, existed bool) (after string, show bool) {
	if !Released(current) {
		return "", false
	}
	if last == "" {
		return "", existed
	}
	if !Newer(current, last) {
		return "", false
	}
	return strings.TrimPrefix(strings.TrimSpace(last), "v"), true
}
