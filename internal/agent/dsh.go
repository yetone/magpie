package agent

// DeepSeek Harness (dsh) boots from its shipped rows and then applies a
// personal patch list: a YAML list of {id, config} entries, each replacing
// the whole config of the row with that id. Since 0.1.5 every profile has
// its own, ~/.dsh/profiles/<name>/cordis.patch.yml (web and desktop read it
// live); before that there was one, ~/.dsh/config.yaml.
//
// Since 0.1.5 magpie is one of dsh's custom model providers: a route named
// magpie in the llm-pi-ai entry's providers (what dsh's Models page writes
// for "Add model provider"), with the gateway as its endpoint, the catalog
// as its models and, as the key, a credential named in apiKeyEnv, which
// magpie puts in ~/.dsh/.env; agent-default-model names the model new
// sessions start on. dsh's own DeepSeek row (llm-deepseek) is left to dsh:
// taken over, it read as DeepSeek in dsh's Models page, and a DeepSeek key
// entered there went to the gateway under magpie's credential name, over
// magpie's own (Discord: 集成进 dsh 会被 dsh 覆写). A route of the user's in
// the same entry stays as it is, and so does one dsh adds beside magpie's.
//
// Before 0.1.5, which has no custom providers, magpie took over
// llm-deepseek, with the key in the entry, and set agent-loop's main agent
// (the TUI) and api-gateway's route (`dsh -p`, `dsh web`). Entries magpie
// writes whole are marked as magpie's, the user's own entries stay as they
// are, and one magpie replaces is stashed and put back when it steps out.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

const dshMark = "# magpie"

// dshKeyRef is the credential dsh signs gateway requests with. It is not
// dshOldKeyRef, the one magpie's llm-deepseek entry named: dsh's Models page
// showed that entry as DeepSeek, so a DeepSeek key entered there may sit in
// dsh's own store under that name, which dsh reads over .env.
const (
	dshKeyRef    = "MAGPIE_GATEWAY_KEY"
	dshOldKeyRef = "MAGPIE_API_KEY"
)

// dshRoute is magpie's route among dsh's custom providers, and the
// provider agent-default-model names for a model through magpie.
const dshRoute = magpieID

// dshPiRow is the row custom providers are configured on, and its plugin.
const (
	dshPiRow    = "llm-pi-ai"
	dshPiPlugin = "@deepseek-ai/dsh-llm-pi-ai"
)

// dshModels are the models dsh reaches on its own, as it ships them — under
// its own name, not DeepSeek: magpie's DeepSeek provider is a group of that
// name too, and two would read as one listed twice (#115).
var dshModels = []Option{
	{Value: "deepseek-flash", Label: "DeepSeek-V41-Flash", Icon: "deepseek-color", Group: "DeepSeek Harness"},
	{Value: "deepseek-v4-pro", Label: "DeepSeek-V4-Pro", Icon: "deepseek-color", Group: "DeepSeek Harness"},
	{Value: "deepseek-v4-flash", Label: "DeepSeek-V4-Flash", Icon: "deepseek-color", Group: "DeepSeek Harness"},
}

func dsh(home string) *Agent {
	dir := os.Getenv("DSH_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".dsh")
	}
	path := filepath.Join(dir, "config.yaml")
	if files := dshProfiles(dir); len(files) > 0 {
		path = files[0]
	}
	return &Agent{
		ID: "dsh", Name: "DeepSeek Harness", Icon: "deepseek-color", Aliases: []string{"deepseek-harness"},
		UA:  []string{"deepseek-harness"},
		Bin: "dsh", Dir: dir, Path: path,
		Sync: func() error { return dshSync(dir) },
		Notice: func() string {
			var notes []string
			if Running(`(^|/)dsh( |$)`) {
				if len(dshProfiles(dir)) > 0 {
					notes = append(notes, "New dsh sessions start on this; one already open keeps its model until you pick another in it. A dsh started with -p or in a terminal reads it at start-up.")
				} else {
					notes = append(notes, "dsh reads its config at start-up — restart open dsh sessions to use this.")
				}
			}
			// only before 0.1.5 does magpie go through llm-deepseek
			if len(dshProfiles(dir)) == 0 && dshSettingsEndpoint(filepath.Join(dir, "settings.yaml")) {
				notes = append(notes, "~/.dsh/settings.yaml sets its own DeepSeek endpoint or key, which dsh puts over magpie's; clear it in dsh's Models page to go through magpie.")
			}
			return strings.Join(notes, " ")
		},
		Check: func() string { return dshCheck(dir) },
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string { return dshGet(dir) },
			Set: func(v string) error { return dshSet(dir, v) },
			Options: func(map[string]string) []Option {
				return append(append([]Option{}, dshModels...), viaMagpie("dsh", magpieID+"/")...)
			},
		}, {
			// the thinking effort sessions start with: agent-default-model's
			// reasoningEffort since 0.1.5 (a model through magpie offers its
			// own levels), llm-deepseek's before (off, low, high or max;
			// dsh's own default is high)
			Key: "effort", Label: "thinking",
			Get: func() string { return dshGetEffort(dir) },
			Set: func(v string) error { return dshSetEffort(dir, v) },
			Options: func(cur map[string]string) []Option {
				if ref, ok := strings.CutPrefix(cur["model"], magpieID+"/"); ok && len(dshProfiles(dir)) > 0 {
					if levels := dshLevels(ref); len(levels) > 0 {
						return static(levels...)
					}
				}
				return static(dshEfforts...)
			},
		}},
	}
}

