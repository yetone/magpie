// Package groupshare shares routing groups on usemagpie.ai
// (site/groups.js; docs/subsystems/gateway-routing.md, "Sharing a
// group"): a link anyone can open, a gallery of the groups shared there
// publicly, and, for a group imported from a link, the author's newer
// version when there is one.
//
// What goes out is provider.PublicGroup: no key, account, folder or name
// of a provider the user added; the user sees it before it goes. The user
// shares, never an agent, and nothing is sent by itself but the ids of the
// shared groups imported here (Report, with the usage stats).
//
// shared-groups.json, in magpie's folder and readable by the user alone,
// keeps the link of each group the user shared and the token that updates
// it or takes it down (usemagpie.ai keeps the token only hashed), and the
// link each imported group came from.
package groupshare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Site is where groups are shared. MAGPIE_SHARE_SITE moves it, as it
// moves shared artifacts, and a test names its own: a test never reaches
// the real site.
var Site = func() string {
	if s := os.Getenv("MAGPIE_SHARE_SITE"); s != "" {
		return strings.TrimRight(s, "/")
	}
	if testing.Testing() {
		return ""
	}
	return "https://usemagpie.ai"
}

// ErrOff is sharing with no site to share on (a test that named none).
var ErrOff = errors.New("sharing is off here")

// ErrGone is a shared group usemagpie.ai doesn't have: taken down by its
// author, or on a report.
var ErrGone = errors.New("this shared group was taken down on usemagpie.ai")

// Meta is what the user says of a group they share.
type Meta struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Author      string `json:"author,omitempty"`
	// Public lists it in the gallery; unlisted, only its link opens it
	Public bool `json:"public"`
}

// Mine is a group the user shared, by its id here.
type Mine struct {
	Meta
	ID      string    `json:"id"` // on usemagpie.ai
	URL     string    `json:"url"`
	Token   string    `json:"token,omitempty"`
	Version int       `json:"version"`
	At      time.Time `json:"at"`
}

// From is the shared group a group here was imported from.
type From struct {
	ID      string    `json:"id"`
	Version int       `json:"version"`
	Title   string    `json:"title,omitempty"`
	At      time.Time `json:"at"`
	// Seen is the newest version the user was told of and passed over:
	// not offered again
	Seen int `json:"seen,omitempty"`
}

// Retired is a link a group shared before it was removed: kept, with its
// token, so it is never lost; a group made later under the same id
// shares at a link of its own (#7883). The link stays up as it was.
type Retired struct {
	Mine
	Group string    `json:"group"` // its id here, when it was removed
	Gone  time.Time `json:"gone"`
}

type record struct {
	Mine    map[string]Mine `json:"mine,omitempty"`
	From    map[string]From `json:"from,omitempty"`
	Retired []Retired       `json:"retired,omitempty"`
	// Ever: a group was imported from a link here once, so the daily
	// report goes on, saying none is used when those were removed
	Ever bool `json:"ever,omitempty"`
	// Liked are the shared groups liked here, by their ids
	Liked []string `json:"liked,omitempty"`
}

// mu is held over each read and write of shared-groups.json, never over
// the network; talkMu is held over each talk with usemagpie.ai that
// changes a shared group, so two clicks never share one group twice.
var mu, talkMu sync.Mutex

// change has f change shared-groups.json, as read now.
func change(f func(r *record)) error {
	mu.Lock()
	defer mu.Unlock()
	r, err := read()
	if err != nil {
		return err
	}
	f(&r)
	return write(r)
}

func path() string { return filepath.Join(settings.Dir(), "shared-groups.json") }

func init() { provider.GroupsGone = forget }

