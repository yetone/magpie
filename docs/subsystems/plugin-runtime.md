# Plugin runtime

magpie's built-in plugins are the parts of magpie not everyone needs, each
one a package under `plugins/` that the user can switch off. The runtime in
`internal/pluginrt` keeps their registry, applies the ones that are on in the
order their services make, and disposes of what each registered when it is
turned off. `plugins.yml` says which are on. This page describes P0 (the
runtime, the file and the window's switch) and P1 (the window's kernel: a
plugin's page, its error boundary, the kit it draws with and its settings
form, with the Gateway page as the first plugin to draw its own page), and
P2 so far (a page moved out of the core whole: its code, styles and words:
Sessions, Library, Routing and Usage), and P3 (the gateway's
extension points: the observer, with OTel export as its first plugin; the
router strategy, with the routings besides Smart and the user's groups; and
the middleware point, with the JS gateway middleware and privacy masking;
the tool point, with web search; and the budget point, with key limits), P4 (community plugins at each of those points) P5 (community panels in magpie's own pages, and a plugin's page in a built-in's place) and P6 (a plugin's items in a provider's and an agent's row menu). OpenCode plugins (npm packages in
the plugin host) are a different thing; see
[provider and plugin ownership](provider-plugins.md) and
[gateway middleware](gateway-middleware.md).

## Responsibilities and boundaries

