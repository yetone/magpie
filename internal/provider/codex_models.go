package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/proc"
)

// codexClientVersion is the Codex CLI version the models list is asked for
// when Codex CLI has not asked itself yet: the list leaves out models newer
// than the client asking.
const codexClientVersion = "0.159.0"

// codexModels asks the ChatGPT backend which Codex models the account's own
// plan has — a Free account lists fewer than a Plus or Pro one, and one it
// doesn't have fails with a 400. It is the list Codex CLI keeps in
// models_cache.json, but that one is of whichever account Codex CLI last
// asked with, if it ran at all.
func codexModels(ctx context.Context, sign func(context.Context, *http.Request, []byte) error) ([]catalog.Model, error) {
	u := CodexBase + "/models?client_version=" + url.QueryEscape(codexVersion())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if err := sign(ctx, req, nil); err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ChatGPT models: %s", resp.Status)
	}
	saveCodexPrompts(b)
	ms := parseCodexModels(b)
	if len(ms) == 0 {
		return nil, errors.New("ChatGPT listed no Codex models")
	}
	return ms, nil
}

var codexVersionCache struct {
	sync.Mutex
	v  string
	at time.Time
}

// codexVersion is the client version the models list is asked for: the
// newest of the Codex CLI installed, the one Codex CLI last asked with and
// codexClientVersion. The list leaves out models newer than the client
// asking, and Codex CLI's models_cache.json keeps the version it was written
// with until Codex next asks — after an update that brought new models
// (0.155 GPT-6 Luna and Sol), asking with it would leave them out.
func codexVersion() string {
	codexVersionCache.Lock()
	defer codexVersionCache.Unlock()
	if time.Since(codexVersionCache.at) >= 10*time.Minute {
		v := codexClientVersion
		newer := func(c string) {
			if c = claudeSemverRE.FindString(c); c != "" && compareClaudeVersion(c, v) > 0 {
				v = c
			}
		}
		var c struct {
			ClientVersion string `json:"client_version"`
		}
		if b, err := os.ReadFile(filepath.Join(codexCLIHome(), "models_cache.json")); err == nil && json.Unmarshal(b, &c) == nil {
			newer(c.ClientVersion)
		}
		if exe := codexExecutable(); exe != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if out, err := proc.ProbeContext(ctx, exe, "--version").Output(); err == nil {
				newer(string(out)) // "codex-cli 0.155.1"
			}
			cancel()
		}
		codexVersionCache.v, codexVersionCache.at = v, time.Now()
	}
	return newerVersion(codexVersionCache.v, codexSeen.get())
}

// codexCLIHome is where Codex CLI keeps its state: CODEX_HOME, else ~/.codex.
func codexCLIHome() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// newerVersion is the later of two versions, a when b isn't one.
func newerVersion(a, b string) string {
	if b = claudeSemverRE.FindString(b); b != "" && (a == "" || compareClaudeVersion(b, a) > 0) {
		return b
	}
	return a
}

// codexSeen is the newest version a Codex client that came through the
// gateway said it was. Codex CLI is often out of magpie's reach — installed
// where a desktop app's PATH doesn't go, its models_cache.json written by
// the version before an update — and the backend serves a model only to a
// client new enough for it: signed by the pool as an older Codex, a model
// Codex CLI reaches on its own answers 400 "The 'gpt-6.1-sol' model is not
// supported when using Codex with a ChatGPT account".
var codexSeen seenVersion

type seenVersion struct {
	sync.Mutex
	v string
}

func (s *seenVersion) get() string {
	s.Lock()
	defer s.Unlock()
	return s.v
}

func (s *seenVersion) saw(v string) {
	s.Lock()
	s.v = newerVersion(s.v, v)
	s.Unlock()
}

// SawCodexClient notes the version a Codex client's request says it is —
// its `version` header, else the one in its User-Agent (codex_cli_rs/0.159.0,
// Codex Desktop/0.159.0) — when the request is Codex's (it names an
// originator, or its User-Agent is codex_…).
func SawCodexClient(h http.Header) {
	ua := h.Get("User-Agent")
	if h.Get("originator") == "" && !strings.HasPrefix(strings.ToLower(ua), "codex") {
		return
	}
	v := claudeSemverRE.FindString(h.Get("version"))
	if v == "" {
		if _, rest, ok := strings.Cut(ua, "/"); ok {
			if m := claudeSemverRE.FindStringIndex(rest); m != nil && m[0] == 0 {
				v = rest[:m[1]]
			}
		}
	}
	if v == "" {
		return
	}
	codexSeen.saw(v)
}

