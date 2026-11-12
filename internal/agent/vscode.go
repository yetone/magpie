package agent

// VS Code's own chat (GitHub Copilot Chat, built into VS Code) takes models
// of other vendors through its Custom Endpoint provider, in VS Code Stable
// since 1.122, with no GitHub sign-in or Copilot plan needed: groups of
// models in chatLanguageModels.json beside its settings.json (the default
// profile's, in its User folder, and each other profile's in its own:
// vscodeProfiles), a JSONC array VS Code watches and reads again when it
// changes. magpie adds a group of its own there,
// {"vendor": "customendpoint", "name": "magpie"}, listing the catalog: each
// model at the gateway's /v1/chat/completions with tool calling on (agent
// mode lists only models that call tools). A group's apiKey can't be
// written in the file: VS Code reads it from its secret storage alone, so
// each model sends magpie's token in requestHeaders' Authorization, which a
// Custom Endpoint model may set and which then stands in for the key; the
// user is asked for none. The model a new chat starts on is the
// chat.defaultModel setting (a model id: the catalog id here); picked in
// Chat's model picker, a model is kept in VS Code's storage, not here.
//
// VSCodium has no Copilot Chat of its own: its users install the
// Marketplace's, whose last release (0.48.1) has no Custom Endpoint
// provider but an OpenAI Compatible one, customoai (xybio, #1014: only Auto
// showed). That one takes the same group in the same file, with each
// model's window as maxInputTokens beside maxOutputTokens, and drops an
// Authorization in requestHeaders, so magpie's token goes as x-api-key
// there (the gateway takes a key from either). It registers its providers
// only while Copilot Chat is signed in to GitHub with a personal Copilot
// plan; the Notice says so. Which of the two magpie writes is the one the
// installed extension's package.json declares (vscodeChatOf).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/proc"
)

// vscodeDefault is chat.defaultModel, a key with a dot in it, escaped for
// edit's key paths.
const vscodeDefault = `chat\.defaultModel`

// vscodeUA is how VS Code's chat names itself to a Custom Endpoint: its
// fetcher's GitHubCopilotChat/<version>, which a model's requestHeaders
// can't change.
const vscodeUA = "githubcopilotchat"

// vscodeAgentHostUA is VS Code's Agents window: its agent host runs
// `codex app-server` with clientInfo named vscode_agent_host (VS Code's
// src/vs/platform/agentHost/node/codex/codexAgent.ts), and Codex makes
// that its originator and the start of its User-Agent
// ("vscode_agent_host/0.163.0 (Mac OS …; arm64) … (vscode_agent_host;
// 0.1.0)"). Its calls were listed under that raw name beside VS Code's
// (justruman on Discord). Insiders' and VSCodium's agent host send the
// same name, so they are counted as VS Code too.
const vscodeAgentHostUA = "vscode_agent_host"

// The providers magpie's group can be written for: VS Code's Custom
// Endpoint, and the OpenAI Compatible one of the Copilot Chat VSCodium
// installs.
const (
	vscodeEndpoint = "customendpoint"
	vscodeOAI      = "customoai"
)

// vscodeVendors are both, the one magpie writes when it can't tell first.
var vscodeVendors = []string{vscodeEndpoint, vscodeOAI}

// vscodeGroupOf is how magpie's group for a provider is found among the
// user's.
func vscodeGroupOf(vendor string) map[string]string {
	return map[string]string{"vendor": vendor, "name": magpieID}
}

// vscodeGroup is magpie's Custom Endpoint group.
var vscodeGroup = vscodeGroupOf(vscodeEndpoint)

// vscodeOurs is magpie's group in a chatLanguageModels.json, whichever
// provider it was written for, and that provider.
func vscodeOurs(lm string) (group, vendor string, ok bool) {
	for _, v := range vscodeVendors {
		if g, ok := edit.GetJSONItem(lm, vscodeGroupOf(v)); ok {
			return g, v, true
		}
	}
	return "", "", false
}