// forget is what a removed group leaves behind: where it was imported
// from is dropped, and the link it was shared at is set aside (Retired),
// so a group made later under its id is neither shared at that link as
// its next version nor told of that link's updates (#7883). Nothing is
// written when none of ids is recorded.
func forget(ids []string) {
	mu.Lock()
	defer mu.Unlock()
	r, err := read()
	if err != nil {
		log.Printf("groups: %v: removed groups %v keep their shared links", err, ids)
		return
	}
	changed := false
	for _, gid := range ids {
		if _, ok := r.From[gid]; ok {
			delete(r.From, gid)
			changed = true
		}
		if m, ok := r.Mine[gid]; ok {
			r.Retired = append(r.Retired, Retired{Mine: m, Group: gid, Gone: time.Now().UTC()})
			delete(r.Mine, gid)
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := write(r); err != nil {
		log.Printf("groups: %v: removed groups %v keep their shared links", err, ids)
	}
}

// read is shared-groups.json; one that doesn't read is an error, never
// "none shared", which would share a group again under a second link.
func read() (record, error) {
	var r record
	b, err := os.ReadFile(path())
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s doesn't read: %v", path(), err)
	}
	return r, nil
}

func write(r record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path()), 0o700); err != nil {
		return err
	}
	if err := edit.WriteAtomic(path(), b); err != nil {
		return err
	}
	return os.Chmod(path(), 0o600)
}

// Shared is each group the user shared, by its id here, with no token:
// what the GUI shows. Groups no longer here are left out.
func Shared() map[string]Mine {
	mu.Lock()
	r, _ := read()
	mu.Unlock()
	here := groupIDs()
	out := map[string]Mine{}
	for gid, m := range r.Mine {
		if here[gid] {
			m.Token = ""
			out[gid] = m
		}
	}
	return out
}

// Sources are the shared groups the groups here came from, by id here.
func Sources() map[string]From {
	mu.Lock()
	r, _ := read()
	mu.Unlock()
	here := groupIDs()
	out := map[string]From{}
	for gid, f := range r.From {
		if here[gid] {
			out[gid] = f
		}
	}
	return out
}

func groupIDs() map[string]bool {
	out := map[string]bool{}
	for _, g := range provider.Groups() {
		if !g.Auto && !g.Hidden {
			out[g.ID] = true
		}
	}
	return out
}

// Publish shares group gid on usemagpie.ai as provider.PublicGroup has
// it, with meta: at a new link, or, shared already, at its link, as its
// next version.
func Publish(ctx context.Context, gid string, meta Meta) (Mine, error) {
	site := Site()
	if site == "" {
		return Mine{}, ErrOff
	}
	if meta.Title = strings.TrimSpace(meta.Title); meta.Title == "" {
		return Mine{}, errors.New("a shared group needs a title")
	}
	s, err := provider.PublicGroup(gid)
	if err != nil {
		return Mine{}, err
	}
	talkMu.Lock()
	defer talkMu.Unlock()
	mu.Lock()
	r, err := read()
	mu.Unlock()
	if err != nil {
		return Mine{}, err
	}
	old, had := r.Mine[gid]
	body, _ := json.Marshal(map[string]any{
		"title": meta.Title, "description": strings.TrimSpace(meta.Description),
		"author": strings.TrimSpace(meta.Author), "public": meta.Public, "group": s,
	})
	method, u, token := http.MethodPost, site+"/api/groups", ""
	if had {
		method, u, token = http.MethodPut, site+"/api/groups/"+old.ID, old.Token
	}
	var got struct {
		ID      string `json:"id"`
		URL     string `json:"url"`
		Token   string `json:"token"`
		Version int    `json:"version"`
	}
	status, err := ask(ctx, method, u, token, body, &got)
	if status == http.StatusNotFound && had {
		// taken down there: not shared again behind the user's back
		if err := change(func(r *record) { delete(r.Mine, gid) }); err != nil {
			return Mine{}, err
		}
		return Mine{}, ErrGone
	}
	if err != nil {
		return Mine{}, err
	}
	m := Mine{Meta: meta, ID: got.ID, URL: got.URL, Token: got.Token, Version: got.Version, At: time.Now().UTC()}
	if had {
		m.ID, m.URL, m.Token = old.ID, old.URL, old.Token
	}
	if !ValidID(m.ID) || !strings.HasPrefix(m.URL, "https://") && !strings.HasPrefix(m.URL, site) || m.Token == "" || m.Version < 1 {
		return Mine{}, fmt.Errorf("usemagpie.ai's answer doesn't read: %+v", got)
	}
	err = change(func(r *record) {
		if r.Mine == nil {
			r.Mine = map[string]Mine{}
		}
		r.Mine[gid] = m
	})
	if err != nil {
		return Mine{}, err
	}
	m.Token = ""
	return m, nil
}