// codexExecutable finds the codex CLI; a var so tests can fake it. Beyond
// PATH it looks where npm, nvm, bun, volta, pnpm, mise and the standalone
// installer put it, which a desktop app's PATH lacks.
var codexExecutable = func() string {
	if p, err := exec.LookPath("codex"); err == nil {
		return p
	}
	names := []string{"codex"}
	if runtime.GOOS == "windows" {
		names = []string{"codex.cmd", "codex.exe", "codex"}
	}
	for _, d := range proc.UserBinDirs() {
		for _, n := range names {
			p := filepath.Join(d, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

// parseCodexModels reads the backend's list, the listed ones in its order.
func parseCodexModels(b []byte) []catalog.Model {
	var list struct {
		Models []struct {
			Slug        string   `json:"slug"`
			DisplayName string   `json:"display_name"`
			Visibility  string   `json:"visibility"`
			Priority    int      `json:"priority"`
			Input       []string `json:"input_modalities"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
			Context int `json:"context_window"`
			Max     int `json:"max_context_window"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	sort.SliceStable(list.Models, func(i, j int) bool { return list.Models[i].Priority < list.Models[j].Priority })
	var out []catalog.Model
	for _, m := range list.Models {
		if m.Slug == "" || m.Visibility == "hide" {
			continue
		}
		mm := catalog.Model{ID: m.Slug, Name: m.DisplayName, Provider: "openai", Context: m.Context}
		if m.Max > m.Context {
			mm.MaxContext = m.Max
		}
		if m.Input != nil {
			yes := slices.Contains(m.Input, "image")
			mm.ImageInput, mm.Images = &yes, yes
		}
		for _, l := range m.Levels {
			mm.Efforts = append(mm.Efforts, l.Effort)
		}
		out = append(out, mm)
	}
	return out
}

// accountModels names where one account's own model list is kept.
func accountModels(agent, user string) string {
	return agent + "@" + keyID(strings.ToLower(user))
}

// Lists reports whether the account's plan has the model, as far as magpie
// knows: one whose list was never fetched is taken to have them all.
func (a *Account) Lists(model string) bool {
	if a == nil {
		return true
	}
	if a.plugin != nil {
		return a.pluginLists(model)
	}
	live, _, ok := catalog.Live(accountModels(a.Agent, a.User))
	if !ok {
		return true
	}
	return slices.ContainsFunc(live, func(m catalog.Model) bool { return m.ID == model })
}

// codexFetchSaved asks for the models of each saved ChatGPT account that
// stands behind the one Codex is signed in to, with that account's own
// sign-in, so the gateway doesn't send one a model its plan lacks. One that
// can't be asked now keeps what it listed last.
func codexFetchSaved(ctx context.Context) {
	for _, l := range Logins("codex") {
		if l.Active || !l.On {
			continue
		}
		user := l.User
		sign := codexSign(func(ctx context.Context) (string, string, error) { return savedLoginToken(ctx, "codex", user) })
		// through the account's own proxy, if it has one
		if ms, err := codexModels(ViaLogin(ctx, "codex", user), sign); err == nil {
			catalog.SaveLive(accountModels("codex", user), CodexBase, ms)
		}
	}
}

// CodexListed is the catalog as a Codex signed in to ChatGPT is handed it,
// after the backend's own models: all but a ChatGPT account's in magpie,
// which the backend lists already. A group answers for its first member
// but is not that provider's.
func CodexListed() []catalog.Model {
	shown, _ := CatalogFor("codex")
	find := GroupFinder()
	return codexListed(shown, func(id string) []Member {
		_, ms, _ := find(id)
		return ms
	})
}

// CodexNativeHidden is the ChatGPT account's own model slugs the user took
// out of Codex's list (HiddenModels): the backend lists them, and the
// gateway drops them from its /models answer as it does the ones not picked.
func CodexNativeHidden() map[string]bool {
	off := HiddenModels("codex")
	if len(off) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, e := range Catalog() {
		if off[e.ID] && e.Group == "" && e.Provider.Account != nil && e.Provider.Account.Agent == "codex" {
			out[e.Model] = true
		}
	}
	return out
}

// CodexListTag names the list Codex is handed, for its ETag: magpie's models
// and the account's own taken out of it, so either changing has Codex ask
// for the list again.
func CodexListTag() string {
	ms := CodexListed()
	off := slices.Sorted(maps.Keys(CodexNativeHidden()))
	for _, slug := range off {
		ms = append(ms, catalog.Model{ID: "-" + slug})
	}
	return codexcat.Tag(ms)
}

// CodexNativePicked is the set of the ChatGPT account's own model slugs the
// user kept, and whether they narrowed that list at all. The backend lists
// every model the account can reach; when the user has picked among them on
// the codex provider, the gateway keeps its /models answer to those (see
// codexModels). Not narrowed — the account's list is left whole.
//
// A provider switched off picks nothing, as it serves no agent anything
// (Provider.Off): its picks are kept for when it is switched on again and
// are not a narrowing now.
func CodexNativePicked() (map[string]bool, bool) {
	p, ok := find(All(), "codex")
	if !ok || p.Off || len(p.Models) == 0 {
		return nil, false
	}
	keep := make(map[string]bool, len(p.Models))
	for _, id := range p.Models {
		keep[id] = true
	}
	return keep, true
}

// codexListed marks a group Fast when a ChatGPT account's GPT model is in
// it, so Codex offers /fast there too; the tier goes out only to that
// account (buildResponses).
func codexListed(shown []Entry, members func(id string) []Member) []catalog.Model {
	var ms []catalog.Model
	// named among all shown: the account's own, which the backend lists,
	// are in Codex's picker beside these
	labels := Labels(shown)
	for i, e := range shown {
		if e.Group == "" && e.Provider.Account != nil && e.Provider.Account.Agent == "codex" {
			continue
		}
		m := catalog.Model{ID: e.ID, Name: labels[i], Efforts: e.Efforts, Images: e.Images, Context: e.Context}
		if e.Group != "" {
			for _, mb := range members(e.ID) {
				if a := mb.Provider.Account; a != nil && a.Agent == "codex" && strings.HasPrefix(mb.Model, "gpt-") {
					m.Fast = true
					break
				}
			}
		}
		ms = append(ms, m)
	}
	return ms
}
