package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Note is one release's notes, as the app shows them after an update.
type Note struct {
	Version string `json:"version"` // no v
	Notes   string `json:"notes"`   // markdown, its Install section taken out
	URL     string `json:"url,omitempty"`
}

// NotesFeed is where the notes of past releases are listed: beside the
// update feed, on magpie's site. MAGPIE_NOTES_FEED points it elsewhere.
func NotesFeed() string {
	if f := os.Getenv("MAGPIE_NOTES_FEED"); f != "" {
		return f
	}
	return Site + "/api/notes"
}

// NotesBetween asks the site for the notes of every release after after
// (none when "") up to and including upto, newest first.
func NotesBetween(ctx context.Context, after, upto string) ([]Note, error) {
	q := url.Values{"upto": {upto}}
	if after != "" {
		q.Set("after", after)
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
	return Between(out.Releases, after, upto), nil
}

// MaxNotes is how many releases' notes are shown at most: magpie releases
// often, and a long-unopened one would have pages of them.
const MaxNotes = 40

// Between keeps the notes of the releases after after up to and including
// upto, newest first, at most MaxNotes; with after "" only upto's. Each
// one's Install section is taken out, and one left empty is dropped.
func Between(notes []Note, after, upto string) []Note {
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
	if len(out) > MaxNotes {
		out = out[:MaxNotes]
	}
	return out
}

var heading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)

// StripInstall takes a release's "Install" section out of its notes: the
// download links, which mean nothing to a magpie that just updated itself.
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
			if strings.EqualFold(m[2], "Install") {
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