// Unpublish takes group gid's shared link down; one not shared, or taken
// down already, is done.
func Unpublish(ctx context.Context, gid string) error {
	talkMu.Lock()
	defer talkMu.Unlock()
	mu.Lock()
	r, err := read()
	mu.Unlock()
	if err != nil {
		return err
	}
	m, ok := r.Mine[gid]
	if !ok {
		// a link a removed group was shared at, by its link or id
		old, ok := retiredIn(r, gid)
		if !ok {
			return nil
		}
		m = old.Mine
	}
	site := Site()
	if site == "" {
		return ErrOff
	}
	status, err := ask(ctx, http.MethodDelete, site+"/api/groups/"+m.ID, m.Token, nil, nil)
	if err != nil && status != http.StatusNotFound {
		return err
	}
	return change(func(r *record) {
		if r.Mine[gid].ID == m.ID {
			delete(r.Mine, gid)
		}
		r.Retired = slices.DeleteFunc(r.Retired, func(o Retired) bool { return o.ID == m.ID })
	})
}

// RetiredLink is the link a removed group was shared at, by the link or
// its id (LinkID): still up, and taken down by Unpublish with it.
func RetiredLink(link string) (Retired, bool) {
	mu.Lock()
	r, err := read()
	mu.Unlock()
	if err != nil {
		return Retired{}, false
	}
	o, ok := retiredIn(r, link)
	o.Token = "" // only Unpublish sends it
	return o, ok
}

func retiredIn(r record, link string) (Retired, bool) {
	if id := LinkID(link); id != "" {
		for _, o := range r.Retired {
			if o.ID == id {
				return o, true
			}
		}
	}
	return Retired{}, false
}

// Page is a shared group as usemagpie.ai has it.
type Page struct {
	ID          string               `json:"id"`
	URL         string               `json:"url"`
	Title       string               `json:"title"`
	Description string               `json:"description,omitempty"`
	Author      string               `json:"author,omitempty"`
	Public      bool                 `json:"public"`
	Version     int                  `json:"version"`
	Updated     int64                `json:"updated,omitempty"`
	Group       provider.SharedGroup `json:"group,omitzero"`
	Uses        int                  `json:"uses,omitempty"`
	Likes       int                  `json:"likes,omitempty"`
}

// Fetch is shared group id.
func Fetch(ctx context.Context, id string) (Page, error) {
	var p Page
	site := Site()
	if site == "" {
		return p, ErrOff
	}
	if !ValidID(id) {
		return p, fmt.Errorf("%q is not a shared group's id", id)
	}
	status, err := ask(ctx, http.MethodGet, site+"/api/groups/"+id, "", nil, &p)
	if status == http.StatusNotFound {
		return p, ErrGone
	}
	if err != nil {
		return p, err
	}
	if p.ID != id || p.Group.Magpie != provider.ShareMark {
		return p, fmt.Errorf("usemagpie.ai's answer for %s doesn't read", id)
	}
	return p, nil
}

// Card is a group in the gallery.
type Card struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Version     int      `json:"version"`
	Updated     int64    `json:"updated,omitempty"`
	Members     []string `json:"members"`
	Routing     string   `json:"routing,omitempty"`
	Rules       int      `json:"rules,omitempty"`
	Uses        int      `json:"uses,omitempty"`
	Likes       int      `json:"likes,omitempty"`
	// Here is how many of its members this magpie has (provider.MembersHere)
	Here int `json:"here"`
	// Mine: one the user shared; Imported: the id of the group here
	// imported from it
	Mine     bool   `json:"mine,omitempty"`
	Imported string `json:"imported,omitempty"`
	Liked    bool   `json:"liked,omitempty"`
}

