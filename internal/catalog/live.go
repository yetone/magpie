package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/filememo"
)

// A vendor's own /models endpoint is the truth about what it serves today;
// models.dev lags and keeps legacy names around. magpie asks the vendor when
// it has a key, remembers the answer next to the models.dev cache, and lets
// the catalog fill in display names and reasoning levels.

// LivePath is where the fetched model list of one provider is kept.
func LivePath(provider string) string {
	return appdir.JoinFile(appdir.Join(filepath.Dir(CachePath()), "models"), provider, ".json")
}

type liveFile struct {
	Fetched time.Time `json:"fetched"`
	Base    string    `json:"base"`
	Models  []Model   `json:"models"`
	// Sides, for a provider whose protocols are served at bases of their
	// own, is what each of those bases listed, before the lists were
	// merged into Models. A base that can't be asked keeps its own part
	// from the list saved before (#904).
	Sides map[string][]Model `json:"sides,omitempty"`
	// byID is the chat models by their id, in the list's order, made
	// once a read: asked for every model of a list, a copy and a scan of
	// the list each time cost 2,500 models' /v1/models seconds (K.Liu on
	// Discord)
	byID map[string][]Model
}

func readLive(provider string) (liveFile, error) {
	return filememo.Read("live models", LivePath(provider), func(b []byte) (liveFile, error) {
		var f liveFile
		if err := json.Unmarshal(b, &f); err != nil {
			return f, err
		}
		mendKinds(f.Models)
		for _, side := range f.Sides {
			mendKinds(side)
		}
		f.byID = map[string][]Model{}
		for _, m := range Chat(f.Models) {
			f.byID[m.ID] = append(f.byID[m.ID], m)
		}
		return f, nil
	})
}

// mendKinds reads a list saved before Kinds as Kinds reads it now: an
// OpenCode Go list kept with media types ("image/jpeg") said its models
// took no image until it was fetched again, so a model that sees is told
// so from the first read after the update, not the next fetch.
func mendKinds(ms []Model) {
	for i := range ms {
		m := &ms[i]
		if m.InputModalities == nil {
			continue
		}
		m.InputModalities, m.OutputModalities = Kinds(m.InputModalities), Kinds(m.OutputModalities)
		if m.ImageInput != nil && !*m.ImageInput && slices.Contains(m.InputModalities, "image") {
			yes := true
			m.ImageInput, m.Images = &yes, true
		}
	}
}

// LiveByID are the models of Live with this id, in the list's order
// (a list may name one twice): what a look through Live finds, without
// the list copied for each model asked about.
func LiveByID(provider, model string) []Model {
	f, err := readLive(provider)
	if err != nil {
		return nil
	}
	return slices.Clone(f.byID[model])
}

// LiveByIDShared is LiveByID without the copy, for a caller that only
// reads it: asked for a model's APIs, price or tiers for every model a
// page lists, the copies were 32 MB of a live magpie's 2.6 GB in a minute
// and a half.
func LiveByIDShared(provider, model string) []Model {
	f, err := readLive(provider)
	if err != nil {
		return nil
	}
	return slices.Clip(f.byID[model])
}

// LiveListed is whether Live lists any model, without the list copied.
func LiveListed(provider string) bool {
	f, err := readLive(provider)
	return err == nil && len(f.byID) > 0
}

// Live returns the model list last fetched from the provider, if any: the
// models to talk to, without the ones that draw (LiveDrawers).
func Live(provider string) (models []Model, fetched time.Time, ok bool) {
	f, err := readLive(provider)
	if err != nil {
		return nil, time.Time{}, false
	}
	ms := Chat(f.Models)
	if len(ms) == 0 {
		return nil, time.Time{}, false
	}
	return ms, f.Fetched, true
}

// LiveAll is the whole model list last fetched from the provider, the
// models that draw or make videos among them, with each one's keys: what
// tells which of the provider's keys serves an image model (#1657), which
// Live, the models to talk to, leaves out.
func LiveAll(provider string) (models []Model, ok bool) {
	f, err := readLive(provider)
	if err != nil || len(f.Models) == 0 {
		return nil, false
	}
	return f.Models, true
}

