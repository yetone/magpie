package agent

import (
	"encoding/json"
	"log"
	"os"
	"path"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
)

// An IDE opens a project that lives in a WSL distro through that distro's
// environment (its Execution Eel API): Air's agent, `opencode acp`, is
// started inside the distro. The Magpie entry in the Windows
// ~/.jetbrains/acp.json is a Windows command (C:\Users\me\.opencode\bin\
// opencode.exe) with a Windows path in OPENCODE_CONFIG, and the IDE refuses
// it there: an absolute command whose environment isn't the project's is
// "ACP executable belongs to a different environment" (AcpAgentLauncher in
// the air plugin of IDEA 2026.3, build 263.7307.200), which the agent list
// shows as "Magpie does not run" (@yanghonghang on X). env values are
// passed as they are, so no one entry serves both.
//
// So each running distro with OpenCode gets an entry of its own, "Magpie
// (WSL Ubuntu)": the command is OpenCode's path in the distro as Windows
// opens it (\\wsl.localhost\Ubuntu\home\me\.opencode\bin\opencode), whose
// environment is the distro's, which the IDE turns back into the Linux path,
// and OPENCODE_CONFIG is a Linux path to a magpie-opencode.json in the
// distro's ~/.jetbrains, with magpie's provider at the address the distro
// reaches the gateway at.

// airWSLName is a distro name an entry can be named for (and a key path
// written, once its dots are escaped).
var airWSLName = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// airWSLPrefix begins the name of each entry magpie adds for a distro.
const airWSLPrefix = airAgent + " (WSL "

// airWSLDistros are the distros Air's entries are made for: the running
// ones with OpenCode on their own PATH. A stopped distro is never opened:
// that would start it. A var for tests.
var airWSLDistros = func() []distro {
	if !wslLooks() {
		return nil
	}
	var out []distro
	for _, d := range wslDistros() {
		if d.Running && d.Has["bin:opencode"] && airWSLName.MatchString(d.Name) {
			out = append(out, d)
		}
	}
	return out
}

// airWSL is a distro's Magpie entry in acp.json.
type airWSL struct {
	d     distro
	entry string // agent_servers.<this>, the dots of the name escaped
	name  string // the name the IDE lists
	cfg   string // magpie-opencode.json as magpie opens it
	linux string // and as the distro does
	bin   string // OpenCode's command: through \\wsl.localhost, "opencode" when not known
}

func airWSLOf(d distro) airWSL {
	name := airWSLPrefix + d.Name + ")"
	linux := path.Join(d.Home, ".jetbrains", "magpie-opencode.json")
	w := airWSL{d: d, name: name, entry: "agent_servers." + strings.ReplaceAll(name, ".", `\.`),
		cfg: d.local(linux), linux: linux, bin: "opencode"}
	if p := d.Paths["opencode"]; strings.HasPrefix(p, "/") {
		w.bin = d.local(p)
	}
	return w
}

func airWSLs() []airWSL {
	var out []airWSL
	for _, d := range airWSLDistros() {
		out = append(out, airWSLOf(d))
	}
	return out
}

// ours: the entry is there, on this config.
func (w airWSL) ours(acp string) bool {
	v, _ := edit.GetJSON(acp, w.entry+".env.OPENCODE_CONFIG")
	return v == w.linux
}

// provider is magpie's provider as the distro reaches the gateway.
func (w airWSL) provider(id string) func() any {
	return openCodeKept(w.cfg, func() any { return magpieProviderJSONAt("opencode", id, w.d.base()) })
}

// put writes the distro's OpenCode config for model v and its entry.
func (w airWSL) put(acp, id, v string) error {
	if err := edit.SetJSON(w.cfg,
		edit.KV{Path: "provider." + magpieID, Value: w.provider(id)()},
		edit.KV{Path: "enabled_providers", Value: []string{magpieID}},
		edit.KV{Path: "small_model", Value: v},
		edit.KV{Path: "model", Value: v},
		edit.KV{Path: "$schema", Value: "https://opencode.ai/config.json"}); err != nil {
		return err
	}
	// it holds the gateway's key: owner-only there, as an agent's own is
	w.d.chmod(w.cfg)
	return edit.SetJSON(acp, edit.KV{Path: w.entry, Value: map[string]any{
		"command": w.bin, "args": []string{"acp"},
		"env": map[string]string{"OPENCODE_CONFIG": w.linux},
	}})
}

// airWSLPut gives every distro with OpenCode its entry for model v. A
// distro that can't be written is logged, not failed: the Windows agent
// is set all the same.
func airWSLPut(acp, id, v string) {
	for _, w := range airWSLs() {
		if err := w.put(acp, id, v); err != nil {
			log.Printf("Air's Magpie agent for WSL %s: %v", w.d.Name, err)
		}
	}
}

// airWSLSync keeps each distro's provider at the address and key the
// distro reaches the gateway with now (it changes with a restart under
// NAT), and gives a distro found since its entry, once: an entry the
// user took out, whose config is still there, stays out.
func airWSLSync(acp, id string, model func() string) {
	for _, w := range airWSLs() {
		switch {
		case w.ours(acp):
			if err := syncJSONInOrder(w.cfg, "provider."+magpieID, w.provider(id)); err != nil {
				log.Printf("Air's Magpie agent for WSL %s: %v", w.d.Name, err)
			}
		default:
			if _, err := os.Stat(w.cfg); err == nil {
				continue
			}
			if v := model(); v != "" {
				if err := w.put(acp, id, v); err != nil {
					log.Printf("Air's Magpie agent for WSL %s: %v", w.d.Name, err)
				}
			}
		}
	}
}

// airWSLDrop takes magpie's entries for distros out of acp.json, and the
// config of each distro that runs (a stopped one isn't opened).
func airWSLDrop(acp string) error {
	raw, _ := edit.GetJSONRaw(acp, "agent_servers")
	var have map[string]any
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &have)
	}
	for name := range have {
		if strings.HasPrefix(name, airWSLPrefix) {
			if err := edit.DelJSON(acp, "agent_servers."+strings.ReplaceAll(name, ".", `\.`)); err != nil {
				return err
			}
		}
	}
	for _, w := range airWSLs() {
		if err := edit.Remove(w.cfg); err != nil && !os.IsNotExist(err) {
			log.Printf("Air's Magpie agent for WSL %s: %v", w.d.Name, err)
		}
	}
	return nil
}

// airWSLCheck says what is off in a distro's Air entry, "" when none is.
func airWSLCheck(acp string) string {
	for _, w := range airWSLs() {
		if !w.ours(acp) {
			continue
		}
		if s := wiringOff("Air in WSL "+w.d.Name, w.cfg, func(k string) (string, bool) { return edit.GetJSON(w.cfg, "provider."+magpieID+".options."+k) },
			"baseURL", w.d.base()+"/v1", "apiKey", keyAt(w.d.base())); s != "" {
			return s
		}
	}
	return ""
}