// Gallery is a page of the groups shared publicly.
type Gallery struct {
	Groups []Card `json:"groups"`
	More   bool   `json:"more,omitempty"`
}

// List is a page of the gallery: sort "top" or "new", q what the user
// searches for.
func List(ctx context.Context, sort, q string, page int) (Gallery, error) {
	var g Gallery
	site := Site()
	if site == "" {
		return g, ErrOff
	}
	if sort != "new" {
		sort = "top"
	}
	v := url.Values{"sort": {sort}}
	if q = strings.TrimSpace(q); q != "" {
		v.Set("q", q)
	}
	if page > 0 {
		v.Set("page", fmt.Sprint(page))
	}
	if _, err := ask(ctx, http.MethodGet, site+"/api/groups?"+v.Encode(), "", nil, &g); err != nil {
		return g, err
	}
	mine, from, liked := Shared(), Sources(), Liked()
	for i := range g.Groups {
		c := &g.Groups[i]
		c.Here = provider.MembersHere(c.Members)
		c.Liked = slices.Contains(liked, c.ID)
		for _, m := range mine {
			c.Mine = c.Mine || m.ID == c.ID
		}
		for gid, f := range from {
			if f.ID == c.ID {
				c.Imported = gid
			}
		}
	}
	if g.Groups == nil {
		g.Groups = []Card{}
	}
	return g, nil
}

// Imported records that group gid here was imported from page, for
// Updates to tell the user of the author's newer versions.
func Imported(gid string, page Page) error {
	updates.Lock()
	updates.at = time.Time{}
	updates.Unlock()
	return change(func(r *record) {
		if r.From == nil {
			r.From = map[string]From{}
		}
		r.From[gid] = From{ID: page.ID, Version: page.Version, Title: page.Title, At: time.Now().UTC()}
		r.Ever = true
	})
}

// PassOver has the user not told of version of group gid's source again.
func PassOver(gid string, version int) error {
	return change(func(r *record) {
		if f, ok := r.From[gid]; ok {
			f.Seen = max(f.Seen, version)
			r.From[gid] = f
		}
	})
}

// Update is a newer version of the shared group a group here came from.
type Update struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Title   string `json:"title,omitempty"`
}

// updates is what Updates asked last, kept an hour: the Routing page asks
// each time it opens.
var updates struct {
	sync.Mutex
	at  time.Time
	got map[string]Update
}

// Updates are the groups here whose source has a version newer than the
// one imported, and than the one the user passed over, by id here.
func Updates(ctx context.Context) (map[string]Update, error) {
	from := Sources()
	updates.Lock()
	fresh, got := time.Since(updates.at) < time.Hour && updates.got != nil, updates.got
	updates.Unlock()
	if fresh {
		return keep(got, from), nil
	}
	out := map[string]Update{}
	site := Site()
	if site == "" || len(from) == 0 {
		return out, nil
	}
	var ids []string
	for _, f := range from {
		if !slices.Contains(ids, f.ID) && len(ids) < 50 {
			ids = append(ids, f.ID)
		}
	}
	slices.Sort(ids)
	// asked with no lock held: Imported and PassOver never wait on the
	// network
	var list struct {
		Groups []Update `json:"groups"`
	}
	if _, err := ask(ctx, http.MethodGet, site+"/api/groups?ids="+strings.Join(ids, ","), "", nil, &list); err != nil {
		return nil, err
	}
	for gid, f := range from {
		for _, u := range list.Groups {
			if u.ID == f.ID {
				out[gid] = u
			}
		}
	}
	updates.Lock()
	updates.at, updates.got = time.Now(), out
	updates.Unlock()
	return keep(out, from), nil
}

// keep is got, past what each group was imported at and passed over.
func keep(got map[string]Update, from map[string]From) map[string]Update {
	out := map[string]Update{}
	for gid, u := range got {
		if f, ok := from[gid]; ok && f.ID == u.ID && u.Version > max(f.Version, f.Seen) {
			out[gid] = u
		}
	}
	return out
}