// LiveSplit returns the provider's fetched list as every endpoint's own part
// of it: sides[base] is what that endpoint listed, and all is the whole list
// those parts were merged into. ok is false where there is no list at all;
// sides is nil for a list saved without its parts, where every endpoint
// answered alike.
func LiveSplit(provider string) (sides map[string][]Model, all []Model, ok bool) {
	f, err := readLive(provider)
	if err != nil || len(f.Models) == 0 {
		return nil, nil, false
	}
	return f.Sides, f.Models, true
}

// LiveDrawers are the models in the provider's fetched list that make
// images, in the list's order.
func LiveDrawers(provider string) []Model {
	f, err := readLive(provider)
	if err != nil {
		return nil
	}
	var out []Model
	for _, m := range f.Models {
		if m.Draws {
			out = append(out, m)
		}
	}
	return out
}

// LiveVideomakers are the models in the provider's fetched list that make
// videos, in the list's order.
func LiveVideomakers(provider string) []Model {
	f, err := readLive(provider)
	if err != nil {
		return nil
	}
	var out []Model
	for _, m := range f.Models {
		if m.Films {
			out = append(out, m)
		}
	}
	return out
}

// Chat is ms without the models that draw or make videos.
func Chat(ms []Model) []Model {
	out := make([]Model, 0, len(ms))
	for _, m := range ms {
		if !m.Draws && !m.Films {
			out = append(out, m)
		}
	}
	return out
}

// SaveLive stores a fetched list; an empty list forgets it.
func SaveLive(provider, base string, models []Model) error {
	return SaveLiveSides(provider, base, models, nil)
}

