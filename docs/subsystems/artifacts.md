# Magpie Artifacts

Pages an agent makes for the user to look at or use: a report, a chart, a
diagram, a prototype, a small app. Claude Code's own Artifact tool keeps
its pages with the claude.ai account that made them, so a page made before
magpie switched the Claude account doesn't open under the next one. Magpie
Artifacts are kept on this computer with every version and open whichever
account or agent made them. Any agent that takes an MCP server can make
them, not Claude Code alone.

Three parts: an MCP server the agents publish through, the gateway serving
each page in a sandbox of its own, and Work › Artifacts in the window.

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
- `Delete` takes the folder away with every version.

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

A row says the title, who made it, when, and how many versions it has. A
click opens the newest in the browser. Its menu copies the link, lists
Earlier versions (when it has any) in the app's menu to open one, and
deletes it after asking. A find field shows once there are 8 or more. The
Library's settings stay on its first page (`builtinsState` takes the first
`nav.page`).

`magpie artifacts` does the same in a terminal
([internal/cli/artifacts_cli.go](../../internal/cli/artifacts_cli.go)):
the list (`--json` with every version's address), `open <id>[@<n>]`, and
`delete <id>`, which takes every version and refuses an `@<n>`. Each takes
an id or an address.

The addresses are this computer's (127.0.0.1): a viewer of `magpie web`
from another computer can list them but not open them.

## Verification

```sh
go test -tags nogui ./internal/artifact
go test -tags nogui ./internal/gateway -run 'TestArtifact'
go test -tags nogui ./internal/library -run 'TestArtifacts'
go test -tags nogui ./internal/gui -run 'TestArtifactsAPI|TestBuiltin'
go test -tags nogui ./internal/cli -run 'TestArtifactsCommand|TestEveryRouteHasACommandOrIsNamed'
node --test internal/gui/tests/artifacts-page.test.cjs
node --test internal/gui/tests/nav-pages-order.test.cjs internal/gui/tests/builtins.test.cjs
```