// Like likes shared group id, or unlikes it, from install (this
// computer's random id, stats.InstallID): its likes after.
func Like(ctx context.Context, install, id string, on bool) (int, error) {
	site := Site()
	if site == "" {
		return 0, ErrOff
	}
	var out struct {
		Likes int `json:"likes"`
	}
	if !ValidID(id) {
		return 0, fmt.Errorf("%q is not a shared group's id", id)
	}
	body, _ := json.Marshal(map[string]any{"id": install, "group": id, "on": on})
	if _, err := ask(ctx, http.MethodPost, site+"/api/groups/like", "", body, &out); err != nil {
		return 0, err
	}
	err := change(func(r *record) {
		r.Liked = slices.DeleteFunc(r.Liked, func(l string) bool { return l == id })
		if on {
			r.Liked = append(r.Liked, id)
		}
	})
	return out.Likes, err
}

// Liked are the shared groups liked here.
func Liked() []string {
	mu.Lock()
	r, _ := read()
	mu.Unlock()
	if r.Liked == nil {
		return []string{}
	}
	return r.Liked
}

// Report tells usemagpie.ai the shared groups imported here that are
// still here, from install, for the gallery's counts: with the usage
// stats, and never when the user turned those off.
func Report(ctx context.Context, install string) error {
	site := Site()
	if site == "" {
		return nil
	}
	mu.Lock()
	r, err := read()
	mu.Unlock()
	if err != nil || len(r.From) == 0 && !r.Ever {
		// nothing ever imported here: nothing to say, every day
		return err
	}
	ids := []string{}
	for _, f := range Sources() {
		if !slices.Contains(ids, f.ID) && len(ids) < 50 {
			ids = append(ids, f.ID)
		}
	}
	// none left is said too: the site counts this computer out of the
	// groups it removed
	slices.Sort(ids)
	body, _ := json.Marshal(map[string]any{"id": install, "groups": ids})
	_, err = ask(ctx, http.MethodPost, site+"/api/groups/report", "", body, nil)
	return err
}

var idRe = lazyre.New(`^[a-z2-7]{12}$`)

// ValidID reports whether id is a shared group's id on usemagpie.ai.
func ValidID(id string) bool { return idRe.MatchString(id) }

// LinkID is the shared group's id in text: a link to its page
// (https://usemagpie.ai/g/<id>), a magpie://group?id=<id> link, or the id
// alone; "" for anything else, a group shared as text included.
func LinkID(text string) string {
	text = strings.TrimSpace(text)
	if ValidID(text) {
		return text
	}
	if strings.HasPrefix(text, "usemagpie.ai/") {
		text = "https://" + text
	}
	u, err := url.Parse(text)
	if err != nil || strings.ContainsAny(text, " \n{") {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case provider.Scheme:
		host := strings.ToLower(u.Host + strings.Trim(u.Opaque+u.Path, "/"))
		if id := u.Query().Get("id"); host == "group" && ValidID(id) {
			return id
		}
	case "https", "http":
		site, _ := url.Parse(Site())
		h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		if h != "usemagpie.ai" && (site == nil || site.Host == "" || u.Host != site.Host) {
			return ""
		}
		if id, ok := strings.CutPrefix(strings.TrimRight(u.Path, "/"), "/g/"); ok && ValidID(id) {
			return id
		}
	}
	return ""
}

// ask sends one request to usemagpie.ai, with token as its bearer when
// there is one, and reads its JSON answer into out: the status, and an
// error for one not 2xx, saying what the site said.
func ask(ctx context.Context, method, u, token string, body []byte, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return res.StatusCode, err
	}
	if res.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return res.StatusCode, fmt.Errorf("usemagpie.ai: %s", e.Error)
		}
		return res.StatusCode, fmt.Errorf("usemagpie.ai: %s", res.Status)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return res.StatusCode, fmt.Errorf("usemagpie.ai's answer doesn't read: %v", err)
		}
	}
	return res.StatusCode, nil
}
