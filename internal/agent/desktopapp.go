package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
)

// An agent that is a desktop app as well as a CLI has the app's version shown
// beside the CLI's (#1334, laacmilan: with Codex's CLI and its app both
// installed, the Agents page showed only the CLI's version). Both read the
// same settings, so one row stands for both.

// desktopApp is the macOS bundle an agent's desktop app is known by: its
// bundle id, and the names it has been installed under. OpenAI's Codex app
// is ChatGPT.app (com.openai.codex) since it took ChatGPT's name; the bundle
// id tells it from the ChatGPT chat app (com.openai.chat) of the same name.
// win is its executable under %LOCALAPPDATA%\Programs, where a per-user
// installer puts it on Windows. store and publisher identify a Microsoft
// Store package by its package name and publisher id. guid is the key an
// electron-builder installer registers the app under for the current user
// (HKCU\Software\<guid>'s InstallLocation, and its Uninstall entry's
// DisplayVersion): its installer lets the user pick another folder. more
// are the app's other builds, each under a bundle id of its own (CodeBuddy's
// China build beside its international one). linux is the folder the
// vendor's own Linux packages install the app to; onLinux finds an app
// others package for Linux (linuxApp).
type desktopApp struct {
	id        string
	names     []string
	win       string
	store     string
	publisher string
	guid      string
	linux     string
	onLinux   *linuxApp
	more      []desktopApp
}