// vscodeKind is one build of VS Code, each with its own User folder, its
// own app and its own row: VS Code, and VS Code Insiders beside it (wani on
// Discord), whose chat is the same and is set up the same way.
type vscodeKind struct {
	id, name, folder, bin string
	aliases               []string
	// ua is what its chat's User-Agent begins with: Stable is told by it.
	// Insiders' chat names itself the same, so it is told by its token
	// instead (gateway.TokenFor), which its models send.
	ua []string
	// exts, for a build without a Copilot Chat of its own, is the folder
	// in home its extensions are installed in; extDir is where that is
	// (vscodeOf), "" for a build whose chat is built in.
	exts, extDir string
}

var (
	vscodeStable   = vscodeKind{id: "vscode", name: "VS Code", folder: "Code", bin: "code", aliases: []string{"vs-code", "copilot-chat", "vscode-chat"}, ua: []string{vscodeUA, vscodeAgentHostUA}}
	vscodeInsiders = vscodeKind{id: "vscode-insiders", name: "VS Code Insiders", folder: "Code - Insiders", bin: "code-insiders", aliases: []string{"vs-code-insiders", "code-insiders"}}
	vscodiumKind   = vscodeKind{id: "vscodium", name: "VSCodium", folder: "VSCodium", bin: "codium", aliases: []string{"codium", "vscodium-chat"}, exts: ".vscode-oss"}
)

// token is the bearer token its models send: Stable's the gateway's own,
// as it always was.
func (k vscodeKind) token() string {
	if k.ua != nil {
		return hereKey()
	}
	return agentKey(k.id)
}

// VS Code keeps its User folder under Application Support on macOS, Roaming
// AppData on Windows and XDG on Linux, Insiders' beside it.
func vscode(home, cfg string) *Agent { return vscodeOf(vscodeStable, home, cfg) }

func vscodeInsidersAgent(home, cfg string) *Agent { return vscodeOf(vscodeInsiders, home, cfg) }

func vscodium(home, cfg string) *Agent { return vscodeOf(vscodiumKind, home, cfg) }

func vscodeOf(k vscodeKind, home, cfg string) *Agent {
	dir, exts := vscodeDirs(k, home, cfg)
	if k.exts != "" {
		k.extDir = exts
	}
	return vscodeKindAt(k, dir)
}

// vscodeOS is the system whose VS Code layout is read; a var so a test can
// lay out a Mac's portable build on any machine.
var vscodeOS = runtime.GOOS

// vscodeDirs are the User folder of the VS Code k, where its settings.json
// is (its extensions', Zoo Code's, too), and its extensions folder, both of
// the same build. A portable build keeps both in its data folder
// (vscodePortableData); an installed build's are the OS's place and the
// home folder's.
func vscodeDirs(k vscodeKind, home, cfg string) (user, exts string) {
	if data := vscodePortableData(k); data != "" {
		return filepath.Join(data, "user-data", "User"), filepath.Join(data, "extensions")
	}
	switch vscodeOS {
	case "darwin":
		cfg = filepath.Join(home, "Library", "Application Support")
	case "windows":
		cfg = appdir.Getenv("APPDATA")
		if cfg == "" {
			cfg = filepath.Join(home, "AppData", "Roaming")
		}
	}
	return filepath.Join(cfg, k.folder, "User"), filepath.Join(home, k.extFolder(), "extensions")
}

// extFolder is the folder in home an installed build keeps its extensions
// in: .vscode, .vscode-insiders, .vscode-oss.
func (k vscodeKind) extFolder() string {
	switch {
	case k.exts != "":
		return k.exts
	case k.id == vscodeInsiders.id:
		return ".vscode-insiders"
	}
	return ".vscode"
}

// vscodeBundles are where a Mac keeps each build's app, by bundle id and
// name.
var vscodeBundles = map[string]desktopApp{
	vscodeStable.id:   {id: "com.microsoft.VSCode", names: []string{"Visual Studio Code.app"}},
	vscodeInsiders.id: {id: "com.microsoft.VSCodeInsiders", names: []string{"Visual Studio Code - Insiders.app"}},
	vscodiumKind.id:   {id: "com.vscodium", names: []string{"VSCodium.app"}},
}

