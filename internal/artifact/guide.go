package artifact

import (
	"fmt"
	"slices"
	"strings"
)

// The guide is how a page calls each capability, written for the agent
// that writes the page: what claude.use(name) resolves to on magpie, the
// calls' shapes and what they answer, and what differs from claude.ai.
// It follows runtime.js, db.go and caps.go here; a change to what a page
// can call changes it too (TestGuideNamesWhatRuntimeServes).

// guideIntro goes first, whatever is asked.
const guideIntro = `How a Magpie Artifact page calls its capabilities.

Declare each one the page uses in publish_artifact's capabilities, then in the page:
  const db = await claude.use("db")   // the capability's calls, or null
null means this view can't run it (not declared, or a shared copy): render the page without it and light the feature up when the promise resolves. window.claude has use() alone; the namespace is frozen, so keep the reference and wrap it, never assign to it. A call that fails rejects with an Error that has .code (below) and .message.

Who views it: a page opens on this computer, from magpie, and its viewer is its owner; db, assets and sample keep their data in magpie here. A link the user shares on usemagpie.ai shows a copy of the page to anyone with it. There db, user and room work for its visitors: they sign in on the link's page (level "interact", guest: true), and one not signed in only views (level "view", not in the room). Every other capability but permissions resolves null there, so a page that may be shared works without them (show the data the version embeds, or say it is read-only).

Values from db, a model's answer or a file are never markup: set them with textContent, or escape & < > " ' before they join HTML.
`