| Part | What it does | Source |
| --- | --- | --- |
| Registry | A plugin calls `pluginrt.Register(Manifest)` from `init`. `Registered` lists them by id; two with one id panic. `plugins/all` imports every plugin, and only `main.go` imports `plugins/all`, so core code and its tests run without any. | [`pluginrt.go`](../../internal/pluginrt/pluginrt.go), [`plugins/all/all.go`](../../plugins/all/all.go) |
| Manifest | `ID`, `API` (a caret range of the host API, `"^1"`; `pluginrt.API` is `"1.0"`), `Requires` and `Provides` (services), `Essential`, `Name`/`Sub` (English; the window translates them), `One`/`Many` (what is in use, of 1 or `{n}`), `InUse`, `Apply`, `Parts` (`{Slot, Key}`: the window's parts it is), `Defaults` (its config), `Settings` (a struct of its settings, made into `Defaults` and a schema at `Register`) and `Client` (its files in the window, under `internal/gui/assets/plugins/<id>/`: scripts, its `i18n.js` and a `.css`). | [`pluginrt.go`](../../internal/pluginrt/pluginrt.go) |
| Lifecycle | `Runtime.Reconcile(on, config)`. Off first: every plugin no longer wanted, and every one requiring a service one of those provided, is disposed newest first. Then on: each wanted plugin whose requirements are provided (by `Core`, `settings`, `providers`, `gateway`, `window`, or an active plugin) is applied, again and again until no more can be. States: `off`, `pending` (waiting for a service, logged), `active`, `failed` (Apply returned an error or panicked, or the API range doesn't match; what it registered before is undone), `disposed`. A failed plugin never stops the others. | [`pluginrt.go`](../../internal/pluginrt/pluginrt.go) |
| Ctx | What `Apply` gets: `OnDispose(f)`, `Contribute(slot, key)`, `Provide(name, v)` (withdrawn when it goes), `Service(name)`, and its composed `Config`. Disposers run in reverse, each one's panic logged and the rest still run. | [`pluginrt.go`](../../internal/pluginrt/pluginrt.go) |
| plugins.yml | `appdir.Config()/plugins.yml`: `preset` (`essentials` or `all`; `all` unless the user picks Essentials), `settled` (the defaults the file was last brought up to) and `plugins`, rows of `{id, enabled, config}` holding only what the user set. | [`config.go`](../../internal/pluginrt/config.go) |
| Settings schema | `SchemaOf(Settings)` makes a JSON Schema object of the struct, in its fields' order (`x-order`): each exported field with a json name is a setting, of `bool`, an int, `float64` or `string`, with tags `title` (English; the window translates it), `description`, `enum` (comma separated) and `min`/`max`. Its values are the defaults. A field a form can't draw, or a tag that doesn't parse, panics at `Register`. `Schema.Validate` takes a value only as the schema allows it (a JSON float64 or a YAML int for an integer); `SetConfig(id, config)` writes the user's row through it, and `Compose` leaves out a value edited into plugins.yml by hand that it doesn't allow, so the default stands. | [`schema.go`](../../internal/pluginrt/schema.go), [`config.go`](../../internal/pluginrt/config.go) |
| Built-in plugins | `groups`, `library`, `otel`, `sessions`, `warmups` (P0), `gateway` (P1), the Gateway page, which is Essential and in use while a gateway key is there (`access.List`), and `routing` (P2), the Routing page, Essential and in use while the user has a routing group, since the page is where groups are made and changed. `rotation` (P3), the routings besides Smart, is Essential and in use while a provider or group is routed by one of them. `middleware` (P3), the JS gateway middleware, is Essential and in use while plugins.json has a middleware on (`middleware.On`). `redact` (P3), privacy masking, is Essential and in use while Settings → Privacy asks for any masking (`masking.Wanted`). `search` (P3), magpie's web search, is Essential and in use while a search API is set up or a searcher is picked. `budget` (P3), key limits, is Essential and in use while a gateway key has a limit. `usage` (P2), the Usage page, is Essential too, with no `InUse`: what it shows the gateway counts anyway, and the tray panel's Usage tab still shows the allowances with it off. The Gateway page's one setting, `callsPage` (10, 20, 50 or 100; 20), is how many recent calls the page shows at a time. The other five aren't Essential. Each one's `InUse` counts what the user made with it: routing groups not Auto and not hidden, library items given to agents, a collector sent to, gateway conversations recorded, warm-ups and check-ins on. | [`plugins/`](../../plugins) |

The boundary is checked by `TestPluginsImportOnlyCore`: no package outside
`plugins/` imports one (but `main.go`, through `plugins/all`), a plugin never
imports another, and `plugins/all` imports every plugin folder. Plugins reach
each other only through services.

## The window's kernel (P1)

`internal/gui/assets/kernel.js` loads after `i18n.js` and before the
plugins' scripts and `app.js`, all classic scripts sharing one scope. It is
`window.magpie`:

- `magpie.plugin(id, setup)`: a plugin's script registers, and `setup` gets
  a context: `config` (from boot.js's `pluginConfig`, then Plugins ›
  Built-in), `onConfig(fn)`, `onDispose(fn)`, `slots.add(slot, part)` and
  `ui`.
- **Slots.** The one slot is `page`: a part `{key, html, loads, mount,
  …hooks}` becomes `<main class="view" id="view-<key>"
  data-feature="<plugin>">` where index.html's `<template data-slot="page">`
  is, at once, so app.js finds the page as it finds its own. app.js calls
  `magpie.start()` when it is set up, which mounts every page, and then each
  hook through `magpie.call(key, hook, …)`: `loading`, `draft` and `render`
  around `/api/providers` for a page that `loads: "providers"`, `open` when
  `show` opens it (a promise it gives back puts the reader back where they
  were once it settles), `relang` when the language changes on it.
  `title` is the page's header (English; `show` translates it).
  `magpie.pages()` adds the plugins' pages to the views `show` and
  `?view=` know.
- **Words.** A plugin's `i18n.js` calls `magpie.strings({zh, "zh-TW", ja,
  de})`, added to i18n.js's `I18N` before app.js translates anything. A key
  already there is logged and not taken. `tests/strings.cjs` reads them as
  the page does, for `gui-de`, `gui-ja`, `gui-zh-tw` and `i18n-keys`
  (no key in two files, every key of a plugin's file in all four
  languages).
- **Error boundary.** A hook that throws, or whose promise fails, is caught
  in `run`: logged with `[magpie] <plugin>: <key>.<hook>`, and the page is
  replaced by a card saying "This page stopped working", the error's message
  and Try again. Further hooks of a broken page do nothing, so nothing half
  drawn comes back, and the other pages go on. Try again puts the page's
  markup back, translates it, mounts it and runs the hook that broke with
  what it was given. A plugin whose `setup` throws is logged and has no page.
- **Kit** (`magpie.ui`): `el`, `button`, `toggle` (the switch Plugins uses,
  `role="switch"`), `menu` (the app's menu through `openProtoMenu`, never a
  native `<select>`), `row` (a settings row) and `form(schema, values,
  onChange)`: a switch for a boolean, a menu for an enum (numbers in the
  window's locale), a field for a number (kept to its range, `aria-invalid`
  when not) or text.
- **Settings.** Plugins › Built-in draws the rows of each plugin on, with a
  schema, from `ui.form` under its row (`.feat-set`), and a change posts
  `{id, config}`; `applyFeatures` hands each plugin's config to
  `magpie.configure`, which calls its `onConfig` only when it changed.

The Gateway page is `plugins/gateway` with `Client: keys.js, client.js`
(`internal/gui/assets/plugins/gateway/`): `keys.js` is the gateway-key
manager that was `caller-keys.js`, and `client.js` is the page's code that
was in app.js, ending in its `magpie.plugin("gateway", …)`, and after it
the viewer of a recorded call's bodies (`callBodyPanel`: a JSON tree, a
stream's events or its reply), which Usage's ledger and Routing's trace
call too; every plugin's scripts load whether it is on or off. Still in
app.js and app.css: its styles, its sidebar and header buttons
(`data-feature="gateway"` in index.html), and `decideEntry`, which the model
info card shares. `TestBuiltinClientsLoaded` holds index.html to load every
plugin's `Client` script between kernel.js and app.js, and its stylesheet in
the head, each with its `?v=`.

## Pages moved out (P2)

Each page moves in a commit of its own, whole: its code into
`assets/plugins/<id>/client.js`, its styles beside it, and the words only it
says from i18n.js into its `i18n.js` (a key app.js, another page, index.html
or Go also says stays in i18n.js). It stays so after: `i18n-keys`' "a
plugin's words are its own" fails when anything outside a plugin's folder
says one of its words, which goes back to i18n.js. A word inside a longer
one doesn't count: a comment's "fetchClaudeRuns asks" doesn't say "Runs
as" (#3897).

- **Sessions** (`plugins/sessions`, `Client: i18n.js, sessions.css,
  client.js`): the page that was `sessions.js`, its markup all drawn by its
  code, so its part has no `html`. Its hooks: `open` and `relang` load it,
  and `reveal(s, e)` is the Usage page's way in (a session's row and All
  sessions, #752), called through `magpie.call("sessions", "reveal", …)`,
  so with no Sessions plugin it does nothing. With Sessions switched off,
  a row of Usage's list unfolds its details in place instead
  (`sessionItem`, #2311). Still in app.js: the Usage
  page's Sessions tab (`/api/sessions`). Settings' Session terminal row
  (`data-feature="sessions"`) and Privacy's Agents' session history are its
  `settings.js`, loaded after app.js and drawn whether it is on or off.
- **Library** (`plugins/library`, `Client: i18n.js, library.css,
  client.js`): the page that was `library.js`. Its code runs at `mount`
  only, where it wires its listeners (the dialog's backdrop and Escape,
  focus and visibility reading it again, its lists' scroll), since the
  script is in before app.js. Its hooks: `open` and `relang` read
  `/api/library`, `redraw` draws it again once the state says which agents
  are hidden, and `close`, `dirty` and `discard` are what `show()` asks
  before the reader leaves it. `window.loadLibrary`, `redrawLibrary`,
  `closeLibraryModal`, `libraryDirty` and `discardLibrary` are gone.
- **Routing** (`plugins/routing`, `Client: i18n.js, routing.css,
  client.js`): the page that was `routing.js`, Essential. Its part's `html`
  is the page's head and boxes that were index.html's; the rest of the page
  is drawn by `routingPage`, run at `mount` because it builds on app.js. It
  keeps itself after that, as it did (its own `hidden`, the language, the
  window's focus), so its hooks are only `dirty` and `discard`, a routing
  group being edited, which `show()` and the window's close ask through
  `magpie.call("routing", …)`: `window.routingDirty` and `discardRouting`
  are gone. Its tab in the tray panel and the panel's section are marked
  `data-feature="routing"`. What other pages use of it is still on
  `window` (`openRoute` for context.js; `kindName`, `swapWhy` and
  `routedWhy` for Usage's Requests; `newGroupWith` for Providers;
  `panelRoutingShown` for the panel). With the plugin off, what leads to
  the page goes with it: the nav and panel tabs, the guide's Open Routing,
  Providers' "Make a routing group of …" (`data-feature="routing"`), the
  Usage list's View routing and its time as a link, and the context card's
  Latest request; a panel left on its Routing tab shows Agents.
- **Usage** (`plugins/usage`, `Client: i18n.js, client.js, page.js`):
  the page that was the largest part of app.js (about 3000 lines: the
  Overview's cards, the Requests ledger, the Sessions tab and the Agents
  list). Its part's `html` is the page's markup that was index.html's
  `#view-usage`. Its code builds on app.js at the top level (its timers,
  `?view=usage`'s tab and filters), so `page.js` is the one script a
  plugin loads after app.js: index.html loads kernel.js, the plugins'
  other scripts, app.js and the scripts beside it, then each `page.js`,
  then `start.js`, which is app.js's old tail (`magpie.start()`, the
  first page shown, the first `load()`), so the first page has its code.
  `TestBuiltinClientsLoaded` holds that order. Its hooks: `open` loads
  it, `refresh(again)` is `load()`'s read of it (coming back to the
  Overview is opening the allowances, #1588), `reload` is Refresh now,
  `costs` draws the costs again when Settings' currency changes, and
  `tab` says which tab it is on, for `focusQuotaCard`, and `leave`, which
  `show()` calls on the page it leaves, gives back what a click lent the
  Requests (`ledLeave`, TJHHHH on Discord): a filter a ranking or chart
  click set, or one brought in by the address or Routing's View usage,
  goes when the reader leaves the Requests for another page or tab. One
  picked in its own menu or typed stays. Still in app.js,
  since the tray panel draws them too: the allowance cards,
  `quotasOpened`, `panelQuotaFocus` and `panelUseFocus`. With the plugin
  off, its nav tab goes, and so do the ways to the page
  (`data-feature="usage"`): the tray panel's Open Usage and Routing's View
  usage. A `show("usage")` left anywhere gives way to Agents. The panel's
  Allowances tab stays.