// vscodePortableData is the data folder of a portable VS Code k, "" for an
// installed build. The build is the one k's launcher is, followed through
// a Scoop shim and every symlink (macOS: /usr/local/bin/code into
// /Applications/Code/Visual Studio Code.app, #4151; Windows: Scoop's
// current junction, #3260). On a Mac whose launcher isn't a build's, the
// app folders' and Spotlight's bundles are read, and taken only when every
// one found is the same portable build: a launcher, or no doubt, decides
// which of two installs the user runs.
func vscodePortableData(k vscodeKind) string {
	if bin := binAt(k.bin); bin != "" {
		if at := scoopShim(bin); at != "" {
			bin = at
		}
		if resolved, err := filepath.EvalSymlinks(bin); err == nil {
			bin = resolved
		}
		if appRoot, appPath := vscodeAppRoot(k, bin); appRoot != "" {
			// a fork's launcher called code (Cursor's) is not k's
			if data, ours := vscodePortableAt(k, appRoot, appPath); ours {
				return data
			}
		}
	}
	app, ok := vscodeBundles[k.id]
	if vscodeOS != "darwin" || !ok {
		return ""
	}
	found := ""
	for _, bundle := range macBundles(app) {
		if !isDir(bundle) {
			continue
		}
		data, _ := vscodePortableAt(k, filepath.Join(bundle, "Contents", "Resources", "app"), bundle)
		if data == "" || (found != "" && data != found) {
			return ""
		}
		found = data
	}
	return found
}

// vscodePortableFile reports whether path is in a portable VS Code's data
// folder: VS Code's own files there, or Zoo Code's beside them.
func vscodePortableFile(path string) bool {
	for _, k := range []vscodeKind{vscodeStable, vscodeInsiders, vscodiumKind} {
		if data := vscodePortableData(k); data != "" {
			if r, err := filepath.Rel(data, path); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// vscodeAppRoot is the app folder (resources/app, where product.json is)
// of the build whose launcher is bin, and the build's own folder (VS
// Code's getApplicationPath): <app>.app/Contents/Resources/app/bin and the
// .app on a Mac; elsewhere <install>/bin beside <install>/resources/app,
// or <install>/<version>/resources/app for a build that updates into a
// versioned folder (win32VersionedUpdate), and <install>. "" for a
// launcher outside any build's bin folder.
func vscodeAppRoot(k vscodeKind, bin string) (appRoot, appPath string) {
	dir := filepath.Dir(bin)
	if !strings.EqualFold(filepath.Base(dir), "bin") {
		return "", ""
	}
	if vscodeOS != "darwin" {
		install := filepath.Dir(dir)
		appRoot = filepath.Join(install, "resources", "app")
		if _, err := os.Stat(filepath.Join(appRoot, "product.json")); err != nil {
			if m, _ := filepath.Glob(filepath.Join(install, "*", "resources", "app", "product.json")); len(m) > 0 {
				appRoot = filepath.Dir(m[len(m)-1])
			}
		}
		return appRoot, install
	}
	appRoot = filepath.Dir(dir)
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(appRoot)))
	if filepath.Base(bin) != k.bin || !strings.HasSuffix(bundle, ".app") ||
		appRoot != filepath.Join(bundle, "Contents", "Resources", "app") {
		return "", ""
	}
	return appRoot, bundle
}

// vscodePortableAt is the data folder of the build at appRoot when it is
// portable, as VS Code's own configurePortable (src/bootstrap-node.ts)
// decides it: its product.json has no target (an installer's build has
// one) and its portable data folder exists. That folder is data beside the
// program on Windows and Linux; on a Mac it is beside the .app, named by
// product.json's portable, else <applicationName>-portable-data. A Mac
// build's product.json must be read, and name k, to name it; elsewhere one
// that can't be read leaves the data folder alone to decide. ours is false
// when product.json names another editor (a fork's launcher called code):
// the build is not k at all.
func vscodePortableAt(k vscodeKind, appRoot, appPath string) (data string, ours bool) {
	var product struct {
		ApplicationName string          `json:"applicationName"`
		Portable        string          `json:"portable"`
		Target          json.RawMessage `json:"target"`
	}
	b, err := os.ReadFile(filepath.Join(appRoot, "product.json"))
	read := err == nil && json.Unmarshal(b, &product) == nil
	if read && product.ApplicationName != "" && product.ApplicationName != k.bin {
		return "", false
	}
	if read && product.Target != nil {
		return "", true
	}
	if vscodeOS == "darwin" {
		if !read || product.ApplicationName == "" {
			return "", true
		}
		name := product.Portable
		if name == "" {
			name = product.ApplicationName + "-portable-data"
		}
		data = filepath.Join(filepath.Dir(appPath), name)
	} else {
		data = filepath.Join(appPath, "data")
	}
	if !isDir(data) {
		return "", true
	}
	return data, true
}

// scoopShim is the launcher a Scoop shim stands for: the path = line of the
// .shim beside it (code.cmd in scoop's shims folder is a small program that
// runs it, which proc.Unwrap doesn't read). "" when bin is no shim.
func scoopShim(bin string) string {
	b, err := os.ReadFile(strings.TrimSuffix(bin, filepath.Ext(bin)) + ".shim")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "path" && f[1] == "=" {
			return strings.Trim(strings.Join(f[2:], " "), `"`)
		}
	}
	return ""
}