var guideCaps = []struct{ name, text string }{
	{"db", `db: data kept outside the page, which the page reads and writes and you read and seed with artifact_data.
Declare {"db": {}} or {"db": {"rules": [{"path": "votes", "read": "view", "write": "owner"}]}}. read and write are levels, least to most: "view", "interact", "admin", "owner"; "self" is a level nobody holds, for a {self} rule's prefix ({"path": "votes", "read": "view", "write": "self"}, {"path": "votes/{self}", "read": "view", "write": "interact"}). Opened on the user's computer, a page's viewer is the owner, so rules decide nothing; on a shared link they decide what its visitors read and write, so write them for that.
Paths: segments joined by "/"; an odd count is a collection ("votes"), an even one a document ("votes/v1"). data/users/<id>/... is the viewer's own (id from user.id()).
  const ref = db.doc("polls/p1")          // or db.collection("polls").doc("p1"); .doc() with no id makes one
  const snap = await ref.get()            // snap.exists, snap.id, snap.data(): the object, or undefined
  await ref.set({title: "Lunch"})          // the whole document; a JSON object
  await ref.update({title: "Dinner"})      // merges into it; fails invalid_argument when it doesn't exist
  await ref.delete()
  const added = await db.collection("votes").add({choice: "a"})   // a ref with a new id
  const q = db.collection("votes").where("choice", "==", "a").orderBy("at", "desc").limit(50)
  const qs = await q.get()                // qs.docs (each like snap), qs.size, qs.empty, qs.docChanges()
  const off = q.onSnapshot((qs) => render(qs.docs.map((d) => ({id: d.id, ...d.data()}))), (err) => show(err))
  off()                                   // stop listening
  ref.onSnapshot((snap) => ..., (err) => ...)   // one document, the same way
where's ops: == != < <= > >= in not-in array-contains (in and not-in take an array). orderBy(field, "asc" | "desc").
onSnapshot calls back at once with what is there, then whenever another view or artifact_data changes it; this page's own writes show at once with snap.metadata.hasPendingWrites true. Subscribe once per query, outside render. An empty db is normal: seed what the page needs with artifact_data after publishing, never data hard-coded in the page.
Codes: invalid_argument, quota_exceeded, transform_error (data that isn't JSON), unavailable, revoked (the artifact was deleted).`},
	{"user", `user: who views the page.
Declare {"user": {}} for the viewer's id: without it id() and me().id are null and profiles() is {}. isOwner(), canEdit() and can(name) need no declaration.
  const user = await claude.use("user")
  await user.id()        // an opaque id, stable for this viewer; store it, never a name
  await user.me()        // {id, name, avatarUrl, color, email, isOwner, canEdit}
  await user.isOwner()   // true here
  await user.canEdit()   // true here
  await user.can("data.write")
  await user.profiles([id1, id2])   // {id: {id, name, avatarUrl, color, isMe}}
  await user.search(q)   // people to pick: [] here
Names are "" on magpie (there is no account to name): show name || "You" or "Someone", and tell people apart by color or avatarUrl.`},
	{"artifact", `artifact: the page saves itself, as the artifact's newest version. Use it for a page that is the record (a checklist, a board, a plan edited in place); data kept apart from the page is db.
Declare {"artifact": {}} ({"artifact": {"publish": false}} keeps the page from saving itself). "self" is its former name and works the same.
  const artifact = await claude.use("artifact")
  await artifact.publish(html)   // a whole document, doctype first: every open view reloads to it
  await artifact.publish({"data.json": JSON.stringify(state)})   // files by path: the page keeps running
     // a file is a string, a Blob, {content, contentType, ifMatch: sha256 | null}, or null / {delete: true}
     // answers {version, changed?, shas?}
Embed the state as data in the page you publish (JSON in a <script type="application/json">, each < written \u003c) and render from it; never serialize the live DOM. Publish after a viewer's action, never on load, and batch quick edits into one publish.
Codes: conflict (another view published first: this view reloads to it; don't retry), not_granted, invalid_content, too_large, revoked.`},
	{"assets", `assets: files people add to the page (images, video, PDF, fonts, CSV, text; 20 MiB each).
Declare {"assets": {}}.
  const assets = await claude.use("assets")
  const a = await assets.upload(blob)   // {id, url, sizeBytes, contentType}
  await assets.list()                   // {assets: [...], usage: {files, bytes, maxFiles, maxBytes}}
  await assets.delete(id)               // {deleted}; only on a viewer's deliberate action
Keep the id in db; show it at "/_blob/" + id (<img>, <video>, <a>; an SVG only in <img> or CSS). Never run an asset as code, CSS or HTML.
Codes: too_large, unsupported_type, quota_or_state (the artifact's assets are full), store_unavailable.`},
	{"sample", `sample: the page asks a model, through magpie's providers.
Declare {"sample": {}}, or {"sample": {"images": true}} to send images too.
  const sample = await claude.use("sample")
  const r = await sample("Summarize: " + text, {onText: ({text}) => (out.textContent = text)})
     // r.text, r.truncated; onText's text is the whole answer so far: assign it, don't append
  const v = await sample.json("Return {\"items\": [...]} for: " + text)   // the answer parsed
  await sample([{role: "user", content: "..."}, {role: "assistant", content: "..."}, {role: "user", content: "..."}])
  opts: {signal, modelTier: "quick" | "default" | "complex", images: [Blob], cache: false,
         tools: [{name, description, inputSchema, execute: async (input) => smallResult}]}
  await sample.limits()   // {maxPromptBytes, tools: {maxCount}, modelTiers, images?}
Each call is memoryless: send the instructions, the data and the format the page wants every time. Answers are kept 5 minutes for the same input (cache: false for a chat). Call it on a viewer's click, not on every keystroke.
Codes: rate_limited (back off; never loop), invalid_json (r.text has what came), cancelled, prompt_too_large, image_rejected, images_unavailable, sampling_disabled (no provider in magpie), upstream_error.`},
	{"comments", `comments: the viewer comments on the page, pinned to its elements, and can send a comment to you (the agent that published it), which answers in the thread. The viewer's comment mode, pins and threads are drawn by magpie's viewer around the page; the page only adds ways in.
Declare {"comments": {"composer_only": true}} for openComposer and anchorFor alone, or {"comments": {}} for the calls that write too. WRITE-ONLY: never build the page's own list of comments; the viewer shows every thread.
  const comments = await claude.use("comments")   // null in a shared copy
  await comments.openComposer({element: el})   // or {range}: the viewer's composer at el; {opened}
  const anchor = await comments.anchorFor(el)   // {path, x, y}
  const off = comments.onSendToClaude(async (req, {signal}) => ({handled: false}))
     // req: {text, anchor, label} when the viewer sends a comment; {handled: true, reply} answers it from the page
  await comments.create({anchor, text})          // {threadId, commentId}
  await comments.reply(threadId, text)           // {commentId}
  await comments.sendToClaude({anchor, text})    // or {threadId, text}: opens the composer filled in; the viewer presses Send
  await comments.canSendToClaude()               // "available", "no_session" or "off"
  await comments.resolve(threadId, true)
  await comments.delete(threadId)
Call the writing calls on a viewer's click, never on load: the first one asks the viewer, and waits for their answer. Text is 1 to 4096 bytes of UTF-8, newlines and tabs but no other control characters. Mark parts that take no comments with data-uncommentable, and the element a comment should pin to with data-comment-target.
Codes: invalid, not_granted, not_found, consent_required (the viewer didn't allow the page to comment as them, or didn't send it; keep the draft), claude_unavailable, unavailable, capability_disabled (customAnchors isn't served here).`},
	{"downloads", `downloads: the page hands the viewer a file it made (an export, a CSV, a picture of a chart). magpie's viewer asks them first, one file at a time, and they may say no.
Declare {"downloads": true}.
  const downloads = await claude.use("downloads")   // null in a shared copy: hide the button
  await downloads.save({filename: "plan.csv", data})   // {status: "saved"}
     // data: a string (saved as UTF-8), a Blob, an ArrayBuffer or a typed array; at most 64 MiB
Call it on a viewer's click. The extension decides the type and is one of gif png jpg jpeg webp mp4 webm txt json md docx pptx epub csv ttf html svg pdf xlsx zip fig. The name is cleaned (no path, no invisible characters, at most 240 bytes).
Codes: declined (the viewer said no: leave it), rate_limited (another file is waiting), rejected_extension, not_signed (mov), too_large, bad_request, request_unknown (request isn't used here), unavailable (open the page in its own tab).`},
	{"tts", `tts: the page reads text aloud to the viewer, in one of four voices, through the text-to-speech model of a provider in magpie. magpie's viewer shows a Stop while it reads; Escape stops it too.
Declare {"tts": {}}.
  const t = await claude.use("tts")   // null in a shared copy: keep the text on screen, hide the speaker
  await t.speak(text)                 // plain text, 1 to 2000 characters; resolves {playedMs} once spoken
  await t.speak(text, {voice: "airy", rate: 1.2})   // voices: buttery (default), airy, glassy, rounded; rate 0.7 to 1.5
  await t.speak([{text, voice, id}, ...])   // up to 100 lines as one reading: a paragraph or one speaker's turn each
  await t.add([...])                  // appends to the reading, or starts one
  t.onState(({state, reason}) => ...)   // "starting", "speaking", "paused", then "idle" with completed, stopped, replaced, viewer_stopped, hidden, time_limit...
  t.onLine(({line, id}) => ...)       // as each line starts
  t.onPosition(({ms, line}) => ...)
  await t.pause(); await t.resume(); await t.stop(); await t.setRate(1.1)
Call speak on a viewer's click: before they have clicked or typed in the page it rejects no_gesture. A new speak replaces the reading; when the reading stops, every pending call rejects stopped (its .reason says why), so stop adding. Speak only what the viewer asked to hear.
Codes: invalid_argument, no_gesture (.retryAfterMs after the viewer stopped one), stopped, rate_limited (.retryAfterMs), not_granted, unavailable (no speech model in magpie, or open the page in its own tab), upstream_error.`},
	{"dictation", `dictation: the viewer's speech as text, through the speech-to-text model of a provider in magpie. magpie's viewer holds the microphone, asks the viewer once for each artifact, and shows Listening and a Stop; the page gets the words, never the sound.
Declare {"dictation": {}}.
  const d = await claude.use("dictation")   // null in a shared copy or a browser that can't record: hide the mic, keep typing
  btn.onclick = () => d.start().catch(e => ...)   // on the click itself, nothing awaited first; resolves once listening
  d.start({language: "es"})                 // the speaker's language, if the page knows it
  d.onPartial(({text}) => ...)              // a live guess, replacing the last; "" once it settles
  d.onFinal(({text}) => ...)                // a settled phrase to append, with the space that joins it
  d.onState(({state, reason}) => ...)       // "starting", "listening", "finishing", then "idle" with stopped, viewer_stopped, hidden, time_limit, not_granted...
  await d.stop()                            // resolves after the last words
  d.active()
A session ends after 60 minutes (time_limit). Escape, the viewer's Stop or a hidden page ends it too.
Codes: invalid_argument, no_gesture, not_granted (the viewer said no: keep typing, don't ask again), dismissed (may be retried on a later click), stopped (.reason), unavailable (no speech-to-text model in magpie, or open the page in its own tab), rate_limited, upstream_error.`},
	{"mcp", `mcp: the tools of MCP servers in the Library on this computer, called as the viewer. The viewer is asked once for each server, on the first call that needs it; tokens never reach the page.
Declare {"mcp": {"servers": [{"server": "github", "tools": ["list_issues", "create_issue"]}]}}: each server by its name in the Library (or "host:<name>", the name with anything outside A-Z a-z 0-9 _ - as _), and only the tools the page calls. Keep it short: the viewer allows what it names.
  const mcp = await claude.use("mcp")       // null in a shared copy: render the page without it
  const r = await mcp.callTool("github", "create_issue", {title: "Bug"})   // an action, once, on the viewer's click
  r.payload                                  // structuredContent, else the first text parsed as JSON, else that text; r.content has every block
  const off = mcp.watchTool("github", "list_issues", {state: "open"}, (ev) => ev.type === "data" ? render(ev.result.payload) : showError(ev.error), {refetchInterval: 60000})
  off()                                      // data to show: replays the cache at once (ev.result.cache = {storedAt, revalidating}), refreshes, polls at most every 30s
  await mcp.invalidate("github", "list_issues")   // after a write: cached reads and watches refetch
  const {servers} = await mcp.listTools()    // [{server, authStatus, tools: [{name, description, annotations}]}]; one not asked about yet: "unknown", names only
  const gh = await mcp.server("github"); await gh.list_issues({state: "open"})   // resolves the payload
  await mcp.describeTool("github", "list_issues")   // {inputSchema, outputSchema?} once the viewer allowed the server
Never guess a tool's input: read describeTool, or the tool's schema where you can see it, else say so when you publish.
A tool marked readOnlyHint is cached ({cache: {staleTime, gcTime, refresh}} tunes it, {cache: false} turns it off); a write is never cached and never watched. Input is plain JSON, at most 1 MiB; file arguments ($file) are not served here.
A tool's failure rejects tool_error (.result is the whole result). A watch never asks the viewer: its handler gets approval_required until a callTool or permissions.request(["mcp:github"]) is allowed.
Codes: not_in_manifest (outside the manifest, or the viewer said no: show that section as not allowed), server_not_connected (no such server in the Library), blocked_by_policy (the Library keeps it from this agent), approval_required, tool_error, server_unavailable (.retryable: retry a read once), upstream_error (.retryable, .retryAfterMs when the viewer didn't answer yet), bad_request, cancelled (an AbortSignal fired; the tool may have run), rate_limited (.retryAfterMs), capability_disabled. Retry only .retryable errors, and a write only on a new click.`},
	{"files", `files: the files of the folder you work in, read by the page on this computer. magpie's viewer asks the viewer once for each artifact, naming the folder; the page never chooses it.
Declare {"files": {}}: the folder is the one you publish from, kept by the versions after it. {"files": {"project": "/abs/path"}} names another folder (a Claude project id such as "chan_..." means yours). Never the home folder, a disk's root or a folder holding home; .git is left out.
  const files = await claude.use("files")   // null in a shared copy or another tab: hide it
  const {entries, cursor} = await files.list({path: "src", recursive: true})   // [{path, isDirectory, size}], 500 a page; pass cursor back for more
  const {bytes, size, revision} = await files.read("README.md", {offset: 0, length: 65536})   // bytes is a Uint8Array, at most 25 MiB a call
  new TextDecoder().decode(bytes)
  await files.download("dist/report.pdf")   // asks the viewer to save it; programs and scripts (.exe, .js, .sh, .bat …) are refused
  const tools = await files.asSampleTools()   // list_project_files and read_project_file for sample's tools (declare sample too)
Call it on a click or after load, never in a loop. Don't copy what you read into db, assets or a version unless the viewer asks: whoever opens the page reads those.
Codes: not_found (no such path, or outside the folder), not_granted (the viewer said no: hide it), changed (the file changed while it was saved: try again on a new click), rate_limited (.retryAfterMs), too_large (over 512 MiB to save), declined (the viewer didn't save it), bad_request, unavailable, capability_disabled.`},
	{"room", `room: whoever has the page open right now (every tab of it on this computer, and the visitors of a shared link), for moments and presence: cursors, who is here, a move in a game, "next slide". Nothing is kept and a message can be lost, so state a late joiner needs goes in db, and the page works alone.
Declare {"room": {}}. Sending on a topic needs level admin unless the declaration opens it: {"room": {"topics": {"move": "interact"}}} lets a shared link's signed-in visitors send on move (never open one to "view").
  const room = await claude.use("room")   // null when the page can't connect, or for a visitor not signed in: work without it
  await room.emit("move", {x: 3, y: 4})   // heard by everyone in the room, this page too (sameTab: true); send absolute state, not a change
  const off = room.on("move", (m) => ...)   // m.data, and who sent it: m.peer, m.by, m.isMe, m.sameTab, m.guest
  await room.presence({cursor: {x, y}})   // this page's own state, merged (a null deletes its key); cleared when it leaves
  room.onPeers(({peers, joined, left, updated}) => render(peers))   // everyone here, this page's own with sameTab: true; at most once a frame
     // a peer: {peer, by (the viewer's id, when the page declares user), kind ("viewer"), isMe, sameTab, guest, presence, updatedAt}
  room.peers(); room.connected(); room.onConnection((up) => ...)
  const r = await room.join("table-2")    // a room of its own, heard only by those who joined it: emit, on, presence, peers, onPeers, leave()
Topics and room names: ^[a-z][a-z0-9_.-]{0,47}$ (a room may start with a digit). data and the merged presence are JSON, 4096 bytes and 8 deep at most; about 40 messages a second, past which they are dropped. A page is in 16 named rooms at most.
What is heard is untrusted (anyone with the link): presence is never authority, and set it with textContent.
Codes (on emit, presence, join, and on's and onPeers' onError): invalid_argument, not_permitted (the topic isn't open to this viewer's level), rate_limited, limit_reached, upstream_error, revoked (the artifact was deleted).`},
	{"permissions", `permissions: built in, never declared.
  const p = await claude.use("permissions")
  await p.state("db")          // "granted" for what the page declared, else "unavailable"
  await p.state("dictation")   // "prompt" until the viewer answers, then "granted" or "denied"
  await p.state("mcp:github")  // the same for one MCP server; "mcp" is every server in the manifest
  await p.state("files")       // the same for the project's files
  await p.request(names)       // asks for what is still "prompt" (dictation, files, then the MCP servers in one dialog); never rejects
  await p.manage()             // opens the Permissions panel and resolves when it closes; on a button, never on load
Everything but dictation, MCP servers and files is set when the page is published. "prompt" means go ahead: the first call asks. A "denied" is final: branch on it, never ask again.`},
}

// GuideNames are the capabilities the guide covers, in its order.
func GuideNames() []string {
	var out []string
	for _, c := range guideCaps {
		out = append(out, c.name)
	}
	return out
}

// Guide is the guide for names, every capability when there are none;
// "self" is artifact's. An error names one it doesn't know.
func Guide(names []string) (string, error) {
	var want []string
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "self" {
			n = "artifact"
		}
		if n == "" || slices.Contains(want, n) {
			continue
		}
		if !slices.Contains(GuideNames(), n) {
			return "", fmt.Errorf("%q isn't a capability a page here has; they are %s", n, strings.Join(GuideNames(), ", "))
		}
		want = append(want, n)
	}
	var b strings.Builder
	b.WriteString(guideIntro)
	for _, c := range guideCaps {
		if len(want) == 0 || slices.Contains(want, c.name) {
			b.WriteString("\n")
			b.WriteString(c.text)
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}