// dshProfiles are the profiles' patch lists (dsh 0.1.5 on), web's first.
func dshProfiles(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "profiles", "*", "cordis.patch.yml"))
	var web, rest []string
	for _, f := range files {
		if filepath.Base(filepath.Dir(f)) == "web" {
			web = append(web, f)
		} else {
			rest = append(rest, f)
		}
	}
	return append(web, rest...)
}

// dshItem is one entry of the patch list, as its lines.
type dshItem struct {
	id     string
	magpie bool
	lines  []string
}

var dshIDLine = regexp.MustCompile(`^(?:- |  )id:\s*['"]?([^'"#\s]+)['"]?\s*(#.*)?$`)

// dshParse splits the patch list at path into what comes before its first
// entry and the entries. A list written in flow style ([ {id: a} ], which
// dsh's own writers keep a profile's [] in) is read as the same list in block
// style, and written back so; a file that is no list is left alone.
func dshParse(path, raw string) (head []string, items []dshItem, err error) {
	if b, ok := edit.BlockList(raw); ok {
		raw = b
	}
	for _, l := range splitLinesKeep(raw) {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "- ") || l == "-":
			items = append(items, dshItem{lines: []string{l}})
		case len(items) == 0:
			if t != "" && !strings.HasPrefix(t, "#") && t != "[]" {
				return nil, nil, fmt.Errorf("%s is not a list of entries magpie can edit", path)
			}
			if t != "[]" {
				head = append(head, l)
			}
			continue
		case t != "" && !strings.HasPrefix(t, "#") && !strings.HasPrefix(l, " "):
			return nil, nil, fmt.Errorf("%s is not a list of entries magpie can edit", path)
		default:
			items[len(items)-1].lines = append(items[len(items)-1].lines, l)
		}
		it := &items[len(items)-1]
		if m := dshIDLine.FindStringSubmatch(l); m != nil && it.id == "" {
			it.id = m[1]
			it.magpie = strings.TrimSpace(m[2]) == dshMark
		}
	}
	return head, items, nil
}

func splitLinesKeep(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func dshRead(path string) ([]string, []dshItem, error) {
	b, err := edit.Read(path)
	if err != nil {
		return nil, nil, err
	}
	return dshParse(path, string(b))
}

// dshWrite puts a patch list back together.
func dshWrite(path string, head []string, items []dshItem) error {
	out := append([]string{}, head...)
	for _, it := range items {
		out = append(out, it.lines...)
	}
	if len(items) == 0 {
		if _, err := os.Stat(path); err != nil {
			return nil // nothing was there, nothing to write
		}
		out = append(out, "[]") // dsh wants a list, even an empty one
	}
	return edit.WriteAtomic(path, []byte(strings.Join(out, "\n")+"\n"))
}

func dshFind(items []dshItem, id string) int {
	for i, it := range items {
		if it.id == id {
			return i
		}
	}
	return -1
}

// dshFindLast is the entry of that id dsh goes by: the last, as its own
// config editor edits.
func dshFindLast(items []dshItem, id string) int {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].id == id {
			return i
		}
	}
	return -1
}

// dshNode reads one entry as YAML: its mapping ({id, name, config}).
func dshNode(it dshItem) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(it.lines, "\n")), &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.SequenceNode || len(doc.Content[0].Content) != 1 || doc.Content[0].Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the %s entry is not one magpie can edit", it.id)
	}
	return doc.Content[0].Content[0], nil
}

// dshNodeLines writes an entry read with dshNode back as lines.
func dshNodeLines(n *yaml.Node) ([]string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{n}}); err != nil {
		return nil, err
	}
	enc.Close()
	return splitLinesKeep(buf.String()), nil
}