func vscodeAt(dir string) *Agent { return vscodeKindAt(vscodeStable, dir) }

func vscodeKindAt(k vscodeKind, dir string) *Agent {
	path := filepath.Join(dir, "settings.json")
	models := filepath.Join(dir, "chatLanguageModels.json")
	key := func(p string) string { return k.id + ":" + p + ":model" }
	getAt := func(p string) string { v, _ := edit.GetJSON(p, vscodeDefault); return v }
	get := func() string { return getAt(path) }
	ours := func() (string, bool) { g, _, ok := vscodeOurs(models); return g, ok }
	joined := func() bool { _, ok := ours(); return ok }
	// inGroup: a model id is one of those magpie's group lists
	inGroup := func(id string) bool {
		g, ok := ours()
		if !ok || id == "" {
			return false
		}
		for _, m := range gjson.Get(g, "models").Array() {
			if strings.EqualFold(m.Get("id").String(), id) {
				return true
			}
		}
		return false
	}
	model := func() string {
		v := get()
		if inGroup(v) {
			return magpieID + "/" + v
		}
		return v
	}
	// restore puts back the chat.defaultModel a settings.json had before
	// magpie's
	restore := func(p string) error {
		if was := unstash(key(p)); was != "" {
			return edit.SetJSON(p, edit.KV{Path: vscodeDefault, Value: was})
		}
		return edit.DelJSON(p, vscodeDefault)
	}
	// profiles runs fn on the files of the profiles VS Code has now, put
	// back as they were if it fails, as atomic does the default's
	profiles := func(fn func(settings, models []string) error) error {
		s, m := vscodeProfiles(dir)
		return edit.Atomically(func() error { return fn(s, m) }, append(s, m...)...)
	}
	// setGroup writes magpie's group in the default's and each profile's,
	// for the provider the chat installed has, in place of one written
	// for the other
	setGroup := func(lms []string) error {
		vendor := k.chat().vendor
		v := vscodeGroupJSON(k, vendor)
		for _, lm := range append([]string{models}, lms...) {
			for _, other := range vscodeVendors {
				if other != vendor {
					if err := edit.DelJSONItem(lm, vscodeGroupOf(other)); err != nil {
						return err
					}
				}
			}
			var want any = v
			if cur, ok := edit.GetJSONItem(lm, vscodeGroupOf(vendor)); ok {
				if want = vscodeGroupKept(cur, v); sameJSON(cur, want) {
					continue
				}
			}
			if err := edit.SetJSONItem(lm, vscodeGroupOf(vendor), want); err != nil {
				return err
			}
		}
		return nil
	}
	return atomic(&Agent{
		ID: k.id, Name: k.name, Icon: "vscode", Aliases: k.aliases, Spelled: prefixed,
		Bin: k.bin, Dir: dir, Path: path,
		UA: k.ua,
		detect: func() bool {
			if Taken(dir) {
				return false
			}
			if isDir(dir) {
				return true
			}
			bin, err := proc.Look(k.bin)
			return err == nil && vscodeCodeBinary(bin, k.bin)
		},
		Notice: func() string {
			if joined() {
				if k.exts != "" {
					return k.chat().notice(k.name)
				}
				return noticeVSCodeJoined.say("agent", k.name)
			}
			return ""
		},
		// magpie's group joins the user's own models: picking one of VS
		// Code's in magpie keeps it, and only switching off takes it out
		Joined: joined,
		Unwire: func() error {
			return profiles(func(settings, lms []string) error {
				for _, p := range append([]string{path}, settings...) {
					if inGroup(getAt(p)) {
						if err := restore(p); err != nil {
							return err
						}
					}
					forget(key(p))
				}
				for _, lm := range append([]string{models}, lms...) {
					for _, v := range vscodeVendors {
						if err := edit.DelJSONItem(lm, vscodeGroupOf(v)); err != nil {
							return err
						}
					}
				}
				return nil
			})
		},
		// a profile made since is given the group too
		Sync: func() error {
			if !joined() {
				return nil
			}
			return profiles(func(_, lms []string) error { return setGroup(lms) })
		},
		Check: func() string {
			g, vendor, ok := vscodeOurs(models)
			if !ok || !gjson.Get(g, "models.0").Exists() {
				return ""
			}
			if c := k.chat(); c.unsupported {
				return c.notice(k.name)
			} else if c.vendor != vendor {
				return k.name + "'s Copilot Chat " + c.version + " lists a group for " + c.vendor + " and magpie's is for " + vendor + "; magpie writes it again when it next starts"
			}
			header, want := "requestHeaders.Authorization", "Bearer "+k.token()
			if vendor == vscodeOAI {
				header, want = "requestHeaders.x-api-key", k.token()
			}
			return wiringOff(k.name, models, func(k string) (string, bool) {
				r := gjson.Get(g, "models.0."+k)
				return r.String(), r.Exists()
			}, "url", vscodeURL(), header, want)
		},
		Fields: []Field{{
			Key: "model", Label: "model", Get: model,
			Set: func(v string) error {
				return profiles(func(settings, lms []string) error {
					if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
						if err := setGroup(lms); err != nil {
							return err
						}
						// a new chat in each profile starts on it
						for _, p := range append([]string{path}, settings...) {
							if !inGroup(getAt(p)) {
								stash(map[string]string{key(p): getAt(p)})
							}
							if err := edit.SetJSON(p, edit.KV{Path: vscodeDefault, Value: ref}); err != nil {
								return err
							}
						}
						return nil
					}
					// one of VS Code's own: the profiles go back to theirs
					for _, p := range settings {
						if inGroup(getAt(p)) {
							if err := restore(p); err != nil {
								return err
							}
						}
					}
					// VS Code's own: its default, where magpie's group stays
					if v == "" {
						if inGroup(get()) {
							return restore(path)
						}
						return edit.DelJSON(path, vscodeDefault)
					}
					forget(key(path))
					return edit.SetJSON(path, edit.KV{Path: vscodeDefault, Value: v})
				})
			},
			Options: func(cur map[string]string) []Option {
				// auto: Copilot's Auto, for one signed in to it
				own := []Option{{Value: "auto", Label: "Auto"}}
				if v := cur["model"]; v != "" && v != "auto" && !usesMagpie(v) {
					own = append(own, Option{Value: v, Icon: modelIcon("", v)})
				}
				return withVia(group(k.name, own), k.id, magpieID+"/")
			},
		}},
	}, path, models, stashPath())
}

