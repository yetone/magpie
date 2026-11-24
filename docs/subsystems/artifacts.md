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
| `publish_artifact` | Publishes a page from `file_path` (relative to the agent's folder, `~` and `file://` read) or `html`, with `files` (a local path, `{text}`, `{base64}`, or null to remove one), `title`, `description`, `label`. With `id` it adds a version; with `version` it refuses a publish over a newer one. Answers the page's address. |
| `read_artifact` | What it is, its versions and files, and the page as published, or one file with `path`, of the newest or a given version. |
| `list_artifacts` | Newest first, with `query` over title and description and a `limit`. |
| `open_artifact` | Opens it in the user's browser (`proc.OpenBrowser`). |
| `delete_artifact` | Deletes it with every version, for good. |

The server's `instructions` and the publish tool's description tell the
agent when to make one: whenever a page would be clearer than text, or the
user would use the page rather than only read it. They say neither magpie's
limits nor a vendor's.

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
  a GET or HEAD under `/a/` alone (`artifactPageRead`), so a page reads its
  own files; on every other route `Origin: null` without a key is refused
  as before. Its files are served with `Access-Control-Allow-Origin: *`
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
carries `shared` and `sharedVersion`. A find field shows once there are 8 or more. The
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
go test -tags nogui ./internal/gateway -run 'TestArtifact'
go test -tags nogui ./internal/library -run 'TestArtifacts'
go test -tags nogui ./internal/gui -run 'TestArtifactsAPI|TestArtifactsShareAPI|TestBuiltin'
go test -tags nogui ./internal/cli -run 'TestArtifacts|TestEveryRouteHasACommandOrIsNamed'
node --test internal/gui/tests/artifacts-page.test.cjs
node --test site/shares.test.mjs
node --test internal/gui/tests/nav-pages-order.test.cjs internal/gui/tests/builtins.test.cjs
```
