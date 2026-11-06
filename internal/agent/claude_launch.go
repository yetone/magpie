package agent

import (
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// claude-magpie (baranwang on X): Claude Code started through magpie while
// a plain `claude` stays on the user's own account, with nothing switched
// back and forth. magpie keeps a settings file of its own,
// <config>/claude-launch/settings.json, wired as it would wire Claude
// Code's (its gateway, key, models and tiers), and `magpie claude run`
// starts claude with --settings and that file. Claude Code puts a
// --settings file over the user's settings.json, its env key by key over
// the user's env and the shell's (tried with Claude Code 2.1.318: a
// claude.ai sign-in, the user's own base URL and token, a shell
// ANTHROPIC_BASE_URL all lost to it), so ~/.claude/settings.json is never
// written. What could still take a request past magpie, an
// ANTHROPIC_API_KEY or a cloud switch from the user's file or the shell,
// is blanked in the file (claudeLaunchBlanks).

// ClaudeLaunchID is the launch file's agent id: claude's lists (ListsFor
// cuts at #), its stash keys under its own name.
const ClaudeLaunchID = "claude#launch"

// claudeLaunchDir is the folder of the launch file; a var so tests can
// put it in their own folder.
var claudeLaunchDir = func() string { return filepath.Join(appdir.Config(), "claude-launch") }

// ClaudeLaunchPath is the settings file `magpie claude run` hands claude.
func ClaudeLaunchPath() string { return filepath.Join(claudeLaunchDir(), "settings.json") }

// claudeLaunch is the launch file as an agent: Claude Code at this home,
// its settings.json the launch file. It is listed nowhere, so the Agents
// page, Check and Notice see only the user's own settings.json.
func claudeLaunch(home string) *Agent {
	a := claudeIn(place{home: home, id: ClaudeLaunchID, dir: claudeLaunchDir()})
	a.ID = ClaudeLaunchID
	a.Name = "claude-magpie"
	a.Aliases = nil
	return a
}

// claudeLaunchBlanks are the variables the launch file sets to "", each
// over the user's settings.json and the shell: an API key would go to
// magpie beside its own key (x-api-key), and a cloud switch takes Claude
// Code to Bedrock, Vertex and the like, past magpie.
func claudeLaunchBlanks() []string {
	return append([]string{"ANTHROPIC_API_KEY"}, agentenv.ClaudeProviders...)
}

// ClaudeLaunch writes the launch file for a start: connected the first
// time as Connect connects Claude Code (the model it would pick, the
// tiers following it), brought up to the catalog after that (Sync), the
// gateway's address and key as they are now, and the blanks. It answers
// the file's path.
func ClaudeLaunch(home string) (string, error) {
	a := claudeLaunch(home)
	path := ClaudeLaunchPath()
	switch {
	case !a.Wired():
		if _, err := a.ConnectHow(); err != nil {
			return "", err
		}
	case claudeLaunchStale(a, path):
		// the gateway moved (another port, MAGPIE_ADDR) or wants another
		// key (LAN sharing) since: set again at the address and key of
		// now, as Rewire does. The Agents page reads a Claude Code on an
		// older address as wired still; the launch file is set at once
		if _, errs := setAgain([]*Agent{a}); len(errs) > 0 {
			return "", errs[0]
		}
	default:
		if err := a.Sync(); err != nil {
			return "", err
		}
	}
	var kvs []edit.KV
	for _, k := range claudeLaunchBlanks() {
		if v, has := edit.GetJSON(path, "env."+k); !has || v != "" {
			kvs = append(kvs, edit.KV{Path: "env." + k, Value: ""})
		}
	}
	if len(kvs) > 0 {
		if err := edit.SetJSON(path, kvs...); err != nil {
			return "", err
		}
	}
	return path, nil
}

// claudeLaunchStale says the launch file names another address or key
// than the gateway's of now, or the Agents page's check finds it unwired.
func claudeLaunchStale(a *Agent, path string) bool {
	at := here("")
	u, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL")
	k, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN")
	if u != at.gw() || k != at.gwKey() {
		return true
	}
	d := a.Drift()
	return d != nil && d.Kind == "unwired"
}

// ClaudeModelMark is what the user's settings.json says of its model
// before a claude-magpie session, for ClaudeLaunchAfter.
type ClaudeModelMark struct {
	model string
	has   bool
}

// ClaudeLaunchBefore marks the user's model before a session.
func ClaudeLaunchBefore(home string) ClaudeModelMark {
	m, has := edit.GetJSON(claudeUserSettings(home), "model")
	return ClaudeModelMark{m, has}
}

func claudeUserSettings(home string) string {
	return filepath.Join(here(home).claudeDir(), "settings.json")
}

// ClaudeLaunchAfter is what a claude-magpie session's /model leaves:
// Claude Code saves a pick there to the user's settings.json, never to a
// --settings file (tried with 2.1.318). A model of magpie's picked goes
// to the launch file, for claude-magpie's next session, and the user's
// file gets its model back while magpie isn't wired into it: a plain
// `claude` would ask Anthropic for a model only magpie serves. A Claude
// model of its own (sonnet) is a pick plain claude can run, and stays.
func ClaudeLaunchAfter(home string, was ClaudeModelMark) error {
	user := claudeUserSettings(home)
	now, has := edit.GetJSON(user, "model")
	if now == was.model && has == was.has {
		return nil
	}
	if claudeRouted(here(home), user) {
		read := provider.ClaudeRead(now)
		if m, _ := tierAt(read); isMagpie(strings.TrimSuffix(m, "[1m]")) {
			return claudeLaunch(home).Apply("model", read)
		}
		return nil
	}
	if !claudeLaunchRewrote(was, now) {
		read := provider.ClaudeRead(now)
		m, _ := tierAt(read)
		if !isMagpie(strings.TrimSuffix(m, "[1m]")) {
			return nil
		}
		if err := claudeLaunch(home).Apply("model", read); err != nil {
			return err
		}
	}
	if was.has {
		return edit.SetJSON(user, edit.KV{Path: "model", Value: was.model})
	}
	return edit.DelJSON(user, "model")
}

// claudeLaunchRewrote says now is Claude Code's own rewrite of the user's
// model at a claude-magpie start, not a pick: with the launch file's opus
// tier on 1M, Claude Code 2.1.318 turns a user's "opus" into "opus[1m]"
// in their settings.json once (migration_opus_to_opus1m, which asks
// whether the opus it would run is opted in to 1M). Plain claude, on the
// user's own account, would then ask for 1M context it may not have.
func claudeLaunchRewrote(was ClaudeModelMark, now string) bool {
	return was.has && !strings.HasSuffix(was.model, "[1m]") && now == was.model+"[1m]"
}

// claudeLaunchStandIn is StandIn for a claude-magpie session: the launch
// file's main model and tiers, while it is on this gateway.
func claudeLaunchStandIn(model string) string {
	return claudeStandIn(ClaudeLaunchPath(), model)
}