// Forks of VS Code ship a launcher named code beside their own, in the
// same bin folder: Cursor's, Qoder IDE's (Contents/Resources/app/bin/code
// beside qoder), and Windows' installers put that folder on PATH. Such a
// code is the fork, not VS Code (ggbr on Discord: Qoder IDE listed as VS
// Code too). So a launcher counts as the build whose command is app only
// when its product.json names that build (applicationName code,
// code-insiders or codium); one that names any other product is ruled
// out. A wrapper without readable product metadata keeps the PATH
// fallback.
func vscodeCodeBinary(bin, app string) bool {
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return true
	}
	dir := filepath.Dir(resolved)
	if !strings.EqualFold(filepath.Base(dir), "bin") {
		return true
	}
	// Launchers live in app/bin or in bin beside resources/app. Do not
	// search arbitrary ancestors of an unknown wrapper.
	for _, path := range []string{
		filepath.Join(dir, "..", "product.json"),
		filepath.Join(dir, "..", "resources", "app", "product.json"),
	} {
		var product struct {
			ApplicationName string `json:"applicationName"`
			NameShort       string `json:"nameShort"`
		}
		b, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(b, &product) != nil {
			continue
		}
		if name := strings.TrimSpace(product.ApplicationName); name != "" {
			return strings.EqualFold(name, app)
		}
		if short := strings.TrimSpace(product.NameShort); short != "" {
			return vscodeNameShort(short)
		}
	}
	return true
}

