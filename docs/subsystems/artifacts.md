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
- `List` is newest `Updated` first. The stamps never tie: a publish gets
  the clock's, in milliseconds, and after the one the last publish got
  (`nextStamp`), so the order says which artifact was published last even
  when the clock stands still or is coarse (#7755 — two artifacts
  published in the same millisecond were ordered by their random ids, and
  the list was a coin toss; the CLI's list with it, #7757). Two in one
  instant (two processes, or artifacts published before this) go by id.
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

An agent on another computer reaches it only through the router, with a
gateway key (#7890: magpie in Docker, the agent in another container).
The router runs `magpie mcp artifacts` in magpie's own folder and calls
the gateway on loopback, so the server would see a local caller. The
router tells it instead: `hubCall` sets `HubCaller.Base` for a caller that
isn't `local` to `imageMCPBase` (`MAGPIE_PUBLIC_URL`, else the scheme and
Host it called), and the Library passes it as `_meta["magpie/base"]` to
this server alone (`HubBaseMeta`). The server keeps it only as an http(s)
address with a host and nothing after (`artifactBase`), and uses it for
the text of its answers, never for access:

- every address it answers is at that base (`artifactCaller.link`); the
  publish answer says so, and names `MAGPIE_PUBLIC_URL` when the base is
  the Host called rather than it;
- a `file_path` (or a file in `files` given by path) that is relative, or
  that magpie's computer doesn't have, is answered with why: the file is
  on the agent's computer, so pass the page as `html` and its files as
  `{text}`/`{base64}`, or put it in a folder mounted into both
  (`artifactFarFile`). An absolute path magpie has is read as before;
- `open_artifact` opens no browser on magpie's computer and answers the
  address to give the user.

An agent on this computer is told no base and answered as before.

| Tool | Does |
| --- | --- |
| `publish_artifact` | Publishes a page from `file_path` (read on magpie's computer, relative to the agent's folder, `~` and `file://` read) or `html`, with `files` (a local path, `{text}`, `{base64}`, or null to remove one), `title`, `description`, `label` and `capabilities` ([Page runtime](#page-runtime)). With `id` it adds a version; with `version` it refuses a publish over a newer one. Answers the page's address. |
| `artifact_guide` | How a page calls each capability (`db`, `user`, `artifact`, `assets`, `sample`, `comments`, `downloads`, `tts`, `dictation`, `mcp`, `files`, `room`, `permissions`): the calls' shapes, answers and error codes as the page runtime serves them, and what differs from claude.ai, for the ones `capabilities` names (a list or a declaration) or all of them ([guide.go](../../internal/artifact/guide.go), `Guide`). Publish's `capabilities` points to it, and a db rule that doesn't read names the levels. `TestGuideNamesWhatRuntimeServes` fails when `runtime.js` serves a namespace (its `impls`) or a `user` call the guide lacks. The guide's opening says what a shared link serves its visitors (db, user and room, [Live sharing](#live-sharing)). |
| `read_artifact` | What it is, its versions and files, its capabilities and how many documents its data holds, and the page as published, or one file with `path`, of the newest or a given version. |
| `artifact_data` | The page's data, as its owner: `get`, `list`, `query`, `set`, `update`, `str_replace`, `delete`, `batch`, with the same paths, limits and errors as the page. A write is told to every open view of the page. |
| `list_artifacts` | Newest first, with `query` over title and description and a `limit`. |
| `open_artifact` | Opens it in the user's browser (`proc.OpenBrowser`). |
| `delete_artifact` | Deletes it with every version, for good. |

The server's `instructions` and the publish tool's description tell the
agent when to make one: whenever a page would be clearer than text, or the
user would use the page rather than only read it. They say neither magpie's
limits nor a vendor's. Their first sentence carries the trigger (every HTML
page, chart, tool or game made for the user goes to `publish_artifact`):
Codex 0.172 defers MCP tools behind `tool_search` and shows a server only as
the first ~250 characters of its instructions, so a trigger later in the
text was never seen (probe on mote, 11-25).

An ask for a page reaches `publish_artifact` without naming it, as Claude
Code's own Artifact tool is reached, only if the model is shown the tool:
an agent that defers MCP tools behind a search shows a server as one line,
and a skill of the user's for diagrams then wins (Pi 1.6 with archify,
owner 11-24). So the publish tool (and the guide and the data tool, below) is declared like a built-in tool
wherever it goes: its `_meta` marks it always loaded (`anthropic/alwaysLoad`
for Claude Code, `antigravity/alwaysLoad` for Antigravity), the MCP router
lists it beside `find_tools` and `call_tool` when it gives tools on demand
(`hubAlwaysLoaded`, [MCP router](mcp-router.md)), and Pi, which reads no
`_meta`, is given `toolExposure` with `"direct"` for `publish_artifact`,
`artifact_guide` and `artifact_data` (their router names on the router's
entry, `artifactsDirect`, `piDirect`), while the entry has no `exposure`
or `toolExposure` of the user's. Behind codemode, a Pi writing a poll page
never found the guide or the data tool: it read the runtime's source for
the calls and seeded the data with curl and the page's token (11-24). A
`toolExposure` naming some of those tools, each `"direct"`, and nothing
else is the one magpie wrote before (v0.1.4756 wrote the publish tool
alone), and is written again (`piDirectBefore`).

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
- A local caller is served at a loopback Host only (`local`,
  `loopbackHost`), so a page rebound to 127.0.0.1 by its DNS reads nothing.
- Another computer's browser is served while magpie is on the network
  (`remoteKeyed`: shared on the LAN, or `MAGPIE_ADDR` beyond loopback, as
  in the Docker image), without a key: `lanGuard` lets a GET or HEAD under
  `/a/<ref>/` with no key through (`artifactPageAsked`), but not under
  `_api/`. Its page gets the runtime a shared copy gets
  (`artifact.SharedRuntime`): no token, each capability null. Its store
  (`_api/*`) and assets (`/_blob/`) stay this computer's, so a page that
  keeps data or assets shows what it shows without them there. `magpie
  web`'s port hands `/a/` to the gateway too (`clientPrefixes` in
  [oneport.go](../../internal/gateway/oneport.go), `_api/` left out), so
  the browser UI behind a proxy or in a container opens one at its own
  address, judged there as from another computer.
- `nosniff`, `Referrer-Policy: no-referrer`. A numbered version is
  immutable and cached for a year; the newest is `no-cache` with an ETag
  of its version.
- A page written as a fragment, with no doctype, is put in a document with
  a charset, a viewport and base styles (`Served`), so it doesn't render in
  quirks mode.

## Install as app

The newest page installs from the browser it opens in, as its own app
with its own window and icon, on every system the browser runs on
([internal/artifact/app.go](../../internal/artifact/app.go),
`artifactApp`). Its folder `/a/<id>/` is the app's scope, start and id,
so the app always opens the newest version. Under its `_api` (a name no
published file can take) the gateway serves, for `/a/<id>/` only:

- `app.webmanifest` (`Manifest`): the title as its name (cut to 24
  characters for `short_name`, the id when untitled), `standalone`, and
  the two icons;
- `app-icon-192.png` and `app-icon-512.png` (`AppIconPNG`): a rounded
  square in one of eight quiet colours, picked by the id, with the
  title's first letter or digit in white, or a page glyph when Go Bold
  has no such letter (a Chinese title); `no-cache`, since a new version
  may retitle it;
- `app-sw.js` (`AppWorkerJS`), with `Service-Worker-Allowed: /a/<id>/`.
  It handles the app's window opening only (a navigation to a document)
  and answers it from navigation preload. It never fetches the window
  itself: a worker's fetch goes as `Sec-Fetch-Dest: empty`, where the
  gateway serves the page alone, not the shell. When magpie can't be
  reached it answers a page saying so in magpie's language (the
  browser's with `system`), which tries again every 3s. A browser with no
  navigation preload goes to the network as before.

Any other name there, or a pinned `/a/<id>@<n>/_api/…`, is not found. The
shell links the manifest (newest only) and the icon (every version), its
CSP allows `manifest-src` and `worker-src 'self'`, and it registers the
worker when not pinned. When the browser offers to install (Chromium's
`beforeinstallprompt`), the bar gets an install button; an offer is used
once and the button goes once installed. Library's **Install as app**
opens the page with `#install`, which the shell takes off the address and
answers: with an offer it asks first (the browser's own question needs a
click in the page), and with none it says how there (Safari: File › Add
to Dock) or that this browser can't. Only this computer reaches the
gateway, so an installed app works where magpie runs.

## Page runtime

A page written for claude.ai's artifact runtime (contract 0.2.99) runs
here as written: `window.claude.use(name)` is there before the page's own
scripts, and resolves `db`, `user`, `artifact` (and `self`, its former
name), `assets`, `sample`, `comments` ([Comments](#comments)), `downloads`, `tts` ([Reading
aloud](#reading-aloud)), `dictation` ([Dictation](#dictation)), `mcp`
([MCP](#mcp)), `files` ([Files](#files)), `room` ([Room](#room)) and the
built-in `permissions`
([internal/artifact/runtime.js](../../internal/artifact/runtime.js),
[runtime.go](../../internal/artifact/runtime.go),
[caps.go](../../internal/artifact/caps.go),
[db.go](../../internal/artifact/db.go),
[artifacts_api.go](../../internal/gateway/artifacts_api.go),
[artifacts_self.go](../../internal/gateway/artifacts_self.go),
[assets.go](../../internal/artifact/assets.go),
[svg.go](../../internal/artifact/svg.go),
[artifacts_assets.go](../../internal/gateway/artifacts_assets.go),
[artifacts_sample.go](../../internal/gateway/artifacts_sample.go)).

Declaring:

- `publish_artifact` takes `capabilities` as Claude's Artifact tool does
  (`ParseCapabilities`): left out, the version before's are carried
  forward; `{}` clears them; any other object is the whole set. A version
  keeps the declaration it was published with (`Version.Capabilities`).
- Every name in the contract is accepted and kept; only `ServedCaps` (`db`,
  `user`, `artifact`, `self`, `assets`, `sample`, `comments`, `downloads`,
  `tts`, `dictation`, `mcp`, `files`, `room`) are served, and the rest resolve null on the
  page, as an unserved capability does on claude.ai. Declaring
  `permissions` is refused: it is built in. `db`'s `rules`, `user`'s
  `scopes`, `artifact`'s `{"publish": false}` and `assets`' `upload`
  (`"interact"` or `"admin"`) and `sample`'s `images` are checked at
  publish.

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

Sample (`sample`), the page asking a model:

- `sample(input, opts)` resolves `{text, truncated, modelTierApplied,
  modelApplied?}`, `sample.json` the value the answer holds, and
  `limits()` what a call may carry. The input is a string or turns that
  start and end on a user turn; turns of one role in a row are read as
  one. A call is at most 256 KiB of text.
- Each round is `POST /a/<ref>/_api/sample` and goes into the gateway's
  own Messages handler (`sampleInner`) under the agent `artifacts`'s
  token, so it is routed, failed over, masked and counted in Usage as an
  agent's request is. None of the page's headers go with it. Its answer
  streams back as NDJSON: `{"text"}` pieces, then `{"end": {stop,
  truncated, tier, model, content}}`, or `{"error": {code, message,
  reason?}}` once text has gone out. A refusal before any text is a JSON
  error with its status.
- The model is the one named while the catalog has it (`modelApplied`
  says so), else the tier's by name (`sampleTierOf`: Haiku and the other
  small ones `quick`, Sonnet `default`, Opus and the other large ones
  `complex`), Claude's first, one with allowance left before one out of
  it, else the first. With images, only one that takes them. None at all
  is `sampling_disabled`.
- The system line is `sampleFraming`, which names no product and no
  vendor (it goes upstream); `json` and tools each add a sentence.
- Tools run on the page: a round that stops for `tool_use` hands back the
  blocks the next round sends verbatim (text, thinking with its
  signature, tool calls). The page runs each tool's `execute(input,
  {signal})`, with the signal aborted on cancel, settle or after 150 s;
  a result is JSON, at most 32 KB, and a throw is `Error: <message>`.
  At most 8 rounds; the last sets `tool_choice: none`. Rounds' text is
  joined with a blank line, and `onText` gets the whole text and the
  delta, never empty and never after settle.
- Images go only from a version that declares `{"sample": {"images":
  true}}` (else `images_unavailable`): JPEG, PNG, WebP or GIF, at most
  20 MB, 10,000 px a side and 64 MP, downsized on the page to about
  1.2 MP and turned the way the image says, at most 8 a call.
- An answer is replayed to the same call (the artifact, the verb, every
  turn, the tier, model, effort and images) for its `gcTime`, 5 minutes
  unless set, at most a day; `cache: false` asks again, `refresh`
  overwrites, a call with tools is never kept, nor a refusal, an empty
  answer or a `json` answer that doesn't parse.
- Limits: 2 rounds of an artifact at once and 6 waiting, 60 a minute;
  more is `rate_limited` (a vendor's 429 too, with `reason:
  "usage_limit"` when it is the account's allowance). A vendor's errors
  map to `prompt_too_large`, `image_rejected`, `invalid_request` or
  `upstream_error`; a stop for `refusal` is `refused`, blank text
  `empty_completion`, and `json` that doesn't parse or is cut short
  `invalid_json` with the text. A rejection is `{code, message, text?}`.
- No consent is asked, and `permissions` says `granted`: every artifact
  here is the owner's own, opened on this computer, and what bounds a
  page is the limits above and Usage, where every round shows under
  `artifacts`. On claude.ai the first call asks the viewer, who may be
  someone else. A shared copy resolves it null.

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
  included. `acquire` takes a lease on a document, from 1 second to 10
  minutes (30 seconds unless asked). The lease only answers the next
  `acquire`: a `set`, `update` or `delete` from anyone with write access
  still goes, as a page's own lock would.
- A query reads its whole collection and filters and orders it in Go,
  which the 25,000-document store bounds; there are no indexes. Values
  order by kind (null, booleans, numbers, strings, arrays, objects), and
  two objects by their JSON with sorted keys.
- A shared copy on usemagpie.ai runs `SharedRuntime`: `window.claude` is
  there, `db`, `room` and `user` are served there by the site ([Live
  sharing](#live-sharing)), and every other capability resolves null, so
  the page shows what it shows without them. Its assets go with it
  ([Sharing](#sharing)), so `/_blob/<id>` shows there as it does here.

## Comments

The viewer comments on a page, pinned to its elements, and can send a
comment to the agent that published it, which changes the page and
answers in the thread, as claude.ai's artifact shell does
([internal/artifact/shell.go](../../internal/artifact/shell.go),
[shell.js](../../internal/artifact/shell.js),
[comments.go](../../internal/artifact/comments.go),
[artifacts_comments.go](../../internal/gateway/artifacts_comments.go),
[artifacts_send.go](../../internal/gateway/artifacts_send.go)).

The viewer shell:

- A tab opening the page (`Sec-Fetch-Dest: document`) gets the shell
  (`artifactShell`), not the page. It runs at magpie's own origin under
  `ShellCSP` (a nonce for its one script and style, `frame-ancestors
  'none'`), `no-store`, with no `Access-Control-Allow-Origin`, and frames
  the same address, query kept; its script adds the fragment, which the
  server never sees. The frame's request is served the page as before,
  sandboxed, with the runtime.
- It holds the shell's token (`ShellToken`, an HMAC apart from the
  page's), which lists and writes threads and sends them to the agent.
  The page's token never does what the shell's does, and the reverse.
- It talks to the page's runtime by `postMessage` (`{magpie:
  "artifact-shell"}`) and hears only `e.source === frame.contentWindow`:
  a frame inside the page is not heard. It posts with `"*"`, since the
  page's origin is opaque; what it sends is what the page could read
  anyway (its pins, its own comments' replies).
- Comment mode (the bar's Add comments): the runtime takes the page's
  clicks in the capture phase, so the page's own handlers never hear
  them, and anchors a thread at the clicked element (`#id` when unique,
  else a path), at a point in its box. `[data-uncommentable]` takes no
  comment; `[data-comment-target]` takes the click to itself. Escape
  ends the mode.
- Pins follow the page as it scrolls (the runtime reports each pin's
  place). A pin shows while its anchor is in the frame, dropped or moved
  to stay inside it at an edge (`drawPins`). A card replies, resolves, reopens and deletes (asking once);
  the list holds Open and Resolved and a composer for the whole page.
  ⌘↩ posts, ⌘⇧↩ sends to the agent.
- `<page>/_api/shell?t=<shell token>` is its stream: `{"comments":true}`
  when threads change, in this process or another, and `{"version":n}`
  when one is published, so a shell on the newest loads it.

The store and API:

- `<id>/comments.json`, beside meta.json, written through a temp file;
  at most `MaxThreads` threads of `MaxReplies` comments, text 1 to 4096
  bytes of UTF-8, newlines and tabs but no other control characters
  (`CheckCommentText`). A thread opens again when a person writes in it,
  not when the agent answers; it goes with its last comment.
- `POST <page>/_api/comments`, JSON as `text/plain` with `t` the token
  and `op`: list, create, reply, resolve, delete (a thread, or one
  comment), send, stop and can. Local callers at a loopback Host only.
- A page's own token, from a version declaring `comments` in full, may
  ask `can` only; a write with it is `consent_required`. The page's
  create, reply, resolve and delete go to the shell (`comment`), which
  asks the viewer once for each artifact ("Let “…” comment as you?",
  kept in localStorage like files and changed in Permissions) and
  writes with its own token, never with `send` (`commentOp` in
  shell.js). `{composer_only: true}` writes nothing and asks nothing: it opens the shell's composer (`openComposer`) and makes
  anchors (`anchorFor`). Like a click, that composer (and
  `sendToClaude`'s) points at what the viewer sees: an element out of
  view is scrolled into it, and the point and the anchor are the middle
  of its visible part (`shown` in runtime.js). Nothing lists threads but the shell (the
  contract's WRITE-ONLY).
- A page never sends to the agent. `sendToClaude` opens the shell's composer filled
  in, and nothing goes until the viewer presses Send; dismissed, or
  posted as a plain comment, it rejects `consent_required`. When the
  viewer sends, the page's `onSendToClaude` hears it first and may answer
  `{handled: true, reply}`: the comment is posted plain and the reply
  shown. It resolves once the comment is posted, the composer already
  closed (a reply's box emptied), not once the list is back, so the page
  may ask again at once; a thread the shell doesn't list yet is waited
  for while a list is on its way (`loading`), then `not_found`.

Sending to the agent:

- The agent is the publisher's (`sendAgentOf`: claude-cli and
  claude-code are Claude Code, opencode and pi by prefix) when its
  command is installed, else the first installed of Claude Code, Codex,
  OpenCode, Pi. With none, `canSendToClaude` is `no_session` and a send
  is `claude_unavailable`, with nothing posted.
- The publisher's session is found among the agent's sessions, in its
  folder, active when it published, with the artifact's id in its file,
  and resumed without a window: `claude -p --resume` (acceptEdits, the
  artifacts tools and Read/Glob/Grep/Edit/Write only), `codex exec
  resume` (workspace-write), `opencode run --session`, `pi -p
  --session`. None found: a new session, in the publisher's folder or
  one of the artifact's own under `.runs`.
- The prompt (`sendPrompt`) names no product. The thread's spot, label
  and quote come from the page, so they are quoted as data; the comments
  are the viewer's. One run at a time per artifact, a later send waiting
  (`waiting`); Stop cancels it; at most 20 minutes. What the agent says
  at the end is posted in the thread, and a version it published shows
  in the shell at once.
- Not on usemagpie.ai: a shared copy has no shell and `comments`
  resolves null.

### Downloads

`downloads.save({filename, data})` hands the viewer a file through the
shell, so it is served only where comments are: a page in its shell.
- The runtime checks the call as contract 0.2.99 has it: the name (a
  string of at most 512 characters, cleaned of path, invisible
  characters and runs of space, cut to 240 bytes), the extension's
  allowlist (`mov` is `not_signed`: a page's file has no content
  credentials), data as a string, Blob, ArrayBuffer or view, at most
  64 MiB, and no `request` (`request_unknown`). The extension gives the
  type.
- The shell checks the name and size again, since a page can post to it
  without the runtime, and asks the viewer: "Save “name”?" with its size,
  Cancel and Save. One file at a time (a second is `rate_limited`); Save
  and Cancel take no click for 400ms after it shows; Escape, Cancel or a
  minute without an answer is `declined`. Save writes the bytes as
  `application/octet-stream` from magpie's origin, never as a page that
  could open there.
- The page's sandbox still has `allow-downloads`, so `<a download>` in
  the page works as before; `downloads` is the call a page written for
  claude.ai makes.

### Reading aloud

`tts` reads a page's text to the viewer
([internal/gateway/artifacts_tts.go](../../internal/gateway/artifacts_tts.go)).
Like downloads it is served only in the shell.
- The runtime keeps the reading: its lines, the calls waiting on them,
  the state (`starting`, `speaking`, `paused`, `idle` with a reason),
  pause, rate (`playbackRate`, 0.7 to 1.5) and the events. For each line
  it asks the shell, which posts `{t, text, voice, version}` to
  `<page>/_api/tts` with its own token and hands the audio back as a
  Blob. The page plays it in its own frame (the iframe has
  `allow="autoplay"`), so the viewer's click in the page is the gesture
  the browser asks for. The next line is fetched while one plays.
- The route takes the shell's token alone, for a version that declares
  `tts`. It reads the line as plain text (`plainOfMarkdown`, 1 to 2000
  characters), maps the contract's voices onto OpenAI's (buttery nova,
  airy shimmer, glassy alloy, rounded onyx), and asks the first speech
  model (`FirstServing(catalog.ServesSpeech)`) through `speak` as the
  `artifacts` agent, so masking, routing and Usage are the speech
  route's. With no speech model it answers `unavailable`, and at most 60
  lines a minute per artifact (`rate_limited`, Retry-After).
- While it reads, the shell shows "Reading aloud" and Stop at the bottom.
  Stop, Escape in the shell, or an Escape in the page that nothing there
  claimed ends it as `viewer_stopped`, and for 6 seconds after a new
  speak is `no_gesture` with `retryAfterMs`, a click or not. A page left
  hidden ends it (`hidden`), and one paused for 5 minutes (`time_limit`).
- `language` is checked and kept, not sent: OpenAI's speech models tell
  the language from the text. While the viewer dictates, `speak` and
  `add` reject `busy`, and a dictation that starts ends the reading
  (`interrupted`).

### Dictation

`dictation` gives a page the viewer's speech as text
([internal/gateway/artifacts_dictation.go](../../internal/gateway/artifacts_dictation.go)),
served only in the shell, in a browser with `MediaRecorder`.
- The shell holds the microphone, at magpie's origin, so the page's
  iframe needs no `allow="microphone"` and the page never gets the sound.
  The page's `start()` must come from the viewer's click or key
  (`navigator.userActivation`, else `no_gesture`); the shell's
  `getUserMedia` runs on that activation, which the browser carries from
  the frame to its parent.
- Consent is the viewer's, once for each artifact, kept in the shell's
  `localStorage` (`magpie.artifact.<id>.dictation`): the first start asks
  "Let “name” take dictation?". Don't allow is kept (`not_granted` from
  then on); closing the dialog, Escape or a minute without an answer is
  `dismissed` and asks again on a later click. `permissions.state`
  reads it as `prompt`, `granted` or `denied`, `request` asks in the same
  dialog, and `manage` opens a Permissions panel with Ask, Allow and
  Don't allow.
- The shell cuts the recording into phrases by level (an `AnalyserNode`
  against a moving noise floor): 800ms of quiet after speech ends a
  phrase, and so do 30 seconds of it or 10 seconds of none. A phrase with
  speech in it is posted to `<page>/_api/dictation` with the shell's
  token, and its text comes back as one `onFinal`, in order; every 2.5
  seconds the phrase so far is sent for an `onPartial` guess. The next
  phrase records while one is sent, so nothing said is lost.
- The route takes the shell's token alone, for a version that declares
  `dictation`, a recording of 1 byte to 8 MiB in a browser's format
  (webm, ogg, mp4, wav), and a language of two letters with an optional
  region. It asks the first speech-to-text model
  (`FirstServing(catalog.ServesTranscription)`) through `transcribe` as
  the `artifacts` agent, so masking, routing and Usage are the
  transcription route's. With no such model it answers `unavailable`,
  and at most 120 pieces a minute per artifact (`rate_limited`).
- While it listens, the shell shows "Listening" and Stop at the bottom.
  Stop, Escape in the shell, or an Escape in the page that nothing there
  claimed ends it as `viewer_stopped`; a hidden page ends it (`hidden`),
  and so do 60 minutes (`time_limit`). `stop()` resolves once the last
  phrase's words are in.

### MCP

`mcp` lets a page call the tools of MCP servers in the Library, as the
viewer ([internal/gateway/artifacts_pagemcp.go](../../internal/gateway/artifacts_pagemcp.go)),
served only in the shell.
- The manifest is `{"servers": [{"server", "tools"}]}` (`MCPConfig`,
  checked at publish: 1 to 32 servers, 1 to 128 tools each, names of
  `[A-Za-z0-9_.-]`). `server` is a Library server's name, or
  `host:<name>` for the server whose name, with anything outside
  `[A-Za-z0-9_-]` as `_`, reads so (`libraryServerOf`). Every server here
  is on this computer, so `host:` and plain names reach the same ones.
- Consent is the viewer's, once for each server and artifact, kept in the
  shell's `localStorage` (`magpie.artifact.<id>.mcp:<server>`). The first
  `callTool` on a server not asked about yet asks "Let “name” use
  server?", naming up to six of its tools. Don't allow is kept
  (`not_in_manifest` from then on); a dialog closed unanswered, or one
  that can't open while another is up, is `upstream_error` with
  `retryable` and `retryAfterMs`, and the call never ran. A watch never
  asks (`approval_required`); `permissions.request(["mcp:<server>"])`
  asks for several in one dialog, and the Permissions panel has a row per
  manifest server. `describeTool` answers only once the server is
  allowed.
- The route, `POST <page>/_api/mcp`, takes the shell's token alone, from
  this computer, for a version whose manifest names the server and tool.
  `list` answers the manifest's servers found in the Library with the
  tools the manifest lists (no schemas), and the manifest itself, so the
  shell lists a server not asked about yet from it (`authStatus:
  "unknown"`, names only). `call` runs `MCPHub.Call` as the agent `""`
  (this computer), with the session `artifact-<id>`, so the Library's
  on/off and a key's `MCPAllows` hold (`blocked_by_policy`), under
  `hubCallWait`; at most 300 calls a minute per artifact, input a JSON
  object of at most 1 MiB. A watch of a tool whose `readOnlyHint` is
  false is refused. A result with `isError` is `tool_error`, with the
  whole result.
- The page runtime keeps the cache: a call's identity is the server, the
  tool and its input as order-insensitive JSON (`undefined`, `null` and
  `{}` are one). A `readOnlyHint: true` tool is cached by default
  (`{staleTime: 0, gcTime: 5 min}`), `{cache: false}` turns it off, a
  declared write never is, and the caps are 7 days. A cached call feeds
  the watches of its identity, concurrent cached calls share one flight,
  and a denial drops the server's entries. `watchTool` replays the cache
  (`cache: {storedAt, revalidating}`), refetches when stale, polls at
  `refetchInterval` (30s at least, paused while the page is hidden, with
  a catch-up when it shows again), and holds at most 64 watches.
- Not served: Claude's own servers for an artifact (`kind: "artifact"`)
  and file arguments (`$file` rejects `capability_disabled`).

### Files

`files` lets a page read the files of one folder on this computer, as
the viewer ([internal/gateway/artifacts_pagefiles.go](../../internal/gateway/artifacts_pagefiles.go)),
served only in the shell.
- The folder is fixed at publish, never by the page (`FilesDir`,
  `Version.FilesDir`). `{"files": {}}`, or a `project` that isn't an
  absolute path (a Claude project id such as `chan_…`), is the folder the
  publishing agent works in (`Publish.Cwd`, from the caller's roots);
  `{"project": "/abs"}` names one. A publish with no folder of its own,
  as a page's publishing itself is, keeps the version before's; a
  publish that can't name one is refused. Refused too: a folder that
  isn't there, a disk's root, the home folder or one holding it,
  compared by the folder itself (`os.SameFile`, so a symlink or another
  case is caught), and every folder when the home folder can't be found.
- Consent is the viewer's, once for each artifact, kept in the shell's
  `localStorage` (`magpie.artifact.<id>.files`). The first call asks "Let
  “name” read the files in folder?", with the whole path under it. Don't
  allow is `not_granted` from then on; a dialog closed unanswered starts
  a 3s wait (`rate_limited`), and one that can't open while another is up
  is `rate_limited` too. `permissions.request(["files"])` asks, and the
  Permissions panel has a Files row.
- The route, `POST <page>/_api/files`, takes the shell's token alone,
  from this computer, for a version that declares files. It opens the
  folder with `os.Root`, so no path, `..` or symlink leaves it, and
  `.git` is never listed or read. `list` answers 500 entries a page in
  walk order with a cursor (the last path); `read` at most 25 MiB, with
  `X-Magpie-Size` and `X-Magpie-Revision` (a hash of size and time
  changed); a download `stat`s the file, the viewer confirms its name and
  size, and `save` sends it only at that revision (`changed` else), at
  most 512 MiB, never a program or script (`.exe`, `.js`, `.sh`, `.bat`
  and the like: `bad_request`). At most 600 calls a minute per artifact.
- `asSampleTools()` gives `list_project_files` and `read_project_file`
  (about 12 KB of text a call, ended at a whole character, with where the
  next part starts; a file with a NUL in its first 8000 bytes is named as
  not text).

### Room

`room` reaches whoever has the page open now
([internal/gateway/artifacts_room.go](../../internal/gateway/artifacts_room.go),
the room section of runtime.js). Here every tab is the owner's; on a
shared copy the site serves it to its visitors too ([Live
sharing](#live-sharing)), on the same wire.

- One WebSocket a page, `GET <page>/_api/live?t=<token>`, from this
  computer, for a version that declares room (`artifactAPIOpen`). The
  page sends `{t: join|leave|emit|presence, room, ref, topic, data,
  patch}`; it hears `hi {sid}`, `joined {ref, room, peers}`, `peer`,
  `gone`, `msg {room, topic, data, from}` and `error {ref?, code,
  message}`. Room `""` is the lobby, which `use("room")` joins; a name
  is `^[a-z0-9][a-z0-9_.-]{0,47}$`, at most 16 a socket.
- A peer is `{peer, by, kind, presence, sameTab, isMe, guest}`; `by` is
  the viewer's id when the page declares user, else null. Here `isMe` is
  true for every tab (all are the owner's) and `sameTab` marks the
  page's own socket.
- Limits, the page's and the hub's alike: `data` and the merged
  presence 4096 bytes and 8 deep, a message 64 KiB, 40 a second with a
  burst of 80 (a page past it drops the message and reports
  `rate_limited` once), 200 peers a room, 256 messages queued a socket
  (a socket that falls further behind is dropped).
- `emit` is heard by everyone in the room, the sender too (as
  `sameTab`). A topic's level is the declaration's (`{room: {topics:
  {move: "interact"}}}`, else admin), checked on the page; here every
  tab is the owner, so the hub checks none.
- Presence merges the patch, a null deleting its key, and is sent whole
  about every 33ms; a newcomer gets every peer's in `joined`.
- The socket is dialled again with a backoff (1s to 30s) and every room
  joined again; `onConnection` says when it drops. The artifact deleted
  closes it with 4001, which ends the room for good (`revoked`).
- Nothing is kept: a page that needs state for a late joiner keeps it
  in `db`.

## Added by the user, and Claude Code's own tool

[internal/library/artifacts.go](../../internal/library/artifacts.go):

- Nothing gives `magpie-artifacts` (`magpie mcp artifacts`) to an agent
  unasked. It is in the Library's market (featured), and Work › Artifacts'
  empty page leads there ("Turn it on in the Library").
- From 11-24 to 11-25 the first Sync added it, ticked for every agent whose
  MCP config takes a command, and recorded `ArtifactsOffered`. Users asked
  why a server and a deny rule appeared that they never chose (X, 11-25),
  so the first Sync after that takes back the one it added, once
  (`withdrawArtifacts`, `ArtifactsWithdrawn`): the server still under its
  name and ticked on every agent it was given to leaves the library and
  each agent's config, with Claude Code's deny below. One the user changed
  (an agent unticked) or added again later is theirs. Pages already
  published stay on Work › Artifacts.
- Claude Code given it leaves out its own Artifact tool: `Artifact` in its
  `permissions.deny`, beside the rules magpie writes for tools turned off
  (`syncToolRules`, `claudeArtifactTool`), recorded in `Applied` and taken
  back when the server goes. A rule of the user's own for `Artifact`
  (allow or ask, `claudeRuled`) is theirs, and magpie writes no deny. The
  market's text says so before the user adds it.

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
menu to open one, installs it as an app ([Install as app](#install-as-app)), shares it after asking (`Share…`, then the shared link
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

The addresses are this computer's (127.0.0.1). A viewer of `magpie web`
from another computer (`remotePage` in app.js) opens, copies and installs
them at the page's own address instead (`at` in artifacts.js), which its
port hands to the gateway: the page reads there as a shared copy, while
magpie is on the network (above). An agent away is given its own address,
above.

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
- A share's assets (#7649): `GET /api/shares/<id>/blobs` lists them,
  `PUT` and `DELETE /api/shares/<id>/blobs/<asset id>` (the token; the
  body the bytes, its type the Content-Type, one of `AssetTypes` within
  its `AssetLimit`) put one up and take it down. A page shows an asset at
  `/_blob/<asset id>`, an address naming no share and no version, so the
  content host serves it by its id alone, from D1's `blobs` (asset id →
  share) and R2's `<share>/_blob/<asset id>`, with the gateway's headers
  (its type, nosniff, a sandbox CSP for all but a PDF). The first share to
  hold an id keeps it: another's PUT or DELETE of it is a 409, so a page
  can't serve over another page's asset. A share holds 256 assets and
  100 MB of them, and each address makes 600 asset calls an hour. A
  takedown deletes its rows and its R2 prefix.

magpie:

- `<id>/share.json` (0600, beside meta.json) holds the link, the token
  and the version it shows (`Shared`). The token is nowhere else, so a
  share.json that doesn't read is an error, never "not shared": sharing
  again would make a second link and lose the first one's token.
- `ShareLatest` shares the newest version, at a new link or at the one it
  has; the same version again sends no version, only brings its assets up
  to date.
- The artifact's assets go with it (`syncAssets`, share_assets.go): those
  the share lacks go up, those deleted here come down. For a shared link
  they go before the new version, so the version viewers reload to finds
  each asset it names; a new link's go right after its POST, before its
  link is anywhere. An upload or delete while shared syncs in the
  background (`assetsChanged`, at most one queued an artifact). An asset
  the site refuses (its budget, or an id another share holds) is left out
  and the rest go; `Share.Note` says which, and the CLI and the publish
  tool's answer say it too. The GUI shows nothing of it. The gateway's publish calls it
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

### Live sharing

A shared page that declares `db`, `room` or `user` is served them on
usemagpie.ai, so its visitors use it together
([site/live.js](../../site/live.js),
[internal/artifact/share_data.go](../../internal/artifact/share_data.go)).

- `shareBody` sends the declaration of those three in meta's
  `capabilities`, and the owner's id (`viewerID`) as `owner`, so the
  owner's `data/users/<id>/` stay theirs there. A version that declares
  `db` sends the store whole as a `data` part (`DumpDocs`); the site
  takes it once, from the first version that declares `db` (the share's
  object is then `held`), and keeps its own after that, which visitors
  have been writing, through versions that drop `db` too. The share
  remembers that it held data (`Share.Data`).
- While shared, the data lives on the site: visitors read and write it
  there, under the page's rules. Visitors sign in on the link's page
  (level `interact`, `guest: true`); one not signed in views (`view`).
  The wrapper hands the frame a session (`hello`, then the token and
  the viewer), and the frame's store calls and room go to
  `/api/shares/<id>/rt/…`. The wrapper opens the frame with a random
  key in its hash (`#magpie-share=`), which the runtime takes off the
  address before the page runs and says in `hello`, with a
  MessagePort it made. The wrapper sends only on that port, so only
  that document hears it, even after a link in the frame loads another
  page there; a document that says another key gets nothing, and the
  frame is opened again at a new key. A new token comes ten minutes
  before the old one ends, on a new socket.
- Bounds: a share's object takes 1,000 sockets, 20 of one signed-in
  viewer; joins, leaves, emits and presence share a page's 40 a second;
  a message is 64 KiB of UTF-8. Sign-ins and shares are counted apart,
  30 an hour from one address each. Taking a share down ends its
  object's data and the owners' keys and codes.
- `Unshare` takes the data back first, when a version declared `db`:
  `GET /api/shares/<id>/data` (the share's token, `{held, docs}`) and
  `RestoreDocs`, which replaces the store whole,
  each document at its version and time, all or none. A read that fails,
  or data the store won't take, keeps the page shared and says why, so
  nothing the visitors wrote is lost. A 404 is a link taken down: there
  is nothing to take, and so is `held: false`. `Delete` doesn't take it
  back.
- The owner opens the shared page as its owner from Work › Artifacts'
  "Open shared page" or `magpie artifacts open-shared` (`OwnerLink`):
  `POST /api/shares/<id>/owner` gives a one-time code in the link's
  hash, which the link's page trades for an owner key its browser
  keeps.

## Verification

```sh
go test -tags nogui ./internal/artifact
go test -tags nogui ./internal/gateway -run 'TestArtifact|TestAssets|TestSample|TestSendAgentOf|TestHubTellsArtifacts'   # TestArtifactPage* hold the page runtime's API, TestArtifactComment* comments, TestArtifactLive* the room, TestArtifactApp installing
go test -tags nogui ./internal/library -run 'TestArtifacts|TestHubTellsMagpieArtifacts'
go test -tags nogui ./internal/gui -run 'TestArtifactsAPI|TestArtifactsShareAPI|TestBuiltin'
go test -tags nogui ./internal/cli -run 'TestArtifacts|TestEveryRouteHasACommandOrIsNamed'
node --test internal/gui/tests/artifacts-page.test.cjs
node --test internal/gui/tests/artifact-runtime.test.cjs
node --test internal/gui/tests/artifact-comments.test.cjs internal/gui/tests/artifact-downloads.test.cjs internal/gui/tests/artifact-tts.test.cjs internal/gui/tests/artifact-dictation.test.cjs internal/gui/tests/artifact-mcp.test.cjs internal/gui/tests/artifact-install.test.cjs
node --test site/shares.test.mjs site/live.test.mjs
node --test internal/gui/tests/nav-pages-order.test.cjs internal/gui/tests/builtins.test.cjs
```
