# Magpie Artifacts

Pages an agent makes for the user to look at or use: a report, a chart, a
diagram, a prototype, a small app. Claude Code's own Artifact tool keeps
its pages with the claude.ai account that made them, so a page made before
magpie switched the Claude account doesn't open under the next one. Magpie
Artifacts are kept on this computer with every version and open whichever
account or agent made them. Any agent that takes an MCP server can make
them, not Claude Code alone.

Three parts: an MCP server the agents publish through, the gateway serving
each page in a sandbox of its own, and Work › Artifacts in the window. The
user can share one on usemagpie.ai ([Sharing](#sharing)).

## Source of truth

The `artifacts` folder in magpie's config folder
([internal/artifact/store.go](../../internal/artifact/store.go), `Dir`):

- `<id>/` is one artifact. The id is 26 random base32 characters
  (`newID`, `ValidID`), so an address can't be guessed.
- `<id>/meta.json` says what it is: title, description, created, updated,
  and its versions, each with its number, time, label, the agent that
  published it, its files and their size.
- `<id>/v<N>/` holds version N whole: the page as `index.html` beside the
  files it uses, by the relative path the page references. A version is
  written into a folder of its own and never changed after.

Rules:

- One writer at a time across magpies (`lock`: a mutex and `filelock` on
  `.lock`), meta.json written through a temp file (`edit.WriteAtomic`).
- A publish without an id makes a new artifact; with one it adds a
  version. A file the publish leaves out is kept from the version before
  (hard-linked, or copied where links can't be made; `carry`), and a nil
  one removes it. With `Base` set, a publish over a newer version is
  refused (`ErrConflict`), so an agent never writes over a change it
  didn't read.
- The title is the page's own `<title>` (`PageTitle`), else the one given.
- Sizes: a page up to `MaxPage`, a file up to `MaxFile`, a version up to
  `MaxVersion`, at most `MaxFiles` files. A path is cleaned (`CleanPath`):
  relative, no `..`, no backslash or drive, never `index.html` (the page).
- A meta.json that can't be read is left out of `List`, named in its error,
  and never written over. A folder with no meta.json (a publish or delete
  cut off) is skipped.
- `List` is newest `Updated` first; two in one instant go by id.
- `Delete` takes the shared link down first ([Sharing](#sharing)), then
  the folder with every version.

## The MCP server

`/mcp/magpie/artifacts` (`ArtifactsMCPPath`,
[artifacts_mcp.go](../../internal/gateway/artifacts_mcp.go)), served as the
other own servers are (`serveMCP`), to agents on this computer alone: it
reads files the agent names, so a remote caller or a browser page gets 403
(`local`, `fromBrowser`). `magpie mcp artifacts`
([browsermcp.go](../../internal/browsermcp/browsermcp.go), `RunArtifacts`)
is its stdio bridge, which sends the folder the agent started it in and
the client name its `initialize` gave, as Magpie Memory's does
(`withFolder`). It is listed among the gateway's own servers (`ownMCP`),
so the MCP router offers it too.

| Tool | Does |
| --- | --- |
| `publish_artifact` | Publishes a page from `file_path` (relative to the agent's folder, `~` and `file://` read) or `html`, with `files` (a local path, `{text}`, `{base64}`, or null to remove one), `title`, `description`, `label` and `capabilities` ([Page runtime](#page-runtime)). With `id` it adds a version; with `version` it refuses a publish over a newer one. Answers the page's address. |
| `read_artifact` | What it is, its versions and files, its capabilities and how many documents its data holds, and the page as published, or one file with `path`, of the newest or a given version. |
| `artifact_data` | The page's data, as its owner: `get`, `list`, `query`, `set`, `update`, `str_replace`, `delete`, `batch`, with the same paths, limits and errors as the page. A write is told to every open view of the page. |
| `list_artifacts` | Newest first, with `query` over title and description and a `limit`. |
| `open_artifact` | Opens it in the user's browser (`proc.OpenBrowser`). |
| `delete_artifact` | Deletes it with every version, for good. |

The server's `instructions` and the publish tool's description tell the
agent when to make one: whenever a page would be clearer than text, or the
user would use the page rather than only read it. They say neither magpie's
limits nor a vendor's.

An ask for a page reaches `publish_artifact` without naming it, as Claude
Code's own Artifact tool is reached, only if the model is shown the tool:
an agent that defers MCP tools behind a search shows a server as one line,
and a skill of the user's for diagrams then wins (Pi 1.6 with archify,
owner 11-24). So the publish tool is declared like a built-in tool
wherever it goes: its `_meta` marks it always loaded (`anthropic/alwaysLoad`
for Claude Code, `antigravity/alwaysLoad` for Antigravity), the MCP router
lists it beside `find_tools` and `call_tool` when it gives tools on demand
(`hubAlwaysLoaded`, [MCP router](mcp-router.md)), and Pi, which reads no
`_meta`, is given `toolExposure` `{"<router name>": "direct"}` on the
router's entry, or `{"publish_artifact": "direct"}` on Artifacts' own
(`piDirect`), while the entry has no `exposure` or `toolExposure` of the
user's.

The agent a version is recorded under comes from the client name
(`magpie/client` in `_meta`, then the bridge's header), the caller's key,
then the MCP session (`newArtifactCaller`).

## Serving a page

`GET /a/<id>/` is the newest version and `/a/<id>@<n>/` version n
(`ArtifactPagePath`, `artifactPage`); each file is at its path after that,
and `/a/<id>` redirects to the folder so relative links work.

A page is someone else's code run in the user's browser at the gateway's
address, so:

- It is served under `Content-Security-Policy: sandbox allow-scripts
  allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox
  allow-downloads` (`artifactPageCSP`), without `allow-same-origin`. Its
  origin is opaque: it reads no cookie, storage or reply of the gateway's.
- Its requests come with `Origin: null`. `corsGuard` lets that through for
  a GET or HEAD under `/a/` (`artifactPageRead`), so a page reads its own
  files, and for a POST to `/a/<ref>/_api/db` (`artifactAPICall`), its
  store, which spends no subscription; on every other route `Origin: null`
  without a key is refused as before. Its files are served with `Access-Control-Allow-Origin: *`
  so its module scripts and fetches load. The address is the key: a frame
  anywhere that knows it can read the page, as anyone given a link could,
  and a foreign http(s) page is refused by `corsGuard` before it.
- Only a local caller at a loopback Host is served (`local`,
  `loopbackHost`), so a page rebound to 127.0.0.1 by its DNS reads nothing.
- `nosniff`, `Referrer-Policy: no-referrer`. A numbered version is
  immutable and cached for a year; the newest is `no-cache` with an ETag
  of its version.
- A page written as a fragment, with no doctype, is put in a document with
  a charset, a viewport and base styles (`Served`), so it doesn't render in
  quirks mode.

## Page runtime

A page written for claude.ai's artifact runtime (contract 0.2.99) runs
here as written: `window.claude.use(name)` is there before the page's own
scripts, and resolves `db`, `user`, `artifact` (and `self`, its former
name), `assets` and the built-in `permissions`
([internal/artifact/runtime.js](../../internal/artifact/runtime.js),
[runtime.go](../../internal/artifact/runtime.go),
[caps.go](../../internal/artifact/caps.go),
[db.go](../../internal/artifact/db.go),
[artifacts_api.go](../../internal/gateway/artifacts_api.go),
[artifacts_self.go](../../internal/gateway/artifacts_self.go),
[assets.go](../../internal/artifact/assets.go),
[svg.go](../../internal/artifact/svg.go),
[artifacts_assets.go](../../internal/gateway/artifacts_assets.go)).

Declaring:

- `publish_artifact` takes `capabilities` as Claude's Artifact tool does
  (`ParseCapabilities`): left out, the version before's are carried
  forward; `{}` clears them; any other object is the whole set. A version
  keeps the declaration it was published with (`Version.Capabilities`).
- Every name in the contract is accepted and kept; only `ServedCaps` (`db`,
  `user`, `artifact`, `self`, `assets`) are served, and the rest resolve null on the
  page, as an unserved capability does on claude.ai. Declaring
  `permissions` is refused: it is built in. `db`'s `rules`, `user`'s
  `scopes`, `artifact`'s `{"publish": false}` and `assets`' `upload`
  (`"interact"` or `"admin"`) are checked at publish.

On the page:

- The runtime and its config are put right after `<head>` (else `<html>`,
  else the doctype; `WithRuntime`), with every `<` in the config escaped.
  Namespaces are frozen; `use()` is memoized and resolves an unknown or
  unserved name null.
- The config carries the page's token: an HMAC of the artifact id under
  `<artifacts>/.secret` (0600, `PageToken`). It goes only into a page the
  browser navigates to (`Sec-Fetch-Mode` navigate or none,
  `artifactRuntimeFor`, with `Vary: Sec-Fetch-Mode`). A page's files are
  served with `Access-Control-Allow-Origin: *`, so another page fetching
  this one's HTML would read a token in it; a fetch gets the page without
  the runtime.
- Store calls are `POST /a/<ref>/_api/db` with JSON as `text/plain` (a
  simple request, so no preflight from the opaque origin); changes come on
  `GET /a/<ref>/_api/events` (SSE, the token in `t`), which a write by the
  page, by the agent, or by another magpie process (looked for every 2s)
  is told on, and `onSnapshot` queries again. A call needs this computer
  at a loopback Host, the artifact's own token, and a version declaring
  `db` (`artifactAPIOpen`), else `not_granted` or `revoked`.
- `user.id()` is one id for the person at this computer (`viewerID`),
  owner of every artifact here: `isOwner()` and `canEdit()` are true.

The page publishing itself (`artifact`):

- `publish(html)` sends a whole document (doctype first, else
  `invalid_content`) to `POST /a/<ref>/_api/publish`, built on the
  version the view is (`base`, the config's `version`). It is saved as
  the newest version with agent `page` (`PageAgent`; Work › Artifacts
  says "Saved from the page"), its capabilities and files carried
  forward. Every open view, this one too, reloads to it. A pinned view
  (`/a/<id>@<n>/`) goes to the artifact's own address.
- `publish({path: content})` saves files: a string, a Blob (sent as
  base64 with its type), `{content, contentType, ifMatch}`,
  `{delete: true}` or null; `index.html` is the page and can't be
  deleted. The saving view keeps running, and its next publish builds on
  the version it made. With `ifMatch` (a sha256, or null for "not there")
  on every entry, only those files are checked (`Publish.IfMatch`, a
  `*FileConflict` naming each path's expected and actual sha), so another
  writer's save to other files may land between; the answer then has
  `changed`, what that save changed, and the view reloads to bring it.
  `shas` comes back whenever `ifMatch` was given.
- A view whose base is no longer the newest is refused `conflict` with
  `live`, and goes to the winner. Other refusals: `not_declared` (the
  newest version no longer declares it), `not_granted` (`publish:
  false`, which `sync` and `edit` refuse too, or another page's token),
  `too_large`, `invalid_content` (with `limit` for the file count),
  `rate_limited` (30 a minute per artifact). `edit()` always refuses: a
  page here changes itself with `publish`. `sync(fn)` runs `fn` and saves
  nothing, and refuses a page using `<artifact-sync>`.
- Each new version is told on the events stream as `{"version": n}`, to
  pages that declare `artifact` or `self`, from the version they are
  (`v`), so a version published while the page loaded is told at once.
  A version heard while the view's own publish is on its way waits for
  that publish's answer, so the view doesn't take its own for another's.
- A shared artifact is shared again after a page's publish
  (`ShareLatest`, in the background).
- `metadata(refs)` (`POST /a/<ref>/_api/meta`) gives the title of each
  artifact here a ref names, by id, `/a/<id>` or a claude.ai
  `/artifact/<title>-<id>` link (host ignored), up to 200; it never
  rejects, and a ref naming nothing gets empty strings.

Assets (`assets`), the files a page keeps for the people using it:

- `upload(blob, {type})` sends the bytes as the body of `POST
  /a/<ref>/_api/upload?t=<token>&type=<type>`, as `text/plain` so the
  opaque origin needs no preflight; the type is `options.type`, else the
  blob's, else `invalid_request`. It answers `{id, url, sizeBytes,
  contentType}`; `url` is `/_blob/<id>`. `list()` and `delete(id or url)`
  are `POST /a/<ref>/_api/assets` with `op`; `delete` answers
  `{deleted}`, false when there is none by that id.
- The types are the contract's 18 (`AssetTypes`), spelled exactly, no
  parameters. An asset is at most 20 MiB, a stylesheet or script 16 MiB,
  an SVG 2 MiB; an artifact holds 1000 and 1 GiB. Text is UTF-8; a
  binary, stylesheet or script that starts with markup is refused, so
  nothing uploaded as an image is HTML when opened. An SVG is stored
  sanitized (`SanitizeSVG`): one `<svg>` root, scripts, `<style>`,
  `<foreignObject>`, animation and event handlers taken out, a
  `javascript:` or `data:` URL dropped, and a `<use>` reaches only into
  the file itself.
- Refusals are the contract's codes: `too_large` (413),
  `unsupported_type` (415), `invalid_request` (400), `quota_or_state`
  (409: the budget, or an artifact deleted), `rate_limited` (60 uploads a
  minute per artifact), `not_granted` (another page's token, or a version
  that doesn't declare `assets`), `store_unavailable` (the disk).
- They are the artifact's, not a version's: `<id>/assets/<asset id>`,
  listed in `<id>/assets.json`, kept by every publish, gone with the
  artifact. An id is 160 random bits; `/_blob/<id>` finds it in whichever
  artifact holds it (`BlobFile`, remembered after the first look), so
  every version of a page reaches it at the same address.
- `GET /_blob/<id>` answers a browser on this computer at a loopback Host
  only, as the type it was uploaded as, with `nosniff`, `Access-Control-
  Allow-Origin: *` and ranges (a video seeks). Opened as a document it
  is under `sandbox` and `default-src 'none'`, so even an SVG runs
  nothing at the gateway's origin; a PDF is left to the browser's viewer.
  corsGuard lets `Origin: null` read it, as it does a page's files.
- `upload: "interact"` is accepted and kept; with one person at this
  computer, owner of every artifact, there is no Contributor to give it
  to, so it changes nothing here.
- `read_artifact` says how many assets an artifact has and their size.

The store:

- `<id>/data.db` (SQLite), made at the first write, kept across versions,
  gone with the artifact (`closeDB` before the folder). Documents at
  slash paths, an even count of segments a document, odd a collection,
  as claude.ai's.
- Its limits are claude.ai's: a document 256 KiB and 32 deep, a path 16
  segments and 1000 bytes, a query 10 filters, 30 `in` values and 1000
  rows; a store 25,000 documents and 256 MiB, counted in the `totals`
  row, which each write updates in its transaction. A write that grows
  past a limit is `quota_exceeded`; one that shrinks a document always
  goes.
- `rules` give each path a read and write level (`view` < `interact` <
  `admin` < `owner`, and `self`, held by nobody), with `{self}` the
  viewer's own id; `data/users/<id>/` is the viewer's alone, the owner
  included. `acquire` takes a lease on a document.
- A shared copy on usemagpie.ai runs `SharedRuntime`: `window.claude` is
  there and every capability resolves null, so the page shows what it
  shows without them. Its data is never shared, and neither are its
  assets: `/_blob/<id>` isn't served there, so an `<img>` of one shows
  nothing on a shared copy (#7649).

## Given to every agent, and Claude Code's own tool

[internal/library/artifacts.go](../../internal/library/artifacts.go):

- The first Sync after it shipped adds `magpie-artifacts` (`magpie mcp
  artifacts`) to the library, ticked for every agent whose MCP config takes
  a command (`adoptArtifacts`), and records `ArtifactsOffered`. A user who
  removes it, or unticks an agent, keeps it that way; one who had it
  already is left as they were.
- Claude Code given it leaves out its own Artifact tool: `Artifact` in its
  `permissions.deny`, beside the rules magpie writes for tools turned off
  (`syncToolRules`, `claudeArtifactTool`), recorded in `Applied` and taken
  back when the server goes. A rule of the user's own for `Artifact`
  (allow or ask, `claudeRuled`) is theirs, and magpie writes no deny.

## Work › Artifacts

The Library plugin adds the page (`artifactsPage` in
[plugins/library/artifacts.js](../../internal/gui/assets/plugins/library/artifacts.js)),
its button after Library's, so it goes with the Library when that is off.
The button is the sidebar's alone: the header's row has no room for
another tab (it pushed the logo out at 860px), so there, as for Routing
groups, Library's head leads to the page (`.lib-toart`, its glyph alone on
a phone) and Library's tab is lit for it (`markNav`). Settings' search
finds it by name (`places` in finder.js).
`GET /api/artifacts` lists them with each version's address and the
agent's name as the Agents page says it, and `POST /api/artifacts/delete`
deletes one; one gone already is gone as asked
([internal/gui/artifacts.go](../../internal/gui/artifacts.go)).

A row says the title, who made it, when, how many versions it has, and
"Shared" when it is. A click opens the newest in the browser. Its menu
copies the link, lists Earlier versions (when it has any) in the app's
menu to open one, shares it after asking (`Share…`, then the shared link
is copied) or copies the shared link and stops sharing it, and deletes it
after asking. `POST /api/artifacts/share {id}` answers `{url, version}`
and `POST /api/artifacts/unshare {id}` takes the link down; the list
carries `shared` and `sharedVersion`. A find field shows once there are 8 or more. With
none yet, the page says what one is, offers three things to ask an agent
for in plain words (a click copies one; only its glyph turns to a
check), and names
the agents Magpie Artifacts is ticked on (more than three as a count, the
names on hover), with the way to its server in the Library (`magpie.call("library",
"server", …)`); for that, `GET /api/artifacts` with an empty list carries
`agents`, their names (`library.ArtifactsAgents`, none when the server is
off), left out when the library can't be read so the page says nothing of
them. The
Library's settings stay on its first page (`builtinsState` takes the first
`nav.page`).

`magpie artifacts` does the same in a terminal
([internal/cli/artifacts_cli.go](../../internal/cli/artifacts_cli.go)):
the list (`--json` with every version's address, and `shared`),
`open <id>[@<n>]`, `share <id>`, `unshare <id>`, and `delete <id>`, which
takes every version; share, unshare and delete refuse an `@<n>`. Each takes
an id or an address.

The addresses are this computer's (127.0.0.1): a viewer of `magpie web`
from another computer can list them but not open them.

## Sharing

Only the user shares an artifact, from Work › Artifacts or
`magpie artifacts share`; no tool lets an agent do it, and the publish
tool's description says so. A shared artifact opens for anyone with its
link on usemagpie.ai ([internal/artifact/share.go](../../internal/artifact/share.go),
[site/shares.js](../../site/shares.js)).

The site:

- `POST /api/shares` takes a version as multipart: a `meta` part
  (`title`, `description`, `files`: the paths, the page first) and part
  `i` holding `files[i]`. It answers `{id, url, token, version}`: the id
  is 16 base32 characters, the link `https://usemagpie.ai/a/<id>`.
  `PUT /api/shares/<id>` with `Authorization: Bearer <token>` puts a new
  version at the same link; `DELETE` takes it down (the token, or the
  site's `SHARE_ADMIN` for a reported page). The site keeps the token
  only as a SHA-256 hash, holds a version to 32 MB and 256 files, and each
  address (hashed, for an hour) to 30 creates and updates an hour.
- The link is a page of the site's own: a slim bar with the title, a
  link to magpie and Report, over a frame of the content host
  `a.usemagpie.ai/<id>/<version>/`, which serves the files under the same
  CSP sandbox as the gateway's pages (an opaque origin, so a page can't
  reach usemagpie.ai's cookies or storage). A content address opened as a
  page is sent to the link, so a shared page is never shown without the
  bar. Only the current version is served; R2 holds `<id>/<n>/<path>` and
  D1 the shares. Both the link and the content say `Cache-Control:
  no-transform`, without which Cloudflare injects its Web Analytics
  beacon into the HTML a worker answers: a shared page runs nothing of
  the site's, and the wrapper loads no analytics.

magpie:

- `<id>/share.json` (0600, beside meta.json) holds the link, the token
  and the version it shows (`Shared`). The token is nowhere else, so a
  share.json that doesn't read is an error, never "not shared": sharing
  again would make a second link and lose the first one's token.
- `ShareLatest` shares the newest version, at a new link or at the one it
  has; the same version again asks nothing. The gateway's publish calls it
  for a shared artifact, so the link follows each new version, and its
  answer says the link shows it (or that it still shows the version
  before, and why). list and read name the link.
- A 404 on an update is a link taken down on the site: share.json goes and
  `ErrSharedGone` says so. It is never shared again on its own, so a
  takedown holds.
- `Unshare` and `Delete` take the link down first; a 404 counts as done.
  Any other failure keeps the artifact and share.json, so the token isn't
  lost with it.
- `shareMu` is held over each talk with the site, so a click and a publish
  at once never make two links; the store's own lock is taken only for
  share.json's write.
- The site is `MAGPIE_SHARE_SITE`, else usemagpie.ai; in a test with no
  `MAGPIE_SHARE_SITE` sharing is off, so a test never reaches the network.

## Verification

```sh
go test -tags nogui ./internal/artifact
go test -tags nogui ./internal/gateway -run 'TestArtifact|TestAssets'   # TestArtifactPage* hold the page runtime's API
go test -tags nogui ./internal/library -run 'TestArtifacts'
go test -tags nogui ./internal/gui -run 'TestArtifactsAPI|TestArtifactsShareAPI|TestBuiltin'
go test -tags nogui ./internal/cli -run 'TestArtifacts|TestEveryRouteHasACommandOrIsNamed'
node --test internal/gui/tests/artifacts-page.test.cjs
node --test internal/gui/tests/artifact-runtime.test.cjs
node --test site/shares.test.mjs
node --test internal/gui/tests/nav-pages-order.test.cjs internal/gui/tests/builtins.test.cjs
```