// vscodeNameShort reports whether a product.json's nameShort is one of VS
// Code's own builds (Code, Code - Insiders, Code - OSS, Visual Studio Code,
// VSCodium) rather than a fork's (Cursor, Qoder, Windsurf, Trae, Kiro…).
func vscodeNameShort(short string) bool {
	s := strings.ToLower(short)
	return s == "code" || strings.HasPrefix(s, "code - ") || strings.HasPrefix(s, "visual studio code") || s == "vscodium"
}

// vscodeProfiles are the settings.json and chatLanguageModels.json of the
// profiles made in VS Code besides its default (whose are in dir): a window
// opened on one reads that profile's own, so a group only in the default's
// is in the Agents window (which uses the default's) and not in the Chat of
// a window on another profile (TJHHHH on Discord). VS Code lists them in
// globalStorage/storage.json's userDataProfiles, each location a folder
// under dir/profiles; a profile set to use the default's settings or
// models (useDefaultFlags) has none of that kind, and one whose folder is
// gone none at all.
func vscodeProfiles(dir string) (settings, models []string) {
	b, err := os.ReadFile(filepath.Join(dir, "globalStorage", "storage.json"))
	if err != nil {
		return nil, nil
	}
	for _, p := range gjson.GetBytes(b, "userDataProfiles").Array() {
		loc := p.Get("location")
		at := loc.String()
		if loc.IsObject() {
			// a URI: its fsPath when kept, else its path (/c:/… on Windows)
			if at = loc.Get("fsPath").String(); at == "" {
				at = loc.Get("path").String()
				if len(at) > 2 && at[0] == '/' && at[2] == ':' {
					at = at[1:]
				}
				at = filepath.FromSlash(at)
			}
		} else if at != "" {
			at = filepath.Join(dir, "profiles", at)
		}
		if at == "" || !isDir(at) {
			continue
		}
		if !p.Get("useDefaultFlags.settings").Bool() {
			settings = append(settings, filepath.Join(at, "settings.json"))
		}
		if !p.Get("useDefaultFlags.languageModels").Bool() {
			models = append(models, filepath.Join(at, "chatLanguageModels.json"))
		}
	}
	return settings, models
}

// vscodeURL is the gateway's Chat Completions URL: a Custom Endpoint model's
// url that ends in /chat/completions is asked as it is.
func vscodeURL() string { return gatewayV1() + "/chat/completions" }