// SaveLiveSides is SaveLive, keeping what each endpoint answered with beside
// the merged list (LiveSplit): a provider whose protocols sit at bases of
// their own asks every base, and a base that can't be asked next time has to
// know which of the models were its own to keep (#904).
func SaveLiveSides(provider, base string, models []Model, sides map[string][]Model) error {
	p := LivePath(provider)
	if len(models) == 0 {
		err := os.Remove(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil {
			Touched()
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(liveFile{Fetched: time.Now(), Base: base, Models: models, Sides: sides}, "", "  ")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return err
	}
	Touched()
	return nil
}

// Changed, when set, is told that the models magpie offers may be others
// now: a provider added, edited or removed, a vendor's list fetched anew.
// The agent package sets it, to bring the model lists agents keep in files
// of their own up to date.
var Changed func()

// Forget, when set, is told so first: the provider package drops the
// catalog a request holds (provider.Hold).
var Forget func()

// Touched tells Forget and Changed, if set.
func Touched() {
	if Forget != nil {
		Forget()
	}
	if Changed != nil {
		Changed()
	}
}

// Fetch asks an endpoint for its models. base is an API base URL of any
// flavour (…/v1, …/anthropic, …/api); the usual list paths are tried
// around it. The result keeps the server's order. anthropic sends the
// Anthropic version header, which the official API requires but which a
// dual-protocol relay like OpenRouter reads as a request for its
// Anthropic-flavoured catalog — namespaced, differently named ids.
func Fetch(ctx context.Context, base, key string, anthropic bool, headers map[string]string) ([]Model, error) {
	ms, _, err := FetchAt(ctx, base, key, anthropic, headers)
	return ms, err
}

// FetchAt is Fetch, and says which URL answered. An OpenAI-style base
// with a version in its path (…/v1, …/api/plan/v3, …/api/paas/v4,
// …/v1beta/openai) names the vendor's API as it is and is asked only as
// written, at base/models: a /v1 is looked for only around a base without
// one (a bare host, a relay's …/api), where it may have been left out. An
// Anthropic base is the root /v1/messages is asked at, so /v1/models
// under it is its own list. When no URL answers, the error names each one
// asked and what it said.
func FetchAt(ctx context.Context, base, key string, anthropic bool, headers map[string]string) ([]Model, string, error) {
	return FetchAtListed(ctx, base, key, anthropic, headers, nil)
}

// ErrAsked is what FetchAtListed says when every list URL of the base was
// already asked for this key and none of them answered: there is nothing
// left to ask there, which is not a refusal of the base and is not
// reported as one to the user. A caller asking several bases of one
// provider reads it as "this base said nothing new" and moves on.
var ErrAsked = errors.New("every list of this base was already asked for this key")

// FetchAtListed is FetchAt over a provider's several bases, and over
// what one of them has already asked. listed is the record of this key's
// requests so far (NewListed), and the key of a request is the list URL
// it is made at and the authentication it is made with — Bearer, or the
// Anthropic version header with it.
//
// Two rules follow, and both are what make a multi-protocol vendor's
// models findable without asking its list twice (#1006). A list URL this
// key has already listed is not asked again, by any protocol: the answer
// is the answer, so a …/v1 base and the bare host beside it that speaks
// Anthropic are one /v1/models, fetched once. And a request already made
// is not made again — but a URL refused under one authentication is not
// refused under the next, so the Gemini list of a base whose Chat request
// was answered 401 is asked, with x-goog-api-key, instead of being taken
// for a list already had.
func FetchAtListed(ctx context.Context, base, key string, anthropic bool, headers map[string]string, listed *Listed) ([]Model, string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, "", errors.New("no base URL")
	}
	all := listURLs(base, anthropic)
	for _, u := range all {
		if ms, ok := listed.listed(u); ok {
			return ms, u, nil
		}
	}
	auth := listAuthFor(anthropic)
	var urls []string
	for _, u := range all {
		if !listed.asked(auth, u) {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return nil, "", fmt.Errorf("%s: no model list: %w", base, ErrAsked)
	}

	var errs []string
	for _, u := range urls {
		ms, err := fetchOne(ctx, u, key, anthropic, headers)
		listed.record(auth, u, ms)
		if err == nil && len(ms) > 0 {
			return ms, u, nil
		}
		if err == nil {
			err = errors.New(u + ": no models listed")
		}
		errs = append(errs, err.Error())
		if ctx.Err() != nil {
			break
		}
	}
	return nil, "", errors.New("no model list: " + strings.Join(errs, "; "))
}

// listAuthFor names how a list request is authenticated, which is half of
// what makes it the request it is: Chat and Responses both send Bearer,
// an Anthropic base sends the Anthropic version header as well.
func listAuthFor(anthropic bool) string {
	if anthropic {
		return "anthropic"
	}
	return "bearer"
}

// Listed is what one fetch of a provider has asked for one key: every
// list URL it asked at, under the authentication it asked it with, and
// the models of the ones that answered. A caller asking several bases of
// one provider passes the same Listed to each of them (FetchAtListed), so
// no list is fetched twice and no request is made twice.
type Listed struct {
	askedAt map[string]map[string]bool // authentication -> list URL -> asked
	lists   map[string][]Model         // list URL -> the models it listed
}

// NewListed is an empty record of one fetch's model list requests.
func NewListed() *Listed {
	return &Listed{askedAt: map[string]map[string]bool{}, lists: map[string][]Model{}}
}

// asked reports whether the list at u has already been asked with this
// authentication for this key. A nil Listed asks nothing and records
// nothing, so a caller that keeps no record pays for every URL as before.
func (l *Listed) asked(auth, u string) bool {
	if l == nil {
		return false
	}
	return l.askedAt[auth][u]
}

// record notes a request made at u with this authentication, and the
// models it answered with, if it answered with any.
func (l *Listed) record(auth, u string, ms []Model) {
	if l == nil {
		return
	}
	if l.askedAt[auth] == nil {
		l.askedAt[auth] = map[string]bool{}
	}
	l.askedAt[auth][u] = true
	if len(ms) > 0 {
		l.lists[u] = ms
	}
}

// listed is what the list at u answered with, if it answered.
func (l *Listed) listed(u string) ([]Model, bool) {
	if l == nil {
		return nil, false
	}
	ms, ok := l.lists[u]
	return ms, ok
}

// listURLs is the list URLs a base is asked at, in the order they are
// asked (FetchAtListed).
func listURLs(base string, anthropic bool) []string {
	var urls []string
	add := func(u string) {
		for _, x := range urls {
			if x == u {
				return
			}
		}
		urls = append(urls, u)
	}
	add(base + "/models")
	if anthropic || !Versioned(base) {
		add(base + "/v1/models")
		root := base
		for _, suffix := range []string{"/anthropic", "/apps/anthropic", "/api/anthropic", "/v1", "/api", "/api/v1"} {
			if strings.HasSuffix(root, suffix) {
				root = strings.TrimSuffix(root, suffix)
			}
		}
		add(root + "/v1/models")
		add(root + "/models")
	}
	return urls
}

// Versioned reports whether an API base URL has a version in its path — a
// segment like v1, v3, v4 or v1beta — and so names the vendor's API as it
// is, with no /v1 left out of it.
func Versioned(base string) bool {
	rest := base
	if _, after, ok := strings.Cut(base, "://"); ok {
		rest = after
	}
	_, path, _ := strings.Cut(rest, "/")
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	for _, seg := range strings.Split(path, "/") {
		if len(seg) >= 2 && (seg[0] == 'v' || seg[0] == 'V') && seg[1] >= '0' && seg[1] <= '9' {
			return true
		}
	}
	return false
}

// FetchURL asks for the model list at exactly url.
func FetchURL(ctx context.Context, url, key string, anthropic bool, headers map[string]string) ([]Model, error) {
	ms, err := fetchOne(ctx, strings.TrimSpace(url), key, anthropic, headers)
	if err == nil && len(ms) == 0 {
		err = errors.New(url + ": no models listed")
	}
	return ms, err
}

// modelPages caps how many pages of one model list are followed before
// the list is taken to be unreadable: a real catalog is one or two pages
// at PageLimit, and a relay that pages for ever stops here rather than
// being asked until it says so.
const modelPages = 20

// PageLimit is the page size asked of a list that pages, which Anthropic
// accepts: a real catalog of its own fits in one or two pages of it.
const PageLimit = 1000

// fetchOne asks the model list at url to its end: the first page as the
// URL stands, and every page after it with the id it ended at (Paged), so
// a list that pages is the whole list and not the twenty of its first
// page — a base whose only list is a paged one (Anthropic's own) has no
// other answer to fall back on, and was left with none (#1006).
func fetchOne(ctx context.Context, url, key string, anthropic bool, headers map[string]string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var out []Model
	err := Paged(url, func(at string) (bool, string, error) {
		ms, more, last, err := fetchPage(ctx, at, key, anthropic, headers)
		if err != nil {
			return false, "", err
		}
		out = append(out, ms...)
		return more, last, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Paged asks a model list that pages for every page of itself: page asks
// for one page, at the URL given, and says whether another page follows
// and the id it starts after. The first page is asked at list as it
// stands, so a list that does not page is asked exactly as it always was,
// and each page after it at the same list with limit=PageLimit and
// after_id set to the id the page before ended at. It stops when a page
// says none follows.
//
// What does not stop it leaves the list unread, and an unread list is no
// answer at all, as a page of it was (#904): a page that fails, a list
// that pages without naming the id to carry on from, one that answers the
// page just asked again under that same id, and one still paging after
// modelPages pages. Half a list is not a list — it is missing every model
// of the pages after, which the caller would take for models the vendor
// dropped, and would then drop from the user's picks.
//
// A caller that has to ask the list itself, for the headers it takes or
// the shape it reads, pages it here rather than with a rule of its own
// (provider.kiloModels).
func Paged(list string, page func(at string) (more bool, last string, err error)) error {
	after := ""
	for n := 0; ; n++ {
		at := list
		if after != "" {
			u, err := url.Parse(list)
			if err != nil {
				return fmt.Errorf("%s: not a URL to page: %w", list, err)
			}
			q := u.Query()
			q.Set("after_id", after)
			q.Set("limit", strconv.Itoa(PageLimit))
			u.RawQuery = q.Encode()
			at = u.String()
		}
		more, last, err := page(at)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		if last == "" || last == after {
			return fmt.Errorf("%s: only a page of the model list (has_more, last id %q), not the whole list", list, last)
		}
		if n+1 >= modelPages {
			return fmt.Errorf("%s: still paging after %d pages of the model list (last id %q)", list, modelPages, last)
		}
		after = last
	}
}

// fetchPage asks one page of the model list at url, and says whether
// another page follows, and the id it starts after. The caller bounds the
// time a whole list may take (fetchOne), and which key header to ask with
// is learned once and kept for the list that learned it (listAuth).
func fetchPage(ctx context.Context, url, key string, anthropic bool, headers map[string]string) ([]Model, bool, string, error) {
	auth := listAuthOf(ctx)
	ask := func(only string) (*http.Response, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "magpie")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("x-api-key", key)
			// a vendor that takes one of the two alone
			OneAuth(req.Header, only)
		}
		if anthropic {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		// The user's own headers, after the defaults so a private auth scheme
		// wins. Written directly so the name keeps the exact case the user typed.
		for k, v := range headers {
			PutUserHeader(req.Header, k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		return res, b, nil
	}
	res, b, err := ask(auth.only)
	if err != nil {
		return nil, false, "", err
	}
	if auth.learned != nil && auth.only == "" && key != "" && BothAuthRefused(res.StatusCode, b) {
		// refused for sending both: each alone, the one taken kept
		for _, only := range OneAuthOrder {
			if res, b, err = ask(only); err != nil {
				return nil, false, "", err
			}
			if res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusForbidden {
				auth.learned(only)
				break
			}
		}
	}
	if res.StatusCode != http.StatusOK {
		if msg := errorMessage(b); msg != "" {
			return nil, false, "", fmt.Errorf("%s: %s (%s)", url, res.Status, msg)
		}
		return nil, false, "", fmt.Errorf("%s: %s", url, res.Status)
	}
	var v struct {
		Data    []liveModel `json:"data"`
		Models  []liveModel `json:"models"`
		HasMore bool        `json:"has_more"`
		LastID  string      `json:"last_id"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, false, "", fmt.Errorf("%s: not a model list", url)
	}
	rows := v.Data
	// each row as it was sent too, for one that is a Codex catalog entry
	// (Model.Codex): rows decoded, so these decode alike
	var raw struct {
		Data   []json.RawMessage `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	json.Unmarshal(b, &raw)
	raws := raw.Data
	if len(rows) == 0 {
		rows, raws = v.Models, raw.Models
	}
	// A list that says it has more is a page of the vendor's catalog, not
	// the catalog: a model the user picked that lives on a later page is
	// not in this reply, and a base that answers without a model is read
	// as one that dropped it (#904). The pick would go for good, on a list
	// that never said the model was gone. So the pages are followed to the
	// end of the list (Paged), and only a list that cannot be read to its
	// end says nothing about what this base serves today, which keeps what
	// it listed last time. The shape is a convention, not a vendor's —
	// Anthropic's and OpenAI's /v1/models both page with has_more and
	// last_id.
	var out []Model
	for i, r := range rows {
		id := r.ID
		if id == "" {
			id = r.Name
		}
		var codex json.RawMessage
		if i < len(raws) && CodexEntry(raws[i]) {
			codex = raws[i]
			if id == "" {
				id = r.Slug // Codex's catalog names a model by its slug alone
			}
		}
		if id == "" {
			continue
		}
		// one that goes from here and comes back (magpie_via.go)
		if loops(r.Via) {
			continue
		}
		// Another magpie's list is already what its user exposed. Keep
		// its retrieval models too; a vendor's general list still leaves
		// out models agents can't chat with.
		decision := r.Kind == "decision"
		ins, outs := Kinds(r.Modalities.Input), Kinds(r.Modalities.Output)
		if ins == nil && outs == nil {
			// Volcengine Ark's names: {"input_modalities": […],
			// "output_modalities": ["video"]}
			ins, outs = Kinds(r.Modalities.InputModalities), Kinds(r.Modalities.OutputModalities)
		}
		// a model the list says makes videos: another magpie's kind, or,
		// where the list marks no kind, Ark's domain "VideoGeneration" or
		// an output of video without text (doubao-seedance-*, wan2-1-*),
		// which is no model to chat with (axelburks on Discord: Ark's
		// Seedance sat among its chat models, not under Video models)
		films := r.Kind == "video" || r.Kind == "" && (r.Domain == "VideoGeneration" || slices.Contains(outs, "video") && !slices.Contains(outs, "text"))
		drawer := !films && !decision && (DrawsID(id) && !strings.Contains(strings.ToLower(id), "deep-research") || r.Kind == "image" ||
			r.Kind == "" && (r.Domain == "ImageGeneration" || slices.Contains(outs, "image") && !slices.Contains(outs, "text")))
		// and none the list says answers without text (Ark's 3D models)
		md := mdModel{ID: id}
		md.Modalities.Output = outs
		if !drawer && !films && !decision && headers["X-Magpie-Drawers"] == "" && !textModel(md) {
			continue
		}
		name, plain := r.DisplayName, ""
		// OpenRouter's "Anthropic: Claude …" is a vendor-prefixed name;
		// leave it to the catalog rather than changing every display name.
		if name == "" && !strings.Contains(r.Name, ": ") {
			name = r.Name
		}
		if r.Label != "" {
			name = r.Label
			if r.DisplayName != "" && r.DisplayName != r.Label {
				plain = r.DisplayName
			}
		}
		if name == "" {
			name = id
		}
		input := imageInput(ins)
		apis := EndpointAPIs(r.Endpoints)
		if native := EndpointAPIs(r.Native); len(native) > 0 {
			apis = native
		}
		if len(apis) == 0 {
			apis = targetAPIs(r.TypeTarget)
		}
		m := Model{ID: id, Name: name, Plain: plain, ImageInput: input, APIs: apis, Draws: drawer, Films: films, Decides: decision,
			InputModalities: ins, OutputModalities: outs}
		if n, ok := r.ContextLength.(float64); ok && n > 0 {
			m.Context = int(n)
		}
		if n, ok := r.Output.(float64); ok && n > 0 {
			m.Output = int(n)
		}
		m.Efforts = levelsOf(r.Levels)
		if r.WebSearch == "native" || r.WebSearch == "magpie" {
			m.WebSearch = r.WebSearch
		}
		if input != nil {
			m.Images = *input
		}
		m.Tiers = tiersOf(r.Tiers)
		var pr *Price
		if len(r.Price) > 0 && json.Unmarshal(r.Price, &pr) == nil && pr != nil && pr.sane() {
			m.Price = pr
		}
		if codex != nil {
			codexFacts(&m, r, codex)
		}
		kimiFacts(&m, r)
		m.Via = slices.Clip(r.Via)
		out = append(out, m)
	}
	return out, v.HasMore, v.LastID, nil
}

// errorMessage is the message of a vendor's JSON error reply
// ({"error":{"message":…}}, {"error":"…"}, {"message":…}), cut short.
func errorMessage(b []byte) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	msg := v.Message
	var e struct {
		Message string `json:"message"`
	}
	var s string
	if json.Unmarshal(v.Error, &e) == nil && e.Message != "" {
		msg = e.Message
	} else if json.Unmarshal(v.Error, &s) == nil && s != "" {
		msg = s
	}
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > 160 {
		msg = string(r[:160]) + "…"
	}
	return msg
}

type liveModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Modalities  struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
		// Volcengine Ark's
		InputModalities  []string `json:"input_modalities"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"modalities"`
	// what Volcengine Ark's list says a model is for: "LLM",
	// "VideoGeneration", "ImageGeneration"…
	Domain string `json:"domain"`
	// the paths the model is served on, where the vendor says: Command
	// Code's Claude models on /messages alone, its open ones on
	// /chat/completions and /responses
	Endpoints []string `json:"supported_endpoints"`
	// the context window, where the list tells it (OpenRouter, Command
	// Code); any, as a vendor's odd value mustn't lose the whole list
	ContextLength any `json:"context_length"`
	// what another magpie's list tells of each model: the APIs its own
	// provider serves it on, where a request goes on as it is rather
	// than translated (native_endpoints), its longest reply and its
	// reasoning levels. any, as a vendor's odd value mustn't lose the
	// whole list
	Native []string `json:"native_endpoints"`
	Output any      `json:"max_output_tokens"`
	Levels any      `json:"supported_reasoning_levels"`
	// another magpie's name for the model with its provider there after
	// it, and "image", "video" or "decision" for its other models
	Label string `json:"magpie_label"`
	Kind  string `json:"kind"`
	// how another magpie searches the web for the model: "native" or
	// "magpie" (Model.WebSearch)
	WebSearch string `json:"web_search"`
	// the service tiers another magpie offers its Codex on the model
	// (Fast's "priority"), as Codex's catalog writes them ({"id":…}) or
	// as plain ids: kept as Model.Tiers (#1234). any, as an odd value
	// mustn't lose the whole list
	Tiers any `json:"service_tiers"`
	// what another magpie counts a call to the model at, asked for with
	// its X-Magpie-Prices header: the price its user set, else its list
	// price. Raw, as an odd value mustn't lose the whole list
	Price json.RawMessage `json:"magpie_price"`
	// the magpies another magpie's model passes through to its vendor,
	// itself last (magpie_via.go)
	Via []string `json:"magpie_via"`
	// the protocol family PipeLLM routes the model by: openai, anthropic
	// or gemini
	TypeTarget string `json:"type_target"`
	// a Codex catalog entry's (CodexEntry): its id, its window and the
	// most it may be raised to, and what it takes. any, as an odd value
	// mustn't lose the whole list
	Slug      string `json:"slug"`
	Window    any    `json:"context_window"`
	MaxWindow any    `json:"max_context_window"`
	Inputs    any    `json:"input_modalities"`
	// what Kimi Code's list (/coding/v1/models) says of a model's
	// thinking and tools, flat and nested: supports_thinking_type
	// "only" is a model that always thinks, supports_dynamic_tools one
	// that loads tools mid-conversation, think_efforts' default_effort
	// the effort it thinks at unasked (Model.AlwaysThinks, DynamicTools,
	// DefaultEffort). any, as an odd value mustn't lose the whole list
	ThinkingType any `json:"supports_thinking_type"`
	DynTools     any `json:"supports_dynamic_tools"`
	ThinkEfforts any `json:"think_efforts"`
	Reasoning    any `json:"reasoning"`
	Tools        any `json:"tools"`
}

// kimiFacts reads what Kimi Code's model list says of a model's thinking
// and tools into m, from its flat fields or their nested forms
// (reasoning.type, reasoning.effort.default, tools.dynamic_load).
func kimiFacts(m *Model, r liveModel) {
	field := func(v any, keys ...string) any {
		for _, k := range keys {
			o, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = o[k]
		}
		return v
	}
	if t, _ := r.ThinkingType.(string); t == "only" {
		m.AlwaysThinks = true
	} else if t, _ := field(r.Reasoning, "type").(string); t == "only" && r.ThinkingType == nil {
		m.AlwaysThinks = true
	}
	if b, ok := r.DynTools.(bool); ok {
		m.DynamicTools = b
	} else if b, ok := field(r.Tools, "dynamic_load").(bool); ok {
		m.DynamicTools = b
	}
	if d, _ := field(r.ThinkEfforts, "default_effort").(string); d != "" {
		m.DefaultEffort = d
	} else if d, _ := field(r.Reasoning, "effort", "default").(string); d != "" {
		m.DefaultEffort = d
	}
}

// codexFacts reads what a Codex catalog entry says of its model into m:
// the window, the most it may be raised to and whether it takes images,
// where the row says nothing else of them. The entry itself is kept
// whole in m.Codex.
func codexFacts(m *Model, r liveModel, raw json.RawMessage) {
	m.Codex = raw
	if n, ok := r.Window.(float64); ok && n > 0 && m.Context == 0 {
		m.Context = int(n)
	}
	if n, ok := r.MaxWindow.(float64); ok && int(n) > m.Context {
		m.MaxContext = int(n)
	}
	if xs, ok := r.Inputs.([]any); ok && m.InputModalities == nil {
		in := []string{}
		for _, x := range xs {
			if s, ok := x.(string); ok {
				in = append(in, s)
			}
		}
		in = Kinds(in)
		m.InputModalities, m.ImageInput = in, imageInput(in)
		m.Images = *m.ImageInput
	}
}

// tiersOf are the ids of a list's service_tiers, as Codex's catalog
// writes them ([{"id":"priority","name":"Fast"}]) or as plain ids.
func tiersOf(v any) []string {
	xs, _ := v.([]any)
	var out []string
	for _, x := range xs {
		id, _ := x.(string)
		if o, ok := x.(map[string]any); ok {
			id, _ = o["id"].(string)
		}
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// levelsOf are the efforts of a list's supported_reasoning_levels, as
// magpie and Codex write them ([{"effort":"high"}]) or as plain names.
func levelsOf(v any) []string {
	xs, _ := v.([]any)
	var out []string
	for _, x := range xs {
		e, _ := x.(string)
		if o, ok := x.(map[string]any); ok {
			e, _ = o["effort"].(string)
		}
		if e != "" && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// EndpointAPIs names the APIs of a model list's supported_endpoints —
// "chat", "responses", "anthropic" — leaving out any magpie doesn't speak
// (Copilot's websocket one); nil when it names none of them.
func EndpointAPIs(endpoints []string) []string {
	var out []string
	for _, e := range endpoints {
		var api string
		switch strings.TrimPrefix(strings.TrimSuffix(e, "/"), "/v1") {
		case "/chat/completions":
			api = "chat"
		case "/responses":
			api = "responses"
		case "/messages":
			api = "anthropic"
		}
		if api != "" && !slices.Contains(out, api) {
			out = append(out, api)
		}
	}
	return out
}

// targetAPIs are the APIs a model is served on by its type_target, as
// PipeLLM's list says it: its own /v1/chat/completions and /v1/responses
// take the openai family alone, /v1/messages the anthropic one (asked
// there rather than through a converter), and its converter for Chat
// (/openai/v1) every family, Gemini's too.
func targetAPIs(target string) []string {
	switch target {
	case "openai":
		return []string{"chat", "responses"}
	case "anthropic":
		return []string{"anthropic"}
	case "gemini":
		return []string{"chat"}
	}
	return nil
}

// Decorate fills in names and reasoning levels for live models from the
// catalog's entry for the same id, keeping the live order.
func Decorate(live []Model, known []Model) []Model {
	byID := make(map[string]Model, len(known))
	var bySame map[string]Model
	for _, m := range known {
		byID[m.ID] = m
	}
	out := make([]Model, 0, len(live))
	fallback := make([]string, len(live))
	for i, m := range live {
		k, ok := byID[m.ID]
		if i := strings.LastIndexByte(m.ID, '/'); !ok && i >= 0 {
			k, ok = byID[m.ID[i+1:]] // a gateway's "deepseek/deepseek-chat"
		}
		if !ok && len(known) > 0 {
			// the same model under another spelling (SameModel): Ark's
			// list says glm-5-3-flash-260828 where its plan's catalog
			// says glm-5.3-flash. Of several, the first known.
			if bySame == nil {
				bySame = make(map[string]Model, len(known))
				for _, x := range known {
					if _, dup := bySame[SameModel(x.ID)]; !dup {
						bySame[SameModel(x.ID)] = x
					}
				}
			}
			k, ok = bySame[SameModel(m.ID)]
		}
		if ok {
			if m.Name == "" || m.Name == m.ID {
				fallback[i] = k.Name
			}
			m.Efforts, m.Released, m.Provider = k.Efforts, k.Released, k.Provider
			m.Reasoning = m.Reasoning || k.Reasoning
			if m.ImageInput == nil {
				m.ImageInput = k.ImageInput
				m.Images = m.Images || k.Images
				m.InputModalities = k.InputModalities
			}
			if m.OutputModalities == nil {
				m.OutputModalities = k.OutputModalities
			}
			if m.Context == 0 {
				m.Context = k.Context
			}
			if m.Output == 0 {
				m.Output = k.Output
			}
			if k.Temperature != nil {
				m.Temperature = k.Temperature
			}
		}
		out = append(out, m)
	}
	fillNames(out, fallback)
	return out
}

// sane reports whether a price another magpie sent is one a vendor could
// charge: no part below zero or out of range, and tiers of a size.
func (p Price) sane() bool {
	ok := func(vs ...float64) bool {
		for _, v := range vs {
			if math.IsNaN(v) || v < 0 || v > 1e6 {
				return false
			}
		}
		return true
	}
	if !ok(p.Input, p.Output, p.CacheRead, p.CacheWrite, p.CacheWrite1h) {
		return false
	}
	for _, t := range p.Tiers {
		if t.Above <= 0 || !ok(t.Input, t.Output, t.CacheRead, t.CacheWrite, t.CacheWrite1h) {
			return false
		}
	}
	return true
}
