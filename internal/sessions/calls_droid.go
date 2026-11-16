package sessions

import (
	"cmp"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/settings"
)

// Factory Droid's own calls (_meouwu on X: Droid's sessions were listed but
// Usage counted none of what they spent). Droid (0.231.0, its binary's
// session schema read, and real runs' files in testdata/droid-skills)
// writes no tokens into a transcript: a message line has its time, the
// message and, for a reply, the modelId it was asked of, and nothing of
// what the call cost. What a session spent is only in its settings
// (<id>.settings.json): tokenUsage, its own running total over every call
// (input without the cache, output with the thinking in it, and the cache
// read and written), and lastCallTokenUsage, the latest call's input and
// output alone, which says nothing of the calls before it. So a session is
// one call here, its whole total, at the time of its last message — the
// day the Sessions page counts the same total on (parseDroid) — under the
// model its settings name. A subagent's transcript has its own total (the
// caller's tokenUsage leaves it out; inclusiveTokenUsage adds it) and is
// counted under the session that called it; a fork's starts at none.
//
// A session on one of magpie's own entries ("custom:magpie/<ref>",
// internal/agent's droid.go), or on a custom model of the user's whose
// baseUrl is magpie's gateway on this computer (as testdata/droid-skills'
// custom:mh is), sent its calls through the gateway, which counted each
// one: such a session is left out whole, when its settings or any of its
// replies name one, as its total can't be split by model. A session is
// judged when it is read, which is again once it or its settings change.

// droidGateway is how the id of a model magpie wrote into droid's
// settings begins.
const droidGateway = "custom:magpie/"

// droidCallSettings are a session's settings as its calls are read.
type droidCallSettings struct {
	Model string      `json:"model"`
	Usage *droidUsage `json:"tokenUsage"`
}

// readDroidCalls reads a session's transcript and its settings beside it
// whole: the settings are rewritten as the total moves on, so there is no
// reading on from where the last read left off.
func readDroidCalls(f file) (*callFile, bool) {
	st := &callFile{Path: f.path, Agent: f.agent}
	b, err := os.ReadFile(strings.TrimSuffix(f.path, ".jsonl") + ".settings.json")
	if os.IsNotExist(err) {
		return st, true // an older session with no settings: nothing said
	}
	if err != nil {
		return st, false
	}
	var set droidCallSettings
	if json.Unmarshal(b, &set) != nil {
		return st, false // half-written, as droid rewrites it: read it next time
	}
	var c droidCarry
	var start droidStart
	var last time.Time
	customs := droidCustomBases()
	gateway := droidViaGateway(set.Model, customs)
	_, err = scanAt(f.path, 0, nil, func(b []byte, _, _ int64) bool {
		var l struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Message struct {
				Model string `json:"modelId"`
			} `json:"message"`
		}
		if json.Unmarshal(b, &l) != nil {
			return true
		}
		if !c.Started {
			c.Started = true
			if l.Type == "session_start" && json.Unmarshal(b, &start) == nil {
				c.Upto, c.Copied = start.Forked, start.Forked != ""
			}
			return true
		}
		if l.Type != "message" {
			return true
		}
		if c.Copied {
			if l.ID == c.Upto {
				c.Copied = false
			}
			return true
		}
		if l.Message.Model != "" && droidViaGateway(l.Message.Model, customs) {
			gateway = true
		}
		if at := tsAt(b, false); at.After(last) {
			last = at
		}
		return true
	})
	if err != nil {
		return st, false
	}
	u := set.Usage
	if u == nil || gateway || u.Input+u.Output+u.CacheRead+u.CacheWrite == 0 {
		return st, true
	}
	id := strings.TrimSuffix(filepath.Base(f.path), ".jsonl")
	session := id
	if start.Calling != "" && start.Calling != id && safeID.MatchString(start.Calling) {
		session = start.Calling
	}
	if last.IsZero() {
		last = f.mod.Time()
	}
	st.Calls = []Call{{
		Time: last, Agent: "droid", Session: session, Model: droidModelName(set.Model), Msg: "droid:" + id,
		Tokens: Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite},
		Cwd:    cmp.Or(start.LastCwd, start.Cwd),
	}}
	return st, true
}

// droidCustomBases are the baseUrl of each custom model in droid's
// settings, by its id ("custom:<id>").
func droidCustomBases() map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(filepath.Join(FactoryDir(), "settings.json"))
	if err != nil {
		return out
	}
	gjson.GetBytes(jsonc.ToJSONInPlace(raw), "customModels").ForEach(func(_, v gjson.Result) bool {
		if id := v.Get("id").String(); id != "" {
			out[id] = v.Get("baseUrl").String()
		}
		return true
	})
	return out
}

// droidViaGateway is whether droid sent a model's calls through magpie's
// gateway: one of magpie's own entries, or a custom model whose baseUrl
// is the gateway's port on this computer.
func droidViaGateway(model string, customs map[string]string) bool {
	if strings.HasPrefix(model, droidGateway) {
		return true
	}
	base, ok := customs[model]
	if !ok || !loopbackURL(base) {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return false
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	for _, a := range []string{settings.GatewayAddr(), settings.SavedAddr()} {
		if _, p, err := net.SplitHostPort(a); err == nil && p == port {
			return true
		}
	}
	return false
}