// vscodeGroupJSON is magpie's group in chatLanguageModels.json for a
// provider (vscodeEndpoint or vscodeOAI): the catalog as the chat of the VS
// Code k is shown it.
func vscodeGroupJSON(k vscodeKind, vendor string) map[string]any {
	list := []any{}
	for _, m := range sharedModels(k.id) {
		context := m.Context
		if context == 0 {
			context = unknownWindow(128000) // what VS Code takes a model of unknown window for
		}
		// VS Code keeps the output budget out of the window it fills with
		// the prompt, so a model whose output limit is near its window
		// would leave the prompt next to no room
		output := maxTokens(m)
		if output <= 0 {
			output = 8192
		}
		output = outputOr(m, min(output, context/4))
		entry := map[string]any{
			"id": m.ID, "name": m.Name, "url": vscodeURL(),
			"toolCalling": true, "vision": m.Images,
			"contextWindow": context, "maxOutputTokens": output,
			"requestHeaders": map[string]string{"Authorization": "Bearer " + k.token()},
		}
		if vendor == vscodeOAI {
			// its window is maxInputTokens plus maxOutputTokens, and an
			// Authorization of the model's own is dropped
			delete(entry, "contextWindow")
			entry["maxInputTokens"] = context - output
			entry["requestHeaders"] = map[string]string{"x-api-key": k.token()}
		}
		if len(m.Efforts) > 0 {
			entry["supportsReasoningEffort"] = m.Efforts
			entry["reasoningEffortFormat"] = "chat-completions"
		}
		list = append(list, entry)
	}
	if vendor == vscodeOAI {
		return map[string]any{"name": magpieID, "vendor": vscodeOAI, "models": list}
	}
	return map[string]any{"name": magpieID, "vendor": vscodeEndpoint, "apiType": "chat-completions", "models": list}
}

// vscodeGroupKept is magpie's group v laid over the one in the file, cur,
// with every key of cur magpie doesn't write kept: VS Code itself writes
// the reasoning effort picked for a model in Chat's model picker into the
// group, as settings["<model id>"].reasoningEffort (languageModels.ts
// setModelConfiguration), and a whole rewrite on each Sync and pick sent
// that model at its first level (chim3744 on Discord: max went out as
// low). The settings of a model magpie no longer lists stay too, as the
// user's. The model list is magpie's whole, so a model gone from the
// catalog leaves it.
func vscodeGroupKept(cur string, v map[string]any) any {
	var have, want map[string]json.RawMessage
	b, err := json.Marshal(v)
	if err != nil || json.Unmarshal([]byte(cur), &have) != nil || have == nil || json.Unmarshal(b, &want) != nil || want == nil {
		return v
	}
	return mergeTheirs(have, want, []string{"models"})
}

// vscodeChatID is Copilot Chat's extension id.
const vscodeChatID = "github.copilot-chat"

// vscodeChat is the Copilot Chat a VS Code build runs, as far as magpie's
// group goes: the provider the group is written for, the version found,
// none (no Copilot Chat installed) and unsupported (one that declares
// neither provider).
type vscodeChat struct {
	vendor, version   string
	none, unsupported bool
}

// chat is the Copilot Chat k runs: a build with one built in has the
// Custom Endpoint provider; one without has the extension installed in its
// extensions folder, whose package.json says which it registers. A folder
// or file that can't be read is not taken as "none": the Custom Endpoint
// group stays as magpie always wrote it.
func (k vscodeKind) chat() vscodeChat {
	if k.exts == "" {
		return vscodeChat{vendor: vscodeEndpoint}
	}
	return vscodeChatOf(k.extDir)
}