func yamlKey(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func yamlSetKey(n *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = v
			return
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

func yamlDelKey(n *yaml.Node, key string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}

// yamlMapping is the mapping at key, made when there is none (or the value
// there is not one).
func yamlMapping(n *yaml.Node, key string) *yaml.Node {
	if v := yamlKey(n, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode}
	yamlSetKey(n, key, v)
	return v
}

// dshConfig reads the scalar fields of an entry's config: dsh writes them
// as it likes, quoted or not.
func dshConfig(it dshItem) map[string]string {
	n, err := dshNode(it)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	if c := yamlKey(n, "config"); c != nil && c.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(c.Content); i += 2 {
			if v := c.Content[i+1]; v.Kind == yaml.ScalarNode {
				out[c.Content[i].Value] = v.Value
			}
		}
	}
	return out
}

// dshRouteIn is magpie's route among the custom providers of a patch list,
// nil when there is none.
func dshRouteIn(items []dshItem) *yaml.Node {
	i := dshFindLast(items, dshPiRow)
	if i < 0 {
		return nil
	}
	n, err := dshNode(items[i])
	if err != nil {
		return nil
	}
	return yamlKey(yamlKey(yamlKey(n, "config"), "providers"), dshRoute)
}

// dshOldWiring reports whether a profile still has what magpie wrote before
// it was a custom provider: dsh's own DeepSeek row taken over.
func dshOldWiring(items []dshItem) bool {
	i := dshFind(items, "llm-deepseek")
	return i >= 0 && items[i].magpie
}

// dshOurs reports whether an entry is magpie's: marked so, or — should dsh
// rewrite it without the mark — a model on magpie's route, which is gone
// with it.
func dshOurs(it dshItem) bool {
	return it.magpie || it.id == "agent-default-model" && dshConfig(it)["provider"] == dshRoute
}

// dshWired reports whether a profile's patch list has magpie's route.
func dshWired(items []dshItem) bool { return dshRouteIn(items) != nil }

// dshServes reports whether magpie gives dsh this model now: the route
// written again would list it, where one naming a model magpie no longer
// gives dsh would not.
func dshServes(id string) bool {
	for _, m := range magpieModels("dsh") {
		if m.ID == id {
			return true
		}
	}
	return false
}

// dshRouteLists reports whether magpie's route carries a model with this id.
func dshRouteLists(route *yaml.Node, id string) bool {
	models := yamlKey(route, "models")
	if models == nil {
		return false
	}
	for _, m := range models.Content {
		if v := yamlKey(m, "id"); v != nil && v.Value == id {
			return true
		}
	}
	return false
}

var dshModelLine = regexp.MustCompile(`^\s+model:\s*(.+?)\s*$`)

// dshGet reads the model new sessions start on: the one last picked in dsh,
// saved in its settings (0.1.x), else the patch list's.
func dshGet(dir string) string {
	files := dshProfiles(dir)
	if len(files) == 0 {
		return dshGetLegacy(filepath.Join(dir, "config.yaml"))
	}
	_, items, err := dshRead(files[0])
	if err != nil {
		return ""
	}
	return dshStart(dir, items)
}

// dshStart reads the model a profile's sessions start on: the one last picked
// in dsh, saved in its settings, which go over every profile, else the
// profile's own agent-default-model entry.
func dshStart(dir string, items []dshItem) string {
	sel := edit.GetYAMLMap(filepath.Join(dir, "settings.yaml"), "agent-default-model")
	if sel["model"] == "" {
		if i := dshFindLast(items, "agent-default-model"); i >= 0 {
			sel = dshConfig(items[i])
		}
	}
	switch {
	case sel["model"] == "":
		return ""
	case sel["provider"] == dshRoute,
		// set before magpie was a provider of its own, not yet moved
		sel["provider"] == "deepseek-official" && dshOldWiring(items):
		return magpieID + "/" + sel["model"]
	}
	return sel["model"]
}

// dshGetLegacy reads agent-loop's model from a config.yaml before 0.1.5.
func dshGetLegacy(path string) string {
	_, items, err := dshRead(path)
	if err != nil {
		return ""
	}
	model := ""
	if i := dshFind(items, "agent-loop"); i >= 0 {
		for _, l := range items[i].lines {
			if m := dshModelLine.FindStringSubmatch(l); m != nil {
				model = yamlScalar(m[1])
				break
			}
		}
	}
	if model != "" && dshOldWiring(items) {
		return magpieID + "/" + model
	}
	return model
}

// dshCheck says what keeps a dsh on one of magpie's models from reaching
// the gateway: magpie's route (its llm-deepseek entry before 0.1.5) pointed
// elsewhere, the model it starts on one the route doesn't list, or — since
// 0.1.5 — the key it names gone from .env, or another key under that name in
// dsh's own store, which it reads first.
func dshCheck(dir string) string {
	if !usesMagpie(dshGet(dir)) {
		return ""
	}
	files := dshProfiles(dir)
	if len(files) == 0 {
		path := filepath.Join(dir, "config.yaml")
		_, items, _ := dshRead(path)
		base := ""
		if i := dshFind(items, "llm-deepseek"); i >= 0 {
			for _, l := range items[i].lines {
				if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && k == "baseURL" {
					base = yamlScalar(strings.TrimSpace(v))
				}
			}
		}
		get := func(string) (string, bool) { return base, base != "" }
		return wiringOff("DeepSeek Harness", path, get, "baseURL", gatewayV1())
	}
	// a profile of dsh's (its desktop app's) without magpie's route:
	// sessions there list dsh's own models alone
	for _, f := range files[1:] {
		if _, items, err := dshRead(f); err == nil && !dshWired(items) {
			return "DeepSeek Harness's " + filepath.Base(filepath.Dir(f)) + " profile (" + f + ") has none of magpie's models, so sessions there list DeepSeek's own alone"
		}
	}
	_, items, _ := dshRead(files[0])
	route := dshRouteIn(items)
	get := func(k string) (string, bool) {
		if v := yamlKey(route, k); v != nil && v.Kind == yaml.ScalarNode {
			return v.Value, true
		}
		return "", false
	}
	if route == nil && dshOldWiring(items) {
		// not moved yet; the next sync does it
		get = func(k string) (string, bool) {
			v, ok := dshConfig(items[dshFind(items, "llm-deepseek")])[k]
			return v, ok
		}
	}
	if off := wiringOff("DeepSeek Harness", files[0], get, "baseURL", gatewayV1()); off != "" {
		return off
	}
	// dsh counts a model its provider doesn't list as none at all and refuses
	// the turn, so a model of magpie's a profile's route has not is as
	// unusable there as a route pointed elsewhere: a catalog that moved on, a
	// profile written elsewhere, or one dsh's Models page wrote leave it that
	// way. Every profile carries a route of its own, and dsh runs whichever
	// one its session names, so every profile is asked.
	for _, f := range files {
		_, it, err := dshRead(f)
		if err != nil {
			continue
		}
		r := dshRouteIn(it)
		if r == nil {
			continue // no route yet: the sync this asks for writes one
		}
		if ref, ok := strings.CutPrefix(dshStart(dir, it), magpieID+"/"); ok && !dshRouteLists(r, ref) {
			// a route out of date is written again — Apply again, or picking
			// a model of magpie's in dsh, does it; one naming a model magpie
			// no longer gives dsh is not, so that one asks for another model
			// rather than for a click that cannot help
			if dshServes(ref) {
				return "DeepSeek Harness starts on " + magpieID + "/" + ref + ", which magpie's route in " + f + " doesn't list: a session there fails with no such configured model until that route is written again (Apply again, or pick one of magpie's models in dsh)"
			}
			return "DeepSeek Harness starts on " + magpieID + "/" + ref + ", which magpie no longer gives dsh: a session there fails with no such configured model until another of magpie's is picked in dsh"
		}
	}
	env := filepath.Join(dir, ".env")
	if off := wiringOff("DeepSeek Harness", env, func(k string) (string, bool) { return edit.GetEnvFile(env, k) },
		dshKeyRef, gateway.Token); off != "" {
		return off
	}
	creds := filepath.Join(dir, ".credentials.yaml")
	if v, ok := edit.GetYAML(creds, "refs."+dshKeyRef); ok && v != gateway.Token {
		return "DeepSeek Harness's own key store (" + creds + ") holds another " + dshKeyRef + ", which it uses over magpie's; remove it there (dsh's Models page, Magpie's key) to go through magpie"
	}
	return ""
}

// yamlScalar reads a plain or quoted YAML scalar.
func yamlScalar(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	if strings.HasPrefix(v, `"`) {
		var s string
		if json.Unmarshal([]byte(v), &s) == nil {
			return s
		}
	}
	return strings.Trim(v, `'"`)
}

func dshStashKey(path, id string) string { return "dsh:" + path + ":" + id }

// dshSet writes v as the model new sessions start on: a catalog model
// through the gateway, one of dsh's own models directly, or "" for dsh's own
// default. Every profile gets it; config.yaml only where there are none.
func dshSet(dir, v string) error {
	ref, viaGateway := strings.CutPrefix(v, magpieID+"/")
	if viaGateway && !isMagpie(ref) {
		return fmt.Errorf("unknown model %q", v)
	}
	legacy := filepath.Join(dir, "config.yaml")
	files := dshProfiles(dir)
	if len(files) == 0 {
		return dshSetFile(legacy, v, false)
	}
	for _, f := range files {
		if err := dshSetFile(f, v, true); err != nil {
			return err
		}
	}
	// what an older dsh was given is no use now
	if _, err := os.Stat(legacy); err == nil {
		if err := dshSetFile(legacy, "", false); err != nil {
			return err
		}
	}
	if err := dshEnv(dir, viaGateway); err != nil {
		return err
	}
	// a model picked in dsh 0.1.x is saved in its settings and goes over
	// the patch list; picking one here takes over from it
	settings := filepath.Join(dir, "settings.yaml")
	if v != "" && edit.GetYAMLMap(settings, "agent-default-model") != nil {
		return edit.DelYAML(settings, "agent-default-model")
	}
	return nil
}

// dshEnv puts the key for the gateway in $DSH_HOME/.env, or with on false
// takes it out; the name magpie's llm-deepseek entry used goes either way.
func dshEnv(dir string, on bool) error {
	env := filepath.Join(dir, ".env")
	if on {
		if v, ok := edit.GetEnvFile(env, dshKeyRef); !ok || v != gateway.Token {
			if err := edit.SetEnvFile(env, edit.KV{Path: dshKeyRef, Value: gateway.Token}); err != nil {
				return err
			}
		}
	}
	for _, k := range []string{dshKeyRef, dshOldKeyRef} {
		// the old name only as magpie left it
		if v, ok := edit.GetEnvFile(env, k); ok && !(k == dshKeyRef && on) && (k == dshKeyRef || v == gateway.Token) {
			if err := edit.DelEnvFile(env, k); err != nil {
				return err
			}
		}
	}
	return nil
}

// dshSetFile writes v into one patch list; modern is the layout of dsh
// 0.1.5 on.
func dshSetFile(path, v string, modern bool) error {
	head, items, err := dshRead(path)
	if err != nil {
		return err
	}
	ref, viaGateway := strings.CutPrefix(v, magpieID+"/")
	effort := ""
	if modern {
		effort = dshEffortOf(items)
	} else {
		effort = dshEffortIn(items)
	}
	put := func(id string, lines []string) {
		i := dshFind(items, id)
		if i >= 0 && !dshOurs(items[i]) {
			stash(map[string]string{dshStashKey(path, id): strings.Join(items[i].lines, "\n")})
		}
		it := dshItem{id: id, magpie: true, lines: lines}
		if i >= 0 {
			items[i] = it
		} else {
			items = append(items, it)
		}
	}
	drop := func(id string) {
		i := dshFind(items, id)
		if i < 0 || !dshOurs(items[i]) {
			return
		}
		if old := unstash(dshStashKey(path, id)); old != "" {
			items[i] = dshItem{id: id, lines: strings.Split(old, "\n")}
			return
		}
		items = append(items[:i], items[i+1:]...)
	}

	switch {
	case v == "":
		drop("agent-default-model")
		drop("api-gateway")
		drop("agent-loop")
		drop("llm-deepseek")
		if items, err = dshPutRoute(items, false); err != nil {
			return err
		}
	case modern && viaGateway:
		drop("llm-deepseek") // magpie's before it was a provider of its own
		if items, err = dshPutRoute(items, true); err != nil {
			return err
		}
		if !contains(dshLevels(ref), effort) {
			effort = ""
		}
		put("agent-default-model", dshDefaultLines(dshRoute, ref, effort))
	case modern:
		drop("llm-deepseek")
		if items, err = dshPutRoute(items, false); err != nil {
			return err
		}
		if !contains(dshEfforts, effort) {
			effort = ""
		}
		put("agent-default-model", dshDefaultLines("deepseek-official", v, effort))
	case viaGateway:
		put("llm-deepseek", dshProviderLines(effort))
		put("agent-loop", dshLoopLines(ref))
		put("api-gateway", dshRouteLines(ref))
	default:
		drop("api-gateway")
		drop("llm-deepseek")
		put("agent-loop", dshLoopLines(v))
	}
	return dshWrite(path, head, items)
}

// dshPutRoute puts magpie's route, with the catalog as it is now, among the
// custom providers of the llm-pi-ai entry, or with on false takes it out.
// Other routes there, the user's or ones dsh added beside magpie's, stay; an
// entry left with nothing goes.
func dshPutRoute(items []dshItem, on bool) ([]dshItem, error) {
	i := dshFindLast(items, dshPiRow)
	if i < 0 && !on {
		return items, nil
	}
	var n *yaml.Node
	if i >= 0 {
		var err error
		if n, err = dshNode(items[i]); err != nil {
			return nil, err
		}
	} else {
		items = append(items, dshItem{id: dshPiRow, magpie: true, lines: []string{"- id: " + dshPiRow + " " + dshMark, "  name: " + yamlQuote(dshPiPlugin)}})
		i = len(items) - 1
		n, _ = dshNode(items[i])
	}
	config := yamlMapping(n, "config")
	providers := yamlMapping(config, "providers")
	if on {
		var route yaml.Node
		if err := route.Encode(dshRouteConfig()); err != nil {
			return nil, err
		}
		// what dsh's Models page added to it (a default level, headers)
		// stays; magpie's fields are put as they are now
		if cur := yamlKey(providers, dshRoute); cur != nil && cur.Kind == yaml.MappingNode {
			for k := 0; k+1 < len(route.Content); k += 2 {
				yamlSetKey(cur, route.Content[k].Value, route.Content[k+1])
			}
		} else {
			yamlSetKey(providers, dshRoute, &route)
		}
	} else {
		yamlDelKey(providers, dshRoute)
		if len(providers.Content) == 0 {
			yamlDelKey(config, "providers")
		}
		if len(config.Content) == 0 {
			yamlDelKey(n, "config")
		}
		if yamlKey(n, "config") == nil && len(n.Content) <= 4 { // id and name alone
			return append(items[:i], items[i+1:]...), nil
		}
	}
	lines, err := dshNodeLines(n)
	if err != nil {
		return nil, err
	}
	items[i] = dshItem{id: dshPiRow, magpie: items[i].magpie, lines: lines}
	return items, nil
}

// dshPiRoute is magpie's route as llm-pi-ai takes a custom provider (the
// shape dsh's Models page writes): the gateway's Chat Completions API, the
// key a credential named here, the catalog as its models.
type dshPiRoute struct {
	DisplayName string       `yaml:"displayName"`
	APIKeyEnv   string       `yaml:"apiKeyEnv"`
	API         string       `yaml:"api"`
	BaseURL     string       `yaml:"baseURL"`
	Models      []dshPiModel `yaml:"models"`
}

// dshPiModel is one model of the route. Without contextWindow and maxTokens
// dsh takes every model for a 128K one writing 16K at most, and without
// input for a text-only one; without reasoningEfforts it offers none.
type dshPiModel struct {
	ID               string       `yaml:"id"`
	Name             string       `yaml:"name,omitempty"`
	ContextWindow    int          `yaml:"contextWindow,omitempty"`
	MaxTokens        int          `yaml:"maxTokens,omitempty"`
	Input            []string     `yaml:"input,omitempty,flow"`
	ReasoningEfforts *dshPiLevels `yaml:"reasoningEfforts,omitempty"`
}

// dshPiLevels maps each of dsh's thinking levels a model takes to the
// reasoning_effort sent for it; a level left out is not offered.
type dshPiLevels struct {
	Off     string `yaml:"off,omitempty"`
	Minimal string `yaml:"minimal,omitempty"`
	Low     string `yaml:"low,omitempty"`
	Medium  string `yaml:"medium,omitempty"`
	High    string `yaml:"high,omitempty"`
	XHigh   string `yaml:"xhigh,omitempty"`
	Max     string `yaml:"max,omitempty"`
}

// dshThinking are dsh's thinking levels (pi-ai's), in its order.
var dshThinking = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// dshModelLevels are the levels of dsh's a catalog model takes, each with
// the effort sent for it: its own, and off for its none.
func dshModelLevels(efforts []string) ([]string, *dshPiLevels) {
	var levels []string
	var out dshPiLevels
	for _, l := range dshThinking {
		e := l
		if l == "off" {
			e = "none"
		}
		if !contains(efforts, e) {
			continue
		}
		levels = append(levels, l)
		switch l {
		case "off":
			out.Off = e
		case "minimal":
			out.Minimal = e
		case "low":
			out.Low = e
		case "medium":
			out.Medium = e
		case "high":
			out.High = e
		case "xhigh":
			out.XHigh = e
		case "max":
			out.Max = e
		}
	}
	// dsh turns away a model offering no level but off
	if len(levels) == 0 || len(levels) == 1 && levels[0] == "off" {
		return nil, nil
	}
	return levels, &out
}

// dshLevels are the thinking levels dsh offers for a catalog model.
func dshLevels(ref string) []string {
	for _, m := range magpieModels("dsh") {
		if m.ID == ref {
			levels, _ := dshModelLevels(m.Efforts)
			return levels
		}
	}
	return nil
}

func dshRouteConfig() dshPiRoute {
	r := dshPiRoute{DisplayName: "Magpie", APIKeyEnv: dshKeyRef, API: "openai-completions", BaseURL: gatewayV1(), Models: []dshPiModel{}}
	for _, m := range magpieModels("dsh") {
		e := dshPiModel{ID: m.ID, Name: m.Name, ContextWindow: m.Context}
		if m.Output > 0 {
			e.MaxTokens = maxTokens(m)
		}
		switch {
		case m.Images:
			e.Input = []string{"text", "image"}
		case m.ImageInput != nil:
			e.Input = []string{"text"}
		}
		_, e.ReasoningEfforts = dshModelLevels(m.Efforts)
		r.Models = append(r.Models, e)
	}
	return r
}

func yamlQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// dshProviderLines is the llm-deepseek entry pointing a dsh before 0.1.5 at
// the gateway, with the catalog as the models its /model offers and the key
// in the entry. effort is the thinking effort sessions start with, high
// (dsh's own default) when "".
func dshProviderLines(effort string) []string {
	if effort == "" {
		effort = "high"
	}
	lines := []string{
		"- id: llm-deepseek " + dshMark,
		"  config:",
		"    apiKey: " + yamlQuote(gateway.Token),
		"    baseURL: " + yamlQuote(gatewayV1()),
		"    thinking: enabled",
		"    reasoningEffort: " + effort,
		"    models:",
	}
	ms := magpieModels("dsh")
	if len(ms) == 0 {
		lines[len(lines)-1] = "    models: []"
	}
	for _, m := range ms {
		lines = append(lines, "      - id: "+yamlQuote(m.ID), "        name: "+yamlQuote(m.Name))
		// what dsh would otherwise take for every model: a million tokens
		// of context, 256K out, and text only
		if m.Context > 0 {
			lines = append(lines, fmt.Sprintf("        contextWindow: %d", m.Context))
		}
		if m.Output > 0 {
			lines = append(lines, fmt.Sprintf("        maxTokens: %d", maxTokens(m)))
		}
		if m.Images {
			lines = append(lines, "        inputModalities: [text, image]")
		}
	}
	return lines
}

// dshDefaultLines is the agent-default-model entry: the model new sessions
// start on, in every entry point, on provider's route, and the thinking
// effort they start with when effort isn't "".
func dshDefaultLines(provider, model, effort string) []string {
	lines := []string{
		"- id: agent-default-model " + dshMark,
		"  config:",
		"    provider: " + provider,
		"    model: " + yamlQuote(model),
	}
	if effort != "" {
		lines = append(lines, "    reasoningEffort: "+yamlQuote(effort))
	}
	return lines
}

// dshLoopLines is the agent-loop entry dsh ships, with model in place.
func dshLoopLines(model string) []string {
	return []string{
		"- id: agent-loop " + dshMark,
		"  config:",
		"    agents:",
		"      - id: main",
		"        provider: deepseek-official",
		"        model: " + yamlQuote(model),
		"        cwd: !!js process.cwd()",
	}
}

// dshRouteLines is the api-gateway entry: the route headless and web
// sessions start on.
func dshRouteLines(model string) []string {
	return []string{
		"- id: api-gateway " + dshMark,
		"  config:",
		"    provider: deepseek-official",
		"    model: " + yamlQuote(model),
	}
}

// dshSync puts the catalog as it is now into magpie's route (its
// llm-deepseek entry before 0.1.5), in every patch list that has it; a
// profile still on dsh's DeepSeek row taken over moves to the route.
// Nothing else in the patch lists changes.
func dshSync(dir string) error {
	files := dshProfiles(dir)
	if len(files) == 0 {
		return dshSyncLegacy(filepath.Join(dir, "config.yaml"))
	}
	defer func() {
		// the key under the name the route gives it, moved from the old one
		if _, items, err := dshRead(files[0]); err == nil && dshWired(items) {
			dshEnv(dir, true)
		}
	}()
	for _, f := range files {
		head, items, err := dshRead(f)
		if err != nil {
			continue
		}
		if dshOldWiring(items) {
			if err := dshMove(f, items); err != nil {
				return err
			}
			continue
		}
		if !dshWired(items) {
			continue
		}
		i := dshFindLast(items, dshPiRow)
		before := strings.Join(items[i].lines, "\n")
		if items, err = dshPutRoute(items, true); err != nil {
			continue
		}
		if strings.Join(items[i].lines, "\n") == before {
			continue
		}
		if err := dshWrite(f, head, items); err != nil {
			return err
		}
	}
	return dshFillNewProfiles(files)
}

// dshMove moves a profile magpie wired by taking over dsh's DeepSeek row
// onto magpie's route, the model and effort kept.
func dshMove(f string, items []dshItem) error {
	model := ""
	if i := dshFindLast(items, "agent-default-model"); i >= 0 {
		if c := dshConfig(items[i]); c["provider"] == "deepseek-official" {
			model = c["model"]
		}
	}
	if model == "" || !isMagpie(model) {
		// nothing of magpie's to start on: dsh's own row back alone
		head, items, err := dshRead(f)
		if err != nil {
			return err
		}
		if old := unstash(dshStashKey(f, "llm-deepseek")); old != "" {
			items[dshFind(items, "llm-deepseek")] = dshItem{id: "llm-deepseek", lines: strings.Split(old, "\n")}
		} else {
			i := dshFind(items, "llm-deepseek")
			items = append(items[:i], items[i+1:]...)
		}
		return dshWrite(f, head, items)
	}
	return dshSetFile(f, magpieID+"/"+model, true)
}

// dshSyncLegacy is dshSync on a config.yaml before 0.1.5.
func dshSyncLegacy(f string) error {
	head, items, err := dshRead(f)
	if err != nil {
		return nil
	}
	i := dshFind(items, "llm-deepseek")
	if i < 0 || !items[i].magpie {
		return nil
	}
	lines := dshProviderLines(dshEffortIn(items))
	if strings.Join(lines, "\n") == strings.Join(items[i].lines, "\n") {
		return nil
	}
	items[i].lines = lines
	return dshWrite(f, head, items)
}

// dshFillNewProfiles gives a profile dsh made after magpie set it up — the
// desktop app's, opened for the first time after the web's was wired —
// magpie's route, and the model magpie set in the others when it starts on
// none of the user's: until then it lists dsh's own models alone.
func dshFillNewProfiles(files []string) error {
	model := ""
	var bare []string
	for _, f := range files {
		_, items, err := dshRead(f)
		if err != nil {
			continue
		}
		if !dshWired(items) {
			bare = append(bare, f)
			continue
		}
		if i := dshFindLast(items, "agent-default-model"); i >= 0 && model == "" {
			if c := dshConfig(items[i]); c["provider"] == dshRoute {
				model = c["model"]
			}
		}
	}
	if model == "" {
		return nil
	}
	for _, f := range bare {
		head, items, err := dshRead(f)
		if err != nil {
			continue
		}
		if i := dshFind(items, "agent-default-model"); i < 0 || dshOurs(items[i]) {
			if err := dshSetFile(f, magpieID+"/"+model, true); err != nil {
				return err
			}
			continue
		}
		// a model of the user's: magpie's are listed beside it
		if items, err = dshPutRoute(items, true); err != nil {
			continue
		}
		if err := dshWrite(f, head, items); err != nil {
			return err
		}
	}
	return nil
}

// dshEfforts are the thinking efforts dsh's own DeepSeek row takes: its
// reasoningEffort is off, low, high or max (low since 0.1.1), sent as
// reasoning_effort before 0.1.7 and output_config.effort from it on.
var dshEfforts = []string{"off", "low", "high", "max"}

// dshEffortIn is the reasoningEffort of the llm-deepseek entry magpie
// wrote, "" when there is none.
func dshEffortIn(items []dshItem) string {
	i := dshFind(items, "llm-deepseek")
	if i < 0 || !items[i].magpie {
		return ""
	}
	for _, l := range items[i].lines {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && k == "reasoningEffort" {
			return yamlScalar(strings.TrimSpace(v))
		}
	}
	return ""
}

// dshEffortOf is the effort sessions start with in a profile (0.1.5 on):
// agent-default-model's, else, not yet moved, magpie's llm-deepseek's.
func dshEffortOf(items []dshItem) string {
	if i := dshFindLast(items, "agent-default-model"); i >= 0 {
		if e := dshConfig(items[i])["reasoningEffort"]; e != "" {
			return e
		}
	}
	return dshEffortIn(items)
}

// dshFiles are the patch lists magpie writes: every profile's, else
// config.yaml.
func dshFiles(dir string) []string {
	if files := dshProfiles(dir); len(files) > 0 {
		return files
	}
	return []string{filepath.Join(dir, "config.yaml")}
}

// DshPatchFiles are the patch lists of the dsh whose home is dir, as magpie
// writes them: every profile's (web's first), else config.yaml.
func DshPatchFiles(dir string) []string { return dshFiles(dir) }

// dshGetEffort reads the effort sessions start with.
func dshGetEffort(dir string) string {
	files := dshProfiles(dir)
	if len(files) == 0 {
		_, items, err := dshRead(filepath.Join(dir, "config.yaml"))
		if err != nil {
			return ""
		}
		return dshEffortIn(items)
	}
	_, items, err := dshRead(files[0])
	if err != nil {
		return ""
	}
	return dshEffortOf(items)
}

// dshSetEffort writes v as the effort sessions start with: into
// agent-default-model (0.1.5 on), or into magpie's llm-deepseek entry
// before, dsh's own row being used whole when there is none.
func dshSetEffort(dir, v string) error {
	files := dshProfiles(dir)
	if len(files) > 0 {
		model := dshGet(dir)
		ref, viaGateway := strings.CutPrefix(model, magpieID+"/")
		levels := dshEfforts
		if viaGateway {
			levels = dshLevels(ref)
		}
		if v != "" && !contains(levels, v) {
			return fmt.Errorf("DeepSeek Harness takes an effort of %s for this model, not %q", strings.Join(levels, ", "), v)
		}
		if model == "" {
			if v == "" {
				return nil
			}
			return fmt.Errorf("pick a model for DeepSeek Harness first; the effort is kept with it")
		}
		for _, f := range files {
			head, items, err := dshRead(f)
			if err != nil {
				continue
			}
			// each profile keeps the model it starts on
			p, m := "deepseek-official", ref
			if viaGateway {
				p = dshRoute
			}
			if i := dshFindLast(items, "agent-default-model"); i >= 0 {
				if c := dshConfig(items[i]); c["model"] != "" && c["provider"] != "" {
					p, m = c["provider"], c["model"]
				}
			}
			i := dshFind(items, "agent-default-model")
			if i >= 0 && !dshOurs(items[i]) {
				stash(map[string]string{dshStashKey(f, "agent-default-model"): strings.Join(items[i].lines, "\n")})
			}
			it := dshItem{id: "agent-default-model", magpie: true, lines: dshDefaultLines(p, m, v)}
			if i >= 0 {
				items[i] = it
			} else {
				items = append(items, it)
			}
			if err := dshWrite(f, head, items); err != nil {
				return err
			}
		}
		return nil
	}

	if v != "" && !contains(dshEfforts, v) {
		return fmt.Errorf("DeepSeek Harness takes an effort of %s, not %q", strings.Join(dshEfforts, ", "), v)
	}
	f := filepath.Join(dir, "config.yaml")
	head, items, err := dshRead(f)
	if err == nil {
		if i := dshFind(items, "llm-deepseek"); i >= 0 && items[i].magpie {
			items[i].lines = dshProviderLines(v)
			return dshWrite(f, head, items)
		}
	}
	if v != "" {
		return fmt.Errorf("pick a model through magpie for DeepSeek Harness first; the effort is kept with magpie's DeepSeek entry")
	}
	return nil
}

// dshSettingsEndpoint reports whether dsh's own settings carry an
// llm-deepseek section with an endpoint or key, which dsh puts over the
// patch list.
func dshSettingsEndpoint(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	in := false
	for _, l := range splitLinesKeep(string(b)) {
		if l != "" && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "#") {
			in = strings.HasPrefix(l, "llm-deepseek:")
			continue
		}
		t := strings.TrimSpace(l)
		if in && (strings.HasPrefix(t, "baseURL:") || strings.HasPrefix(t, "apiKey:")) {
			return true
		}
	}
	return false
}