var desktopApps = map[string]desktopApp{
	// On Linux OpenAI's chatgpt package (a deb, an rpm and an Arch
	// package, in preview since 08-11) installs the app to /usr/lib/chatgpt:
	// ChatGPT, with its codex in resources and its version in
	// resources/linux-package-metadata.json (26.1027.30824's deb, seen in
	// the WSL distro magpie-desktop). Its app-server reads ~/.codex, as the
	// CLI does (pota on Discord: no account switch for it on Linux).
	"codex": {id: "com.openai.codex", names: []string{"ChatGPT.app", "Codex.app"}, store: "OpenAI.Codex", publisher: "2p2nqsd0c76g0", linux: "/usr/lib/chatgpt"},
	// DeepSeek Harness Desktop (0.2.0-rc.2's DeepSeek Harness.app,
	// com.deepseek.dsh) reads ~/.dsh as the CLI does — its own profile,
	// profiles/desktop, beside the CLI's — and needs no CLI: its terminal
	// command is optional (star on Discord: 未找到 CLI with the desktop app
	// in use). On Windows its installer (electron-builder's per-user NSIS,
	// productName DeepSeek Harness) puts it under %LOCALAPPDATA%\Programs
	// by default; the folder can be changed ("Choose an empty folder or the
	// registered DeepSeek Harness installation folder"), and the installer
	// registers it under HKCU\Software\1bf39983-… (#2279, read from
	// 0.2.0-rc.2's dsh-latest-windows-x64.exe installer script). DeepSeek
	// publishes no Linux build; its electron-builder config (0.2.2-rc.1)
	// has an AppImage target, productName DeepSeek Harness, executable
	// deepseek-harness and the dsh:// scheme. The AUR's
	// deepseek-harness-desktop builds it into
	// /usr/lib/deepseek-harness-desktop, with a deepseek-harness-desktop
	// script in /usr/bin and deepseek-ai-dsh-desktop.desktop (xianhongtao
	// on Discord: ~/.dsh shown as "CLI not found" and wired as the web
	// profile with that app in use); deepseek-harness-desktop-bin unpacks a
	// .deb of it into /opt/DeepSeek Harness, electron-builder's folder.
	"dsh": {id: "com.deepseek.dsh", names: []string{"DeepSeek Harness.app"}, win: filepath.Join("DeepSeek Harness", "DeepSeek Harness.exe"), guid: "1bf39983-50d0-5fe0-9ef4-cece76f67c5e",
		onLinux: &linuxApp{dirs: []string{"/usr/lib/deepseek-harness-desktop", "/opt/DeepSeek Harness"}, exe: "deepseek-harness", name: "DeepSeek Harness", scheme: "dsh"}},
	// DS Bot's desktop app (app/electron-builder.yml, 0.3.0: appId
	// io.github.fei2411.dsbot, productName DS Bot, DS Bot.app; DeepSeek
	// Bot, its name before) runs a DSH of its own on its data folder
	// (dsbot.go) and needs no CLI. On Windows its per-user NSIS installer
	// puts it under %LOCALAPPDATA%\Programs by default, lets the folder be
	// changed, and registers it under the guid its config names.
	"ds-bot": {id: "io.github.fei2411.dsbot", names: []string{"DS Bot.app", "DeepSeek Bot.app"}, win: filepath.Join("DS Bot", "DS Bot.exe"), guid: "32c46dfc-ed78-5d41-85a4-a3c7f083f2e0"},
	// PenguinHarness's desktop app (packages/desktop's electron-builder.yml,
	// desktop 0.2.17: appId com.prismshadow.penguinharness, productName
	// PenguinHarness) runs its own server on the CLI's data root,
	// ~/.penguin/data (app-identity.ts: "the release profile shares the
	// CLI's root by design"), and needs no CLI: it links
	// /usr/local/bin/penguin only when the user lets it (cli-install.ts),
	// and with no such link magpie took ~/.penguin for a leftover
	// (rankyurky_80676 on Discord: the app not detected). On Windows its
	// assisted NSIS installer puts it under %LOCALAPPDATA%\Programs by
	// default and lets the folder be changed; guid is the key
	// electron-builder registers it under, the UUID v5 of the appId in its
	// namespace, as dsh's above is of com.deepseek.dsh. Linux's deb links
	// /usr/bin/penguin, so Bin finds it there.
	// mu's desktop app (desktop/packages/desktop/electron-builder.yml,
	// qybaihe/mu b71cd18: appId io.github.qybaihe.mu, productName and
	// executable mu) carries mu-agent itself (resources/harness) and runs
	// it on the CLI's agent folder (MU_AGENT_DIR, kyrn/naming.ts), so it
	// needs no CLI. Windows' assisted NSIS installer and its guid as
	// penguin's below; Linux's deb puts it in /opt/mu (v0.3.9's
	// mu-0.3.9-linux-amd64.deb), whose /usr/bin/mu link is no mu-agent
	// (notMuAgent).
	"mu": {id: "io.github.qybaihe.mu", names: []string{"mu.app"}, win: filepath.Join("mu", "mu.exe"), guid: "8be59721-9051-5f2b-8372-96932fdbf5da",
		onLinux: &linuxApp{dirs: []string{"/opt/mu"}, exe: "mu", name: "mu", scheme: "mu"}},
	// Craft Agents (apps/electron/electron-builder.yml, 0.15.8: appId
	// com.lukilabs.craft-agent, productName Craft Agents) reads its config
	// folder on its own (craftagents.go). Its NSIS installer is one-click
	// and per user, so electron-builder names the folder under
	// %LOCALAPPDATA%\Programs after the package, @craft-agent/electron
	// with its "/" taken out (getWindowsInstallationDirName; the README's
	// own path); guid is the UUID v5 of the appId, as dsh's above.
	"craft-agents": {id: "com.lukilabs.craft-agent", names: []string{"Craft Agents.app"}, win: filepath.Join("@craft-agentelectron", "Craft Agents.exe"), guid: "61dc82ee-e3b9-557b-98c4-20b9178a0f78"},
	"penguin":      {id: "com.prismshadow.penguinharness", names: []string{"PenguinHarness.app"}, win: filepath.Join("PenguinHarness", "PenguinHarness.exe"), guid: "8bb9acb7-2bdc-596b-9669-7b014ab1c47f"},
	// The CodeBuddy IDE (Tencent's VS Code fork, 4.12.2: CodeBuddy.app,
	// com.tencent.codebuddy, from codebuddy.ai; CodeBuddy CN.app,
	// com.tencent.codebuddycn, from codebuddy.cn) reads the CodeBuddy Code
	// CLI's ~/.codebuddy/models.json for models of one's own (codebuddy.go)
	// and needs no CLI. Its Windows install folder isn't known here, so on
	// Windows only the rule that a desktop app's folder is never gone holds.
	// Antigravity's app (antigravity.go), not Antigravity IDE
	// (com.google.antigravity-ide). Its Windows and Linux installs aren't
	// known here: there the file its backend writes finds it.
	"antigravity": {id: "com.google.antigravity", names: []string{"Antigravity.app"}},
	// MiniMax Code Desktop (3.1.10: MiniMax Code.app, com.minimax.agent.cn
	// from filecdn.minimax.chat, com.minimax.agent from file.cdn.minimax.io;
	// app-identity.js) runs the mcode runtime on ~/.minimax/config.yaml, as
	// the CLI does, and needs no CLI. Its Windows installer is
	// electron-builder's NSIS ("MiniMax Code Setup 3.1.10.exe"), whose
	// per-user default is %LOCALAPPDATA%\Programs\<productName>; that
	// folder is the default's, not read from an install.
	"minimax-code": {id: "com.minimax.agent.cn", names: []string{"MiniMax Code.app"}, win: filepath.Join("MiniMax Code", "MiniMax Code.exe"),
		more: []desktopApp{{id: "com.minimax.agent", names: []string{"MiniMax Code.app"}}}},
	"codebuddy": {id: "com.tencent.codebuddy", names: []string{"CodeBuddy.app"},
		more: []desktopApp{{id: "com.tencent.codebuddycn", names: []string{"CodeBuddy CN.app"}}}},
}

