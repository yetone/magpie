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
| Native host | `Run` starts the Wails v3 app: the tray icon, the panel, the main window, single-instance handover, `magpie://` import links and open at login. `host` implements `Windows` for the API: show and hide, open URLs and folders, copy to the clipboard, fit and tint the panel, and set the text size. | [`app.go`](../../internal/gui/app.go), [`mainwindow.go`](../../internal/gui/mainwindow.go), [`traymenu.go`](../../internal/gui/traymenu.go) |
| Backend | `startBackend` starts the gateway this process serves, unless another magpie already serves it. In that case this process only shows the other one's status (`gw` is nil). `stopServing` drains requests in flight. | [`backend.go`](../../internal/gui/backend.go) |
| API and assets | `Handler` serves `assets/` (embedded with `//go:embed assets`) and the `/api/*` routes, such as `/api/state`, `/api/set` and `/api/agents/{action}/{id}`. `held` lets one API request's reads share one catalog build (`provider.Hold`), and drops it after any write. `revalidated` and `versionedPage` name each script by its content hash, so a cache never serves an old `app.js` under a new page. | [`api.go`](../../internal/gui/api.go) |
| Browser mode | `StartWeb` serves the page and the gateway for `magpie web`. The browser signs in with a run key in a link. Gateway mode shows Providers, Gateway, Routing, Usage, Sessions, Plugins and Settings on a machine that is only a gateway. | [`web.go`](../../internal/gui/web.go), [`gatewaymode.go`](../../internal/gui/gatewaymode.go) |
| The page | `app.js` keeps one state object per view and renders it into a list, with no framework. The views have their own files: `routing.js`, `library.js`, `plugins.js`, `sessions.js` and `context.js` (Usage's Context tab, and the context window card the Routing page draws under a request's story, fed by `/api/context` in `context.go`, which scores and tags each agent; its "Tuned for you" cards are fed by `/api/tune` in `tune.go`, see [agent wiring](agent-wiring.md)). Strings go through `t()` with translations in `i18n.js` (zh, zh-TW, ja and de; the English text is the key and the fallback). zh-TW is Traditional Chinese with Taiwan's words; a system language of zh-TW, zh-HK, zh-MO or any zh-Hant picks it, and the tray menu and notifications follow (`trayLang`). Dropdowns use `openProtoMenu`. Every app menu (`openProtoMenu`, `openRowMenu`, `openSessCombo`) is placed by `placeMenu`: under its anchor, or over it when the window runs out below; one that fits neither way takes the larger room, its height held to it, and scrolls inside itself, and a scroll inside a menu doesn't close it (#1437, `removed-groups-menu-scroll.test.cjs`). Scrolling is done only by `scrollOnPurpose(e)` with the reader's event. | [`assets/app.js`](../../internal/gui/assets/app.js), [`assets/i18n.js`](../../internal/gui/assets/i18n.js) |
| No GUI build | `-tags nogui` builds magpie without the GUI: `hasGUI` is false and `runGUI` says to use `magpie tui`. The files of `internal/gui` that need Wails build under `!nogui`. | [`gui_off.go`](../../gui_off.go) |

## Runtime path

1. `magpie` with no arguments, `magpie app` or `magpie tray` calls `runGUI`, which calls `gui.Run`. A `nogui` build runs the TUI instead. A second launch hands over to the instance already running.
2. `startBackend` brings up the gateway, or finds another magpie serving it.
3. The panel and the window load `index.html` from `Handler`. `boot.js` passes the saved language, theme, text size and desktop font choices before the first paint.
4. The page reads `GET /api/state` and writes through `POST` routes. Each write answers with the new state, built under `held`.
5. Closing the window hides it. On the Mac a full-screen window first leaves full screen (`closeStep`). The system's close on the panel (a title bar's X, Alt+F4, a window manager's close key) hides it as Escape does, so the tray icon opens it again; a panel lightweight mode let go, no longer `h.panel`, closes (`makePanel`, `TestPanelCloseHidesIt`). Lightweight mode lets go only of a window closed this way: one covered by another app's window, minimised, or open as magpie was hidden is still open (`windowUp`, not Wails' `IsVisible`, which is the occlusion state), and so is it for the Dock while the window is open and for a restart (#1381, `TestCoveredWindowStaysUp`).
6. The window opens as it was last left. Its settled size and whether it was maximised are kept in `settings.Window` and `settings.WindowMaximised` (`settle`; per machine, see `KeepOwn`). A maximised window keeps the size it restores to. `makeMain` opens it at that size (`openSize`). On its first show, `placeMain` maximises it again on the Mac and Windows (on Windows once the page has come). On Linux, `makeMain` makes it maximised with `StartState`. On Windows a size larger than the screen's work area is fitted and centred (`fitRoom`), and the larger size stays kept. The window's position is not kept.
7. The page's header is the window's title bar. `plainTitlebar` sets it up once the app has started, and again on a window lightweight mode made again (`madeAgain`). On Linux it hides GTK's title bar and keeps the shadow and the resize edges; GTK 3 leaves the corners square. On the Mac the traffic lights are inset into the header (`MacTitleBarHiddenInset`). In the design of macOS 26 a window with a toolbar has larger corners than one with a plain title bar, so `insetLights` ([`titlebar_darwin.go`](../../internal/gui/titlebar_darwin.go)) takes the toolbar off, and `MagpieLights` keeps the lights and the title bar's height where the toolbar had them: after a resize, a new title, light or dark, more contrast or less transparency, and leaving full screen. A sheet, such as the folder chooser, still begins below the title bar. A first press while another app is in front still moves the window from anywhere in that title bar (`firstPress`), as AppKit moved it with the toolbar; AppKit itself now does so only in a plain title bar's top 32pt. AppKit gives a program that design from macOS 26 when the program is linked against the macOS 26 SDK or a later one and its Info.plist doesn't ask for the earlier design (`UIDesignRequiresCompatibility`); `designOf26` reads each at run time, as the SDK depends on the toolchain that built magpie. The toolbar stays in the earlier design, where it doesn't change the corners, with the lights on the right (a right-to-left language), or when the title bar isn't laid out as expected; this is decided once, when the window is set up. In full screen the title bar is AppKit's own. Windows keeps its own title bar.

## Constraints and failure behavior

### Routing purpose filter

The request heading's purpose menu in [`routing.js`](../../internal/gui/assets/routing.js)
uses `openProtoMenu`'s live checkbox selection: each tick immediately shows
requests matching any selected purpose, with the menu kept open. An empty
selection, All purposes or Clear filter restores every purpose without
changing the day or request/session view. A live menu stays open when its
redraw clamps the list's scroll; the reader scrolling outside still dismisses it.
Choosing All purposes closes the menu and returns keyboard focus to the
purpose button without scrolling; unticking the last checkbox keeps the
menu open and focused on that checkbox.
Counts, the current story and replay
follow the same filtered list. Selected purposes remain available when a day
has no matching requests. Opening a request from another page clears the
filter only when that request is excluded. Usage's purpose picker remains a
single choice sent to the ledger API. See `purpose-filter.test.cjs` and
`routing-purpose-state.test.cjs`.

### Routing mode chip

The stage's chip and the Accounts and keys heading in
[`routing.js`](../../internal/gui/assets/routing.js) name how a route
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
[`routing.js`](../../internal/gui/assets/routing.js) (`FOLD_AT`,
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

### Desktop fonts

Settings → General offers independent interface and code fonts, each with
the installed family's actual styles. `GET /api/fonts` calls
[`fonts.List`](../../internal/fonts/fonts.go): DirectWrite on Windows,
CoreText on macOS and Fontconfig on Linux. Discovery is lazy, cached in the
handler, and refreshed by the menu. It returns names and CSS traits, never
font files or paths. A failed refresh answers 503 and preserves the previous
collection; an empty successful collection is distinct from failure.
`magpie web` and `nogui` do not expose this desktop catalogue.

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

### Shared UI rules

- Settings' search-provider picker offers **Off**, saved as `searcher: "off"`. Its help explains API-only search and suppresses provider fallback/own-search hints while off; the search priority row is hidden because no provider competes with the APIs. See [gateway search behavior](gateway-routing.md#turning-provider-search-off), [`renderSearcher`](../../internal/gui/assets/app.js), `TestSettingsSearchProviderOff` and `searcher-pick.test.cjs`.
- Every user-visible string has zh, zh-TW, ja and de translations with the same placeholders. `gui-zh-tw.test.cjs`, `gui-ja.test.cjs` and `gui-de.test.cjs` fail on a missing one; `gui-zh-tw.test.cjs` also fails on a zh-TW string with a Simplified character left in it.
- A click never moves the page. Code scrolls a view only with the reader's event in hand (`scrollOnPurpose`); `click-scroll.test.cjs` guards this.
- There are no native `<select>` elements and no colored left-border stripes. State is shown with a dot or a swatch.
- The Usage page's cards and the tray panel's Allowances tab share one order, `settings.UsageOrder` (`byUsageOrder`). In the panel a balance-only card stands where that order puts it, and balances next to each other share one *Balances* grid (`panelBalances`, #1510); none is gathered after the plans. The panel's *Arrange* (`panelArrange`) moves rows in it and hides subscriptions from that tab alone (`settings.PanelUsageHidden`); both save through `POST /api/usage/arrange`, which changes only the field it is sent, and the Settings save keeps both. Hiding is display only: a menu bar cell's card shows though hidden (`panelPeek`). See `TestUsageArrangePanelHidden`, `panel-arrange.test.cjs` and `panel-balance-order.test.cjs`.
- The page must work in Chromium (Windows' WebView2) and WebKit (macOS, and WebKitGTK on Linux). GUI tests run in both engines.
- In the design of macOS 26 the main window has the corners of a window with a plain title bar, and its lights stay where the toolbar had them. `TestMainWindowCorners` ([`titlebar_darwin_test.go`](../../internal/gui/titlebar_darwin_test.go)) measures this in a process of its own, with package `windowshape`: after a new title and size and a new system appearance, as AppKit draws that appearance, where a first press with another app in front begins a window drag, and where a sheet begins. It runs that process as linked against the macOS 26 SDK and against macOS 15's, whatever SDK the toolchain has (`runAppKitSDK` and `stampSDK` give the copy it runs that SDK, and sign it again). In the earlier design, on an older macOS or with macOS 15's SDK, it checks that the window keeps its toolbar; `TestMainWindowRightToLeft` checks that it keeps it with the lights on the right. Both run in CI's macOS job. No test covers `UIDesignRequiresCompatibility`: AppKit reads it from an app bundle's Info.plist, LaunchServices registers each bundle that runs, and a press sent within a process run from one doesn't always begin a window drag.
- On Linux the panel is frameless and the main window has a hidden title bar (`plainTitlebar`). GTK 3 asks KDE's Wayland session (KWin's decoration protocol) for a frame drawn by KWin on any window without client-side decorations, an undecorated one too, so `ownFrame` tells KWin the panel draws its own on each realize (#1283). GTK 4 builds ask for none themselves. With no KWin title bar, and with a Wayland window placed by the compositor rather than by the icon, the panel's header is its drag handle on Linux, as the window's is (`--wails-draggable` in `app.js`; `panel-drag-linux.test.cjs`); on the Mac and Windows the panel isn't dragged (#1430).
- Wails calls the tray's click handler both for the icon's click (`Activate`) and when the tray host opens the icon's menu (dbusmenu's "opened" event). KDE opens the menu on a right-click, and the panel that click opened took the focus and shut the menu on Wayland. On Linux the handler ignores the call when it comes from the menu opening (`menuOpening` in [`trayclick.go`](../../internal/gui/trayclick.go), which looks for Wails' `(*linuxSystemTray).Event` among its callers; `TestTrayMenuOpenFrameIsWails` fails if Wails renames it or stops calling the handler there) (#1430).
- On GTK 3 builds, the app gives GDK a font DPI before the first webview when it has none (`fontDPI`, [`fontdpi_gtk3.h`](../../internal/gui/fontdpi_gtk3.h)). GDK's unset value is -1, and WebKitGTK 2.54's GTK 3 settings reader multiplies it by 1024 unchecked, so every page lays out at a negative width with a 9000000px font (#1371). An unset DPI takes `gtk-xft-dpi`, or 96, times a valid `GDK_DPI_SCALE`; a positive one is left alone. Wails initialises GTK (its GtkApplication) only inside `Run` and makes each window from a default-idle-priority callback on the main loop, so `fontDPI`, called just before `Run`, queues the check as a `G_PRIORITY_HIGH` idle: it runs on the main thread after GTK's own start, before the first window. `TestFontDPIUnset` opens a real WebKitGTK page through `magpie_font_dpi` and needs a display (Xvfb).
- Tests never touch a live agent config. The package's `TestMain` runs under `testenv`'s home of its own. A macOS test that shows windows runs them in a process of its own through `runAppKit` ([`appkit_darwin_test.go`](../../internal/gui/appkit_darwin_test.go)). AppKit and WebKit take their home from the account, not from HOME, so `runAppKit` gives them a temporary one with `CFFIXED_USER_HOME`; appearance, contrast and languages still come from the account. The test fails when that process leaves a folder in the real `~/Library/WebKit` or `~/Library/Caches`. Playwright tests serve `assets/` with isolated `/api` fixtures.
- `POST /api/settings/codex-auto-review` accepts an empty value (Codex's own choice) or an exact model/group ID in `provider.Served`, including unlisted providers. A known provider with an unknown or empty model name is rejected without changing the saved reviewer or catalog tag. Generic gateway request resolution remains permissive; it is not the validator for this setting. See `Handler` in [`api.go`](../../internal/gui/api.go) and `TestCodexAutoReviewSetting` in [`codex_auto_review_test.go`](../../internal/gui/codex_auto_review_test.go).

`openModal` makes the background inert when an editor opens fresh, leaving
confirmations and menus interactive through redraws. `closeModal` releases
that background as soon as the close animation starts, so clicks reach the
page while the editor fades away. Opening an editor during that animation
protects the background again. `ux-safety.test.cjs` checks these transitions.
Routing's New group buttons share `newGroup`, which asks before replacing a
dirty group draft; Cancel retains the draft and Discard opens the new editor.
`newGroupWith` applies the same guard when creating a group from a model and
waits for `show` to accept navigation before creating its draft.

## Verification

```sh
go test -tags nogui ./internal/gui
go test -run TestMainWindow ./internal/gui
GOARCH=amd64 CGO_ENABLED=1 go test -run TestMainWindow ./internal/gui  # as for an Intel Mac
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
