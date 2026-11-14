package library

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// billion-context's own settings file (billion-context.json): the few of
// its settings the Library's billion-context tab changes, carried by a
// sync with the rest of the library, and every running bili told to read
// it again once it changes. bili reads it as JSON or JSONC; magpie edits
// it the way it edits an agent's JSONC file (edit.PatchJSON), so comments,
// key order and every key magpie doesn't know stay as they were.

// BiliConfigFile is billion-context.json where bili itself looks for it
// (src/paths.ts): BILI_CONFIG_FILE, else billion-context/ under
// XDG_CONFIG_HOME, else under ~/.config — on Windows too, where bili's
// homedir() is %USERPROFILE%.
func BiliConfigFile() string {
	if f := appdir.Getenv("BILI_CONFIG_FILE"); f != "" {
		if abs, err := filepath.Abs(f); err == nil {
			return abs
		}
		return f
	}
	base := appdir.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home(), ".config")
	} else if abs, err := filepath.Abs(base); err == nil {
		base = abs
	}
	return filepath.Join(base, "billion-context", "billion-context.json")
}

// biliCredFile is bili's private store of the keys its summary models
// are reached with (src/external-summary-credentials.ts). It is this
// computer's and never carried.
func biliCredFile() string { return BiliConfigFile() + ".summary-credentials.json" }

// biliRecipe is the name of the recipe magpie adds to billion-context.json
// for a summary model magpie serves, and of its key in bili's private
// store: bili sends the summary requests to magpie's gateway.
const biliRecipe = "magpie"

// biliRecipeAgent is the agent magpie's gateway counts those requests
// under (gateway.TokenFor).
const biliRecipeAgent = "billion-context"

// biliCarriedURL is the gateway address a carried billion-context.json
// names in magpie's recipe: each computer's own gateway goes in when it is
// written there (biliLocal), so two computers on different ports carry the
// same bytes and don't sync it back and forth.
const biliCarriedURL = "http://" + gateway.DefaultAddr + "/v1"

// biliGatewayURL is this computer's gateway, as magpie's recipe names it.
var biliGatewayURL = func() string { return gateway.URL() + "/v1" }

// maxBiliCarried is the largest billion-context.json a sync carries; a
// bigger one isn't a settings file anyone typed.
const maxBiliCarried = 1 << 20

// biliCfgMu keeps two of magpie's own edits of the file apart.
var biliCfgMu sync.Mutex