// appFolders are where apps are installed; a var so tests can point it
// elsewhere: /Applications and ~/Applications on macOS, %LOCALAPPDATA%\Programs
// on Windows. Store packages are read from the current user's AppX registry.
var appFolders = func() []string {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return proc.SystemDirs("/Applications")
		}
		return macAppFolders(home)
	case "windows":
		if local := appdir.Getenv("LOCALAPPDATA"); local != "" {
			return []string{filepath.Join(local, "Programs")}
		}
	}
	return nil
}

// macAppFolders are the folders a Mac keeps apps in: /Applications and
// home's Applications. /Applications goes through proc.SystemDirs, so in a
// test binary testenv isolated it lies under the test's home: tests read
// the developer's own /Applications, and on a Mac with DeepSeek Harness.app
// installed dsh's CLI was never missing (#1823, mdwsk88). Every reader of a
// Mac's installed apps takes its folders from here.
func macAppFolders(home string) []string {
	return append(proc.SystemDirs("/Applications"), filepath.Join(home, "Applications"))
}

// windowsStorePackages lists the package full names registered for the
// current user on Windows, nil elsewhere; a var so a test can give its own.
// In a test binary testenv isolated it is none: the AppX registry is the
// machine's own, which no home or PATH a test stages takes away, so on a
// Windows machine with the Codex app installed every package's tests saw
// Codex's app and codex's CLI was never gone there (#2254, ggbdpq). Only
// agent's own TestMain stood in for it before.
var windowsStorePackages = storePackages

func storePackages() []string {
	if proc.Isolated() {
		return nil
	}
	return installedWindowsStorePackages()
}

// windowsRegisteredApp is the folder and version an installer registered
// the app with guid under on Windows (registeredIn), "" elsewhere or when
// none is; a var so tests never read the machine's own registry.
var windowsRegisteredApp = registeredWindowsApp

// registryRead is a string value of a key under a Windows registry hive
// ("HKCU" or "HKLM"), "" when there is none.
type registryRead func(hive, key, name string) string

// registeredIn is the folder and version an electron-builder installer
// registered the app with guid under, read through read. It writes
// Software\<guid>'s InstallLocation and an Uninstall\<guid> entry, under
// HKCU for a per-user install and HKLM for one for all users; that entry's
// InstallLocation, else the folder of its DisplayIcon (the app's exe,
// ",0" after it) or UninstallString, is the folder when the first is
// missing. DeepSeek Harness Desktop installed to D:\tools\dsh was found by
// neither %LOCALAPPDATA%\Programs nor HKCU's InstallLocation alone (#2325,
// cjyrainbow).
func registeredIn(guid string, read registryRead) (dir, version string) {
	uninstall := `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + guid
	for _, hive := range []string{"HKCU", "HKLM"} {
		at := read(hive, `Software\`+guid, "InstallLocation")
		if at == "" {
			at = read(hive, uninstall, "InstallLocation")
		}
		for _, name := range []string{"DisplayIcon", "UninstallString"} {
			if at != "" {
				break
			}
			at = registeredExeDir(read(hive, uninstall, name))
		}
		if at = strings.Trim(strings.TrimSpace(at), `"`); at != "" {
			return at, read(hive, uninstall, "DisplayVersion")
		}
	}
	return "", ""
}