## Gateway extension points (P3): observer

P3 opens the gateway to plugins at five points: observer, router strategy,
middleware, tool and budget. The observer is the first. It only reads, so it is
the safest to move first. `plugins/otel` is its first consumer, and with the
plugin off the gateway does no observing work.

| Part | What it does | Source |
| --- | --- | --- |
| `usage.Observer` | `Serve() (stop)`, `Bodies()`, `Begin(Request) Watch` and `Ledger(Record)`. An observer watches what the gateway serves and what the ledger is written, and never changes either. `Observe(o)` registers one until its remove; `Observers` lists them. It lives in `internal/usage`, not the gateway, because every row is a usage `Record` and the ledger's other writers reach it too. | [`observe.go`](../../internal/usage/observe.go) |
| Serving | `ServeObservers()` starts every observer registered, and each one registered later, until the last of its stops (it is counted). Only the process serving the gateway calls it (`Server.ListenAndServe`), so a CLI that writes a usage row exports nothing. Remove stops a serving observer. | [`observe.go`](../../internal/usage/observe.go), [`gateway.go`](../../internal/gateway/gateway.go) |
| `Begin` and `Watch` | As a request starts the gateway asks each observer (`observeRequest`) with `usage.Request`: its header, agent, session (the native one when the agent names it), call kind (`auxiliary` for Claude's tool-less helper calls) and `Plain` (loopback, no gateway key, not passed on from another magpie). A `Watch` it returns gets `Attempt` for each routing attempt, unbilled failures and retries too, and `End` once. With no observer, or none watching, the request carries nothing. | [`observe.go`](../../internal/gateway/observe.go) |
| Bodies | `Bodies()` is `NoBodies`, `CapturedBodies` (the first 256 KiB, cut with `usage.BodyCut`) or `WholeBodies` (the reply spooled whole as it streams, #538). The gateway takes the most any observer wants now (`ObservedBodies`): it keeps the whole reply only when one asks for it, and puts bodies on an attempt's record only when one asks for any. | [`observe.go`](../../internal/gateway/observe.go), [`capture.go`](../../internal/gateway/capture.go) |
| `Ledger` and `Watched` | Every row `Append` writes is passed to each observer. A row of a request some observer watched has `Watched` set (`appendUsage`), so an observer that sent the request's attempts doesn't send its row again. | [`usage.go`](../../internal/usage/usage.go), [`lan.go`](../../internal/gateway/lan.go) |
| `internal/otlp` | The OTLP/HTTP exporter, moved whole out of `internal/usage`: `otlp.New()` is an `Exporter`, a `usage.Observer`. Its Begin leaves a local agent's own turn to the agent's transcript when session tracing reads it, and takes a W3C `traceparent` as the parent. Settings are read on each call, as before. Both signals' resource is `service.name=magpie` with `OTEL_RESOURCE_ATTRIBUTES` merged over it and `OTEL_SERVICE_NAME` over that (`otelResourceAttributes`, #5167); an invalid variable is discarded whole and logged once, by entry position only. Nothing about the host is collected. |  [`otlp/`](../../internal/otlp) |
| `plugins/otel` | Requires `window`, `settings` and `gateway`. Apply registers `otlp.New()` and disposes of it when turned off. `InUse` is 1 while `settings.OTelExport` says to send (settings.json or `MAGPIE_OTEL_*`), so a user who exports only through the environment keeps it on under Essentials. | [`otel.go`](../../plugins/otel/otel.go) |

## Gateway extension points (P3): router strategy

The router strategy point says the order a pool of routes is tried in: a
provider's accounts or keys, or the heads of a group's members. The gateway
still lists the pool, and keeps rests, affinity, rules, pins and the walk
over the order itself; a strategy only orders. Smart, the default, stays in
the core. The seven other routings are the built-in plugin `rotation`'s.

| Part | What it does | Source |
| --- | --- | --- |
| `routes.Strategy` | `Order(pool, seats) []int`: a permutation of the seats' indexes. `pool` is a provider's id or `group/<id>`, for a strategy that keeps a round per pool. `Exact` keeps the order as given; otherwise a provider's API keys that fit the request better go first, each in its order. `ByUse` puts each seat's recent tokens in the trace; `ByFree` has the gateway fill `Seat.Free`, and `BySpeed` `Seat.TTFT` and `TPS` (its speed lately, gateway `speed.go`), each only for a strategy that asks. | [`routes.go`](../../internal/routes/routes.go) |
| `routes.Seat` | What a strategy weighs, read by `weighRouted` under the routing state's lock: its rest key, whether it is an account, whether its allowance is known or it learns it as it answers, the share used, when its windows renew, its weekly pace, its key's weight, whether it rests now, its tokens of the last hours, a prepaid wallet's balance and the provider and unit it is in (`Left`, `LeftIn`, #3950), and, when the strategy asks, whether its model is free, whether its account's plan is free (`FreePlan`, `ByPlan`) and its ms to the first token and tokens a second lately. | [`routes.go`](../../internal/routes/routes.go), [`routing.go`](../../internal/gateway/routing.go) |
| `Register`, `Find`, `Apply` | `Register(routing, s)` serves a routing until its remove; registering again stacks, and the last one serves. `Find("")` is `Smart` always. `Apply` fails open: a strategy that panics, or answers what isn't a permutation, leaves the pool in its listed order, logged. Every route is still tried. | [`routes.go`](../../internal/routes/routes.go) |
| Strategies | `Smart` (core), and `Ordered`, `Rotate`, `LeastUsed`, `Pace`, `Weighted`, `FreeFirst`, `Fastest`, `Cheapest` (a group's only: `provider.normalize` drops `cheap` from a provider), `Drain` and `FreeExpiring` (a group's only too: free quota first, the soonest lost first, then the paid as `Smart` orders them) in `routes.Builtin`. `Drain` remembers the seat each pool is on (`drains`). `Rotate` keeps a turn per pool and `Weighted` a smooth weighted round (`Round`, `MoveRound`), both moved out of the gateway, and `Fastest` a probe count per pool, so a seat not measured lately gets one request in `ProbeEvery`; `Forget` clears them, `Drain`'s too. | [`strategies.go`](../../internal/routes/strategies.go), [`round.go`](../../internal/routes/round.go) |
| Off | A routing no strategy serves is its listed order. `routingOf` in the gateway reads it as `order` wherever the core asks about a routing (a turn's mark, `sinks`, a rotate group's sessions, a group's heads), so nothing of the routing is left half on. | [`routing.go`](../../internal/gateway/routing.go) |
| `plugins/rotation` | Requires `providers` and `gateway`, `Essential`. Apply registers every `routes.Builtin` and removes them when turned off. `InUse` counts the providers and groups providers.json routes by one of them (`provider.Routings`, the file alone, not the catalog); an unreadable providers.json is an error, so it stays on. The gateway's `KeepFresh` watches providers.json too, so a routing set from the CLI or the TUI turns it on within 2s. Off, the window's routing pickers (the provider editor, the Routing page's pools and groups) offer Smart alone: their options are marked `data-feature="rotation"` (`routingSegs`). | [`rotation.go`](../../plugins/rotation/rotation.go), [`provider.go`](../../internal/provider/provider.go), [`app.js`](../../internal/gui/assets/app.js) |

The user's own routing groups are the `groups` plugin's on this point. The
groups magpie finds (a model two providers serve, `auto/<model>`) stay the
core's, as Smart does: they are how a model several providers serve is one
model with failover, which a user has with the Essentials preset,
where `groups` is off. `planGroup` and the walk over a group's members are
the core's either way: a strategy only orders a pool, and a group off is
not offered at all.

| Part | What it does | Source |
| --- | --- | --- |
| `GateGroups`, `ServeGroups` | The gateway holds `GateGroups` while it serves (`ListenAndServe`), and the plugin holds `ServeGroups` while it is on. Gated with none served, `groupsIn` leaves out every group not `Auto`, so `Groups`, `FindGroup`, `GroupFor`, the catalog, `/v1/models` and a group's `Unlists` go without them: a request for one is a model not served. A process not serving the gateway (the CLI, the TUI) is never gated and lists them all. Nothing built from the groups is kept past a call, so a flip is seen by the next lookup. Gated and off, a lookup that finds a group of the user's own first asks the gate's refresh (the gateway's is `pluginrt.Refresh`), at most once a second and never from inside one, then looks again: a group made a moment ago turns the plugin on and is served by the request that asks for it, not after `KeepFresh`'s next look (`TestOwnGroupRefreshesTheGate`). | [`group_serve.go`](../../internal/provider/group_serve.go), [`group.go`](../../internal/provider/group.go), [`gateway.go`](../../internal/gateway/gateway.go) |
| `plugins/groups` | Requires `window` and `providers`, not Essential. Apply holds `ServeGroups`. `InUse` is `provider.NamedGroups`: the groups providers.json holds that are neither Auto nor hidden, read from the file, never through the gate (else off would count none and stay off); an unreadable file is an error, so it stays on. The `routing` plugin's `InUse` is the same count. The gateway's `KeepFresh` watches providers.json, so a group made with `magpie group add` turns it on within 2s, and a request for it before then turns it on itself (the gate's refresh, above); the window's New group button is `data-feature="groups"`. | [`groups.go`](../../plugins/groups/groups.go), [`routing.go`](../../plugins/routing/routing.go) |

## Gateway extension points (P3): middleware

The middleware point is what changes a request on its way through the
gateway and its reply on the way back. It has two places, each with its own
failure policy, in [`internal/gwmw`](../../internal/gwmw/gwmw.go):

| Part | What it does | Source |
| --- | --- | --- |
| `gwmw.Agent` | A middleware on a request an agent sent, in the agent's own API, before the gateway routes it, and on its reply as the agent gets it: `Begin(Info) Run`, and the run's `Request` (a `Reject` ends the request in the agent's error shape), `Wrap` and `End`. `Use(a)` registers one until its remove; `Agents` lists them in order. `serveAgent` runs each in turn; with none it calls `serve` directly. It fails open, as the JS middleware always did. | [`gwmw.go`](../../internal/gwmw/gwmw.go), [`middleware.go`](../../internal/gateway/middleware.go) |
| `plugins/middleware` | Requires `gateway`, Essential. Apply registers `middleware.Chain`, the JS gateway middleware ([Gateway middleware](gateway-middleware.md)), whose place, API, limits and fail-open policy are unchanged. `InUse` is `middleware.On()`, the middleware plugins.json has on. Off, agents' requests go as they came and no middleware is called, while plugins.json is left as it is. The gateway's `KeepFresh` watches plugins.json too, so a middleware added from the CLI or the window turns it on within 2s, and a request before then asks the gate's refresh itself (`gwmw.AgentsWanted`: with no agent middleware registered and `middleware.On()` above 0, at most once a second), so the middleware runs on that request, as it did within a second before it was a plugin. | [`middleware.go`](../../plugins/middleware/middleware.go), [`run.go`](../../internal/middleware/run.go) |
| `gwmw.Masker` | Masks what magpie sends a vendor and puts the values back in what comes back: `JSON`, `Text`, `Restore` and `Writer`. `Mask(m)` serves one until its remove (the last registered serves). It fails closed: `Masking(wanted)` is the masker for a request, or `ErrUnmasked` when the settings ask for masking and none serves, and `Safely` makes a masker that panics an error. The gateway's call sites (`serve`, count_tokens, embeddings and audio, transcriptions, text to speech, images, video, the search stand-ins and their queries, the Codex backend) answer either with 503 "…nothing was sent", and the vendor gets nothing. `Served`, with none, asks the gate's refresh (the gateway's is `pluginrt.Refresh`, set by `gwmw.Refresh` in `ListenAndServe`) at most once a second and never from inside one, then looks again, so masking turned on a moment ago turns the plugin on for the request that needs it. | [`gwmw.go`](../../internal/gwmw/gwmw.go), [`redact.go`](../../internal/gateway/redact.go), [`gateway.go`](../../internal/gateway/gateway.go) |
| `gwmw.Teller` | What a masker can say besides masking: `JSONHits` and `TextHits` mask as `JSON` and `Text` do and also return each placeholder made or reused (`Hit`: placeholder, kind, finder, head, length), and `Value` is what a placeholder stood for while this masker remembers it. Optional: the gateway records a request's masking only when the served masker is one (`masking.Masker` is), and Settings → Privacy's records and values need it; a masker without it masks the same and leaves no record. | [`gwmw.go`](../../internal/gwmw/gwmw.go), [`masked.go`](../../internal/gateway/masked.go), [`masking.go`](../../internal/masking/masking.go) |
| `plugins/redact` | Requires `settings` and `gateway`, Essential. Apply serves `masking.Masker`, which reads settings.json on each call and masks with `internal/redact` as before (it lives in `internal/masking` because `settings` imports `redact`). `InUse` is `masking.Wanted`: *Mask secrets*, *Mask personal data* or a word of the user's own; settings.json that can't be read is an error, so it stays on. Since it is in use whenever masking is asked for, it can only be off while nothing is; the 503 is for a refresh not yet seen or a plugins.yml edited by hand. | [`redact.go`](../../plugins/redact/redact.go), [`masking.go`](../../internal/masking/masking.go) |

## Gateway extension points (P3): tool

The tool point is the tools magpie answers itself for an agent, in place of
the vendor. Web search is the one now: the stand-in `web_search` a model
that can't search is given, the search MCP server, and Codex's search when
ChatGPT can't answer it ([Gateway routing](gateway-routing.md)). A vendor
that searches by itself is never the point's: its own `web_search` goes to
it, on or off.

| Part | What it does | Source |
| --- | --- | --- |
| `gwtool` | `Serve(name)` serves a tool until its remove; `Gate()` makes every tool wait on `Serve` until its stop; `Served(name)` is not gated, or served. The gateway holds `Gate` while it serves (`ListenAndServe`, `TestServingGatesTools`), so a process not serving it (the CLI, the TUI, an agent's config written there) has every tool, as before the point. `WebSearch` is the search's name. | [`gwtool.go`](../../internal/gwtool/gwtool.go), [`gateway.go`](../../internal/gateway/gateway.go) |
| Off | `searchServed` gates the three places every search path goes through: `canSearch` (the stand-in tool, `/v1/models`' search note to another magpie, the subscriptions' search, `CanSearch` for the agents' rows), `searchesOwn` (a Kimi Code, GLM Coding or WorkBuddy plan's own search service, a Google sign-in's Gemini) and `webSearch` itself, which answers `errSearchOff` without asking a searcher or a search API. So off, a request with `web_search` goes to the vendor as it does with no searcher set up, and the MCP server and Codex's search say the plugin is off (`TestSearchOnAndOff`). | [`search.go`](../../internal/gateway/search.go), [`search_api.go`](../../internal/gateway/search_api.go), [`search_kimi.go`](../../internal/gateway/search_kimi.go) |
| `plugins/search` | Requires `settings` and `gateway`, Essential. Apply serves `gwtool.WebSearch`. `InUse` counts the search APIs set up (`provider.SearchAPIsSet`, the file alone) and a searcher picked in Settings (neither magpie's pick nor off); an unreadable providers.json or settings.json is an error, so it stays on. The gateway's `KeepFresh` watches both files, so a search API added from the CLI or the window turns it on within 2s. Settings › Models › Web search's rows are its script in the window, `plugins/search/settings.js`, loaded after app.js and drawn whether it is on or off (#4133). | [`search.go`](../../plugins/search/search.go), [`settings.js`](../../internal/gui/assets/plugins/search/settings.js) |

## Gateway extension points (P3): budget

The budget point holds a gateway key to the limits its owner set (#585,
#2227) before any of the key's requests goes to a provider. It is the last
of the P3 points. A request without a key, or of a key with no limit, never
asks it.

| Part | What it does | Source |
| --- | --- | --- |
| `gwbudget.Limiter` | `Reserve(who, body, model, now) (release, refused)`: lets a key's request in, holding what it is expected to use until release, or refuses it with a `Refusal` (`RetryAfter`, `ResetAt`). `Limit(l)` serves one until its remove; registering again stacks, and the last one serves. | [`gwbudget.go`](../../internal/gwbudget/gwbudget.go) |
| Fail closed | `Limiting(who)` is nil for a request with no key or a key with no limit, the served limiter otherwise, and `ErrUnlimited` when the key has a limit and none serves. With none it first asks the refresh (the gateway's is `pluginrt.Refresh`, set by `gwbudget.Refresh` in `ListenAndServe`), at most once a second and never from inside one, then looks again, so a limit set a moment ago turns the plugin on for the request that needs it. `reserveFor` is the gateway's one way in, for the chat APIs (`keyLimited`), the search MCP server and the vision one: a refusal is 429 `gateway_key_limit` with Retry-After and X-Magpie-Limit-Reset; `ErrUnlimited` is 503 `gateway_key_limit_off`, X-Should-Retry false, nothing sent, and a rejected usage row (`TestKeyLimitsOnAndOff`, `TestVisionMCPHeldToKeyLimit`). | [`gwbudget.go`](../../internal/gwbudget/gwbudget.go), [`key_limit.go`](../../internal/gateway/key_limit.go) |
| `plugins/budget` | Requires `settings` and `gateway`, Essential. Apply serves `budget.Limiter`, `internal/budget`'s windows and reservations as before. `InUse` counts the gateway keys with a limit (`access.List`); a keys file that can't be read is an error, so it stays on. Since it is in use whenever a key has a limit, it can only be off while none has; the 503 is for a refresh not yet seen or a plugins.yml edited by hand. The gateway's `KeepFresh` watches the keys file too, so a limit set from the CLI or the window turns it on within 2s. | [`budget.go`](../../plugins/budget/budget.go) |
| Stays in the core | What a key has used and has left (`budget.All`, `budget.Binding`) for `/v1/magpie/limit`, the Gateway page and the CLI, and `budget.Append`, so the usage ledger counts a key's use the same with the plugin on or off. The key editor's limit fields stay with the plugin off, since setting a limit is what turns it back on. | [`budget.go`](../../internal/budget/budget.go) |

## P4: community plugins at every point

P4 opens each extension point, the gateway's and the window's, to
community plugins (OpenCode plugins, npm packages in the plugin host). A
gateway point stays behind the built-in plugin that serves it, so off
still means off; a page is its own plugin's to turn on and off.

| Point | What a community plugin gives | Served by | Reference |
| --- | --- | --- | --- |
| Router strategy | A module's `export const strategies = {name: (pool, seats, options) => order}` (or `{order}` objects) are routings `<short name>/<name>`, registered with `routes.Register` while the gateway serves `middleware.Points()` and the plugin is on and loaded; off or reloaded, they are removed. 20ms each, fail open: an error, a timeout or what isn't an order of the seats is the listed order, counted on the Plugins page. The routing pickers offer the served ones (`jsRoutings` in the settings state); a routing saved whose plugin is gone shows as it is and is the listed order. providers.json keeps such a routing as saved (`provider.PluginRouting`), so a plugin turned off and on again routes its groups again. | `plugins/middleware` ("JS gateway plugins", from the same module as its [middleware](gateway-middleware.md)) | [`points.go`](../../internal/middleware/points.go), [`app.js`](../../internal/gui/assets/app.js), [`client.js`](../../internal/gui/assets/plugins/routing/client.js), [`groups_cli.go`](../../internal/cli/groups_cli.go), [`routing.go`](../../internal/provider/routing.go) |
| Usage observer | `export function onUsage(row, options)` gets each usage row of an allow-list (no bodies, prompts or ids), from a bounded queue off the request path: a full queue drops and counts, and a request never waits. 250ms each, fail open. | `plugins/middleware` ("JS gateway plugins", from the same module as its [middleware](gateway-middleware.md)) | [`points.go`](../../internal/middleware/points.go) |
| Search API | Its `search` hook's entries, each a web search API in Settings › Web search (`<plugin>:<id>`), asked in the search APIs' order with a quarter of a search's time. An entry with a key, none (`keyOptional`) or the user's address (`url`), or one with `auth` naming a provider the plugin's own `auth` hook signs in to: it takes no key, asks that provider's accounts that are on in the order its model requests take them, and its `ctx.getAuth()` is the one account chosen, renewed first within the search's time; a refusal rests the account for search only. Off, no search asks it. | `plugins/search` (Web search, Essential) | [Plugin tools](plugin-tools.md) ("Search APIs from a plugin"), [provider plugins](provider-plugins.md) |
| Tool | Its OpenCode `tool` hook's tools, offered to every agent by magpie's MCP server at `/mcp/magpie/tools`, each named `<plugin>_<tool>`, each call held to the calling key's limit and run in the host within 55s. Off, no tool is listed and a call says the plugin is off. | `plugins/tools` ("Plugin tools", Essential; `InUse` counts the plugins on that give tools, from `plugin-tools.json`), `gwtool.PluginTools` | [Plugin tools](plugin-tools.md) |
| A page in the window | `magpie.page {title, file, icon}` and `magpie.permissions` in package.json, installed from npm, GitHub or a folder like any plugin (`plugin.WindowPage`: `file` an .html inside the plugin, the icon in the page's folder, reads kept to `plugin.PageReads`). An on plugin's page gets a tab in the sidebar's Plugins group, after the built-in pages (`pluginpages.js`, a kernel `page` part). It runs in an iframe sandboxed to `allow-scripts`, its files served from `/_magpie/plugin/<token>/` (an HMAC of the spec with a secret of this run) under a policy of `sandbox allow-scripts`, its own folder only, `connect-src 'none'`. The frame talks to the window by `postMessage`: the window answers only its own frame (`ev.source`), with the theme, its own options (`/api/plugin-pages/options`, at most once a second, saved without restarting the host for a page-only plugin) and the reads it declared (`/api/plugin-pages/read`, checked again by the server): `usage:summary` (totals and the top 20 models) and `providers:list` (ids and names). Nothing secret is readable, since a page can still send its frame to another site. `noFramedAPI` turns away every `/api/` request a sandboxed frame or another site can send (`Origin: null`, `Sec-Fetch-Dest` a frame, a cross-site subresource), since the desktop window's API has no key; `webGuard` lets the prefix through without the cookie the frame never sends, and the token is the key. A page that throws, or doesn't say hello in 10s, is broken through the kernel (`ctx.broke`), with Try again. The frame is made the first time the page is opened. When the reader goes to another page it is told (`{event: "visibility", data: {visible: false}}`, the bridge's `magpie.visible` and `magpie.onVisibility(fn)`), since a frame in a hidden view isn't hidden to itself, and told again on coming back. A page left 10 minutes (`IDLE_MS`) has its frame let go, with its scripts, timers and connections, and loads again when it is next opened; `magpie.page.keepAlive: true` (`plugin.PageSpec.KeepAlive`, anything but `true` is false) keeps it however long, for a page that works while no one looks at it. Tests: `TestWindowPanels` (keepAlive), `plugin-page-idle.test.cjs`. The Plugins page shows the page and its reads on the row before the plugin is on. A plugin that is only a page is in-magpie-only: no Bun host starts for it. A page whose plugin also has a part for the host (a local service its page goes to, as Magiczerowxy/magpie-chat's does) starts the host when its HTML is served, if none is up (`pageHost`, `plugin.Warm`), waiting up to 7s: the host otherwise starts only on a call, so after a Restart (a plugin installed, updated or set, a Bun update) or a crash the page's service was down, and WebKit leaves a frame that navigates to a refused address on its old document with no error. A page reaches its service through magpie too, when the plugin declares it (`magpie.page.service {port, option}`, `plugin.ServiceSpec`: the port, or the plugin option named, from its plugins.json `options`): in `magpie web`, where 127.0.0.1 is the viewer's own computer, not the server (aigcfree on Discord: a server behind nginx, a Windows PC's browser, "127.0.0.1 refused to connect"), `/api/plugin-pages` gives the page a `service` under its token, `_magpie/service/`, and the bridge hands it to the frame as `magpie.service`, an absolute address (the desktop window gets none, and the page goes to 127.0.0.1 as before; a slot page gets none). `servePageService` reverse-proxies it to 127.0.0.1:<port>: Host set to the service's own (its rebinding check), the window's cookie taken off, Origin only absent or `null` (403 otherwise), Set-Cookie dropped, everything it sends sandboxed (`sandbox allow-scripts; frame-ancestors 'self'`, `Referrer-Policy: no-referrer`), streamed as it comes (`FlushInterval -1`, `X-Accel-Buffering: no` on an event stream, which `webCompressed` never gzips), a redirect to its own address made relative, and the gateway's port never served (a request from this server is loopback, which the gateway takes without a key). The way in is the page's: the token, which only a signed-in `/api` hands out, behind `AllowGuard`; the service stays on loopback. Its entry starts the host as the page's HTML does, and a service not up yet answers its frame with a 502 page that tries again in 2s. The chat page (`@magpie-community/page-chat` 0.1.1) goes to `magpie.service` when given one, else 127.0.0.1, and asks its `api/…` by relative address. Tests: `TestPluginPageServiceListedToBrowserOnly`, `TestPluginPageServiceSpec`, `TestPluginPageServiceProxied`, `TestPluginPageServiceDown`, `TestServiceLocation`, `plugin-page.test.cjs` (`magpie.service`). Discover lists a market entry of `kind: "page"` under Pages with the Page chip, and a GitHub repository tagged `magpie-plugin` whose package.json has `magpie.page.file` as a Page (`TaggedRepos`, which then checks that file is committed, as it does a provider's entry; `TestTaggedRepos`, `plugin-kinds-discover.test.cjs`). Tests: `TestWindowPageStaysInside`, `TestPluginPageServedShutIn`, `TestPluginPageReadsWhatItDeclared`, `TestPluginPageNoAPI`, `TestPluginPageWebGuard`, `TestPluginPageStartsItsHost`, `plugin-page.test.cjs`. Example: `internal/gui/testdata/page-plugin`. A magpie that serves no pages (a command such as `magpie plugin update`, `servesPages` in `internal/cli`) starts its host with each service its plugin's option moves on a free port of its own (`plugin.ServePages`, `hostOptions`): a plugin that waits on its port, as page-chat does, loads at once beside the app's host holding it, and doesn't go on trying for it to take it when the app's host restarts (#5187). A service no option moves is started as before. Tests: `TestHostServingNoPagesLeavesHeldPort`, `TestHostOptionsMoveOnlyTheService`, `TestServesPages`. | the window, no built-in plugin: the page is the plugin's own on/off | [`page.go`](../../internal/plugin/page.go), [`pluginpage.go`](../../internal/gui/pluginpage.go), [`pagebridge.js`](../../internal/gui/pagebridge.js), [`pluginpages.js`](../../internal/gui/assets/pluginpages.js) |

`plugins/middleware` is now "JS gateway plugins". Apply registers the chain and `usage.Observe(middleware.Points())`; off, no JS middleware, strategy or `onUsage` runs, and a pool routed by a JS strategy is its listed order. `InUse` is unchanged. ([`middleware.go`](../../plugins/middleware/middleware.go))

## P5: panels in magpie's own pages, and pages that replace one

P5 lets a community plugin into the pages magpie draws itself, not only a
page of its own. Both come from package.json and are read by
`plugin.Window` ([`page.go`](../../internal/plugin/page.go)); neither
starts a Bun host.

- **Panels.** `magpie.panels: [{slot, file, title}]`, at most 8 a plugin
  (`maxPanels`), each `slot` one of `plugin.PanelSlots`: `agents` (the end
  of the Agents page), `usage` (the end of Usage's Overview), `settings` (a
  Plugins tab in Settings, shown only while a panel is in it, in the tab
  strip and in the sidebar of a wide window) and `provider` (each saved
  provider's editor, told `{provider: {id, name}}`). `file` is an .html in
  the plugin's folder (`plugin.FileIn`). `GET /api/plugin-panels` lists an
  on plugin's panels in plugins.json order, each with a token of its own
  (`pageToken(spec + "\x00panel\x00" + i)`), served under the same policy
  as a page ([`pluginpage.go`](../../internal/gui/pluginpage.go)). A
  panel's frame is sandboxed to `allow-scripts` like a page's, and shares
  its plugin's reads (`magpie.permissions`) and options through the same
  `/api/plugin-pages/read` and `/options`. The bridge
  ([`pagebridge.js`](../../internal/gui/pagebridge.js)) gives it
  `magpie.context` and posts its height (`size`), which the window holds
  to 0..2400px. A panel that throws, or doesn't say hello in 10s, is
  replaced by its own card ("{name} stopped working", Try again); the page
  around it and the panels beside it go on. The size watch's own
  "ResizeObserver loop" notice, which WebKit gives a sandboxed page as a
  bare "Script error.", is not a throw.
- **Titles and look.** `magpie.page.titles` and a panel's `titles`, an
  object of language code to title (`plugin.titles`: trimmed, empty ones
  and a non-object dropped), name the tab and the panel's heading in the
  window's language, `zh-TW` falling back to `zh` and anything else to
  `title` (`titled` in [`pluginpages.js`](../../internal/gui/assets/pluginpages.js),
  redrawn when `<html lang>` changes). The bridge prepends a stylesheet of
  `:where()` rules, of no weight, for the font, colours, headings, buttons,
  fields and tables, so a page with no stylesheet reads as the window's
  and any rule of the page's wins. Tests: `TestWindowTitles`,
  `plugin-page.test.cjs`.
- **The policy's scheme.** The desktop window serves on `wails://localhost`
  (macOS, Linux), and a sandboxed frame has an opaque origin, so a CSP
  source without a scheme matches only http(s) there. `ownWindow` puts the
  native scheme on the request's context and `pagePolicy` names the folder
  under it (#2501). iOS Safari (18.7) takes a missing scheme from that
  opaque origin on http(s) too, and loaded none of a page's scripts from
  `magpie web` (#4198): a browser's policy names the folder
  under both `https://` and `http://` (`ownSources`), since a proxy that
  ends TLS (nginx, Cloudflare) leaves magpie nothing it trusts to say
  which. A page's service sends its own policy, where `'self'` meant its
  whole 127.0.0.1 origin; sandboxed, WebKit refused the service page's
  fetches under `connect-src 'self'`. `serviceSelf` names the service's
  root under both schemes after each `'self'` in a policy header it sends
  (and `ws://`, `wss://` in `connect-src` and `default-src`); a `<meta>`
  policy is the plugin's to write with whole sources.
  `TestPluginPageWindowPolicy`, `TestPluginPageWebPolicyNamesItsScheme`,
  `TestServiceSelf` and `TestPluginPageServiceProxied` check the policies, and
  `TestPluginPageLoadsItsFilesInTheWindow` loads a page through
  `ownWindow` in a real WKWebView; Playwright, on http, can't see it.
- **Slots.** A page names a slot with `<div class="plugin-slot"
  data-slot="…" hidden>` in its markup, or app.js draws one with
  `magpie.slot(name, context)` ([`pluginpages.js`](../../internal/gui/assets/pluginpages.js)).
  A slot is filled when the panels come, again only when they changed (a
  panel open goes on as it is), and when the kernel writes a page's markup
  afterwards (`magpie.fillSlots`, from `add` and Try again).
- **Replacing a page.** `magpie.page.replaces`, one of `plugin.Replaceable`
  (`sessions`, `library`, `routing`, `usage`), puts the plugin's page in
  the built-in's place in the sidebar: the built-in's button is hidden
  (`data-replaced`) and `show()` of it opens the plugin's page
  (`magpie.replacing`). The first on plugin in plugins.json's order takes
  a page; another asking for the same is a page of its own. The built-in
  page is still there, and comes back when the plugin is off.
- **The Plugins page** says, on the row, where a plugin's panels go and
  what they read, and which page it takes the place of (`panelsLine`,
  `pageLine` in [`plugins.js`](../../internal/gui/assets/plugins.js)),
  with a Panels chip.

Tests: `TestWindowPanels`, `TestPluginPanelsServedShutIn`,
`TestPluginPanelsReadWhatTheyDeclared`, `TestPluginPageReplacesFirstWins`,
`plugin-panels.test.cjs`. Example: `internal/gui/testdata/panel-plugin`.

## P6: actions in magpie's menus

P6 lets a plugin's page add items to the row menus magpie draws: a
provider row's (right-click) and an agent row's (its handle, or
right-click). An item opens the plugin's page and tells it what was picked;
the page does the work. Read by `plugin.Window` with the rest of the window
manifest ([`page.go`](../../internal/plugin/page.go)); no Bun host.

- **The manifest.** `magpie.actions: [{id, menu, title, titles}]`. `menu`
  is one of `plugin.ActionMenus` (`provider`, `agent`); a session's menu is
  left out, since a session's title is the user's content. `id` matches
  `actionID` (`^[a-z0-9][a-z0-9-]{0,39}$`), an id given twice is the
  first's, and a plugin has 8 at most (`maxActions`). A plugin with no page
  has no actions: an action is a way into its page. A missing title is the
  page's, in each language (`windowParts` in
  [`pluginpage.go`](../../internal/gui/pluginpage.go)); `/api/plugin-pages`
  carries them on the page.
- **The menus.** `magpie.actions(menu, context)`
  ([`pluginpages.js`](../../internal/gui/assets/pluginpages.js)) gives the
  items, after magpie's own and a separator, named by `titled` and drawn
  as the plugin wrote them (`literal` in `openRowMenu`, not through `t()`).
  `openProviderMenu` passes `{provider: {id, name}}`, `openAgentMenu`
  `{agent: {id, name}}`, and nothing else of the row.
- **Picking one** runs `show(key)` (an unsaved edit the reader keeps
  stops it there), waits for the page's hello, and posts `{event:
  "action", data: {id, ...context}}` to that frame only. The bridge
  ([`pagebridge.js`](../../internal/gui/pagebridge.js)) hands it to
  `magpie.onAction(fn)`; an action that came before `fn` was given waits
  for it, and a throw in `fn` is the page's error ("stopped working").
- **The Plugins page** says on the row which menus a plugin adds to
  (`actionsLine` in [`plugins.js`](../../internal/gui/assets/plugins.js)),
  from the row's `actions` ([`plugins.go`](../../internal/gui/plugins.go)),
  said of a plugin switched off too.

Tests: `TestWindowActions`, `TestPluginPageActions`,
`plugin-actions.test.cjs`.

## Source of truth and composing

What a plugin runs with is composed field by field (`Compose`), each value
remembering its layer (`Source`):

1. `default`: every plugin on, its `Manifest.Defaults`;
2. `preset`: Essentials leaves every plugin not `Essential` off;
3. `user`: the rows of plugins.yml (`enabled`, and `config` merged into the
   defaults key by key, maps deeply);
4. `in use`: a plugin whose `InUse` is above 0 is on whatever the rows say,
   since off its things would go on working with nowhere to see them. An
   `InUse` error counts as in use.

`Composed.Chosen` is plugins.yml's say before step 4. `magpie plugin dump`
(`--json`) prints the composed list with each plugin's state and why.

## Runtime paths

- **Start** (`internal/cli/main.go`, after `settings.Migrate`):
  `pluginrt.Settle()` writes a first plugins.yml, `all`, when there is
  none, on a new install too: a new user sees everything magpie does and
  turns off what they don't want. A file below `settled: 1` is brought up
  once: the `essentials` a new install was written from 10-13 to 10-17
  becomes `all`, the user's own rows kept. Every write sets `settled`, so
  an Essentials the user picks afterwards stays theirs. Then
  `pluginrt.Refresh` makes the process's runtime and reconciles it. An error
  is printed and magpie starts.
- **Every read**: `Refresh` loads plugins.yml again, composes it and
  reconciles. The window's `/api/state`, `/api/plugins/builtin` and boot.js
  each call it (`builtinsState`), so a change made by the CLI shows on the
  next read.
- **Switch**: `Set(id, on)` (`POST /api/plugins/builtin`, `magpie plugin
  builtin on|off <id>`) writes the user's row; `SetPreset` the preset
  (`magpie plugin preset essentials|all`). Off is refused while the plugin
  is in use.
- **The window**: see [GUI app shell](gui-shell.md), "Built-in plugins".
- **The gateway**: the process serving it runs `pluginrt.KeepFresh` (every
  2s), which refreshes when plugins.yml, settings.json or providers.json
  changes (and plugins.json, for `middleware`), so a
  plugin turned on or off in the window or by `magpie plugin builtin` comes
  and goes in the gateway without a restart, and a headless `magpie serve`
  follows the file too.

## Failure and compatibility

- A plugins.yml that can't be read or doesn't parse, or names another
  preset, is an error, never an empty file. `Refresh` returns it and leaves
  the runtime as it was; before the first good read every plugin is on, as
  before plugins existed. `Set` and `SetPreset` refuse to write over it, the
  window says it above the list and disables every switch, and the CLI exits
  with it. The user fixes the file or removes it.
- Writes hold `filelock.Mutex` (`plugins.yml.lock`) against other magpies
  and a mutex in the process, are written whole (temp file, sync, rename),
  and keep the file they replace as `plugins.yml.bak`.
- A plugin written for another major API, or a newer minor, is `failed`
  with the reason; the rest run.
- A plugin's page whose code throws breaks only that page (the boundary
  above); a POST of a config the schema refuses is a 400 and writes nothing.
- A page plugin's off is the window's alone: its page, sections and rows
  leave the window. The gateway, the TUI and the CLI serve the same either
  way, and nothing the user made is deleted. A gateway plugin's off is the
  gateway's: with `otel` off nothing is exported and no reply is kept
  whole for it. An observer can't fail a request; it only reads. With
  `rotation` off a pool set to one of its routings is tried in its listed
  order; a strategy that fails falls back to the same order, never to an
  error. A JS middleware that fails leaves the request as it was; masking
  that is asked for and can't be done sends nothing (503). Davsync doesn't carry
  plugins.yml.

## Verification

- `go test ./internal/pluginrt/` (`TestReconcile`, `TestCompatible`,
  `TestPluginsImportOnlyCore`, `TestCompose`, `TestSettleAndSet`,
  `TestSettleBringsUpAnEssentialsInstall`, `TestBrokenFileIsKept`).
- `go test ./internal/pluginrt/ -run 'TestSchemaOf|TestValidate|TestSetConfig'`.
- `go test ./internal/gui/ -run 'TestBuiltin|TestBootSaysBuiltinsOff'`
  (`TestBuiltinConfig`, `TestBuiltinClientsLoaded`).
- `i18n-keys`, `gui-de`, `gui-ja`, `gui-zh-tw`, and a moved page's own
  tests (`sessions-*`, `session-*`, `sess-*`; `library-*`, `skill-*`,
  `mcp-*`, `found-skill-*`) in Chromium and WebKit.
- `internal/gui/tests/builtins.test.cjs` (the settings form) and
  `plugin-kernel.test.cjs` (the boundary, Try again, `callsPage` from
  boot.js and from a change) in Chromium and WebKit, every language, 1100,
  800 and 420 points; the Gateway page's own tests (`gateway-*`,
  `recent-calls-*`) unchanged.
- P3 observer: `go test ./internal/usage/ -run 'TestObserver|TestBodiesStayOutOfTheLog'`,
  `go test ./internal/gateway/ -run 'TestObserverPoint|OTel|Deferred'`,
  `go test ./internal/otlp/ ./plugins/otel/`
  (`TestOnExportsOffSendsNothing`, `TestInUseByEnvironment`) and
  `go test ./internal/pluginrt/ -run TestKeepFreshFollowsPluginsYml`.
- P3 router strategy: `go test ./internal/routes/ ./plugins/rotation/`
  (`TestRegisterStacks`, `TestApplyFailsOpen`, the strategies' orders,
  `TestOnServesOffDoesNot`, `TestInUse`), `go test ./internal/gateway/`
  whole (its `TestMain` registers the strategies, so every routing test is
  the parity run with the plugin on; `TestRotationOnAndOff` turns it off),
  and `routing-save.test.cjs` (Smart alone with it off, every width and
  engine).
- P3 groups: `go test ./internal/provider/ -run 'TestOwnGroupsWaitOnThePlugin|TestNamedGroupsUnreadable'`,
  `go test ./plugins/groups/` (`TestOnServesOffDoesNot`, `TestInUse`) and
  `go test ./internal/gateway/` whole (its `TestMain` holds
  `ServeGroups`, the parity run with the plugin on; `TestGroupsOnAndOff`
  turns it off: the user's group leaves `/v1/models` and isn't served,
  the found one is both).
- P3 middleware: `go test ./internal/gwmw/ ./plugins/middleware/
  ./plugins/redact/` (`TestMaskingFailsClosed`, `TestServedRefreshesOnce`,
  `TestSafelyTurnsAPanicIntoAnError`, `TestAgentsWantedRefreshesWhenWanted`, `TestOnServesOffDoesNot`,
  `TestInUse`) and `go test ./internal/gateway/` whole (its `TestMain`
  serves `masking.Masker` and `middleware.Chain`, the parity run with both
  on; `TestMaskingOnAndOff`, `TestMaskedQueryFailsClosed` and
  `TestMiddlewareOnAndOff` turn them off: masking asked for is a 503 and
  the vendor gets nothing, middleware off sends the request as it came).
- P5: `go test ./internal/plugin/ -run TestWindowPanels`,
  `go test ./internal/gui/ -run 'TestPluginPanels|TestPluginPageReplaces'`
  and `plugin-panels.test.cjs` (every slot, a broken panel and Try again,
  the size watch's loop notice, a replaced Library, the Settings tab
  coming and going) in Chromium and WebKit, every language, 1100 and 420
  points (the Plugins page's rows at 980 and 420).
- P6: `go test ./internal/plugin/ -run TestWindowActions`,
  `go test ./internal/gui/ -run TestPluginPageActions` and
  `plugin-actions.test.cjs` (both menus, a page that gives its handler
  late, no plugin adding nothing, the Plugins row) in Chromium and WebKit,
  every language, 1100 and 420 points.
- By hand, under a sandbox HOME: `magpie plugin builtin`, `magpie plugin
  builtin on groups`, `magpie plugin dump`, and a plugins.yml broken by hand.