// biliText is the file's text and whether it is there; an error is a file
// that is there and can't be read, which is not an empty one.
func biliText(p string) (string, bool, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

// biliParse is the file read as bili reads it: JSON or JSONC, a byte
// order mark read through. ok is false when it can't be.
func biliParse(text string) (gjson.Result, bool) {
	if strings.TrimSpace(text) == "" {
		return gjson.Parse("{}"), true
	}
	j := jsonc.ToJSON(edit.TrimBOM([]byte(text)))
	if !json.Valid(j) {
		return gjson.Result{}, false
	}
	r := gjson.ParseBytes(j)
	if !r.IsObject() {
		return gjson.Result{}, false
	}
	return r, true
}

// biliOurs is whether the file's recipe under biliRecipe is the one magpie
// writes: its key from bili's store under that name, sent to this
// computer. A recipe of the user's by that name is left alone.
func biliOurs(r gjson.Result) bool {
	p := r.Get("providers." + biliRecipe)
	if !p.Exists() {
		return false
	}
	return p.Get("credentialRef").String() == biliRecipe && !p.Get("apiKeyEnv").Exists() && biliLoopback(strings.TrimSuffix(strings.TrimSuffix(p.Get("baseUrl").String(), "/"), "/v1"))
}

// biliAt is text with magpie's recipe sent to url; text as it was when
// the recipe isn't magpie's or is sent there already.
func biliAt(text, url string) string {
	r, ok := biliParse(text)
	if !ok || !biliOurs(r) || r.Get("providers."+biliRecipe+".baseUrl").String() == url {
		return text
	}
	out, err := edit.PatchJSON([]byte(text), []edit.KV{{Path: "providers." + biliRecipe + ".baseUrl", Value: url}}, nil)
	if err != nil {
		return text
	}
	return string(out)
}

// carriedBili is billion-context.json as a sync carries it: nil when
// there is none, it can't be read (unknown, which the other computer
// keeps its own for), or it isn't one bili could read either.
func carriedBili() *string {
	text, there, err := biliText(BiliConfigFile())
	if err != nil || !there || len(text) > maxBiliCarried {
		return nil
	}
	if _, ok := biliParse(text); !ok {
		return nil
	}
	text = biliAt(text, biliCarriedURL)
	return &text
}

// biliChanged is when billion-context.json last changed, zero when there
// is none.
func biliChanged() time.Time {
	if fi, err := os.Stat(BiliConfigFile()); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// putBili writes a carried billion-context.json here, with this
// computer's gateway in magpie's recipe and its key in bili's store; the
// file it replaces is kept with the change's backups. It says whether it
// wrote the file. One bili couldn't read is not written.
func putBili(text string, kept *backups) (bool, error) {
	r, ok := biliParse(text)
	if !ok {
		return false, nil
	}
	biliCfgMu.Lock()
	defer biliCfgMu.Unlock()
	p := BiliConfigFile()
	text = biliAt(text, biliGatewayURL())
	cur, there, err := biliText(p)
	if err != nil {
		return false, err
	}
	wrote := false
	if !there || cur != text {
		if err := kept.keep("billion-context", p); err != nil {
			return false, err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return false, err
		}
		if err := edit.WriteAtomic(p, []byte(text)); err != nil {
			return false, err
		}
		wrote = true
	}
	if biliOurs(r) {
		if err := biliKey(); err != nil {
			return wrote, err
		}
	}
	return wrote, nil
}

// biliKey puts the key magpie's recipe is sent with into bili's private
// store, the way bili writes it: under the store's lock file, a 0600 file
// renamed in, every other entry as it was. Magpie's gateway takes any key
// from this computer, so the key only says who is asking.
func biliKey() error {
	p := biliCredFile()
	want := gateway.TokenFor(biliRecipeAgent)
	have, err := readBiliCreds(p)
	if err != nil {
		return err
	}
	if have[biliRecipe] == want {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("billion-context's key store is locked or unavailable: %w", err)
	}
	defer func() { lock.Close(); os.Remove(p + ".lock") }()
	have, err = readBiliCreds(p) // again, under the lock
	if err != nil {
		return err
	}
	have[biliRecipe] = want
	if len(have) > 16 {
		return errors.New("billion-context's key store is full (16 keys)")
	}
	data, err := json.Marshal(have)
	if err != nil {
		return err
	}
	var n [8]byte
	rand.Read(n[:])
	tmp := p + "." + hex.EncodeToString(n[:]) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// readBiliCreds is bili's private store, as bili would read it: one it
// wouldn't (open to others, not a plain file, not a map of names to keys)
// is an error, never an empty store to write over.
func readBiliCreds(p string) (map[string]string, error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	bad := errors.New("billion-context's key store " + p + " can't be read: repair its permissions or contents")
	if !fi.Mode().IsRegular() || runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 || fi.Size() > 256<<10 {
		return nil, bad
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if json.Unmarshal(data, &m) != nil {
		return nil, bad
	}
	return m, nil
}

// biliReload is how many of the bilis running here read their settings
// again, of how many there are: each one registered (biliOrigins) is
// asked at /__bili/config/reload.
func biliReload() (done, of int) {
	origins := biliOrigins()
	ctx, cancel := context.WithTimeout(context.Background(), biliAsk)
	defer cancel()
	var wg sync.WaitGroup
	var n sync.Mutex
	for _, o := range origins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if biliAskReload(ctx, o) == nil {
				n.Lock()
				done++
				n.Unlock()
			}
		}()
	}
	wg.Wait()
	return done, len(origins)
}

// biliAskReload asks one bili to read its settings again. A var for tests.
var biliAskReload = func(ctx context.Context, origin string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/__bili/config/reload", nil)
	if err != nil {
		return err
	}
	c := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", origin, res.Status)
	}
	return nil
}

// BiliSettings are the settings of billion-context.json the Library's tab
// shows: compress's nudge step and whether a fold needs a nudge, its
// external summary (the fast model summaries are written with) and its
// compression philosophy.
type BiliSettings struct {
	Path string `json:"path"`
	// Bad is why the file can't be read as bili reads it; nothing is
	// changed in it then
	Bad string `json:"bad,omitempty"`
	// Growth is compress.nudgeGrowthTokens, 0 when unset (bili's 50000)
	Growth int `json:"growth"`
	// Nudge is compress.requireNudge
	Nudge bool `json:"nudge"`
	// Summary is compress.externalSummary.enabled, Targets its targets
	// ("recipe/model")
	Summary bool     `json:"summary"`
	Targets []string `json:"targets"`
	// Recipes are the "recipe/model" a target can be from the file's own
	// recipes, magpie's left out
	Recipes []string `json:"recipes"`
	// Philosophy is compress.prompts.compressPhilosophy, used by bili
	// only with compress.acknowledgePromptsRisk (Risk)
	Philosophy string `json:"philosophy"`
	Risk       bool   `json:"risk"`
	// Markers is compress.visibilityMarkers: the 📦/❌ lines bili adds
	// after a compress, decompress or search_context call runs, on unless
	// the file turns them off
	Markers bool `json:"markers"`
	// Reloaded of Running are the bilis that read a change again, of the
	// ones running here
	Reloaded int `json:"reloaded,omitempty"`
	Running  int `json:"running,omitempty"`
}

// BiliGrowthDefault is bili's nudge step when none is set.
const BiliGrowthDefault = 50000

// ReadBiliSettings is billion-context.json's settings the tab shows.
func ReadBiliSettings() (*BiliSettings, error) {
	p := BiliConfigFile()
	text, _, err := biliText(p)
	if err != nil {
		return nil, err
	}
	return biliSettingsOf(p, text), nil
}

func biliSettingsOf(p, text string) *BiliSettings {
	s := &BiliSettings{Path: p, Targets: []string{}, Recipes: []string{}, Markers: true}
	r, ok := biliParse(text)
	if !ok {
		s.Bad = "billion-context.json isn't JSON billion-context can read"
		return s
	}
	c := r.Get("compress")
	s.Growth = int(c.Get("nudgeGrowthTokens").Int())
	s.Nudge = c.Get("requireNudge").Bool()
	es := c.Get("externalSummary")
	s.Summary = es.Get("enabled").Bool()
	for _, t := range es.Get("targets").Array() {
		if t.Type == gjson.String {
			s.Targets = append(s.Targets, t.String())
		}
	}
	s.Philosophy = c.Get("prompts.compressPhilosophy").String()
	s.Risk = c.Get("acknowledgePromptsRisk").Bool()
	// bili takes only a boolean here; anything else isn't false to it
	s.Markers = c.Get("visibilityMarkers").Type != gjson.False
	r.Get("providers").ForEach(func(k, v gjson.Result) bool {
		name := k.String()
		if name == biliRecipe && biliOurs(r) || !v.IsObject() || !v.Get("baseUrl").Exists() && !v.Get("api").Exists() {
			return true
		}
		v.Get("models").ForEach(func(m, _ gjson.Result) bool {
			s.Recipes = append(s.Recipes, name+"/"+m.String())
			return true
		})
		return true
	})
	slices.Sort(s.Recipes)
	return s
}

// BiliChange is a change to the settings BiliSettings shows; a nil field
// is left as it is.
type BiliChange struct {
	// Growth 0 takes the setting out, back to bili's default
	Growth *int  `json:"growth"`
	Nudge  *bool `json:"nudge"`
	// Summary switches the external summary; switched on, it needs a
	// target, Target or one the file has
	Summary *bool `json:"summary"`
	// Target is the summary model, first of the targets: "recipe/model"
	// from the file's recipes, or Model
	Target *string `json:"target"`
	// Model is a model magpie serves, as the target: magpie's recipe sends
	// it to this computer's gateway
	Model *string `json:"model"`
	// Philosophy "" takes it out; one set turns acknowledgePromptsRisk on,
	// which bili needs to use it
	Philosophy *string `json:"philosophy"`
	// Markers false turns compress.visibilityMarkers off; true takes the
	// setting out, back to bili's default, which shows them
	Markers *bool `json:"markers"`
}

// SetBiliSettings changes billion-context.json and has the bilis running
// here read it again.
func SetBiliSettings(c BiliChange) (*BiliSettings, error) {
	biliCfgMu.Lock()
	p := BiliConfigFile()
	text, err := setBiliSettings(p, c)
	biliCfgMu.Unlock()
	if err != nil {
		return nil, err
	}
	s := biliSettingsOf(p, text)
	s.Reloaded, s.Running = biliReload()
	return s, nil
}

func setBiliSettings(p string, c BiliChange) (string, error) {
	text, _, err := biliText(p)
	if err != nil {
		return "", err
	}
	r, ok := biliParse(text)
	if !ok {
		return "", errors.New("billion-context.json isn't JSON billion-context can read, so magpie leaves it alone: " + p)
	}
	var set []edit.KV
	var del []string
	if c.Growth != nil {
		switch g := *c.Growth; {
		case g == 0:
			del = append(del, "compress.nudgeGrowthTokens")
		case g < 1000 || g > 10_000_000:
			return "", errors.New("the nudge step is a number of tokens from 1,000 to 10,000,000")
		default:
			set = append(set, edit.KV{Path: "compress.nudgeGrowthTokens", Value: g})
		}
	}
	if c.Nudge != nil {
		if *c.Nudge {
			set = append(set, edit.KV{Path: "compress.requireNudge", Value: true})
		} else {
			del = append(del, "compress.requireNudge")
		}
	}
	key := false
	targets := []string{}
	for _, t := range r.Get("compress.externalSummary.targets").Array() {
		targets = append(targets, t.String())
	}
	target := ""
	if c.Model != nil {
		m := strings.TrimSpace(*c.Model)
		if m == "" || strings.ContainsAny(m, "\x00\n\r\t") {
			return "", errors.New("no model given")
		}
		rp := r.Get("providers." + biliRecipe)
		if rp.Exists() && !biliOurs(r) {
			return "", errors.New(`billion-context.json has a provider of yours named "magpie", so magpie can't add its own there`)
		}
		if !rp.Exists() {
			set = append(set, edit.KV{Path: "providers." + biliRecipe, Value: map[string]any{
				"baseUrl": biliGatewayURL(), "api": "openai", "credentialRef": biliRecipe,
				"models": map[string]any{m: map[string]any{}}}})
		} else {
			if rp.Get("baseUrl").String() != biliGatewayURL() {
				set = append(set, edit.KV{Path: "providers." + biliRecipe + ".baseUrl", Value: biliGatewayURL()})
			}
			if !rp.Get("models." + gjson.Escape(m)).Exists() {
				set = append(set, edit.KV{Path: "providers." + biliRecipe + ".models." + gjson.Escape(m), Value: map[string]any{}})
			}
		}
		key = true
		target = biliRecipe + "/" + m
	} else if c.Target != nil {
		target = strings.TrimSpace(*c.Target)
		if !slices.Contains(biliSettingsOf(p, text).Recipes, target) {
			return "", fmt.Errorf("billion-context.json has no model %q", target)
		}
	}
	if target != "" {
		targets = append([]string{target}, slices.DeleteFunc(targets, func(t string) bool { return t == target })...)
		if len(targets) > 16 {
			targets = targets[:16]
		}
		set = append(set, edit.KV{Path: "compress.externalSummary.targets", Value: targets})
	}
	if c.Summary != nil {
		if *c.Summary && len(targets) == 0 {
			return "", errors.New("pick the model summaries are written with first")
		}
		set = append(set, edit.KV{Path: "compress.externalSummary.enabled", Value: *c.Summary})
	}
	if c.Philosophy != nil {
		if ph := strings.TrimSpace(*c.Philosophy); ph == "" {
			del = append(del, "compress.prompts.compressPhilosophy")
		} else {
			set = append(set, edit.KV{Path: "compress.prompts.compressPhilosophy", Value: ph},
				edit.KV{Path: "compress.acknowledgePromptsRisk", Value: true})
		}
	}
	if c.Markers != nil {
		if *c.Markers {
			del = append(del, "compress.visibilityMarkers")
		} else {
			set = append(set, edit.KV{Path: "compress.visibilityMarkers", Value: false})
		}
	}
	// a key taken out of nothing changes nothing; PatchJSON would refuse it
	del = slices.DeleteFunc(del, func(k string) bool { return !r.Get(k).Exists() })
	if len(set) == 0 && len(del) == 0 {
		return text, nil
	}
	out, err := edit.PatchJSON([]byte(text), set, del)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	if key {
		if err := biliKey(); err != nil {
			return "", err
		}
	}
	if !bytes.Equal(out, []byte(text)) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := edit.WriteAtomic(p, out); err != nil {
			return "", err
		}
	}
	return string(out), nil
}