// registeredExeDir is the folder of the program a registry value names
// ("D:\tools\dsh\DeepSeek Harness.exe,0", "\"…\Uninstall X.exe\" /currentuser"),
// "" when it names none.
func registeredExeDir(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) {
		if end := strings.Index(v[1:], `"`); end >= 0 {
			v = v[1 : 1+end]
		}
	} else if i := strings.LastIndex(strings.ToLower(v), ".exe"); i >= 0 {
		v = v[:i+len(".exe")]
	}
	if !strings.HasSuffix(strings.ToLower(v), ".exe") {
		return ""
	}
	i := strings.LastIndexAny(v, `\/`)
	if i <= 0 {
		return ""
	}
	return v[:i]
}

var (
	plistIDRe      = lazyre.New(`<key>CFBundleIdentifier</key>\s*<string>([^<]+)</string>`)
	plistVersionRe = lazyre.New(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)
)

// macBundle reports whether app's bundle is in one of appFolders, under one
// of its names: with its bundle id, or an Info.plist that isn't XML (a
// binary one says nothing either way, and the name is the app's own).
func macBundle(app desktopApp) bool {
	for _, bundle := range macBundles(app) {
		info, ok := bundleInfo(bundle)
		if ok && (!info.hasID || info.id == app.id) {
			return true
		}
	}
	return false
}

// plistInfo is what an Info.plist says of its bundle: its id and version,
// each when it says one.
type plistInfo struct {
	id, version       string
	hasID, hasVersion bool
}

// bundleInfo reads bundle's Info.plist, false when there is none. The
// Agents page reads every desktop app's on every poll, so what a file said
// is kept while its size and modification time stay, and a time too recent
// to tell two writes apart is read again (racyDir).
func bundleInfo(bundle string) (plistInfo, bool) {
	p := filepath.Join(bundle, "Contents", "Info.plist")
	mod, size, ok := statStamp(p)
	if !ok {
		return plistInfo{}, false
	}
	plists.Lock()
	k, ok := plists.m[p]
	plists.Unlock()
	if ok && k.mod.Equal(mod) && k.size == size && time.Since(mod) > racyDir {
		return k.info, true
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return plistInfo{}, false
	}
	var info plistInfo
	if id := plistIDRe.FindSubmatch(b); id != nil {
		info.id, info.hasID = strings.TrimSpace(string(id[1])), true
	}
	if v := plistVersionRe.FindSubmatch(b); v != nil {
		info.version, info.hasVersion = strings.TrimSpace(string(v[1])), true
	}
	plists.Lock()
	if plists.m == nil {
		plists.m = map[string]keptPlist{}
	}
	plists.m[p] = keptPlist{mod: mod, size: size, info: info}
	plists.Unlock()
	return info, true
}

var plists struct {
	sync.Mutex
	m map[string]keptPlist
}

type keptPlist struct {
	mod  time.Time
	size int64
	info plistInfo
}

