# GUI app shell

The desktop app has a tray (menu bar) icon whose click drops a compact
panel, and a regular window. Both show one small web UI served from
embedded assets. The page talks to Go over a JSON API on the same handler,
with no binding generator and no bundler. `magpie web` serves the same page
to a browser for a machine with no desktop. Package `internal/gui` hosts all
three.

## Responsibilities and sources of truth

| Part | Responsibility | Source |
| --- | --- | --- |
| Native host | `Run` starts the Wails v3 app: the tray icon, the panel, the main window, single-instance handover, `magpie://` import links and open at login. `host` implements `Windows` for the API: show and hide, open URLs and folders, copy to the clipboard, fit and tint the panel, and set the text size. A copy on Linux sets PRIMARY before CLIPBOARD, since GTK 3 serves CLIPBOARD empty when PRIMARY is set after it on a compositor with no primary selection (KDE Plasma with middle-click paste off, #1881); on Wayland the copy is read back through the compositor, and one that doesn't read back whole answers `/api/copy` with 501, so the page tries its clipboard API, then the copy command, then says the copy failed with the text to copy by hand (`copy-fallback.test.cjs`). On Windows, Wails reads the screens again on each display change (`WM_DISPLAYCHANGE`, and `WM_SETTINGCHANGE` for the work area) through `w32.GetAllScreens`, which makes a new Win32 callback each time; Go never frees one and throws past about 2000, killing the app and its gateway after hours of display changes (1111_13879 on Discord). `screensWndProc`, the app's `WndProcInterceptor`, takes those messages on Wails' main-thread window first and reads the screens with one callback made once (`allScreens`), then hands them to `Screen.LayoutScreens` as Wails would; see `TestScreensReadFarPastTheCallbackLimit`, `TestDisplayChangesAreReadOnWailsWindowOnly` (Windows) and `TestWailsScreensAreReadOnTheMainThreadWindow`, which fails when a Wails upgrade changes what that relies on. | [`app.go`](../../internal/gui/app.go), [`screens_windows.go`](../../internal/gui/screens_windows.go), [`clipboard_gtk3.go`](../../internal/gui/clipboard_gtk3.go), [`clipboard_gtk3.h`](../../internal/gui/clipboard_gtk3.h), [`mainwindow.go`](../../internal/gui/mainwindow.go), [`traymenu.go`](../../internal/gui/traymenu.go) |
| Backend | `startBackend` starts the gateway this process serves, unless another magpie already serves it. In that case this process only shows the other one's status (`gw` is nil), and `watchGateway` looks at it again every 15 s (`gatewayWatch`). A magpie of another version there (`otherServes` in `gateway_older.go`) is told the page as `gatewayOther` in `/api/state` and said over the Agents list (`gatewaySkewLine` in `plugins/gateway/client.js`), in the panel and the window: pi got 404 for a model the newer app had written it while an older copy of the app kept the gateway, and only the Gateway page said so (matrix0487 on Discord). An older released copy of the app (`serves` app or tray, with a pid) is this app's own predecessor: once its `/v1/magpie/concurrency` says nothing is in flight (`gateway.ServedBusy`; unknown is never idle) the app quits it as the Gateway page's button does (`quitMagpieOnPort`, a magpie only) and serves the gateway, so no stream is cut; at most three times a run (`replaceMost`), and a quit that fails is left to the button. Any other (magpie serve or the service, `magpie web`, a TUI, a newer magpie, a build of its own) is left running, with what to do; a dev build of this magpie says nothing. A request that starts between the idle read and the quit gets the older app's short drain. See `TestAppReplacesItsOlderGatewayOnceIdle`, `TestGatewaySkewOfAnotherIsSaidNotQuit` and `gateway-skew.test.cjs`. `magpie serve` (the service's, launchd's `com.yetone.magpie.serve` or systemd's) does the same beside a magpie serving or another program holding the port: before it moves or starts anything (`startUp`), it waits, looking every 15 s, and serves once the port is free (`awaitGatewayFree` in [`serve_wait.go`](../../internal/cli/serve_wait.go); `--web` excepted, which shows the other one). Before, it ran the start-up moves, the plugin host and every account's model fetch, failed to bind, exited 1 and was started again by the service, 6,035 times in two days with a 279MB serve.log and requests 504 behind it (#4938, `TestServeWaitsForTheGatewayBeforeStartingUp`). `stopServing` drains requests in flight. A restart to update can hand the listening socket to the new version instead (see [A restart to update hands the gateway over](#a-restart-to-update-hands-the-gateway-over)). | [`backend.go`](../../internal/gui/backend.go), [`gateway_older.go`](../../internal/gui/gateway_older.go) |
| API and assets | `Handler` serves `assets/` (embedded with `//go:embed assets`) and the `/api/*` routes, such as `/api/state`, `/api/set`, `/api/agents/{action}/{id}` and `/api/agents/restart/{id}` (a desktop app's Restart, after the row's confirm). `held` lets one API request's reads share one catalog build (`provider.Hold`), and drops it after any write. `revalidated` and `versionedPage` name each script by its content hash, so a cache never serves an old `app.js` under a new page; a file asked for under its current hash (`?v=`) is cached for good (`immutable`), anything else is revalidated by its ETag. | [`api.go`](../../internal/gui/api.go) |
| Settings from the terminal | `POST /api/settings` saves through `settingsapi.Apply` (`internal/settingsapi`, outside the GUI so the `nogui` build has it): the carry of keys a page leaves out (`Merge`), the vision, image and searcher checks, `settings.Save`'s own checks, and `catalog.Touched`. `magpie settings list\|get\|set\|reset` (#4745) builds the same body with `settingsapi.Body` (the whole file with one key changed, so a key the save keeps when it isn't sent is set even to off) and calls the same `Apply`; a refused value writes nothing. `settingsapi.Keys` names what the save writes; `TestKeysAreWhatTheSaveWrites` fails when a field of `settings.Settings` is saved without it, `TestSettingsCLISetsWhatTheWindowSaves` when a key the page's `savePrefs` sends isn't one, and `TestEveryRouteHasACommandOrIsNamed` when an `/api` route has neither a command nor a reason in `notYet` (`internal/cli/settings_parity_test.go`). The running app's in-process follow-ups (fonts, Dock, tray menu, alerts, language, update check) run only from the window's save; a change from the terminal is read from the file. | [`internal/settingsapi/settingsapi.go`](../../internal/settingsapi/settingsapi.go), [`settings_cli.go`](../../internal/cli/settings_cli.go) |
| Browser mode | `StartWeb` serves the page and the gateway for `magpie web`. The browser signs in with a run key in a link. Added to a phone's home screen the page opens as an app of its own, with no browser bars (ninja_s6114 on Discord): `index.html` names `manifest.json` (display standalone), an apple-touch-icon and the iOS meta tags, the status bar keeps its own ground (`default`), and its theme colours match only `(display-mode: standalone)`, so a browser's tab and the app's windows take none. `webGuard` serves those three files (`webAppFiles`: the manifest, `icons/apple-touch-icon.png`, `icons/icon-512.png`) without the key, to GET and HEAD at those exact paths alone, as a home screen may ask without the cookie; see `TestWebAppAddsToHomeScreen` and `web-app-standalone.test.cjs`. Gateway mode shows Providers, Gateway, Routing, Usage, Sessions, Plugins and Settings on a machine that is only a gateway. `webHandler` gzips the page's text files and the API's JSON for a browser that takes gzip (`webCompressed`, with a `-gzip` ETag that still answers 304; never an event stream, which gzip would hold back until its buffer fills); the app's own windows read the page in-process and get it uncompressed. The page's port answers the gateway's client routes too (`onePort`, `gateway.ClientPath`: `/v1/…`, `/v1beta/…`, `/mcp/…`, the Codex backend path and the routes without `/v1`; never `/api/*` or `/_magpie/*`, never a path that isn't clean), so one reverse-proxy location serves the page and the agents (zgcwkj on Discord). They skip the page's key and gzip and go to the gateway's own guards, always as a request from another machine (`gateway.FromPagePort`, the `X-Magpie-Page-Port` mark: `local` is false for it, from loopback too and under `MAGPIE_TRUST_PROXY`, so magpie must be shared and the request carry an enabled gateway key), since a raw TCP forward or a proxy that adds no forwarding header reaches this port from loopback looking like an agent here (reported privately by Coffemon); agents here use the gateway's own port. `Server.Front` (lanGuard over Handler) serves them in process when this process serves the gateway, or `toGateway`, a reverse proxy to `gateway.URL()`, when another magpie does; it keeps the Host, the forwarding headers and the mark, adds X-Forwarded-For for a peer that isn't loopback, and the peer's loopback address as X-Forwarded-For when none came, so a gateway of an older magpie that doesn't know the mark still asks for a key. `lanGuard` moves the mark into the request's context and takes the header off, so it never goes upstream. The page itself calls no client route. See `TestWebOnePortServedHere`, `TestWebOnePortServedElsewhere`, `TestWebOnePortAgentsHereUseTheGatewayPort`, `TestPagePortMarkIsNeverLocal` and `TestClientPathCoversHandler`. | [`web.go`](../../internal/gui/web.go), [`gatewaymode.go`](../../internal/gui/gatewaymode.go), [`oneport.go`](../../internal/gateway/oneport.go) |
| The app's page in a browser | Settings › Network and sharing › Browser access (`renderWebPage` in `plugins/gateway/settings.js`, drawn with the Web pages rows) has the app serve its own page to a browser while it runs, so a tunnel (Cloudflare Tunnel, SSH) or a computer on the network can reach it without `magpie web` kept running in a terminal (jiakun_zhao on X). Off, This computer (`127.0.0.1`, which a tunnel's service points at) or Local network (every address; a "Not encrypted" row without `--tls-cert`). It is `magpie web`'s page and guard, not a second kind: `startAppPage` (from `Run`, after `startBackend`) serves `webServed` over a `Handler` of its own, whose `Windows` is `appWebHost` (a browser tab for `isWeb`, but not `ownWeb`, so the app's windows aren't taken for `magpie web`'s: `webPage` and gateway mode stay the app's; quitting quits the app, and a restart to update from it brings back the windows the app has up, `restartFrom`). The key is always asked for: no mode serves without it. It lives with the mode and port in `web-page.json` beside settings.json (0600, never in settings.json, so sync and backup don't carry it), is made the first time the page is turned on, kept across restarts (a browser's cookie stays good), and replaced by New key, which turns away every browser signed in with the old one (the guard is swapped atomically, `appPage.cur`). A file that can't be read, or names no known mode, is off and says why; Settings rewrites it only given a whole choice. A port another program holds (`magpie web`; the magpie before a restart to update) is said and tried again every 4 s (`appPageRetry`); the port must be 1024 up and not the gateway's. `GET/POST /api/web-page` are on the app's own windows' API only, never a browser's, so a browser on the page can neither read the key it came in without nor turn the page off under itself. `magpie web` on the port the app serves prints the app's link and opens it rather than starting a second (`gui.AppPage`, `samePort`). Sessions are warmed once a process however many `Handler`s there are (`warmOnce`). See `TestAppPageServesOnlyWithItsKey`, `TestAppPageWaitsForABusyPort`, `TestAppPageUnreadFileServesNothing`, `TestAppPageTurnsDownBadChoices`, `TestAppPageBrowserIsATab` and `browser-access.test.cjs`. | [`apppage.go`](../../internal/gui/apppage.go), [`web_cli.go`](../../internal/cli/web_cli.go) |
| The page | `app.js` keeps one state object per view and renders it into a list, with no framework. The views have their own files: `plugins.js` and `context.js`, and those a plugin adds under `assets/plugins/<id>/` (the Gateway, Sessions, Library, Routing and Usage pages, see [plugin runtime](plugin-runtime.md)) (Usage's Context tab, and the context window card the Routing page draws under a request's story, fed by `/api/context` in `context.go`, which scores and tags each agent; a client whose User-Agent names only an HTTP library, a runtime or an API SDK (`usage.IsLibrary`: curl, Bun, python-httpx, Go-http-client, OpenAI/Python), or nothing (`other`), is no agent and goes on one card, `scripts`, named Scripts with its libraries listed, read from each route's kept agent so older history folds too, while the Usage page's Agents list keeps each by its own name (theforeveriris on X, `context_scripts_test.go`, `context-scripts.test.cjs`); clicking a part, a cell or an item opens its text from `/api/context/text`, the last 10 requests' masked bodies in memory, else the request archive's copy masked the same way, with a 404 saying why there is none (`archive`: off, missing, cut, unread; `context_archive.go`), and its Reply the reply from `/api/context/reply` (`openCtxText`, in `ctxtext.js`, `context-text.test.cjs`), rendered 100 items and 20000 characters at a time; its "Tuned for you" cards are fed by `/api/tune` in `tune.go`, see [agent wiring](agent-wiring.md)). Strings go through `t()` with translations in `i18n.js`, and a plugin's own in its `i18n.js` (zh, zh-TW, ja and de; the English text is the key and the fallback). zh-TW is Traditional Chinese with Taiwan's words; a system language of zh-TW, zh-HK, zh-MO or any zh-Hant picks it, and the tray menu and notifications follow (`trayLang`). Dropdowns use `openProtoMenu`. Every app menu (`openProtoMenu`, `openRowMenu`, `openSessCombo`) is placed by `placeMenu`: under its anchor, or over it when the window runs out below; one that fits neither way takes the larger room, its height held to it, and scrolls inside itself, and a scroll inside a menu doesn't close it (#1437, `removed-groups-menu-scroll.test.cjs`). The highest any popover goes is `popTop`: 8px under `header.top` on a Mac window, where the traffic lights are drawn over the page, and 8px from the top elsewhere; `placeMenu`, the field picker's `placePop` (which also holds `#pop`'s height to that room) and an agent's model list (`openAgentModels`) all use it (#1573, `popover-mac-chrome.test.cjs`). Scrolling is done only by `scrollOnPurpose(e)` with the reader's event. | [`assets/app.js`](../../internal/gui/assets/app.js), [`assets/i18n.js`](../../internal/gui/assets/i18n.js) |
| Window navigation | In the window from 1024 points wide (`SIDE_AT`, the same width as app.css's `side-nav` rules), `layoutNav` moves the brand, the pages (`#nav`) and Settings into the sidebar (`#side`): the pages in four groups (Work, Models, Insights, Extend; a group none of whose pages shows, as in gateway mode, leaves out its name), the gateway's address at its foot (`sideGwRead`, read from `/api/providers` only while there is a sidebar, or drawn at once from boot.js's `sideGateway`; on a page other machines open, `magpie web`, it is `MAGPIE_PUBLIC_URL` while they are let in, as the Gateway page's address and About's Gateway URL are, noahhrreion on Discord; a port applied in Settings redraws it), and the header a breadcrumb (`renderCrumb`). A page whose head holds only its tools (Library, Plugins, Settings) has them in the title bar after the breadcrumb, `tools` setting the room between it and the window's own buttons. When they don't fit there the tools show their icons alone (`tools-tight`; each keeps its name as `aria-label` and in its tooltip), then the breadcrumb drops its leading parts, and last the page's name is cut at its end, so no tool lies over the title (#2625, `title-bar-tools.test.cjs`). The open page's own tabs (`NAV_SUBS`: Usage, Library, Plugins, Settings) are listed under it as `.nav-s` rows (`role="tab"`, `data-of` the page tab's id) and the page's strip is hidden; a row's click clicks that tab at once, the selection (`.nav-hl`) having set off as the row was pressed (`navSubGlide`; back if the press leaves the row), and is the list's only shape: a row hovered or pressed has no ground of its own, and a `MutationObserver` draws the list again when the strip changes. A page's tabs share its one scroll, so a row that picks another tab opens it at its top (`scrollOnPurpose` with the row's click); the tab already open, picked again, leaves the page where it is (`sidebar-tab-top.test.cjs`). The list opens and folds with an iOS-like spring (`navSubEnter`, `navSubLeave`; none under reduced motion), and a folding list is out of reach at once. The open page's own row is marked the same way, by one shape (`.nav-top-hl`, the rows' own ground given up to it, hovered or pressed too) that `navTopGo` moves to the row picked on a damped spring, as iOS moves a selection (`NAV_SPRING`, SwiftUI's response 0.42s and damping 0.84; `navTopStep` steps it each frame): it sets off as the row is pressed and comes back if the press leaves the row or the page isn't left, turns with the speed it has when the press changes its mind, and follows its row while the list above it folds, landing where the row ends up. It is put in place, not sent, when the window opens on a page or the rows move under it at rest (a `ResizeObserver`), is away while Settings is open (it has its own selection at the foot), comes back fading in on its row, jumps under reduced motion, and isn't drawn in the header's tabs, which have their `.thumb`. `#nav` is `position: relative` in the sidebar for it, so `.seg`'s centring `left` and `top` are reset. See `sidebar-glide.test.cjs`. Narrower, the page beside a sidebar would be narrower than Usage's requests table needs (768 points), so they go back into the header's row, as a Mac sidebar gives way in a narrow window. At every width the reader can pick the sidebar or the header's tabs from the title bar's button (`#sideToggle`, ⌃⌘S or Ctrl+Shift+S; `toggleSideNav`), kept in localStorage `magpie.navLayout` (`side` or `top`) and so laid out from the first frame; none picked, the sidebar comes from `SIDE_AT`. Picked in a narrow window the sidebar is narrower (184 points under 860), and magpie web on a phone keeps its own tab bar and has no button. The two layouts trade places in a view transition (`navMorph`, app.css `.nav-morph`): the sidebar slides, each page's tab flies to its place in the other, and the page slides as one (its old image dropped, so no line prints twice); without view transitions, under reduced motion, or in the Linux app the change is at once: WebKitGTK with compositing off (`WEBKIT_DISABLE_COMPOSITING_MODE`, which `webkit_linux.go` sets under Wayland) crashes in a view transition. Elsewhere a transition still going after `MORPH_MAX` (1.5s) is skipped, so a webview that never ends one can't leave the window on the old layout's picture. See `side-toggle.test.cjs`. A window made wider or narrower drops what a click held (`heldWidth`): the page laid out again isn't the click's doing. The reader's own place is kept instead for a second while the page is laid out again (`relaid`, a `ResizeObserver` on the open page's parts calling `backToReader`): crossing `SIDE_AT` the browser clamps the scroll to each layout it passes through, and the page ended short of its foot (#1672, `text-size.test.cjs`). See `sidebar.test.cjs`. A sidebar longer than a short window keeps no scroll bar beside its pages: the native bars of `.side-scroll` and `.side-foot` are hidden, and `sideBars` lays a thin bar over each box's edge that shows while it scrolls, fades about a second after it stops (kept while hovered or dragged), can be dragged or clicked to page, and is gone when the box fits, the same in WebKit, WebView2 and WebKitGTK. See `sidebar-overlay-scrollbar.test.cjs`. The window opens on the page the reader last left, as a Mac app comes back as it was (molvqingtai and lc on Discord): `show` keeps the page in localStorage `magpie.lastView` (which the app's webview keeps across restarts, as it keeps `magpie.navLayout`), and `keptView` opens it at start. A page asked for in the address (`?view=`, the tray's links, a reload) wins, Agents too; a kept name that is no page opens the first page, and one gateway mode leaves out opens Providers (`show`). Opening on it scrolls nothing. See `last-page.test.cjs`. | [`assets/app.js`](../../internal/gui/assets/app.js), [`assets/app.css`](../../internal/gui/assets/app.css) |
| Settings › Privacy | The page opens on what masking kept from vendors (`drawMaskObs` in app.js, fed by `/api/masking/summary`): the values masked over 7 or 30 days with a chart of the days, the kinds, what only the local model found, agents to providers, and the values masked most, each shown only while pointed at (`maskTip`, `/api/masking/value`, which says it is gone after a restart). Its records (`drawMaskRecs`, `/api/masking/records` and `/api/masking/record`) take the page's place, filtered by kind, finder, agent or provider, a day at a time, each opening on the text the vendor saw with its placeholders marked. `maskedBadge`, on the Gateway page's recent calls and the Routing page's log, opens a request's own record (`openMaskRecord`), in amber with the count sent as written when every value found was on Warn. Below the card, *What to mask*, *How it's found* (the rules, always on while anything is masked, and the local model on top of them), *When a value is found* (Mask, Warn or Block per kind, the secret kinds first and the personal ones behind *Show all kinds*, and the *Never mask* list; `renderMaskActions`, with *Never mask* and *Masked words* drawn a value a row by `wordList` in `plugins/redact/settings.js`, saved as the same list of strings the one comma-separated box saved, `redact-words-list.test.cjs`), *Try it* (`renderMaskTry`, the text as the vendor would get it from `/api/masking/preview`, kept in no storage) and *Sharing*. *Agents' session history* (`renderSessionReads`, read from `/api/sessions/reads` only while the Privacy part is shown) has *Agents found later* and a row per agent found, the choice the Sessions page asks first on a fresh install; see [gateway sessions](gateway-sessions.md#which-agents-sessions-magpie-reads). While anything is masked, *Instead of masking, send to* (`redactRouteRow` in `plugins/redact/settings.js`, the redact plugin's part of Settings, which wraps `renderRedact` so app.js carries none of it) picks, in the app menu, a model a request masking would change goes to as written: Off by default, and only the models `/api/settings` lists as `maskRouteModels` (every route of theirs unmasked, `gateway.MaskRoute`), saved with `POST /api/settings/redact-route` (`redact-route.test.cjs`). See [gateway routing](gateway-routing.md) for what is recorded. | [`assets/app.js`](../../internal/gui/assets/app.js), [`masking.go`](../../internal/gui/masking.go) |
| Built-in plugins | Plugins › Built-in, the page's first tab (`drawBuiltin` in `plugins.js`, rows by `renderBuiltins` in `app.js`), lists magpie's built-in plugins, each with a name, a line on what it is and a switch: Routing groups, Library, Observability, Sessions, Warm-up and check-in, and the Gateway and Routing pages (Essential). A plugin on with settings has their rows under its row, drawn from its schema by the kernel's `ui.form`, and a change posts `{id, config}`. The Gateway page is drawn by its plugin's own scripts through the window's kernel (`kernel.js`, `assets/plugins/gateway/`); see the [plugin runtime](plugin-runtime.md), "The window's kernel". They are `plugins/*` registered with the [plugin runtime](plugin-runtime.md); `GET /api/plugins/builtin` says each one (`builtinJSON`: on is active, the user's own choice, what of it is in use) and `POST {id, on}` (or `{id, config}`) writes plugins.yml. A page marks what goes with a plugin `data-feature="<id>"`. Off, `featureStyle` hides everything so marked (rows drawn later too), `navHides` leaves its page's tab out of the sidebar and the header, `show` opens Agents for an address kept for it, and `setSetTab`, `partShows` and `findSettings` leave its Settings tab and rows out, of the search too. One in use stays on: its switch is disabled and the row says what uses it, and the POST refuses it off. A plugins.yml that doesn't parse is said above the list and no switch moves. `boot.js` says which are off (`featuresOff`) and their settings (`pluginConfig`) before the first paint, and `settings.builtins` carries the list on every state read. The TUI doesn't follow them. See `builtins.test.cjs`, `builtins_test.go`. | [`builtins.go`](../../internal/gui/builtins.go), [`assets/plugins.js`](../../internal/gui/assets/plugins.js), [`assets/app.js`](../../internal/gui/assets/app.js) |
| The app's log | `runGUI` and `runPanel` (so `magpie`, `app`, `tray`, `panel`, a window view and a `magpie:` link) call `startLog` before `gui.Run`: `applog.Start` sends the standard logger, which the gateway, the plugin host and Wails' error handler write to, to `app.log` in `appdir.Logs` as well as stderr (`quiet`, which never fails, first, since io.MultiWriter stops at a failed writer and an app opened from Explorer has no stderr), and points `debug.SetCrashOutput` at it, so a panic is in it. Opened from the Finder, the Dock or the Start menu the app has no terminal, and wrote its log nowhere (rayem1w on X, 0.1.2330). The folder is `~/Library/Logs/magpie` on macOS (beside the launch agent's `serve.log`, which launchd writes for `magpie serve`: the two processes never share a file), `%LOCALAPPDATA%\magpie\logs` on Windows, `$XDG_STATE_HOME/magpie` (`~/.local/state/magpie`) elsewhere, and `logs` in a portable data folder. The file is 0600. At 5 MiB (`Limit`) it becomes `app.old.log`; when another magpie (an update's new process) rotated it already (`os.SameFile` of the open file and the path differs), it is only opened again, and when it can't be moved it is emptied. Each line goes through `scrub`, which masks bearer and basic credentials, key and token query parameters, and `sk-`, `AIza`, `gh?_` and JWT shapes, though nothing logged is meant to carry one. Settings › About's Log row (`#logRow`, `settingsJSON.Log` from `applog.Current`, so `magpie web`, which writes no file, shows none) has its path and an Open (`POST /api/settings/log/reveal`) that shows its folder; `magpie log` prints where every log is, `-n` the last lines, `-f` follows across a rotation (`TestTheAppWritesItsLog`, `TestLogFollows`). | [`internal/applog/applog.go`](../../internal/applog/applog.go), [`gui_on.go`](../../internal/cli/gui_on.go), [`log_cli.go`](../../internal/cli/log_cli.go) |
| No GUI build | `-tags nogui` builds magpie without the GUI: `hasGUI` is false and `runGUI` says to use `magpie tui`. The files of `internal/gui` that need Wails build under `!nogui`. | [`gui_off.go`](../../internal/cli/gui_off.go) |
| The terminal quitting | `magpie tui` that served the gateway with an agent connected hands the gateway on as it quits, on Linux where nothing opens magpie at login (a WSL distro, a server over SSH; zaidu259434 on X): `keepGateway` starts magpie's service when it is installed, installs it (`daemon.Install`) when the user never removed it (`daemon.Removed`, a mark `magpie daemon uninstall` leaves), and otherwise starts `magpie serve` in its own session (`serveInBackground`, log in magpie's cache). Elsewhere, or with nothing connected, it only says the gateway stopped. Where magpie opens at login (`autostart.Enabled`, which on Linux counts a package's own autostart entry, also on a box no desktop logs into) it leaves the gateway to the app, and the note says `magpie autostart off` has the terminal hand it on where no desktop logs in (`atLoginNote`, #3756). | [`keep.go`](../../internal/tui/keep.go) |
| The app quitting | A Quit the user chooses (the tray's Quit, the panel's and window's Quit, Command-Q on the Mac) while agents are connected to the gateway this process serves asks first (see [A Quit asks while agents go through magpie](#a-quit-asks-while-agents-go-through-magpie)). Closing the window only hides it: magpie stays in the menu bar or the system tray and the gateway keeps serving, on Windows too. | [`quitask.go`](../../internal/gui/quitask.go), [`quitask_app.go`](../../internal/gui/quitask_app.go), [`quitask.js`](../../internal/gui/assets/quitask.js) |

## Runtime path

1. `magpie` with no arguments, `magpie app` or `magpie tray` calls `runGUI`, which starts the app's log (`startLog`, above) and calls `gui.Run`. A `nogui` build runs the TUI instead. A second launch hands over to the instance already running. On Windows, before any window is made, `webView2Check` asks Wails whether the WebView2 runtime is there (`Env.Info().PlatformInfo["WebView2"]`, "" when its lookup fails; a key Wails doesn't set counts as there). Without it Wails would exit on the first window with nothing on screen, so magpie logs it, shows a Windows message box in the reader's language naming the runtime, Microsoft's download page (`webView2Page`) and `magpie web` / `magpie tui`, opens the page on Yes, and `Run` ends (#1806, `webview2.go`, `TestWebView2MissingFromWails` on Windows). The box can't come from `ErrorHandler`, which runs on the thread Wails makes its windows on.
2. `startBackend` brings up the gateway, or finds another magpie serving it.
3. The panel and the window load `index.html` from `Handler`. `boot.js` passes the saved language, theme, colorblind mode, text size and desktop font choices before the first paint.
4. The page reads `GET /api/state` and writes through `POST` routes. Each write answers with the new state, built under `held`.
5. Closing the window hides it. On the Mac a full-screen window first leaves full screen (`closeStep`). The system's close on the panel (a title bar's X, Alt+F4, a window manager's close key) hides it as Escape does, so the tray icon opens it again; a panel lightweight mode let go, no longer `h.panel`, closes (`makePanel`, `TestPanelCloseHidesIt`). A hidden macOS window keeps its WKWebView's page marked visible, so `hideWebview` tells the page it is out of sight before `orderOut`; that makes `document.hidden` and `visibilityState` true, stops the page's polling and frame loops, and pauses CSS animations. `showWebview` tells it it is visible again (#4293). Lightweight mode lets go only of a window closed this way: one covered by another app's window, minimised, or open as magpie was hidden is still open (`windowUp`, not Wails' `IsVisible`, which is the occlusion state), and so is it for the Dock while the window is open and for a restart (#1381, `TestCoveredWindowStaysUp`). On the Mac the tray icon and a usage cell close an open panel by the same test (`windowOpen` in `togglePanelNow` and `trayCellClick`, not Wails' `ToggleWindow`), so under a window over the whole screen, such as a remote-desktop server's privacy curtain, the icon still closes the panel instead of showing it again (#1661, `TestTrayCellClickReleasedPanel`). On Windows a hidden window's WebView2 is put on its low memory target (`lowWhileHidden`, [`memtarget_windows.go`](../../internal/gui/memtarget_windows.go): `ICoreWebView2_19::put_MemoryUsageTargetLevel`, on each hide, on a page that comes while hidden, and back to normal on each show), so the panel and the window kept loaded in the tray stop holding what they last drew; the page stays loaded and running, unlike lightweight mode. In the tray the WebView2 processes' private working set went from ~170 MB to 20 to 40 MB, while their commit charge stayed where it was (molamaker11 on X). WKWebView and WebKitGTK have nothing magpie can set for it.
6. The window opens as it was last left. Its settled size and whether it was maximised are kept in `settings.Window` and `settings.WindowMaximised` (`settle`; per machine, see `KeepOwn`). A maximised window keeps the size it restores to. `makeMain` opens it at that size (`openSize`). On its first show, `placeMain` maximises it again on the Mac and Windows (on Windows once the page has come). On Linux, `makeMain` makes it maximised with `StartState`. On Windows a size larger than the screen's work area is fitted and centred (`fitRoom`), and the larger size stays kept. Where it was left is kept too, in `settings.WindowAt` (its top left corner across every screen, in the system's coordinates), so after a restart or an update it opens there rather than in the middle (jorbenzhu on Discord). `keepAt` saves it once a move or resize has rested for half a second, unless the window is maximised, full screen, minimised or hidden; `settings.Save` writes through temp and rename, and a settings file that can't be read is left alone. On its first show `placeMain` puts it back (`putBack`): `openAt` takes the screen holding most of the window, moves the window fully onto that screen's work area, and leaves it centred when no screen holds any of it, such as a monitor that is gone, or when the screens or the file can't be read (`TestOpenAt`). Under Wayland a window can't place itself, so nothing is kept or put back there unless GDK_BACKEND is x11 (`keepsPlace`, `TestKeepsPlace`). The tray's panel isn't kept: on the Mac and Windows it opens under the tray icon, and on Linux where the click was.
7. The page's header is the window's title bar. `plainTitlebar` sets it up once the app has started, and again on a window lightweight mode made again (`madeAgain`). On Linux it hides GTK's title bar and keeps the shadow and the resize edges; GTK 3 leaves the corners square. On the Mac the traffic lights are inset into the header (`MacTitleBarHiddenInset`). In the design of macOS 26 a window with a toolbar has larger corners than one with a plain title bar, so `insetLights` ([`titlebar_darwin.go`](../../internal/gui/titlebar_darwin.go)) takes the toolbar off, and `MagpieLights` keeps the lights and the title bar's height where the toolbar had them: after a resize, a new title, light or dark, more contrast or less transparency, and leaving full screen. A sheet, such as the folder chooser, still begins below the title bar. A first press while another app is in front still moves the window from anywhere in that title bar (`firstPress`), as AppKit moved it with the toolbar; AppKit itself now does so only in a plain title bar's top 32pt. AppKit gives a program that design from macOS 26 when the program is linked against the macOS 26 SDK or a later one and its Info.plist doesn't ask for the earlier design (`UIDesignRequiresCompatibility`); `designOf26` reads each at run time, as the SDK depends on the toolchain that built magpie. The toolbar stays in the earlier design, where it doesn't change the corners, with the lights on the right (a right-to-left language), or when the title bar isn't laid out as expected; this is decided once, when the window is set up. In full screen the title bar is AppKit's own. Windows keeps its own title bar.
8. On the Mac, Settings › Show in Dock puts magpie in the Dock never (`DockNever`), while the window is open (the default; `DockWindow` records it chosen, for an older magpie that took none for never) or always (`Dock`, which wins). The default was never until ShouguangCao on X found the open window out of Command-Tab, so going back to it to paste a key copied elsewhere took the menu bar; a new install (no settings.json) reads With window, and Hide is a choice of its own (`inDock`, `dockChoice` in app.js, `TestDockFollowsWindowByDefault`, `dock-choice.test.cjs`). A file written before that has no `dockSettled`, and stored Hide as nothing, so a user who chose Hide can't be told from one who never chose: such a file with no Dock choice reads as Hide, and the first save writes it so with `dockSettled` (`settleDock`, `TestOlderSettingsKeepHide`, `TestNewInstallFollowsWindow`). A file nobody can read reads the same way. The page never sends `dockSettled`; the save keeps the file's (`TestSettingsSaveKeepsDockReading`). It is this computer's own, as the Dock choice is (`KeepOwn`). A full-screen window joins it whatever the setting (`inDock`, `dockOnFullscreen`). `openMain` joins before it shows the window (`setDock` runs on the main thread at once). An app activated out of the Dock (the quick panel's focus) that then joins it stays last in Command-Tab's order though it is in front, so joining to the front activates magpie by way of the Dock (`dockStep`, `activateAgain`; #1381, `TestDockStep`, `TestDockJoinLeadsCommandTab`). It does so without asking NSApp whether magpie is active: the Mac makes it the front app at once, and NSApp says so only once the main thread has taken that in, after the join on a busy Mac (#1651).
9. Settings › General › Quick panel shortcut binds a key combination that opens the tray's panel from any app (ben hanks on Discord); none is set by default. The page records the keys and posts them to `POST /api/settings/panel-shortcut`, which checks them (`panelShortcut`: a letter, digit, Space or F1–F20 with Ctrl, Alt/Option or Cmd/Super, or an F key alone, written as Wails' accelerator), has the running app bind them (`onPanelShortcut`, `bindPanelKey` through Wails' `GlobalShortcut`: Carbon's hot keys on the Mac, `RegisterHotKey` on Windows, `XGrabKey` on X11) and saves them only once bound. One the system refuses (on Windows and X11, another app holding it) is not saved and the old one stays bound; one saved but refused as magpie starts is retried after a handed-over magpie exits and else said in the row (`panelKeysJSON.Taken`). A pressed shortcut toggles the panel as a tray click does (`togglePanel`). The Mac's Carbon hot keys don't refuse a combination another app registered too, so there both are taken and magpie can't tell the user. The shortcut is per computer (`KeepOwn`), and the Mac's old process lets go of it as it hands over (`leaveMenuBar`). On Wayland no app may grab keys itself, so the field is off and the row says the command to bind in the desktop's own keyboard settings (`magpie panel`); Wails' GlobalShortcuts portal is not used, since the compositor decides its keys and Hyprland binds none unless configured. From there a not-connected Claude Code's or Codex's opened row switches its account (shuizhuyu on X, [Switch accounts only](providers-accounts.md)).
10. A click on the tray icon opens the quick panel, or the main window when Settings › General › Click the tray icon to says so. The panel opens the main window from one control, on its edge nearest the tray (isaac_bai on Discord): hung below its icon (the Mac's menu bar, a taskbar or bar at the top) it is the header's icon (`#open`, named "Open the main window"); standing above it (Windows' taskbar at the bottom, a bottom panel on Linux) it is the foot's "Open the main window" left of Quit (`#openMain`), and the header has no icon. The two are never shown together (yogurt36 on Discord). Once `togglePanelNow` has placed and shown the panel, `tellPanelSide` reads the panel's bounds and its screen's and sends `magpiePanelOpenAt("foot")` when the panel is nearer the screen's bottom, `"head"` otherwise (`panelOpenAt`; the Mac is always `head`). Under Wayland a window can't read where the compositor put it, and until the app says, layout.js sets `body[data-open-at]` by the system: `foot` on Windows, `head` elsewhere ([`panelside.go`](../../internal/gui/panelside.go), `TestPanelOpensTheWindowFromTheEdgeNearestTheTray`, `panel-open-once.test.cjs`). On Windows a double-click on the icon opens the main window too (isaac_bai on Discord): Wails passes the icon's `WM_LBUTTONDBLCLK` on as `OnDoubleClick`, the panel its first click opened is hidden by `openMain`, and the `WM_LBUTTONUP` that ends the double-click is swallowed once (`trayDouble`, within `trayDoubleRelease`) so it does not open the panel over the window. The Mac's status item calls its action on each mouse-down with no click count, and Linux's StatusNotifierItem has only `Activate`, so there a double-click is two clicks and is left as such (`trayDoubleClicks`, `TestTrayDoubleClickOpensTheWindowOnWindowsOnly`, `TestTrayDoubleClickReleaseSwallowedOnce`; [`traydouble.go`](../../internal/gui/traydouble.go)).

## Constraints and failure behavior

### Allowance card refreshes

A card refresh applies only the requested account from the API's full
card list. A whole-page read begun before that refresh completed keeps
the newer card when its older response arrives; other cards still update,
and a later whole-page read can replace or remove the card. This also
keeps two different card refreshes from undoing each other's results.
If a refresh first supplies the account's name, its provider's cards are
replaced while newer card refreshes and other providers stay. The replaced
keys are protected too: an older whole-page response cannot restore an
unnamed card or erase its newly named replacement. Only a completed
whole-page read retires the ordering markers it no longer needs.
A remote magpie's summary can likewise become its namespaced child cards;
refreshing it preserves local providers and other remote magpies.
`keepRefreshedQuotas` and `quotaRefresh` in
[`app.js`](../../internal/gui/assets/app.js) own this publication order;
`quota-refresh-order.test.cjs` covers held responses and failures, and
`usage-card-refresh.test.cjs` checks the real button in both browser
engines and all five languages. A cached `readDeferred` card says no
refresh was requested this time; a failed read retains its failure notice.
The tray panel's Allowances cards use the same `quotaRefresh`, at the
start of the line under the account (the reset deadline, the error, or a
balance's name), opened as the card is hovered or the button is focused.
That line names the deadline ("Resets 14:26"), so no bare `↻` stands for
it; `renderPanelQuota` puts the focus back on the button after the redraw.
`panel-quota-refresh.test.cjs` covers it in both engines, all five
languages, at 440 and 360px.
Redrawing the cards preserves their refresh controls and any running turn.
The reply updates the reading immediately; the icon then finishes its
current full turn before stopping (no rotation under reduced motion).
A manual refresh whose HTTP reply succeeds but whose requested card has
an error reports that error through the status line too (#4817), including
when the card already showed it. Errors on other cards in the reply do
not become the requested account's error. An unnamed provider or remote
summary that gains named cards reports only failures accepted from that
reply; an older error replaced by a newer reading is not reported again.
Claude Code's first-run screens are gone through by the helper itself
(claude-subscription-bridge.md); one it can't get past ends the read, and
that is said on the editor row, Usage card, tray card and refresh notice:
finish Claude Code's first-run setup, then refresh; `magpie claude-code`
names the executable, including a build magpie downloaded. The original
error stays in the card's tooltip, and the read never falls back to `-p`.
`claude-quota-onboarding.test.cjs` presses the active account's button in
both engines and all five languages, with an account error in a successful
HTTP reply, then a successful read after setup and an unrelated card error.

### Streamed output speed

Usage rows and aggregates use `Record.Decode` and `DecodeOf` in
[`internal/usage/usage.go`](../../internal/usage/usage.go). Routing uses
`decodeOf` in [`plugins/routing/client.js`](../../internal/gui/assets/plugins/routing/client.js); Usage
rows use `ledDecode` in [`plugins/usage/page.js`](../../internal/gui/assets/plugins/usage/page.js).

When an answer follows reasoning, only answer tokens are timed from the
first answer text. Without answer text, all output tokens are timed only
when the gateway records paced visible reasoning and a usable content flow.
`firstToken.reasoningMs` in
[`ttft.go`](../../internal/gateway/ttft.go) requires reasoning at the first
content and subsequent reasoning deltas at least 100 ms later. It excludes
native Responses and Gemini, whose summaries do not measure their reported
reasoning tokens, and redacted thinking, including translated sealed blocks.
The upstream protocol is retained when the client receives a translation.
These are receive-side timing checks, not access to the vendor's generation
clock: a relay that releases a buffered reply evenly can still look paced.
Without an upstream guarantee, this is an observed output-rate estimate.
Summaries stay excluded rather than extending their visible portion's time
to an unobserved reasoning-token count.

`Record.FirstReasoning` (`first_reasoning_ms`) and the corresponding
`Call`, `Route` and `Try` field (`firstReasoning`) record this evidence with
the same origin as their duration. Historical records have no evidence and
keep their previous speed. Request-page caches and compressed snapshots
preserve the optional field; the packed row keeps `ReasoningHidden` in
its own `Flags` bit (`rowReasoningHidden`), apart from pricing's
`swapKnown`, which every priced row carries. `ReasoningHidden` (`reasoning_hidden` in the
ledger, `reasoningHidden` in traces) suppresses a no-text speed even when
redacted/encrypted thinking has no reported reasoning-token count. A
Responses summary is marked this way on its own-login path too, and after
an endpoint fallback, where the try's protocol still names the first
endpoint, by the upstream that answered (`summaryUpstream` in
[`continuation.go`](../../internal/gateway/continuation.go)). Optional
OTel attributes carry the evidence without response text. The existing burst and held-content limits in
`DecodeOf` still apply, and hidden reasoning does not enter average speed.

A streamed request's duration (`Millis`, which ends every speed window)
ends when its last try's reply ended, not when `serve` returns: what the
gateway writes down after an answer (`answered`'s sticks, the provider's
last answer in `NoteServed`) goes to disk, and on a busy disk took
seconds, which made a paced reply read as held back and tell no speed
(#1939, `TestSpeedEndsWithTheReply`). A decision group's row is timed the
same way.

`TestVisibleReasoningSpeed`, `TestReasoningSpeedEvidence`,
`TestReasoningSpeedThroughGateway`, `TestMixedRedactedReasoning`,
`TestHiddenReasoningThroughTranslation`, `TestSlowReleasedReasoningIsUntimed`,
`TestPricedRowKeepsReasoningEvidence`, `TestSummaryAfterEndpointFallbackStaysHidden` and
[`reasoning-speed.test.cjs`](../../internal/gui/tests/reasoning-speed.test.cjs)
cover the calculation, cache round trip, translated streams and both pages.

### Routing purpose filter

The request heading's purpose menu in [`plugins/routing/client.js`](../../internal/gui/assets/plugins/routing/client.js)
uses `openProtoMenu`'s live checkbox selection: each tick immediately shows
requests matching any selected purpose, with the menu kept open. An empty
selection, All purposes or Clear filter restores every purpose without
changing the day or request/session view. A live menu stays open when its
redraw clamps the list's scroll; the reader scrolling outside still dismisses it.
Choosing All purposes closes the menu and returns keyboard focus to the
purpose button without scrolling; unticking the last checkbox keeps the
menu open and focused on that checkbox.
A Claude Code subagent's turn has the Subagent tag (`tagKind`,
`Route.SubagentID`) and no kind, so it stays under Unmarked, as on the
Usage page. Counts, the current story and replay
follow the same filtered list. Selected purposes remain available when a day
has no matching requests. Opening a request from another page clears the
filter only when that request is excluded. Usage's purpose picker remains a
single choice sent to the ledger API. See `purpose-filter.test.cjs` and
`routing-purpose-state.test.cjs`.

### Routing mode chip

The stage's chip and the Accounts and keys heading in
[`plugins/routing/client.js`](../../internal/gui/assets/plugins/routing/client.js) name how a route
routes only where that can be changed: a group, or a provider in
`/api/groups`' `pools` (two or more accounts or keys on). There they are
`.rt-go` controls (`goTo`, `goRouting`) that bring the group's card or the
provider's row under Several accounts or keys into view through
`scrollOnPurpose`, mark it (`rt-found`) and focus its routing. A provider
with one on names no routing, and the mode paragraph says how to get one
(`LONE`). The header is drawn again when the groups come in (`headOf`).
See `routing-mode-goes.test.cjs`.

### Routing key and account folds

Three or more API keys, or three or more accounts, of one provider standing
next to each other for one model (same fixed effort, fallback and
group-in-group heading) fold into one stage row, `li.rt-fold` in
[`plugins/routing/client.js`](../../internal/gui/assets/plugins/routing/client.js) (`FOLD_AT`,
`rebuild`). It shows the count and their state together: the one that is
lit and what it is doing, how many are available and how many rest
(`together`). Requests that land on a folded seat fly to the fold
(`shownRow`). Clicking it (or Enter / Space) shows each in place, wired
from the fold. Accounts and keys folds a provider's keys, and its accounts,
into one `.rt-keys` row each with their summed tally.

A group in the group's heading (`li.rt-sub`, `subNode`) folds the same way
(`toggleSub`): folded, it says how many it routes to and what they do
together, a request to any seat under it flies to the heading
(`row.shut`), and its wire is the last one drawn (`wiresTo`).

Keys start folded. Accounts and group-in-group headings start open when
they hold fewer than `LONG` (6), folded otherwise (Aiirobyte on Discord: a
group of many accounts took the page). What the reader opens is kept in
localStorage `magpie.routingKeysOpen` and what they fold in
`magpie.routingFolded` (`keepOpen`, `isOpen`), keyed `provider/model` for
keys and `provider/model@` for accounts on the stage, `provider/*` and
`provider/@` in the list, and `group/<id>` for a heading. Display only: the
gateway's order and choice of seat don't change. Two stay two rows. See
`routing-keys-fold.test.cjs` and `routing-group-fold.test.cjs`.

### Routing account windows

A subscription account's row in Accounts and keys shows the one figure
routing went by, and each of its windows, with when each resets
("5 hours 40% used · resets in 2h"), on hover over that figure (the
`.st`'s title), as the Usage page reads them (#1833). A row with no figure
of its own (a remote's, or an account not read yet) shows its windows as
its one line instead (`.wins`, cut to one line, all of it on hover).
`cardOf` in [`plugins/routing/client.js`](../../internal/gui/assets/plugins/routing/client.js) matches
the route's account to app.js's `quotas`: a `subscription` card of this
computer (no `from`) whose provider is the account's `agent` (a plugin's
provider id) or its provider, and whose user is the account's, ignoring
case. `winsOf` names them with `familyWindows`, `quotaText` and
`resetInText`. Nothing new is read: opening Routing asks
`/api/usage/quotas` in the ordinary way (`show`, when the readings are
missing or over a minute old), never `?asked=1`. A kept day's list shows
none, since those requests were weighed by readings long gone. See
`routing-account-windows.test.cjs`.

A Claude (or Codex) joined over the network, a Remote magpie provider's
row, shows the windows of the accounts signed in on that computer the same
way (`remoteWinsOf` in `client.js`): the Usage page's cards with `from`
whose provider id is the remote's, a slash and the card's. The computer
that holds the account tells its usage; this one reads nothing new. A
Claude model's row is told Claude's cards alone and a ChatGPT model's
Codex's; with more than one card the account's user leads each window. A
remote whose read failed, or whose card of the row's family failed there
(the remote read, its Claude's read didn't), shows "Allowance unavailable"
with the reason on hover, never a figure and never nothing; a remote that
holds this computer's key tells why the same way (`errRemoteHeld`). See `routing-remote-windows.test.cjs`.

The refresh beside the Accounts and keys heading (`#rtReadAllowances`,
#1834) reads them again by hand: `loadQuotas(true)`, the asked read the
Usage page's Refresh now makes. It turns while the read is on its way,
and a click then asks nothing more. Nothing on the page reads again by
itself. It shows only live, with a subscription account listed. See
`routing-read-allowances.test.cjs`.

On the stage, an account's seat tells the same windows in its tooltip,
under its id (`render`, #1835). The row itself keeps one line: the
fullest window while idle, what it is doing while it answers. See
`routing-seat-windows.test.cjs`.

### Routing account cards

Each row under Accounts and keys is one cell like the others: a dot, its
name, plan and Try now on one line, and under it what is left (or its
rest) in full (ferock4669 and iyou00 on Discord: many keys ran the list
on and on). The dot is green for the one in use (of a provider's rows not
resting, the one that answered last), amber resting, red for what the
reader has to do (`.act`: sign in again, a restriction or verification, a
key failing with no answer), grey otherwise. Red is only on `.act` rows:
a quota rest or old failures are muted (danzi002 on Discord, "显示的好
乱"). How it fares (tried, answered, failed, last answered) is a line of
its own only on a row with nothing else to say or one to act on; on the
others (`.said`) it is in the tooltip with the models it answered, under
its id. A folded row of keys or accounts names no models. Columns are
`min(240px, 40vw)` wide, so a name and its Try now share a line
(`routing-acts-calm.test.cjs`).
`renderActs` in
[`plugins/routing/client.js`](../../internal/gui/assets/plugins/routing/client.js)
shows the first `ACTS_SHOWN` (8) rows of a longer list and puts the rest
behind "Show N more" at its foot (`.rt-acts-foot`), kept across a reload
in localStorage `magpie.routingActsAll`. The button clicked stays where it
is on the screen while rows come in or go above it (`actMore.onclick`
scrolls the page by what it moved). Never behind it: a row that needs
the reader (to sign in again, resting, a model resting, failures with no
answer, a remote whose allowance can't be read), the rows under a fold
the reader opened themselves, and the heading of a provider with a row
shown. A row to sign in again is drawn under its fold even while the fold
is shut. Two more alone aren't folded. "View usage" beside it opens the
Usage page's Overview (`window.openUsageOverview` in
`plugins/usage/page.js`), where each account's card says it all, without
keeping that tab as the reader's own. See `routing-acts-compact.test.cjs`.

### A provider's accounts together

A Usage card of several accounts (not a card of keys, which has its sum)
has "All N accounts" under its head, and the tray panel's group of
several the same strip above its cards (`quotaAll`, CryptoSarom on
X). A routing group's row has a line for each provider among its
members: what is left of that provider's accounts together
(`groupQuota` in `plugins/routing/client.js`, drawn again when
`quotas` is new). `quotaAll` and `mergeQuotas` are in `quotaall.js`, which app.js imports
once a provider has two accounts (`quotaAllJS`) and the Routing page
once a group's member has readings, so they weigh nothing on the core;
each place is drawn again once it is in. All three come from
`mergeQuotas`, which puts together only what reads alike:

- a counted window (`limit`) of one name and unit adds its amounts and
  limits, its share the share of the sum; a wallet (`balance`) adds its
  balances in one unit;
- a window said only as a share is the mean of the accounts on one
  plan, the same allowance each; two plans are never averaged, and a
  window more than one plan has is named with its plan;
- a share is never added to a count; `text` windows are left out;
  unlimited only where every account read says so;
- an account whose reading failed is unknown, not 0, and is counted
  ("1 couldn't be read"); a window an `asOf` reading stands in for,
  whose reset has passed, is left out and counted in its tooltip;
- a window the account can't spend counts as used up: its `heldBy`
  (`provider.WithHeldWindows`, set on `/api/usage/quotas`) names another
  window that stops the account for every model, is used up and starts
  again after it, as 7 days at 0% left holds 5 hours at 100% (#4791).
  Not the other way: a week with room behind five hours used up keeps
  it. The tooltip counts how many were held; each account's own card
  still shows the vendor's figure;
- only what two or more accounts have goes together; family and pool
  stay, so `familyWindows` folds the result as an account's.

No account is named in it, and each account stays below it as it was.
There is no switch: it is additive, and the figure follows used or left
with the rest. See `usage-merge.test.cjs` (the merge) and
`usage-all-accounts.test.cjs` (the three places).

### The Sessions page's folders

The Sessions page lists an agent's project folders as one list, newest
session first ([`client.js`](../../internal/gui/assets/plugins/sessions/client.js)).
A row says the folder's name, the folder it is in (home as `~`, from the
manage reply's `home`), its newest session's title, when, and how many.
Temporary folders, the disk's root and apps' hidden folders under home
(`scratch`) wait folded under Other folders at the end; a search, or a
session opened from elsewhere, unfolds them. Checkboxes and the Delete bar
come only with Select, and Done lets the pick go. Relocate project and
Delete all sessions are in a folder's ⋯ menu, shown on hover (always on a
touch screen) and on a right-click. See `sessions-folder-list.test.cjs`.

### Routing day tallies

Live, an account's row under Accounts and keys counts its tried, answered
and failed over the day's whole history, not over the last few requests
the gateway keeps (#3351): the two said different things by orders of
magnitude, and the day tabs above the section count the day. The counts
come with the history read the page already polls (`/api/gateway/history`
with no day, its `tallies`), counted by `DayTallies` in
[`internal/gateway/history.go`](../../internal/gateway/history.go) from
the same files the Usage page reads, keyed as the page groups accounts
(provider and user, a key or provider by its id). Each look folds in only
the whole lines added to today's files since the last one: a busy day's
file is hundreds of MB (#3383), and the page asks every 15 seconds. A
request done since the last look is counted at the next one. The note
under the heading says the counts are today's. What the history cannot
say — the readings the latest request was weighed by, the rests, one
model resting alone, the accounts in the kept requests — the page still
takes from the trace over these tallies; a day picked, or a purpose
picked, counts its own list as before, and the note then points at that
list. An older answer without tallies counts the kept requests as it
did. See `routing-day-tallies.test.cjs`.

### Stored agent sessions in WSL

The Sessions page uses `sessions.ListAgent`; Usage → Sessions uses
`sessions.List` and `StatsFor` through the handlers in
[`sessions.go`](../../internal/gui/sessions.go) and
[`sessions_manage.go`](../../internal/gui/sessions_manage.go).
`ListAgent` also lists a session nothing was said in, so it can be cleared
away; `List` leaves such a session out. The routes a row opens
(conversation, Markdown, Terminal) look the session up with
`sessions.Find`. `Find` finds every session `ListAgent` lists, empty ones
included, so such a row doesn't answer "no such session" (#2273,
`TestFindEmptySession`, `TestEmptySessionOpens`).
On Windows, [`internal/sessions/wsl.go`](../../internal/sessions/wsl.go)
reads running distros through their WSL shared paths. A stopped distro keeps
its cached listing and is not opened; running distros are relisted at most
every 30 seconds.

For omp, [`ompRootsIn`](../../internal/sessions/omp.go) includes both
`~/.omp/agent/sessions` and
`~/.omp/profiles/<name>/agent/sessions`. Subagents and advisor files count
in their parent session. A profile session's Resume command selects its
own profile with `omp --profile <name> --resume <id>`, including when
resumed through `wsl.exe`. Delete and Restore preserve its original
profile path and artifacts. Windows does not discover arbitrary stores
selected by a distro's `PI_CODING_AGENT_DIR`, XDG variables or
`--session-dir`.

### Desktop fonts

Settings → General offers independent interface and code fonts, each with
the installed family's actual styles. `GET /api/fonts` calls
[`fonts.List`](../../internal/fonts/fonts.go): DirectWrite on Windows,
CoreText on macOS and Fontconfig on Linux. Discovery is lazy, cached in the
handler, and refreshed by the menu. It returns names and CSS traits, never
font files or paths. A failed refresh answers 503 and preserves the previous
collection; an empty successful collection is distinct from failure.
A `nogui` build can't read the catalogue and answers the app 404.

A browser tab (`magpie web`, or the app's page opened in a browser) always
has the same rows (Norcleeh on Discord). Its choice is the
browser's own, kept in `localStorage` under `magpie.fonts` and applied by
`fonts.js` before the first paint: the tab can be on another computer, so
it neither changes nor takes the app's `settings.UIFont`/`CodeFont`. A
tab's `/api/fonts` is the server's families (none in a nogui build such as
Docker or `magpie-cli`, and none when reading them fails, never an error)
plus `commonFamilies` in `internal/gui/fonts.go`, in regular and bold.
The tab keeps those it draws (`webFonts.drawn` in `webfonts.js`: text in
the family measures apart from a generic family's on a canvas), so another
computer's fonts, and Safari's user-installed ones, aren't offered. When
none is kept, the rows still offer System default.

Those are the server's names, so fonts installed only on the browser's
computer were missing (Norcleeh again, magpie in Docker). `webfonts.js`,
which only a tab loads (app.js `loadFonts` imports it; the app's windows never
need it, and it is outside the core script budget), adds them: where the
Local Font Access API is (Chromium on https or localhost),
`queryLocalFonts()` is called in the press that opens a family menu, once
a page, and at load when the `local-fonts` permission is already given;
its style names ("SemiBold Condensed Italic") become weight, slope and
width. Elsewhere (Safari, Firefox, a plain-http LAN address, a refused
permission) the menu's search also takes a typed family: "Use “name”" is
offered when the canvas probe says this browser draws it, and greyed out
when not. A chosen family the server doesn't list stays in the tab's list
on later loads while the browser draws it, so `desktopFonts.available`
keeps applying it. Until 10-28 the rows were
gated on a boot `fonts` flag, so a nogui server had none, and what
"flashed and vanished" on a refresh was Text size: the rows a tab never
shows (Text size, the tray click, Open at login, the menu bar's
allowances) are `.app-only`, which `body.web` hides before the first paint.

`settings.UIFont` and `settings.CodeFont` keep the family, style name,
OpenType weight, CSS slope and width percentage. A missing choice keeps the
platform default. A settings POST can restore that default with an explicit
null; omitting the field preserves it, as does every browser-mode save.
`KeepOwn` preserves this computer's choices during sync and backup restore.
Invalid traits reject the save without replacing the previous settings.

[`fonts.js`](../../internal/gui/assets/fonts.js) applies choices at boot and
through `applyPrefs`. After a successful font change `host.fontsChanged`
notifies both existing webviews through `receiveFonts`, without waiting for
the other window to gain focus; a released lightweight webview uses boot
settings when recreated. An in-flight local save ignores the notification
and finishes its own queue.
The selected family precedes the existing platform fallback stack. The
interface and code traits apply independently; explicit component weights
still mark emphasis. A choice takes precedence over Omarchy's font, while
restoring the default follows the theme again. If discovery finds a saved
face missing, the page retains its name, explains the fallback and uses the
default until the face is available or the user chooses another. A discovery
error never clears a saved choice. The browser's own typography is unchanged.

The menus support search, keyboard selection and refresh. Styles are ordered
by weight, then normal, italic and oblique; width and name break remaining
ties. Family changes keep a matching style where possible, otherwise the
nearest regular face; named variable instances are offered without arbitrary
axis controls.

### Recent-call bodies

`providersState` in [`providers.go`](../../internal/gui/providers.go) sends
recent-call metadata without request or response bodies, including after
provider writes (#1521: forty calls' bodies made `/api/providers` about
20 MB). Each call in the gateway's 40-call ring has an `id`, numbered in
`Server.record`. Opening a recent call on Gateway fetches its captured
previews from `GET /api/gateway/call?id=…` (`Server.RecentCall`), sent
`Cache-Control: no-store`. The route sits on the same mux as the rest of
the API, so `magpie web`'s key guard covers it. The bodies are the ones the
ring kept, masked as the request was (`redacted`); the route doesn't fetch
S3 or change capture, export or archival. A call evicted from the ring, or
a gateway served by another magpie, returns 404. The page keys a row by
that id (`callRow`), keeps fetched bodies in memory while their calls
remain in the list, preserves them across summary refreshes, and offers a
retry after a failed read. See `TestRecentCallBodiesOnDemand` and
`recent-call-bodies.test.cjs`.

A request on the Routing page opens to the same bodies (Fate on Discord):
its story's head has "Request and response", which fetches
`GET /api/gateway/call?route=…` (`Server.RouteCall`) only when pressed.
A call carries the Routing request it was (`Call.Route`, the trace's
`Route.ID`, set where `serve`, the Codex backend, a refused title and a
System One request an app sends (`/v1/systemone`, `serveDecideTries`)
record it; the Codex backend's refused key and empty failure too, but not
a compaction's, which opens no route: `TestCodexKeyRefusedCarriesItsRoute`);
a route id is not a call id. When the ring no longer has it,
the story shows the request archive's copy through `archivePanel` if the
route names one (`Route.Archive`, `call.archiveName()`, set only when the
archive was on for that request), and otherwise says how to turn the
archive on; with the archive on (`gateway.archive.on` in
`/api/providers`) it says instead that this request isn't in it, made
while the archive was off or not saved (wangb on Discord). With the archive
on, `/v1/systemone` is kept as a chat request is, and a routing group's
Jev classifier, which magpie asks itself, is kept as it went (`archiveOwn`
in `systemOne`; its usage row names it): `TestArchiveKeepsSystemOne`,
`TestArchiveKeepsJevClassifier`. Still not archived: the Codex backend,
draw, embeddings and video, and replies magpie writes itself
(`answeredHere`). The Routing page captures nothing of its own. The archive's switch
(`POST /api/settings/archive`) sets `RequestArchive` while sync has an S3
bucket and `RequestArchiveLocal` while it has none; `GET
/api/archive?name=…` reads this computer's copy first and then the bucket's
(`TestArchiveSwitchKeepsOnThisComputer`, `request-archive.test.cjs`). See
`TestRoutingRequestOpensItsBodies`, `TestArchiveWholeBodies` and
`routing-bodies.test.cjs`.

Under the bodies, on Gateway and on Routing, "HTTP headers" folds open a
call's headers both ways (usrname1833 on Discord: 查看原始请求和原始响应的
http头): the client's, as they reached magpie, then each request magpie
sent upstream for the call, its method, address and headers, and the
status, protocol, headers and time of its answer, or the error in place of
one (`wireHeadersView` in plugins/usage/page.js; `headers` in
`/api/gateway/call`, `Call.Headers`,
[`wire_headers.go`](../../internal/gateway/wire_headers.go)). The client's
are taken by `headerLogged` in `Handler()` for every request that isn't a
GET, HEAD or OPTIONS; what goes upstream by `rpmTransport`, under
`s.client`, as it is sent (so a header Go's transport adds itself,
`Accept-Encoding` or `Content-Length`, isn't there), and by
`provider.SendsThroughPlugin` for an account served by a plugin, which is
shown as handed to the plugin: the plugin sends it on to its vendor with
headers of its own that magpie doesn't see, and the view says so. A call
keeps its first 8 upstream requests (`headerTriesMost`) and a count of the
rest. Recording is a copy of each header map; nothing is masked as a
request goes. Secrets are taken out when the headers are read, by what
each header is (`redact.SecretHeader`: Authorization, cookies, `x-api-key`,
any `*-Key`, `*-Token`, `*-Secret`, `*-Auth`, `*-Signature` but
`Idempotency-Key` and a count of tokens such as
`anthropic-ratelimit-tokens-remaining`), any Bearer or Basic value, and any
secret `redact.Scrub` finds in the rest; an address loses its password, a
secret-named query value (`?key=`) and its fragment (`scrubURL`). They live
with the call in the 40-call ring, and the request archive keeps the
upstream ones as `upstream` beside the request's and response's headers,
masked the same way; magpie's own requests (System One's decision, a
title, a search) keep none. See `TestRecentCallHeadersBothWays`,
`TestRecentCallHeadersEachTry`, `TestPluginHandOffHeaders`,
`TestSecretHeaderRealNames` and `call-headers.test.cjs`.

### Plugin check-in switches

A plugin's Usage-card Auto check-in button reads the persisted
`settings.pluginCheckins[provider]` first (`pluginCheckinOn` in
[`app.js`](../../internal/gui/assets/app.js)). `/api/state` returns that map
without the derived `checkinPlugins` list included by `/api/settings`.
An explicit saved false wins over the list; when no saved choice exists,
the list's resolved vendor default remains the fallback, and without the
list (a state refresh) the switch of the built-in check-in the plugin took
over is (`PLUGIN_CHECKIN_INHERITS`, the ids `provider.PluginCheckinOn` names;
a test reads both). This keeps a saved
switch on through a focus refresh and lets the next click turn it off.
[`plugin-checkin.test.cjs`](../../internal/gui/tests/plugin-checkin.test.cjs)
covers both response shapes, refreshes and the subsequent toggle.

### Plugin options

A plugin provider's editor shows the options its plugin declares
(`pluginOptionsField` in [`app.js`](../../internal/gui/assets/app.js); see
[provider-plugins.md](provider-plugins.md#a-plugins-own-options)). The
plugin's label, description and `confirm` text go in as text, never as HTML.
A change is kept in `draft.pluginOptions` until Save, which posts each one
to `POST /api/settings/plugin-option` before the provider's own save. An
option switched back to where it was is dropped from the draft, so it isn't
posted and doesn't make the editor dirty.
[`plugin-options.test.cjs`](../../internal/gui/tests/plugin-options.test.cjs)
covers it.

### A plugin's own page

A community plugin that declares `magpie.page` gets a tab in a Plugins
group at the sidebar's foot (`pluginpages.js`, loaded after `plugins.js`),
listed from `GET /api/plugin-pages` and again whenever Plugins reloads its
list (`window.loadPluginPages`). The tab's title is the plugin's own and is
not translated; `renderCrumb` and the sub-nav's label read its plain
`<span>` where a built-in tab has `span[data-t]`. The frame is made the
first time the page opens and fills the view (`.view.plugin-page`,
`.plugin-frame`). The theme and language reach it on every change of
`data-theme`, `lang`, `class` or `style` on `:root`. A page gone off or
away loses its tab, and the window goes to Plugins if it was on it. The
security model, the bridge and the reads are in
[plugin-runtime.md](plugin-runtime.md#p4-community-plugins-at-every-point).
[`plugin-page.test.cjs`](../../internal/gui/tests/plugin-page.test.cjs)
covers it in both engines, every language and a narrow window.

### Shared UI rules

- Windows desktop windows and tray panels keep the modal's dimming veil without backdrop blur or saturation: full-window filtering can stall WebView2 while a provider test or sign-in dialog is open. The existing `win` marker from [`layout.js`](../../internal/gui/assets/layout.js) scopes the rule in [`app.css`](../../internal/gui/assets/app.css) to the desktop app; browser pages and other platforms keep their existing effect. `modal-backdrop.test.cjs` checks the computed filter and veil in both engines and modes.

- Settings → General is laid out as macOS System Settings is (1ooogle on X): a row says its name alone when that says enough (Appearance, Colorblind mode, Language, Text size, the fonts, the tray click, Open at login, Show in Dock), and keeps one short line where the setting isn't clear from its name or has a consequence (Lightweight mode, Prevent sleep, Session terminal). Codex's, Claude Code's and Grok Build's own settings (Codex subagents, Long conversations, which is all of Grok Build's, background compaction, thread titles and descriptions, composer predictions, the auto-review and memories models) aren't here: they are in the agent's opened row on the Agents page (`AGENT_PREFS`, `connectPanel`), the panel's row has a Settings line that opens them in a sheet (`openAgentPrefs`), as does the agent's menu, and Settings' search finds them with a row that opens that sheet (`#agentPrefLinks`). Each row is one element moved between its places, so its handlers and state go with it (the owner, 10-20; `agent-prefs.test.cjs`). Long conversations is the exception with no Codex, Claude Code or Grok Build here, in gateway mode or a magpie in Docker: it sets the windows the gateway serves, which agents on other computers read, so it stays in General (`renderAgentPrefLinks`, Linka on Discord). A font row's line shows only while its fonts load or fail; the text size keys are the choices' tooltip, and Colorblind mode's note is its Off/On control's (`#colorblindSegs`). Colorblind mode (`settings.Colorblind`, `data-colorblind="1"` on `:root`, #2073) switches the chart series (`--c1`…`--c7`) in both light and dark themes to an Okabe–Ito palette where adjacent series differ in hue and lightness. On a quota curve each window (`data-series`, its index mod 7, set by `drawQuotaPlot` and `quotaCurve`'s legend only) gets a dash of its own on `path.qc-line`, its legend key shows a sample of that line (`.qc-sample`) for the dot, and the even paces (`line.qc-even`) are dotted; the curve's tooltip says so. The balance curve sets no `data-series` and keeps its solid line, dashed fit and dot. The Context tab's "good" grade, which takes `--c2` as a colour rather than a series, keeps a blue (`--c1`). Language is one menu button showing the language picked, opened in the app's menu (`renderLangPick`). See `settings-general.test.cjs` and `quota-curve.test.cjs`. Open at login reads `autostart.Enabled`, which on macOS 13 and later also asks Background Task Management about magpie's launch agent (`SMAppService.statusForLegacyURL:`, matched by the plist's Label, not its path); a record the user switched off in System Settings › General › Login Items reads as needing allowing, and the row keeps On with one line saying so and a button that opens Login Items (`/api/open` allows that one `x-apple.systempreferences` URL). The status read again when the window comes back to the front clears the line. On macOS 12, in a build without cgo, or when the system can't be asked, the plist alone decides, as before (#2120, `login-allow.test.cjs`, `login_allow_test.go`). On Linux it reads the autostart entry the desktop starts: the user's own over a package's in `$XDG_CONFIG_DIRS` (/etc/xdg when unset), as the XDG Autostart spec has it. Off over a package's entry that starts magpie writes a Hidden entry of the user's over it, and on takes that away rather than writing this copy's path (#3372, `autostart_other.go`).
- A setting row's line under its name (`.row.pref .sub`, in Settings, Plugins › Built-in and an agent's own settings) wraps whole beside its control, never cut to one line with an ellipsis (arculongus on Discord, `settings-lines-wrap.test.cjs`).
- Settings' search-provider picker offers **Off**, saved as `searcher: "off"`. Its help explains API-only search and suppresses provider fallback/own-search hints while off; the search priority row is hidden because no provider competes with the APIs. See [gateway search behavior](gateway-routing.md#turning-provider-search-off), [`renderSearcher`](../../internal/gui/assets/plugins/search/settings.js), `TestSettingsSearchProviderOff` and `searcher-pick.test.cjs`.
- A searcher named there gets an "If it fails" row right under it (`renderSearcherThen`). The row holds numbered chips for `searcherThen`, the providers that search in its place, in order. Add offers every searcher but the one named and the ones already listed. A chip's picker changes it or removes it. The row isn't shown for Automatic or Off. The failure line names the one that searched instead (`searchFailed.instead`), or says the ones after it failed too. See `searcher-then.test.cjs`. A picker's item is pressed through `pickItem` in `reader.cjs`, which waits for the list drawn again after `/api/model-speeds` answers (#4274).
- Settings' search APIs are asked in their listed order: each API's row (with its keys' row) is dragged as one, or moved with Alt+↑/↓, and has a switch that turns it off without removing its key (#2286). See [the search APIs' order](gateway-routing.md#the-search-apis-order-and-one-switched-off), `searchArrange` in [`plugins/search/settings.js`](../../internal/gui/assets/plugins/search/settings.js) and `search-order.test.cjs`.
- Settings' Search APIs add row has a base URL field under the key for every API (#1580): the vendor's own address is its placeholder, picking an API already added fills in its saved URL so a new key alone keeps it, and Add with no key changes only the URL. See [a search API's base URL](gateway-routing.md#a-search-apis-base-url), [`renderSearch`](../../internal/gui/assets/plugins/search/settings.js) and `search-api.test.cjs`.
- Every user-visible string has zh, zh-TW, ja and de translations with the same placeholders. `gui-zh-tw.test.cjs`, `gui-ja.test.cjs` and `gui-de.test.cjs` fail on a missing one; `gui-zh-tw.test.cjs` also fails on a zh-TW string with a Simplified character left in it.
- A click never moves the page. Code scrolls a view only with the reader's event in hand (`scrollOnPurpose`); `click-scroll.test.cjs` guards this. A click is taken where the reader left the view: a scroll not theirs made just before it, whose scroll event the browser sends only at the next frame, after the click, is put back first (`backToReader` in the click guard, #1554). What was clicked is held where it was (`hold`); a view at its top when clicked stays at its top, and what comes in above the control moves it down instead (#2134, `update-auto.test.cjs`), as long as it moves less than half its own height and so stays under the pointer. When what the click does leaves the page shorter under it (a list emptied), the view keeps room at its foot for it to stay (`.view-room`), room that goes as the reader scrolls back. A control under what it unrolls (`data-unrolls`, "Show 7 more") goes down with what it opens: what's held is the part it is in, so the rows open downwards. Anything larger above it, as the stage a day picked on Routing brings in above the bar, is scrolled past, and the control stays where it was clicked (`routing-days-fit.test.cjs`, `routing-failed-open.test.cjs`).
- A page scrolled a screen down shows Scroll to top and Scroll to bottom (emo172, #1090): a small capsule of two chevron buttons at the open page's lower right, in its right margin when a wide window leaves one, else over its corner; above Providers' sticky Add buttons. It goes again near the page's top and over a dialog, and, over the corner, at the page's foot, where the last row can't scroll out from under it, unless it has the pointer or focus. It is placed again on a scroll, a resize, a page shown or hidden, and whenever the open page or one of its own children changes size (a `ResizeObserver`, so a Routing group opened or closed moves it off the editor with no scroll, Jiabubu; see `view-ends-relayout.test.cjs`). It never changes the page's height, which the guard below measures. A press is the reader's scroll (`scrollOnPurpose`, then `arrived` once the page stands still), smooth unless motion is reduced; Scroll to bottom is `aria-disabled` at the foot. Each button's name and tooltip are in every language. The code is [`scrollends.js`](../../internal/gui/assets/scrollends.js), imported on the window's first scroll so the core doesn't carry it; the tray panel has none. Home and End scroll a page as before. See `view-ends.test.cjs`.
- A scroll not the reader's is put back, so every way the reader scrolls has to count as theirs: the wheel and a trackpad's coast after the lift (WebKit on the Mac plays the coast itself and the page hears its scrolls with no wheel for each, so the wheel starts the same chain a touch fling does, `flings`/`flingUntil`: each scroll keeps the next one the reader's till it rests, and a click ends it; issenn on Discord, `view-trackpad-coast.test.cjs`), a wheel or coast the page is never told of (macOS 13's WebKit, Safari 16.6's, scrolled a view under wheels it never dispatched, so every scroll was put back and only the bar's drag moved the page: three scrolls put back the same way within a second are taken as the reader's run and given back, and so is each one after it that goes on the same way within a second of the last, so a wheel turned slowly or on a loaded Mac loses no step; a scroll by code is one, not a run, and two at rest are still put back; issenn on Discord, `view-wheel-unheard.test.cjs`. Under a click's 4s hold the same holds: only a put-back that comes with the page redrawn since the hold last held (`redrawn`: the view's height or where the first held parts sit moved from `h.lay`) is the hold's and never counts toward a run, so a profile opened on the Agents page and wheeled through at once scrolls, issennknight on Discord, `view-wheel-after-click.test.cjs`. The parts `h.lay` measures are those still in the page that scroll with it: a part drawn again since (the Usage page's tabs, the agents' list under an agent opened or closed) is a redraw once, not at every scroll, and a sticky one (Settings' tabs at a narrow width) is left out, as it sits still while the page scrolls; `view-wheel-after-redraw.test.cjs`), a touch and its fling, the keys, a drag, the scrollbar's own drag (`barDrag`, #1089) and the middle button's autoscroll (`panning`, 多喝热水 on Discord). Chromium and WebView2 give the autoscroll a press of button 1 and then no event of the reader's while it scrolls; from that press the view follows it till Chromium ends it, on the next press (which reaches the page only as a pointerup), a key (a keyup), or the lift after the mouse moved while held. A field the guard brings back into view after putting a scroll back is one focused since the reader last scrolled that view, not one they scrolled away from (`scrolledFrom`): the middle press focuses the button under it. See `view-middle-autoscroll.test.cjs`.
- A load that lands puts the view back where the reader left it (`show`'s `back` on the page's first reads, the Providers pages' reload after `render`): what it drew may have pulled the view up, the browser clamping it to a page shorter for a moment. While the reader scrolls, the place last heard is late (the page hears of a scroll a frame or more after it, WebKit scrolling on its own thread), and putting it back took their last steps and stopped their coast each time a load landed under the wheel, so Routing and Usage, scrolled as they opened, shook and snapped back (issennknight on Discord, macOS 13). `backAfterLoad` keeps a view that has gone on the way the reader's own scrolls go (`readerWay`, set from each scroll of theirs) as theirs, and puts back only a move against it, which is a redraw's clamp. The browser's scroll anchoring stays on: as data comes in above, it lowers `scrollTop` with what's on the screen still, which the reader never sees, so the test tells a put-back (the view up, what was under the pointer going down with it) from that.
- Where the window's WebKit scrolls under wheels it never tells the page of (macOS 13's, 16.6), each gesture's first scrolls were put back until three the same way made a run (`puttingBack`), a shake at the start of every gesture, the first on a page just opened included. The first run given back is remembered (`deaf`, kept in localStorage as `magpie.unheardWheels` for the next start); from then on a scroll at rest waits 120ms before it is put back (`waitsBack`), and one more the same way within 80ms (a frame or a few; code's two scrolls in a row come further apart and are both put back, `view-wheel-unheard.test.cjs`), with the page's height the same (no redraw between them), makes it the reader's at once. A lone scroll (a redraw's, code's) is still put back, 120ms later; one under a click's hold is put back at once as before. See `view-open-wheel.test.cjs`, which starts the wheel before the first data, tells the page of each scroll 80ms late or never, and counts every write that moves the view up under the wheel. What the wait puts back is recorded as an at-once put-back is, so a wheel heard later than 120ms still gives it back (`givesBack`).
- Typing never moves the page either. A textarea that grows with its text (the Library's `autosize` in plugins/library/client.js: a set's text, an agent's own text, a skill's description, a subagent's fields) is measured at height auto with what holds it kept at its height, so the page never goes short for a moment: a page gone short has its view's scroll clamped by the browser at every input, an input method's every composing step included, and the guard's putting it back pulled a textarea taller than the view to one of its edges (#2636, `library-ime-scroll.test.cjs`).
- A box that scrolls inside a page keeps the wheel at its ends: wheeled past its top or foot, the page stays where it was (碳碳双键 on Discord, Requests' list on Usage). `holds` in app.js marks every box between the pointer and its `.view` that scrolls vertically and overflows (`.holds`), on `pointerover` and on each wheel, and app.css gives `.holds` `overscroll-behavior-y: contain` (the Mac's bounce stays, on the box). It is not one CSS rule for every box because Chromium (WebView2, magpie web) holds the wheel in a box told to contain even when it doesn't overflow, and in one that scrolls only sideways, so a short list or a wide table would stop the page. `.holds` is under `:where()`, so a rule that passes the wheel on still does (`.mchips` in a short window, #833). The page itself (`.view`) and the document chain as before. An engine with no `overscroll-behavior` (Safari 15, macOS 12's WebKit until Safari is updated; Safari 16+, WebKitGTK and Chromium have it) gets the same from `compat.js`: a wheel over a `.holds` box at its end in the wheel's direction is cancelled, so there the Mac's bounce at the box's end is lost. A guard in `inner-scroll-contain.test.cjs` builds every rule in every stylesheet under assets that scrolls a box vertically from its own selector, fills it and points at it, and fails naming the file and selector of one that doesn't hold the wheel (a rule that sets `overscroll-behavior` back to auto, or a box `holds` doesn't reach). See `inner-scroll-contain.test.cjs`.
- There are no native `<select>` elements and no colored left-border stripes. State is shown with a dot or a swatch. A dot never says good or failed by colour alone: a failed state's dot is the same box drawn as an x (`--x-mark`, a mask over its red, scaled 1.5 so it reads as heavy as the round dot), since red and green dots read as one colour to a red-green colour-blind reader (coolmantou on Discord). That holds for Test models (`.tdot`), an agent's status line (`.ag-dot`), the check-in (`.ci-dot`), the request ledger (`.led .st .dot`), Routing's tries, steps and preview (`.rt-req`/`.rt-probe`, `.rt-steps`, `.ps-dst`) and a Library server's health (`.lib-health.err`); see `status-dot-shape.test.cjs`.
- What the window shows can be selected and copied, its controls can't, as in a Mac app (Outvi V on Discord: nothing could be copied into a bug report). The page is `user-select: none`, and two `:where()` rules after `body` in `app.css` make text content selectable (paragraphs, `pre` and `code`, a detail's values, hints and notes, every error class, a conversation, a request's story on Routing) and keep every control unselectable with what is in it (buttons, labels, tabs, menus, the navigation, grips). Both have no specificity, so a view's own rule still decides, as a list being sorted does; a field stays selectable. See `user-select.test.cjs`.
- The Usage page's cards and the tray panel's Allowances tab share one order, `settings.UsageOrder` (`byUsageOrder`). In the panel a balance-only card stands where that order puts it, and balances next to each other share one *Balances* grid (`panelBalances`, #1510); none is gathered after the plans. The panel's *Arrange* (`panelArrange`) moves rows in it and hides subscriptions from that tab alone (`settings.PanelUsageHidden`); both save through `POST /api/usage/arrange`, which changes only the field it is sent, and the Settings save keeps both. Hiding is display only: a menu bar cell's card shows though hidden (`panelPeek`). See `TestUsageArrangePanelHidden`, `panel-arrange.test.cjs` and `panel-balance-order.test.cjs`.
- An allowance card can be hidden from Usage › Overview (whqtian_07012 on Discord: StepFun's plan wanted, its balance not). Right-click a card, or press it long on a touch screen (`quotaCardMenus` in `plugins/usage/page.js`), and the app menu offers *Hide from Overview*; an account of a card with several says *Hide this account from Overview*, and a plan card carrying its key's balance (`keyCards`) also offers *Hide its balance from Overview*, which keeps the plan. Each is kept by its tray card id and kind (`cardHideKey`: `stepfun|balance`, `kimi|me@x|plan`) in `settings.UsageCardsHidden` through the same `POST /api/usage/arrange` (`cardsHidden`), and kept by the Settings save. `overviewCards` leaves them out before `keyCards`, so routing, caps, the counting, the Accounts list, the tray panel (which has its own *Arrange*) and the menu bar cells never hear of it; a cell clicked for a hidden card shows it until the page is left (`usagePeek`). Such a card's menu offers *Show in Overview*, not Hide again, since the "{n} hidden" line leaves it out while it shows. A "{n} hidden" line under the cards (`#quotaHid`, `renderQuotaHid`) opens them, each with *Unhide*, and *Show all* from two. The line is drawn in `--muted`, not `--faint`, so it can be read and found (3:1 or more), and while any are hidden every card's menu ends with *Show {n} Hidden*, which opens the line with the keyboard on it and brings it into sight on the menu's click (`openHidden`; yuu003348 on Discord found no way back). See `TestUsageArrangeCardsHidden`, `usage-cards-hide.test.cjs` and `usage-hidden-way-back.test.cjs`.
- A client in the Usage page's Agents list can be hidden from that list (ikunbots on X): its row's "…" button or right-click opens the row menu with *Hide from this list*, saved by client id in `settings.UsageAgentsHidden` through the same `POST /api/usage/arrange` (`agentsHidden`), and kept by the Settings save. The ledger, the totals and the chart still count a hidden client. A "{n} hidden" line under the list opens them, each with *Unhide* (`hideUsageAgent`), and while any are hidden the row menu ends with *Show {n} Hidden*, which opens it as the cards' does (`openHidden`). See `TestUsageArrangeAgentsHidden`, `usage-agents-hide.test.cjs` and `usage-hidden-way-back.test.cjs`.
- Usage › Overview's Agents, Models, Accounts and Gateway keys lists each fold under their heading (#2378): the heading is a `.fold` button with its chevron, as on the Gateway page, and the click doesn't move the page (`backToReader`). What is folded is kept per viewer in localStorage `magpie.usageFolded` (`mountUsageFolds`, `showUsageFolds`). The totals leave the cache out, as before, unless the tokens tile's label, a pop-up button (`usageTokensPick`, the app menu), is set to *With cache*: then the tile, the chart and every row's count and share add cache read and written (`usageTokens`), and the tile says the cache's count; kept in localStorage `magpie.usageWithCache`. The Sessions tab counts by the same pick (#2591): its tokens tile carries the same label pop-up and says the cache's count, and the list's rows, the projects and models bars and pickers, the day chart (whose day and week buckets carry the cache's counts), the calendar and the top sessions read `usageTokens` too, the top list ranked again by the count shown (`sessTokens`, `sessSums`, `renderSessChart`, `renderSessTop`); a pick made on that tab refreshes the tab, not the Overview. The per-session median and p90 stay counted in and out (`Stats.Overview`). The Requests tab's total already counts every token (`allTokens`). A token line in parts (the Requests strip's Tokens card, the Overview's and the Sessions tab's tokens tiles) wraps between its counts and keeps each whole (`usageParts`, `.parts`); only a count wider than its card on its own is cut. See `usage-overview-fold.test.cjs` and `sessions-cache-tokens.test.cjs`.
- A request's details in the Usage ledger say its tokens at every width (#1839, `ledDetail`): "{i} in · {o} out · {r} cache read · {w} cache creation", 0 said as 0, with how much was written for 5 minutes and for an hour when it says so. A failure that spent nothing has no Tokens row. See `ledger-cache-tokens.test.cjs` and `usage-ledger-detail.test.cjs`.
- The requests magpie makes by itself (a routing classifier's, a web search's, an image description's) are told apart from the agents' on the Usage page (#1839): magpie's row among the agents and its rows in the ledger carry a "magpie's own" mark (`ownBadge`), an account's, key's or model's row adds "{n} by magpie itself" from the summary's `own`, and an account's card says how many of the period's requests were magpie's own (`ownLine`, drawn again once the summary comes after the allowances). See `own-requests.test.cjs` and `TestSummaryCountsMagpiesOwnCallsApart`.
- Opening the allowances is asking to see them (#1588, `quotasOpened` in `app.js`): the Usage page from the nav or its Overview tab picked, the window brought back to the front on the Overview, the tray panel's Allowances tab picked or the panel brought up on it, and a menu bar cell clicked each load `usage/quotas?asked=1`, as Refresh now does. An asked read makes the backend read every account again (`AskClaudeUsage`), so an opening within 30s (`QUOTAS_OPEN_GAP`) of the last asked read sends an ordinary read instead, which joins one on its way; Refresh now always asks. A panel page loaded while it is put away (hidden, or without focus) doesn't ask. The timed refresh is unchanged and never asks. See `allowance-open-read.test.cjs`.
- The menu bar can show magpie's own totals beside the allowances (#1984): `usage:today` and `usage:7d` in `settings.TrayUsages`, offered after the cards in the same picker. `trayUsageCards` sums them with `usage.QueryPage(p, Filter{}, 0, 1).Sum` (the Requests tab's and the panel's cached ledger sum, gateway and session-file calls together) and asks no vendor when only totals are picked; `trayTotalCard` makes every token (in, out and cache, as `allTokens`), marked `Σ` so it doesn't read as the input, over the cost in Settings' currency, `+` when some calls had no price, the cost row left out when none had one. The Mac draws them as plain cells, the bird beside them saying whose. A click opens the Requests tab at the period (`quotaView`, `?tab=requests&period=`) or the panel's Usage tab (`panelUseFocus`). No 3-day total: the Usage page has no such window. See `TestTrayTotals` and `tray-totals.test.cjs`. Settings › *Menu bar cost* (`settings.TrayNoCost`, on by default, on every OS, per computer through `KeepOwn`) turned off leaves the money out (BussanQ on Discord): `trayMoney` drops the total's cost and its count of unpriced calls before `trayTotalCard` and the no-card tooltip `trayIdleTip` see it, so the cards, their tooltip and the idle tooltip say the tokens and requests alone. A relay's or subscription's balance card is money itself and stays. See `TestTrayNoCost` and `tray-cost.test.cjs`.
- The tray's icon is the bird alone where the bar's other icons are glyphs: the Mac's menu bar takes `tray.png` as a template image, and a Linux bar (StatusNotifierItem `IconPixmap`) gets the same silhouette in one colour on transparency (`trayIconFor`, `linuxTrayIcon` in [`trayglyph.go`](../../internal/gui/trayglyph.go)), so a bar that tints its icons by their alpha tints the bird, not a tile (ylorn on Discord). A bar that doesn't tint shows it light, or dark when the portal's `org.freedesktop.appearance` `color-scheme` prefers light (`traySchemeLight`), and it is drawn again when that changes (`watchTrayScheme`). No `IconName` is set: a host that found a `-symbolic` name and drew it untinted would show a black bird on a dark bar, with no pixmap to fall back on. Windows' tray keeps the app's coloured icon, unless a card is in the menu bar: Windows' notification area shows no text beside an icon, so the icon itself is the first card's figure (Benjaminliang_1 on X; `trayIconFigure`, `trayNumberIcon` in [`traynumber.go`](../../internal/gui/traynumber.go)). It is the first row of the first card whose first row is a percentage, used or left as Allowance display has it, through *Menu bar windows*' pick, its digits alone, drawn in Segoe UI Semibold (Go's bold where the font can't be read) at `SM_CXSMICON`, white on a dark taskbar and near black on a light one, as tall as fits with a pixel above and below, centred by its ink, and three digits narrowed into the square. A balance or magpie's totals are skipped for the next card; with none the icon is the bird. It is drawn again when the figure, the icon size or the taskbar's theme changes (`SystemThemeChanged` wakes the loop); the tooltip still says every card. Settings › *Tray icon number* (`settings.TrayNoNumber`, Windows' alone, on by default) keeps the bird when off. One icon is set for the current theme, not a light and a dark one: Wails' Windows tray shares one handle between them. Linux keeps its label and tooltip; the Mac its cells. See `TestTrayIconFigure`, `TestTrayNumberIcon` and `tray-windows.test.cjs`. With no cards in the menu bar the tooltip is magpie over today's total (`trayIdleTip`: every token, split into input, output and cache as the today card's tooltip is, then the cost and the requests), brought up to date with the cards' refresh; the tray's menu stays Open, the version and Quit. Wails sets the tooltip only on Windows: on Linux `setTrayTip` sets the StatusNotifierItem's `ToolTip` and sends `NewToolTip` ([`traytip_linux.go`](../../internal/gui/traytip_linux.go)), and on the Mac it is the status item's button's `toolTip` (`trayTipNative` in [`trayimage_darwin.go`](../../internal/gui/trayimage_darwin.go); NextCommit on Discord: no hover there showed anything). See `TestTrayIconPerOS`, `TestTrayIdleTip` and `TestMacTrayTipIsSet`. Every tooltip, the idle one, magpie's totals' and each card's (its use or what is left, the pace, when it resets, "as of …"), is in magpie's language, as the tray menu is: `watchTrayUsage` passes `trayLang(settings.Lang, systemLang)` to `trayCardsView`, `trayUsageCards` and `trayIdleTip`, whose words are in `trayWords` beside the menu's, and a change of language in Settings refreshes it at once (`onTrayUsage`). A window's name and an error are the card's own (yi_time on Discord: English on a Windows magpie set to Chinese; `TestTrayTooltipFollowsMagpiesLanguage`).
- A window's Pace is a number on its card's reset line (fflake33 on Discord): the share used less the share of the window gone by, in points, "Pace +12%" over an even pace and "−8%" under it, turned with the meter when it shows what is left, amber while more is used than an even pace would have (`quotaPace`, `paceLine`). The window's start is the gateway's, `startsAt` from `provider.WithWindowStarts` (its reset less its span, `QuotaWindow.Bounds`); a window whose span isn't known, with no limit, or a past cycle has none. Settings › *Menu bar pace* (`settings.TrayPace`, off by default, hidden on Windows, whose tray shows no text but the first card's figure as its icon) puts the same number beside each figure in the menu bar and in its tooltip (`trayPace`, `QuotaWindow.Pace`). See `TestWindowPace`, `TestTrayUsagePace` and `quota-pace.test.cjs`.
- Settings › *Menu bar windows* (`settings.TrayWindows`, StrikingRaw on X) picks which of a subscription's windows its menu bar card shows, keyed by provider id (every account of it, the in-use `|*` card too) and by window kind, not position: `trayWindowKind` names a window by its span ("5h", "1d", "7d", "month", "15m"), by `name:` its name when the span isn't known. Unset shows every window, as before; a pick none of whose kinds the card still has shows every window again (`trayShownKinds`), never an empty card. Only the bar's rows, the Linux label and Windows' icon figure are filtered; the tooltip still says every window. One row draws in the menu bar's own font (13pt regular), two stacked rows in 10pt regular, both with digits of one width (`mpRowFont`, `TestTrayRowFont`; qingnianxiaozhe on X found the old 9.5pt semibold and 11pt medium heavier than Surge's and iStat Menus' figures beside them). The GUI learns the kinds from `trayKind` on `/api/usage/quotas` (`withTrayKinds`; model and aside windows get none). The row lists a pill per ticked subscription with two or more kinds, opening the app's menu (All windows, then each kind), shown on Windows too, where the first card's pick decides the tray icon's figure; its code is in `traywindows.js`, imported when Settings draws ticked cards. Per computer through `KeepOwn`. See `TestTrayWindowsPicked`, `TestTrayWindowsKept` and `tray-windows.test.cjs`.
- In Usage → Allowances, Trends → Cycle offers each account's own recorded cycle menu beside its curve (`quotaCurve`, `quotaRecordedCycles` in [`app.js`](../../internal/gui/assets/app.js)). The longest window (usually weekly) owns the menu; shorter windows are drawn over the selected dates with their own reset breaks. `/api/usage/quotas/history?days=45` supplies all retained readings, including remote cards. Completed cycles come from observed boundaries, not seven-day arithmetic; an early reset ends the previous cycle at the vendor's new start if it fits between readings, otherwise at the first new reading. Each menu option gives its observed duration, so an early reset is not labelled as a full seven days. The selected meters, plot, legend, tooltip and dashed pace use that cycle's last readings, never today's percentages. A window without a reading in the selected interval says so instead of showing a current value. A local account's past cycle asks `/api/usage/quotas/history/holds?provider=&user=&from=&to=` once per selection (`quotaCycleHoldsOf`), which asks no vendor (#2009, NorethSea): `HistoricalWindowHolds` in [`window_holds_history.go`](../../internal/usage/window_holds_history.go) reckons each window's whole at its own last reading in that cycle, from the request log up to that reading, so the 5-hour figure is the cycle's last 5-hour window, never its short windows added up. Its tooltip names that window's start and the reading's time. It shows "Loading…" until the answer comes. A window it can't reckon says "not enough data" with the reason, as a current card does; a reply that comes after another cycle was picked is dropped, and a card's Refresh or an asked read reckons again. Historical meters show the cycle end and sampling time; current account actions, balances and policies are left out until returning to the current view. Accounts are independent, and a selection survives card refreshes in the same page. Trends → 2 days temporarily shows recent readings; Off removes the curves; the tray always shows its current cycle. See `quota-curve.test.cjs` and the [history limits and reset inference](providers-accounts.md#constraints-and-failure-behavior).
- The Usage page's cards are packed into columns, never all stretched to a row's height (`packUsage`, #860): each card in that order goes to the leftmost column that ends highest, on 1px grid rows, so short cards stack beside a tall one. The last card in each column then reaches down to where the tallest column ends (a `min-height`, cleared before the cards are measured again), so cards side by side in one row are as tall as each other and a stack ends with the tall card beside it (ikunbots on X); content stays at each card's top. The page's order (DOM, dragging, Alt+arrows) stays `UsageOrder`. The column count is the most 260px columns that fit, or one fewer when that leaves much less empty room; it is chosen again only when the cards change or the width fits another number of columns, so a card opened or folded stays where it is, even as the scrollbar comes in. See `usage-pack.test.cjs`.
- The Providers page has a search box over its list (#1614, `findProviders` in [`app.js`](../../internal/gui/assets/app.js)): the words typed keep the rows, on and off, whose text or data say them all: name, id, preset, addresses, every model's id and name, the account's user and plan and each login's, account names and key names. The Turned off fold stays open while searching; Escape or clearing the box shows every row and the fold as it was. A row that is on reorders by dragging its logo, Alt+↑/↓, Alt+Shift+↑/↓ (top, bottom) or its right-click menu (`openProviderMenu`: Move to top, Move up, Move down, Move to bottom). While searching, a step or a drop moves it among the rows shown (it takes the place of the row it passes, `stepProvider`, `shownToOn`) and the hidden ones keep their order; Move to top and Move to bottom go to the ends of the whole list. Every move saves the whole order through `POST /api/providers/arrange`. See `provider-find.test.cjs` and `provider-order.test.cjs`.
- Hide accounts (`window.hideAccounts` at the end of [`app.js`](../../internal/gui/assets/app.js)) masks every account name and address in the window's text and tooltips, its menus and dialogs too, by one switch for the whole window and the tray panel. Routing's, Usage's and Providers' buttons (`#rtMask`, `#usageMask`, `#pvMask` beside Providers' search box) and Settings › Privacy's row are that one switch, stored in `magpie.maskEmails`: turned on any page, every page is hidden and every button says so, and another window or the panel follows through the `storage` event (paultian in #feedback: Providers had a switch of its own, so one page was hidden while another wasn't). A `magpie.maskProviders` left by the versions that had Providers' own switch is folded in once and removed: hidden on either page then is hidden everywhere now. While it is not chosen, it follows Mask personal data, and turning that on turns it on (`provider-mask.test.cjs`). While it is on, an account's name on the provider editor's row (`accountNameButton`, and the one-account line) and on its Usage card (`quotaWho`), and on the tray panel's Usage card (`panelQuotaCard`'s `.pq-who`, in sight while the card is hovered), has an eye (`hideAccounts.eye`), in sight on hover or keyboard focus. A click shows that one account, its address and the name the user gave it together (`kin`), everywhere that page draws it, tooltips included, while the others stay hidden (yogurt36 on Discord, for a screen share). The accounts shown are a set held in memory that `hits()` skips; nothing stores them. The window and the panel are pages of their own, each with its own set: one shown in the window stays hidden in the panel and the other way round, so a screenshot of either shows only what was shown there. Closing the window hides them again: the page stays loaded while the window is hidden, so `hideMain` runs `mainClosedJS` (`hideAccounts.closed`) first. Putting the panel away hides the panel's again, from its `visibilitychange` (the one that drops `panelPeek`) (`account-eye.test.cjs`, `TestClosingHidesShownAccounts`). What it hides is the accounts: their addresses, their vendors' values (`user`, `account`, Routing's `who`) and a name that stands for one, a plugin's (`accountUsers`, a card's `alias` without `named`) or a seat's. The name the user gave an account (`AccountNames`; a card's `alias` with `named`, which `withAccountNames` sets) is no account and stays in sight, beside the account it names, hidden (faketrader in #3656: several accounts couldn't be told apart). A label is scoped by its element, never by its value: in the user's own name (`.own-name`), a plan (`.plan`, the Providers row's `.acct-plan`), a model (`.model-value`) and a Usage card's title (`.subscription-head > b`) only an address is hidden, so an Antigravity account whose value is "Google" leaves "Google AI Pro" readable while an address written in a plan is still hidden; the same holds for the tooltips of plans and models (`TIPS`), not for the own name's, which says the account (`LABELS` in app.js, `account-mask-alias-plan.test.cjs`). The tray panel card's tooltip joins the account and the plan in one string, and still hides a plan's word that is an account's value.
- A new user's Agents page (the window's first page and the panel's Agents tab) has a *Get started* card above the agents in the welcome's place (wangye1644294 on X, `guideCard` in [`app.js`](../../internal/gui/assets/app.js)). Its three steps are: add a model source; connect an agent, where the agents magpie found that can be connected are named and each is connected in one click (`agents/connect`); and, optional, a routing group, with the most harmless starter template that can be made from the user's models (`provider.Templates`, Never stuck first) added in one click through `POST /api/groups/template`. Every tick comes from the state: the models magpie serves (`served` in `/api/state`, groups left out), an agent wired, or a group that isn't automatic. None comes from the card having been seen. The card goes once the first two steps are done. Its close button saves `settings.GuideHidden` through `POST /api/settings/guide`, so the panel, the window and a reload agree, and the Settings save keeps it. See `getting-started.test.cjs`.
- A connected agent's row in the window opens and closes on a click anywhere on it that isn't one of its controls, as a disclosure row does on the Mac (NG_shiyu on X, `connectRow` in [`app.js`](../../internal/gui/assets/app.js)): the click is handed to the row's *Models {n}* link (`expandLink`), which stays the control the keyboard and a screen reader use and keeps `aria-expanded`. Buttons, links, inputs, anything with a role, the grip and the opened sheet (`.ag-exp`) keep their own clicks, and a click that ends a text selection does nothing. The panel's rows did this already (`agent-row-disclose.test.cjs`).
- An agent whose own model menu is the list picked in magpie (ZCode, Claude Desktop, Cursor Private Inference, …: `listsMenu`) says that list as its ticks are, on its button and on the panel row (`menuSaid` in [`app.js`](../../internal/gui/assets/app.js)): every model ticked is *All N models*, one or two by their names, more as *Showing x / N models*, none as *No models*. It never says the first names and a +N: routing groups come first, so that read as a pick of one group and one model while every model was on (NG_shiyu on X). Other agents' start pickers still say the one model or group they start on (`agent-menu-label.test.cjs`).
- Every page of the window keeps to one width, at most 1200px of content centred, with a 16px gutter in a narrow window (`padding: max(16px, calc(50% - 600px))` on each `#view-…`, #860). Routing, Usage and Sessions are in that rule too (面条 on Discord), so a large screen doesn't stretch them edge to edge while the others sit in the middle; their columns follow the room they get (`byWidth`). See `page-width.test.cjs`.
- Every dialog in `#modal` is 600px wide (`.modal .dialog`), a long text's editor 880 (`.wide`). A provider's editor (`drawEditor`, its accounts, meters and many rows) is `.roomy` and grows with a large window as a Mac sheet takes the room its content needs: 600 up to a 1071px window, then 56% of it, at most 880 (5046312 on Discord). `openModal` copies both classes from the content, so a question that takes the editor's place (Sign out's) stays 600. Its height already follows the content up to the window. See `editor-roomy.test.cjs`.
- The page must work in Chromium (Windows' WebView2) and WebKit (macOS, and WebKitGTK on Linux). GUI tests run in both engines.
- In the design of macOS 26 the main window has the corners of a window with a plain title bar, and its lights stay where the toolbar had them. `TestMainWindowCorners` ([`titlebar_darwin_test.go`](../../internal/gui/titlebar_darwin_test.go)) measures this in a process of its own, with package `windowshape`: after a new title and size and a new system appearance, as AppKit draws that appearance, where a first press with another app in front begins a window drag, and where a sheet begins. It runs that process as linked against the macOS 26 SDK and against macOS 15's, whatever SDK the toolchain has (`runAppKitSDK` and `stampSDK` give the copy it runs that SDK, and sign it again). In the earlier design, on an older macOS or with macOS 15's SDK, it checks that the window keeps its toolbar; `TestMainWindowRightToLeft` checks that it keeps it with the lights on the right. Both run in CI's macOS job. No test covers `UIDesignRequiresCompatibility`: AppKit reads it from an app bundle's Info.plist, LaunchServices registers each bundle that runs, and a press sent within a process run from one doesn't always begin a window drag.
- On Linux the panel is frameless and the main window has a hidden title bar (`plainTitlebar`). GTK 3 asks KDE's Wayland session (KWin's decoration protocol) for a frame drawn by KWin on any window without client-side decorations, an undecorated one too, so `ownFrame` tells KWin the panel draws its own on each realize (#1283). GTK 4 builds ask for none themselves. With no KWin title bar, and with a Wayland window placed by the compositor rather than by the icon, the panel's header is its drag handle on Linux, as the window's is (`--wails-draggable` in `app.js`; `panel-drag-linux.test.cjs`); on the Mac and Windows the panel isn't dragged (#1430).
- On Linux the panel is a solid window, as the main window is, and its page paints `--bg` whole (`panelOptions`, `body.linux.panel` in `app.css`). The Mac's and Windows' panels stay translucent. A see-through panel is an RGBA, app-paintable GTK window under a transparent webview, and on KDE Plasma 6 its page, painted at 93%, left a fainter copy of the old scroll position as it scrolled (Wayland and XWayland, compositing mode on or off) while the main window never did (#1803, `TestPanelSolidOnLinux`, `panel-linux-opaque.test.cjs`). Xvfb with picom on WebKitGTK 2.54 doesn't show the ghost either way, so the fix rests on the reporter's machine.
- A Wayland window can't place itself, so under Hyprland and niri the compositor is asked to put the panel under the bar. Under Hyprland `placePanel` gives it a window rule at the cursor (`omarchy.PlacePanel`) and closes it on a click outside (`omarchy.ReportClicks`). The rule names the monitor under the cursor and moves the panel to x, y from that monitor's corner (`omarchy.PanelAt`): Hyprland adds the window's monitor's place to a `move`, so a position across the whole layout put the panel off a monitor not at 0,0 (#1785, `TestPanelAtMonitorOffScreenOrigin`). The cursor and the clients' places that `omarchy.ClickedOutside` compares are both across the layout. niri (`omarchy.Niri`, `NIRI_SOCKET` set) takes no rules at runtime and doesn't report the cursor. There `omarchy.NiriPanel` watches niri's event stream and moves the panel each time it opens: the window of magpie's pid titled `panelTitle`, moved with `MoveFloatingWindow` (after `MoveWindowToFloating` if it was tiled). It goes 6 px under the working area's top, centred on the x the tray host passed to `Activate(x, y)`. Wails keeps that x in its Linux tray (`trayClick`, `TestTrayClickIsWails`), and it is taken across the bar, since that's all a Wayland bar knows. With no click (`magpie panel`), or with one off the output, it goes top right. Wails' `HideOnFocusLost` closes it, since a click on another window takes niri's focus. It still opens in the middle and slides up, and a bar at the bottom or the side isn't accounted for (#1670).
- Wails calls the tray's click handler both for the icon's click (`Activate`) and when the tray host opens the icon's menu (dbusmenu's "opened" event). KDE opens the menu on a right-click, and the panel that click opened took the focus and shut the menu on Wayland. On Linux the handler ignores the call when it comes from the menu opening (`menuOpening` in [`trayclick.go`](../../internal/gui/trayclick.go), which looks for Wails' `(*linuxSystemTray).Event` among its callers; `TestTrayMenuOpenFrameIsWails` fails if Wails renames it or stops calling the handler there) (#1430).
- On GTK 3 builds, the app gives GDK a font DPI before the first webview when it has none (`fontDPI`, [`fontdpi_gtk3.h`](../../internal/gui/fontdpi_gtk3.h)). GDK's unset value is -1, and WebKitGTK 2.54's GTK 3 settings reader multiplies it by 1024 unchecked, so every page lays out at a negative width with a 9000000px font (#1371). An unset DPI takes `gtk-xft-dpi`, or 96, times a valid `GDK_DPI_SCALE`; a positive one is left alone. Wails initialises GTK (its GtkApplication) only inside `Run` and makes each window from a default-idle-priority callback on the main loop, so `fontDPI`, called just before `Run`, queues the check as a `G_PRIORITY_HIGH` idle: it runs on the main thread after GTK's own start, before the first window. `TestFontDPIUnset` opens a real WebKitGTK page through `magpie_font_dpi` and needs a display (Xvfb).
- Tests never touch a live agent config. The package's `TestMain` runs under `testenv`'s home of its own. A macOS test that shows windows runs them in a process of its own through `runAppKit` ([`appkit_darwin_test.go`](../../internal/gui/appkit_darwin_test.go)). AppKit and WebKit take their home from the account, not from HOME, so `runAppKit` gives them a temporary one with `CFFIXED_USER_HOME`; appearance, contrast and languages still come from the account. The test fails when that process leaves a folder in the real `~/Library/WebKit` or `~/Library/Caches`. Playwright tests serve `assets/` with isolated `/api` fixtures.
- `POST /api/settings/codex-auto-review` accepts an empty value (Codex's own choice, or the session's model when Codex's sign-in is on a Free plan), `session` (the session's own model when it is one of magpie's; "Same as the session" in the picker), or an exact model/group ID in `provider.Served`, including unlisted providers. A known provider with an unknown or empty model name is rejected without changing the saved reviewer or catalog tag. Generic gateway request resolution remains permissive; it is not the validator for this setting. See `Handler` in [`api.go`](../../internal/gui/api.go) and `TestCodexAutoReviewSetting` in [`codex_auto_review_test.go`](../../internal/gui/codex_auto_review_test.go).
- `Settings -> Sync and backup` and `/api/davsync/{action}` configure and run encrypted WebDAV, S3 or GitHub setup sync. They share the CLI's operations and expose credential presence, never the secrets. State, merge, failure and compatibility behavior are described in [Sync and backup](sync-backup.md).

`openModal` makes the background inert when an editor opens fresh, leaving
confirmations and menus interactive through redraws. `closeModal` releases
that background as soon as the close animation starts, so clicks reach the
page while the editor fades away. Opening an editor during that animation
protects the background again. `ux-safety.test.cjs` checks these transitions.
Routing's New group buttons share `newGroup`, which asks before replacing a
dirty group draft; Cancel retains the draft and Discard opens the new editor.
`newGroupWith` applies the same guard when creating a group from a model and
waits for `show` to accept navigation before creating its draft.
The Routing page's head also has Routing groups (`#rtGoGroups`), since the
groups sit below every request. Its click goes through `scrollOnPurpose`,
brings `.rt-gsec` to the top of the view and focuses it; the page's layout is
unchanged. `fitGo` gives it only the head's spare room: named, its arrow
alone, or left out, so nothing else in the head moves or wraps for it
(`routing-groups-jump.test.cjs`).
Live, the Routing page's card (its head, story and context window) tells of
one request, `cur`, chosen by `told()` in `plugins/routing/client.js`: it
stays on the request it tells of until that one is done, then goes to the
newest, except that a newer request routing hasn't picked for yet does not
take it from one that was routed. Requests that come meanwhile still fly on
the stage, without a caption of their own. With several
conversations sending at once it used to follow the newest and jump between
them (qiang2040 on Discord, `routing-concurrent-hold.test.cjs`).
While two or more sessions send at once, a block for each sits under the
context window card (`renderCtxSessions`), so the card never moves when one
comes or goes. Each shows its session's newest reading,
keyed as the card's own line of prompts is (`sessionKey`, else the
conversation), named by the agent's title for it (`sessionName`), else
its folder's last level, never its id alone. A session is live while a
request of it is under way or its newest is within 15 minutes of the newest
request; at most six show, the newest session first, and a block keeps its
place while the sessions take turns. The block of the request the card
tells of is lit (`aria-pressed`); a click picks that session's newest
request. The card's footer names the session by its title too (tjhhhh on
Discord, `routing-ctx-sessions.test.cjs`).

The Plugins page updates the npm search-results section when a search answers,
leaving suggested plugin cards in place so a click begun before the answer still
opens the intended plugin. `plugin-market.test.cjs` holds the search response
between pointerdown and pointerup to check this in Chromium and WebKit.

### Routing shown by the magpie that serves the gateway

A magpie on this computer that does not serve the gateway shows the serving
one's routing in its Routing page, instead of the notice that its routing
cannot be watched here (#2606). The trigger is `served.Load()` being nil while
a magpie answers at the gateway's address. Then `/api/gateway/trace` reads
the serving magpie's `GET /v1/magpie/trace` over loopback
([`OwnerTrace`](../../internal/gateway/trace.go)) and answers with it as its
own (`mine` true, so the page plays it), and `/api/lanes` reads
`GET /v1/magpie/concurrency` ([`OwnerLanes`](../../internal/gateway/trace.go))
so a provider's rows show the same pills. Both are read on loopback alone
(`ownerURL`): a gateway address on another interface or host is never asked,
whatever `MAGPIE_ADDR` says, and one listening everywhere is read at
127.0.0.1. `OwnerTrace` lets a hung magpie go 5s past the 25s long poll, or
when the page stops asking; `OwnerLanes` waits 700ms, as `ServedBy` does, and
is asked only when a magpie answers. The state read carries the serving
magpie's `serves` and `pid` ([`gatewayJSON`](../../internal/gui/providers.go)),
and the trace carries them as `by`. The Routing page's heading names the
magpie from `by` ("serve (pid 49217) is routing · 127.0.0.1:3525"), so the
name goes with the trace: when the page's trace comes from another magpie
than before (a takeover, either way), it drops the routes it had and reads
the new one's whole trace. Nothing is written; serving and takeover are
unchanged. A
magpie that serves the gateway keeps its own trace, and one older than the
endpoint keeps the notice as before (a refused or unreadable read is unknown,
never empty: `OwnerTrace` says so with its second result). See
`TestConsoleShowsTheServingMagpiesRouting` and
`TestConsoleKeepsTheNoticeFromAnOlderGateway` in
[`trace_peer_test.go`](../../internal/gui/trace_peer_test.go), and
`TestGatewayTraceIsLoopbackOnly` in
[`trace_peer_test.go`](../../internal/gateway/trace_peer_test.go).

### A Quit asks while agents go through magpie

Quitting magpie ends the gateway, and each agent wired to it fails until
magpie runs again with nothing to say why: Codex retried "502 Bad Gateway"
on `/backend-api/codex/responses` five times and gave up, and its user
found magpie's `openai_base_url` in config.toml only after a long search
(Aisiverto on X). So a Quit the user chooses asks first when
`quitAgents` (`agent.Detected()` filtered by `Wired`) names any agent and
this process serves the gateway (`served`; when another magpie serves it,
that one keeps serving the agents and the Quit goes on).

- The tray's Quit and the page's Quit (`POST /api/window/quit` without
  `now`, on a host that is a `quitAsker`) call `AskQuit`. On the Mac,
  `askingQuit` puts magpie's own Command-Q item in place of the app menu's
  Quit role. `AskQuit` brings the window up on the question: it opens
  `/?ask=quit` when the window is gone or loading, else runs
  `window.askQuit()` in the loaded page.
- `quitask.js` (lazy-loaded, so it adds nothing to the core) reads
  `GET /api/quit` and draws the sheet: the agents, the sentence that they
  stop while magpie is closed, a note that closing the window keeps magpie
  running (the menu bar on a Mac, the system tray elsewhere), and Disconnect
  and quit, Cancel, and Quit (focused). Quit posts `window/quit?now=1`.
  Disconnect and quit disconnects each agent in turn, as its row's
  Disconnect does, and a failure leaves magpie open, named in the status
  line. With nothing connected any more it quits without drawing.
- A second Quit within a minute of the question (`quitNow`, `quitAgain`)
  quits, so a window that can't show the question doesn't trap the user.
- Don't ask again (zhujiu on Discord), a tick above the buttons, unticked
  each time, goes with the Quit or Disconnect and quit chosen as
  `noAsk=1`: the handler saves `settings.QuitNoAsk` (`SetQuitNoAsk`, that
  field alone) before quitting, and a save that fails keeps magpie open.
  Cancel saves nothing. With `QuitNoAsk` set, `quitAtOnce` quits without
  asking; a settings file that can't be read asks. Settings › General ›
  Ask before quitting (`#quitAskRow`, the app's only) shows it as On/Off
  and saves `quitNoAsk` through `POST /api/settings`, which keeps it when a
  page doesn't send it.
- Not asked: a restart to update, Uninstall, a browser's page (`magpie
  web`), logging out or shutting down, and the Dock's Quit (AppKit's
  `terminate:`).

See `TestWindowQuitAsksFirst`, `TestQuitListsWiredAgents`,
`TestQuitAPIListsAgents`, `TestQuitNow`, `TestCommandQAsksFirst`,
`TestQuitDontAskAgain`, `TestSetQuitNoAsk`, `TestQuitNoAskKeptUnlessSent`,
`quit-ask.test.cjs` and `quit-ask-setting.test.cjs`.

### A restart to update hands the gateway over

On a Mac and on Linux, a restart into a downloaded update (the Update pill,
Settings › Version, the tray's Restart to update, Restart automatically)
starts the new version beside the running one and hands it the gateway's
listening socket, rather than ending the gateway and waiting for the new
one to listen again (xuanwo on X). Nothing an agent sends meanwhile is
refused: the port never closes, and requests already in flight finish in
the old process.

The trigger is `handsOver` in
[`handover.go`](../../internal/gui/handover.go): the app (`Run` sets
`handoverHost` and `startExe`), on darwin or linux, serving the gateway
itself, not a dev backend (whose own handover is `gateway.Handover`,
below) and no `MAGPIE_NO_HANDOVER` in the environment. `restartInto` in [`update.go`](../../internal/gui/update.go)
then calls `beginHandover` instead of the relaunch-after-exit. The steps:

1. The old magpie opens an Offer ([`internal/handoff`](../../internal/handoff/handoff.go)):
   a unix socket in a 0700 temporary folder (in `/tmp` when `TMPDIR`'s
   path leaves a socket's name no room, `sockDir`), named by `handover.json` in
   the config folder with its pid, its program path and the time. Then it
   starts the new version beside itself: `open -n` on the bundle on a Mac
   (`update.OpenBeside`), the binary at the path it started from on Linux
   (`update.StartBeside`; `startExe`, since `/proc/self/exe` reads
   "(deleted)" once the update replaced it).
2. The new magpie's `Run` calls `takeOver` before anything else: a pointer
   fresh (2 minutes), of a process still running, for the same program and
   not its own is dialled, the new one says hello, and receives the
   listening socket (SCM_RIGHTS) with the address, the bridge's callback
   socket and the old pid (`handoff.Take`). The peer's uid is checked on
   accept. `gateway.Inherit` keeps the socket for the next `ListenAndServe`
   on the same address (`sameAddr`; another address lets it go), which
   serves on it and then acks.
3. The old one, on the ack (`AwaitAck`), withdraws the pointer, hides
   itself where it can (below), calls `gateway.Server.HandOff`, ends its
   backend and waits for its requests, then quits. `HandOff` closes the
   old one's own copy of the socket at once, so every connection from
   then on waits there for the new one: one the old one took as its end
   began was closed unanswered (#3568). A handed-off server's shutdown
   waits up to `handedOffDrain` (15 minutes) for requests in flight, not
   the 2s an ordinary quit gives them (`shortDrain`), and first lets each
   connection it took send its first request (`unreadWait`, 5s at most),
   since `http.Server.Shutdown` closes one whose request it reads after
   it began. Its `OnShutdown` then skips what goes with the gateway,
   which the new one now does: ending Codex's failover, porting Codex
   rollouts and installing the update.
4. Should the Offer not open, the new version not start, not say hello in
   60s (`helloWait`), not ack in 90s (`ackWait`), or drop the connection,
   the old one keeps the socket, takes its instance lock back and restarts
   as before: the new one opened once it has exited (`fail`,
   `update.Relaunch` or `relaunchBinary`). It first waits, still serving,
   for its requests in flight to finish (at most `fallbackWait`, 15
   minutes), since the restart was let go without waiting for them. A
   second restart click while a handover is under way does nothing
   (`pending`).

Once handed over, the old one does nothing beside the new one but finish
its requests (#4938): its loops end with its backend (`backendCtx`):
the linked instruction sets' sync (`library.KeepLinked`), quota alerts
(`provider.WatchQuotas`), the menu bar's usage and the update check and
automatic restart (`updates.start`); its hidden pages are let go as in
lightweight mode (`lighten`, so their polls stop); and it exits by the end
of its drain and `quitGrace` (3 minutes) after, whatever holds its quit up
(`quitBy`, `TestHandedOverQuitsByItsDeadline`). Before, a copy an update
left lived on hidden for three hours, writing and fetching beside the new
one.

Two magpies never show at once. On a Mac the old one lets go of its
instance lock before it gives the socket (`releaseInstance`), so the new
one's `runningAlready` takes it, and once the ack comes the old one hides
its menu bar icon, panel and window and leaves the Dock (`onHandedOver`,
`leaveMenuBar`). AppKit keeps a status item's `setVisible:NO` in the
app's defaults by its autosave name (`NSStatusItem VisibleCC Item-0` on
macOS 26), which the new one's item and every later one start from, so
the hide is forgotten once it is made (`forgetTrayHiddenLater`,
[`trayshown_darwin.go`](../../internal/gui/trayshown_darwin.go)). From
v0.1.1354 to v0.1.1367 it wasn't, and the icon stayed gone after the
update, through a quit and reopen too (Mitsuha, audie). `bringTrayBack`
forgets a kept hide once per computer before the tray is made, and a
magpie handed over to by an older one forgets it again once that one has
quit (`awaitExit`), unless a hide was kept when it started: one kept after
the bring-back is the user's (`TestHandoverLeavesTheMenuBarIconForTheNextMagpie`).
On Linux the D-Bus name Wails holds can't be let go, so the new one serves
the gateway at once but waits for the old pid to end (`awaitExit`, at most
`handedOffWait`, 16 minutes) before it makes its app and tray; the old one
keeps its tray icon until it has drained.

What the old process alone has:

- Sticky routing (which account each conversation stuck to) is flushed to
  disk before the socket is given (`gateway.FlushSticks`).
- Claude Code runs the subscription bridge started send their tool calls
  to the gateway's port, which the new one now serves. The old one serves
  them on a unix socket in the Offer's folder (`ServeCallbacks`), and the
  new one forwards a claude-mcp token it doesn't know there
  (`ForwardCallbacks`, `forwardCallback`; `ownCallback` lets them in while
  forwarding is on). See [Claude subscription bridge](claude-subscription-bridge.md#a-restart-to-update).
- Bridge turns can't move to another process, so a restart that hands
  over waits only for them (`restartBusy`, `blocking`: `Busy.Bridge` as
  Requests, `Tools`, `BridgeLast` as Last). The page is told
  (`updateJSON.Handover`) and says it waits for the Claude subscription's
  replies, with other requests carrying on across the restart.
- Refreshes of rotating OAuth tokens hold a file lock across both
  processes (see [Providers and accounts](providers-accounts.md)).

Not carried over, accepted: each process counts its own requests per
minute and concurrency, so for the drain a provider's limits can be
exceeded by what the old one still has in flight; a bridge run idle
between turns ends with the old process, and the next turn starts a new
run as after any restart; the new one's start-up migrations run while the
old one still serves its last requests.

Windows has no SCM_RIGHTS for a listening socket in this form, and
`magpie web` re-executes itself in place; both restart as before, waiting
for the gateway to be idle (#577). `make dev` keeps its own handover, a
new backend listening beside the old one (`gateway.Handover`, `devBackend`).

`magpie update` in a terminal restarts the app the same way
(gaubeebangeel on X: an agent the gateway serves ran the update at 4am,
and the app ran on as the old version until it was quit and reopened,
which cut the agent off). Once it has put the new version in
(`updateCmd` in [`update_cli.go`](../../internal/cli/update_cli.go)),
`restartRunning` posts the version to `gateway.RestartPath`
(`/v1/magpie/restart`, [`restart.go`](../../internal/gateway/restart.go)),
answered only to a program on this computer over loopback, never to a
page or another Host (`restartRefused`, the heap profile's rule). The
updater's `restartBeside` ([`update_beside.go`](../../internal/gui/update_beside.go))
restarts only when it was the copy replaced (`replaced`, which now also
compares a Mac's `.app` binary, `bundleExe`), takes the version as
`ready` without asking the feed, and runs Settings' AutoRestart restart
(`autoRestart`): at once when the restart cuts nothing short, else
through `waitIdle`. It answers `restarting`, `waiting` (with `Busy`),
`current` (another copy) or `unsupported` (`magpie serve`, which has no
restarter and is restarted the way it was started), and the command says
which. A magpie older than this answers 404, and the command says to
quit and reopen it, so the first update into this version still needs
that. On Windows, with no handover, the restart waits for an idle
gateway as above, so nothing in flight is cut but the port is closed
while the new version starts (#4521). See `TestUpdateInATerminalRestartsTheAppWithoutCuttingIt` in
[`update_beside_unix_test.go`](../../internal/gui/update_beside_unix_test.go),
`TestRestartAskedOnlyFromThisComputer` in
[`restart_test.go`](../../internal/gateway/restart_test.go) and
`TestUpdateAsksTheRunningMagpieToRestart`.

The app and `magpie web` open together each have an updater of their own
over the same program, and the gateway's ask reaches only the one serving
the gateway (visamzh on X: one restarted to update, and the other ran on
as the old version, its Update pill still offering the version already
in). So every updater also looks at its binary every 5 s
(`followReplaced` in [`update_beside.go`](../../internal/gui/update_beside.go),
`replacedTick`) and, once another magpie replaced it, restarts into it
through `restartBeside` once, whatever Settings' automatic restart says:
an old version left running is what the user asked to leave behind. A
gateway busy has it wait as above (`waitFor` restarts into what is on
disk without asking the feed again), a process restarting into its own
install (`installed`) or already following one (`followed`) doesn't, and
a build that isn't a release (`update.Released`; `make dev`, `go build`)
is never followed. A browser's page (`magpie web`, the app's page) learns
the version that served it from boot.js (`bootPrefs.version`) and reloads
when `/api/update`'s `current` names another (`servedAnew` in app.js), on
the reads it already makes (load, focus, coming back into view) and once a
minute while it is shown; the app's own windows come back with their new
process. See `TestAnotherMagpiesUpdateRestartsThisOne`,
`TestOwnUpdateIsNotFollowedAgain`, `TestOwnBuildRebuiltIsNotFollowed`,
`TestAnotherMagpiesUpdateWaitsForTheGatewayThenRestarts`,
`TestUpdateClickedOverAnothersIsNotFollowedAgain` and
`TestBootNamesTheVersionThatServedThePage` in
[`update_follow_test.go`](../../internal/gui/update_follow_test.go), and
`update-served-anew.test.cjs`.

See `TestRestartHandsTheGatewayOver` (the new version a second process,
a stream across the restart finishing whole, requests during it answered,
and the old one quitting only after its stream) and
`TestHandoverNotMadeRestartsAsBeforeOnceIdle` in
[`handover_unix_test.go`](../../internal/gui/handover_unix_test.go),
`TestRestartWaitsForWhatAHandoverCantCarry` in
[`handover_test.go`](../../internal/gui/handover_test.go), the gateway's
[`handover_test.go`](../../internal/gateway/handover_test.go), the
socket passing in [`handoff_test.go`](../../internal/handoff/handoff_test.go),
and `update-wait.test.cjs`.

## Verification

The session readers include Cursor CLI chat stores, Cursor editor project
transcripts and older editor `cursorDiskKV` conversations. CLI stores win
when the same session ID has a project transcript. Editor conversations are
read only and cannot be resumed by cursor-agent. A chat the editor still keeps
in `state.vscdb` (its `composerData:<id>`) is never deleted, the delete API
included: Cursor's own delete clears several keys of a database it holds open,
and its in-memory copy, which magpie can't do from outside. A project
transcript whose chat the editor no longer keeps (`fileMore.loose`: the store
was read and has no such chat, or there is no store) is files of its own and
is deleted as a CLI chat's are: `<id>/`, `<id>.jsonl` and `<id>.txt` in its
`agent-transcripts` folder (`cursorEditorPaths`), never the folder. A store
that can't be read says nothing, so no transcript is loose then. Each row of
an agent magpie deletes from has its delete; one it can't delete carries
`undeletable` (`cursor-editor`, `read-only`; `rowDeletable`), and the row's
delete is shown off with that reason as its tooltip, never left out (ysm119 on
Discord). A row without Resume, its terminal, Continue in, and a folder without
its ⋯, keep hidden room for them (`lanes`, `slot` in plugins/sessions/client.js),
so the right-hand columns line up on every row (`TestCursorRowsSayWhetherTheyCanBeDeleted`
in `cursor_editor_delete_test.go`, `sessions-cursor-align.test.cjs`).
Project transcripts contain no message timestamps or billing data; their
existing file modification time supplies the activity date, and tokens and
cost are not inferred. Database bubbles retain their own timestamps.
The editor database is opened as one read-only snapshot per listing, shared
by its chats and removed when the listing ends. Metadata changes invalidate
the cached summary even if the project transcript has not changed. See
[`cursor_editor.go`](../../internal/sessions/cursor_editor.go),
`TestCursorEditorTranscript` and `TestCursorEditorLiveDatabaseAndMetadataRefresh`
in [`cursor_editor_test.go`](../../internal/sessions/cursor_editor_test.go).

Messages without tokens or an active-time estimate still count as sessions
in the overview. Cursor and OpenCode conversations can be opened through
the existing transcript API, including CLI reasoning and tool calls. So can
Kimi Code's main agent, from its `wire.jsonl`: a turn's `turn.prompt` and
loop events from protocol 1.4 on (so a turn still running shows), and the
untimed `context.append_message` records of a session imported from
kimi-cli; Kimi Code's own timed messages repeat those and are skipped
([`transcript_kimi.go`](../../internal/sessions/transcript_kimi.go),
`TestKimiCodeTranscript`). Each reply in the conversation names its model
(#2253), muted after its first part and again where it changes inside a
reply: a part's `model` is what its own line in the agent's file names,
Claude Code's `message.model` (the model that answered; none for its own
`<synthetic>` lines), Codex's `turn_context.model` (the model the turn was
sent to), Pi's and omp's `message.model`, OpenCode's `modelID` or OpenCode
2's `model.id`. Kimi Code (whose model is only on a `usage.record` after
the reply), Cursor and a gateway transcript (which records the model asked
for, not the one that answered) name none, and nothing is guessed in their
place. The Usage page's per-request content doesn't repeat it. See
`TestTranscriptModel*` and `sessions-talk-model.test.cjs`. When the
selected date range is empty but
matching history exists, Usage offers **All sessions**, preserving the agent,
model, folder and search filters. See `sessions-empty-range.test.cjs`.

```sh
go test -tags nogui ./internal/gui
go test -run TestMainWindow ./internal/gui
GOARCH=amd64 CGO_ENABLED=1 go test -run TestMainWindow ./internal/gui  # as for an Intel Mac
go test -run TestWebView2 ./internal/gui   # on Windows: Wails' runtime lookup, missing and present
go test -v ./internal/fonts         # native discovery on each desktop OS
make test-ui                      # every internal/gui/tests/*.test.cjs, Chromium and WebKit
BROWSER=webkit node --test internal/gui/tests/click-scroll.test.cjs
```

`make test-ui` needs Node.js and Playwright (`playwright install chromium
webkit`). Set `NODE_PATH` when Playwright is installed outside the repo. The
Test workflow in CI does not run this suite, so a GUI change must run it
locally and report the result. See [`tests/README.md`](../../internal/gui/tests/README.md).

`TestMainWindowCorners` and `TestMainWindowRightToLeft` run on a Mac with a
desktop session, as CI's macOS job has, and show a few windows for some
seconds. On macOS 26 they check both designs, whatever SDK the toolchain
links against. CI builds them for arm64. Built for amd64 (under Rosetta on
Apple silicon), they check a program that names its SDK in
`LC_VERSION_MIN_MACOSX`, as go test links one for an Intel Mac. Their
presses are sent within the test's own process, and no system setting
changes.

### Agent visibility management

The Agents page's Manage button sits inside the list's footer beside the fold
control, and stays available when nothing is folded. The tray panel keeps its
existing fold control. The Manage agents dialog includes folded and hidden agents.
Search matches names and IDs; select all and invert apply to the search results,
while Select unconnected agents replaces the selection with matching agents not
on magpie (neither wired, added nor drifting). Selection survives search changes.
Hide selected and Unhide selected each save one arrangement through the existing
`POST /api/agents/arrange`, preserving order and agents outside the operation.
Only a successful save changes the list; a failure keeps the selection for retry.
While saving, Done, Escape and the backdrop cannot close the manager and expose
conflicting row actions. Closing is restored on either success or failure.
A completed save invalidates older in-flight state reads for the arrangement
fields only. Fresh reads still accept other windows' visibility changes; bulk
unhide does not add a lasting client-side override. These races are covered by
`agent-visibility-saving.test.cjs`.
While the manager is open, state refreshes update its visibility labels and
action availability in place, preserving the search, selection and keyboard focus.
These operations never connect, disconnect or edit an agent's files. Unhiding
still follows the existing Not set up folding rules. The dialog explains that
restoring visibility does not pin an unconfigured agent into the main list.
See `agent-bulk-visibility.test.cjs` for persistence, partial selection, failed
saves, narrow layouts and all five languages in Chromium and WebKit.