func vscodeChatOf(dir string) vscodeChat {
	at, version, found := vscodeChatInstalled(dir)
	if !found {
		return vscodeChat{vendor: vscodeEndpoint, none: true}
	}
	b, err := os.ReadFile(filepath.Join(at, "package.json"))
	if err != nil || !gjson.ValidBytes(b) {
		return vscodeChat{vendor: vscodeEndpoint, version: version}
	}
	if version == "" {
		version = gjson.GetBytes(b, "version").String()
	}
	has := func(vendor string) bool {
		return gjson.GetBytes(b, `contributes.languageModelChatProviders.#(vendor=="`+vendor+`")`).Exists()
	}
	switch {
	case has(vscodeEndpoint):
		return vscodeChat{vendor: vscodeEndpoint, version: version}
	case has(vscodeOAI):
		return vscodeChat{vendor: vscodeOAI, version: version}
	}
	return vscodeChat{vendor: vscodeEndpoint, version: version, unsupported: true}
}

// vscodeChatInstalled is the folder of the Copilot Chat installed in an
// extensions folder: the one its extensions.json lists, else the highest
// version of the github.copilot-chat-<version> folders not marked
// obsolete. found is false only when the folder was read and has none.
func vscodeChatInstalled(dir string) (at, version string, found bool) {
	if b, err := os.ReadFile(filepath.Join(dir, "extensions.json")); err == nil && gjson.ValidBytes(b) {
		for _, e := range gjson.ParseBytes(b).Array() {
			if !strings.EqualFold(e.Get("identifier.id").String(), vscodeChatID) {
				continue
			}
			if rel := e.Get("relativeLocation").String(); rel != "" {
				return filepath.Join(dir, rel), e.Get("version").String(), true
			}
			if p := e.Get("location.fsPath").String(); p != "" {
				return p, e.Get("version").String(), true
			}
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", "", !os.IsNotExist(err)
	}
	var obsolete map[string]bool
	if b, err := os.ReadFile(filepath.Join(dir, ".obsolete")); err == nil {
		_ = json.Unmarshal(b, &obsolete)
	}
	for _, e := range ents {
		v, ok := strings.CutPrefix(strings.ToLower(e.Name()), vscodeChatID+"-")
		if !e.IsDir() || !ok || obsolete[e.Name()] {
			continue
		}
		// a platform build is github.copilot-chat-<version>-<platform>
		if i := strings.IndexByte(v, '-'); i > 0 {
			v = v[:i]
		}
		if at == "" || vscodeNewer(v, version) {
			at, version = filepath.Join(dir, e.Name()), v
		}
	}
	return at, version, at != ""
}

// notice is what a build without its own chat is told with magpie's group
// written: what its Copilot Chat needs to list it.
func (c vscodeChat) notice(name string) string {
	setup := noticeVSCodeSetup.say("agent", name)
	switch {
	case c.none:
		return noticeVSCodeNoChat.say("agent", name) + " " + setup
	case c.unsupported:
		return noticeVSCodeOld.say("agent", name, "version", c.version)
	case c.vendor == vscodeOAI:
		return noticeVSCodeOAI.say("agent", name, "version", c.version) + " " + setup
	}
	return setup
}

// vscodeNewer is whether extension version a is later than b, compared
// number by number.
func vscodeNewer(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// what VS Code and its forks say after a change (notice.go)
var (
	noticeVSCodeJoined = newNotice("magpie's models are in {agent}'s Chat model picker, under magpie, in each of its profiles (VS Code 1.122 or later). If they don't show, run Developer: Reload Window in {agent}.")
	noticeVSCodeSetup  = newNotice("{agent}'s Chat features must be enabled (chat.disableAIFeatures=false) and its product.json must include defaultChatAgent and trustedExtensionAuthAccess for GitHub.copilot-chat. Then run Developer: Reload Window in {agent}.")
	noticeVSCodeNoChat = newNotice("Install GitHub Copilot Chat in {agent} to see magpie's models in its Chat.")
	noticeVSCodeOld    = newNotice("{agent}'s GitHub Copilot Chat {version} has neither a Custom Endpoint nor an OpenAI Compatible provider, so it can't list magpie's models.")
	noticeVSCodeOAI    = newNotice("magpie's models are in {agent}'s Chat model picker under magpie, from GitHub Copilot Chat {version}'s OpenAI Compatible provider, which it offers only while it is signed in to GitHub with a personal Copilot plan.")
)