// macBundles are where app's bundle may be on a Mac, the likeliest first:
// under one of its names in appFolders, or in a folder of theirs one level
// down (sungoku keeps DeepSeek Harness.app in /Applications/AI, and magpie
// 0.1.1253 said 未找到 CLI, #2325), then wherever Spotlight knows its bundle
// id to be (spotlightApps), as Zed's is found (zedAppPath). A caller still
// reads each one's Info.plist.
func macBundles(app desktopApp) []string {
	out := inAppFolders(appFolders(), app.names...)
	if app.id != "" {
		for _, p := range spotlightApps(app.id) {
			if strings.HasSuffix(p, ".app") && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// inAppFolders are the paths names may have in dirs, a Mac's app folders:
// in each, then in each folder of its one level down that is no bundle
// itself (/Applications/AI/DeepSeek Harness.app, #2325).
func inAppFolders(dirs []string, names ...string) []string {
	var out []string
	for _, dir := range dirs {
		for _, name := range names {
			out = append(out, filepath.Join(dir, name))
		}
		for _, sub := range folderSubs(dir) {
			for _, name := range names {
				out = append(out, filepath.Join(sub, name))
			}
		}
	}
	return out
}

// folderSubs are dir's folders that are no bundle, the ones inAppFolders
// looks one level down in. Every desktop app asks on every Agents read, and
// /Applications holds hundreds of entries, so the list is kept while dir's
// modification time stays: adding, removing or renaming an entry changes
// it. A time too recent to tell two changes apart is not trusted, as git
// treats a racily clean index.
func folderSubs(dir string) []string {
	mod, _, ok := statStamp(dir)
	if !ok {
		return nil
	}
	appSubs.Lock()
	k, ok := appSubs.m[dir]
	appSubs.Unlock()
	if ok && k.mod.Equal(mod) && time.Since(mod) > racyDir {
		return k.subs
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var subs []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".app") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		subs = append(subs, filepath.Join(dir, e.Name()))
	}
	appSubs.Lock()
	if appSubs.m == nil {
		appSubs.m = map[string]keptSubs{}
	}
	appSubs.m[dir] = keptSubs{mod: mod, subs: subs}
	appSubs.Unlock()
	return subs
}

// racyDir is how recent a folder's change may be for folderSubs to list it
// again rather than trust what it kept.
const racyDir = 2 * time.Second

var appSubs struct {
	sync.Mutex
	m map[string]keptSubs
}

type keptSubs struct {
	mod  time.Time
	subs []string
}

// spotlightApps are the bundles Spotlight has with bundle id id, on a Mac;
// none elsewhere. A var so tests never ask this machine's Spotlight.
var spotlightApps = spotlightBundles

// spotlight keeps each bundle id's answer for spotlightFresh: the Agents
// page asks whether an app is installed on every read, and an app that
// isn't anywhere would ask mdfind each time.
var spotlight struct {
	sync.Mutex
	m map[string]spotlightAnswer
}

type spotlightAnswer struct {
	paths []string
	at    time.Time
}

const spotlightFresh = 5 * time.Minute

// spotlightSandbox is testenv.Marker, which agent can't import: set in a
// test binary testenv isolated, where this Mac's Spotlight would find the
// developer's own apps (#1823) from every package that asks for one.
const spotlightSandbox = "MAGPIE_TEST_SANDBOX"

func spotlightBundles(id string) []string {
	if runtime.GOOS != "darwin" || os.Getenv(spotlightSandbox) != "" {
		return nil
	}
	spotlight.Lock()
	defer spotlight.Unlock()
	if a, ok := spotlight.m[id]; ok && time.Since(a.at) < spotlightFresh {
		return a.paths
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var paths []string
	out, err := proc.CommandContext(ctx, "mdfind", "kMDItemCFBundleIdentifier == '"+id+"'").Output()
	if err == nil {
		for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			// an app dragged to the Trash, as a Mac app is uninstalled,
			// is still in Spotlight: it isn't installed
			if p = strings.TrimSpace(p); p != "" && !inTrash(p) {
				paths = append(paths, p)
			}
		}
	}
	if spotlight.m == nil {
		spotlight.m = map[string]spotlightAnswer{}
	}
	spotlight.m[id] = spotlightAnswer{paths: paths, at: time.Now()}
	return paths
}

// inTrash reports whether p is in a Mac's Trash: the user's ~/.Trash, or
// a volume's .Trashes.
func inTrash(p string) bool {
	return strings.Contains(p, "/.Trash/") || strings.Contains(p, "/.Trashes/")
}

// AppVersion is the version of the agent's desktop app installed here, ""
// for an agent without one magpie knows, one not installed, or an Info.plist
// that doesn't say (a binary one isn't read).
func (a *Agent) AppVersion() string {
	app, ok := desktopApps[a.ID]
	if !ok || a.WSL != "" {
		return ""
	}
	for _, b := range app.builds() {
		if v := b.version(); v != "" {
			return v
		}
	}
	return ""
}

// builds are app and its other builds (more), the first one first.
func (app desktopApp) builds() []desktopApp {
	return append([]desktopApp{app}, app.more...)
}

// version is the version of app installed here, "" when it isn't or doesn't
// say (see AppVersion).
func (app desktopApp) version() string {
	for _, bundle := range macBundles(app) {
		info, ok := bundleInfo(bundle)
		if !ok || !info.hasID || !info.hasVersion || info.id != app.id {
			continue
		}
		return info.version
	}
	if app.store != "" {
		if v := storeAppVersion(windowsStorePackages(), app.store, app.publisher); v != "" {
			return v
		}
	}
	if dir, v := app.registered(); dir != "" {
		return v
	}
	return app.linuxVersion()
}

// linuxDir is the folder the app's Linux package installed it to, ""
// when it isn't there. Through proc.SystemDirs, so a test binary testenv
// isolated never reads the machine's own /usr/lib.
func (app desktopApp) linuxDir() string {
	if app.linux == "" {
		return ""
	}
	dir := proc.SystemDirs(app.linux)[0]
	if !isFile(filepath.Join(dir, "resources", "linux-package-metadata.json")) {
		return ""
	}
	return dir
}

var linuxPackageVersionRe = lazyre.New(`"version"\s*:\s*"([^"]+)"`)

// linuxVersion is the version of the app's Linux package, from its
// resources/linux-package-metadata.json ({"codexAppBrand":"chatgpt",
// "codexBuildFlavor":"prod","version":"26.1027.30824"}); "" when it isn't
// installed or doesn't say.
func (app desktopApp) linuxVersion() string {
	dir := app.linuxDir()
	if dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "resources", "linux-package-metadata.json"))
	if err != nil {
		return ""
	}
	if m := linuxPackageVersionRe.FindSubmatch(b); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

// registered is the folder the app's installer registered, where its
// executable is, and the version it registered; "" when there is none.
func (app desktopApp) registered() (dir, version string) {
	if app.guid == "" || app.win == "" {
		return "", ""
	}
	dir, version = windowsRegisteredApp(app.guid)
	if dir == "" || !isFile(filepath.Join(dir, filepath.Base(app.win))) {
		return "", ""
	}
	return dir, strings.TrimSpace(version)
}

// storeAppVersion finds the newest version of an app among Microsoft Store
// package full names (name_version_architecture_resourceid_publisher). Only
// the main package counts, the one with no resource id: the AppX registry
// keeps a resource package (…_neutral_split.scale-125_…) of a version the
// main package has moved past, so one is no sign the app is installed.
func storeAppVersion(packages []string, name, publisher string) string {
	prefix, suffix := name+"_", "_"+publisher
	var newest [4]uint64
	found := ""
	for _, pkg := range packages {
		if !strings.HasPrefix(pkg, prefix) || !strings.HasSuffix(pkg, suffix) {
			continue
		}
		fields := strings.Split(strings.TrimSuffix(strings.TrimPrefix(pkg, prefix), suffix), "_")
		if len(fields) != 3 || fields[2] != "" {
			continue
		}
		parts := strings.Split(fields[0], ".")
		if len(parts) != len(newest) {
			continue
		}
		var version [4]uint64
		valid := true
		for i, part := range parts {
			var err error
			version[i], err = strconv.ParseUint(part, 10, 16)
			if err != nil {
				valid = false
				break
			}
		}
		if valid && (found == "" || slices.Compare(version[:], newest[:]) > 0) {
			newest, found = version, fields[0]
		}
	}
	return found
}

// HasApp reports whether the agent's desktop app is installed here: its
// bundle on macOS (AppVersion), its executable on Windows: under
// %LOCALAPPDATA%\Programs, or in the folder its installer registered; on
// Linux the folder its package installed (desktopApp.linux), or one a
// packager's install or an AppImage's desktop entry names (linuxApp).
func (a *Agent) HasApp() bool {
	app, ok := desktopApps[a.ID]
	return ok && a.WSL == "" && slices.ContainsFunc(app.builds(), desktopApp.installed)
}

// installed reports whether app is installed on this machine (see HasApp).
func (app desktopApp) installed() bool {
	if app.version() != "" || app.linuxDir() != "" || app.onLinux.installed() {
		return true
	}
	if app.win == "" {
		return false
	}
	for _, dir := range appFolders() {
		if isFile(filepath.Join(dir, app.win)) {
			return true
		}
	}
	dir, _ := app.registered()
	return dir != ""
}
