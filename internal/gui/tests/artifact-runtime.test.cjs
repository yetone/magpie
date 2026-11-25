// Run with Node's test runner and Playwright on the module path; see README.md.
// The page runtime (internal/artifact/runtime.js), as a page written for
// claude.ai's artifact runtime meets it: window.claude is there before the
// page's own scripts, use() resolves db and user later and null for what
// isn't served, a bad path throws a TypeError at once, a refused call
// rejects with its code, a query's onSnapshot shows the page's own writes
// and another's (the agent's, told on the events stream), a page that
// declares artifact publishes itself (its whole page, which every open
// view reloads to, or files, which keep it running), a page that
// declares assets uploads a file as its bytes and shows it from /_blob/,
// a page that declares sample asks the model and streams its answer,
// runs its own tools between rounds, and is refused before any request
// for what it gets wrong, and a shared copy resolves every capability null. Under the sandbox CSP magpie serves
// pages with, so every call is from an opaque origin. In both engines.
// The store is faked here; internal/gateway's TestArtifactPage* hold the
// real one.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const runtime = fs.readFileSync(path.resolve(__dirname, "../../artifact/runtime.js"), "utf8").replace(/\n+$/, "");
const CSP = "sandbox allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads";

// the page as magpie serves it: the runtime and its config right after <head>
function served(page, cfg) {
  const tag = "<script>" + runtime + "(" + JSON.stringify(cfg).replace(/</g, "\\u003c") + ");</script>";
  return page.replace(/<head>/i, "<head>" + tag);
}

const PAGE = `<!doctype html><html><head><title>Board</title><script>
window.early = typeof window.claude === "object" && typeof window.claude.use === "function";
</script></head><body><ul id=list></ul><script>
(async () => {
  const out = (window.out = { snaps: [] });
  const first = claude.use("db");
  out.sameUse = first === claude.use("db");
  const db = await first;
  const user = await claude.use("user");
  out.room = await claude.use("room");
  out.nope = await claude.use("nope");
  out.frozen = Object.isFrozen(db) && Object.isFrozen(window.claude);
  try { db.doc("tasks"); out.typeError = false; } catch (e) { out.typeError = e instanceof TypeError; }
  out.id = await user.id();
  out.owner = await user.isOwner();
  out.perm = await (await claude.use("permissions")).state("db");
  db.collection("tasks").orderBy("n").onSnapshot((s) => {
    out.snaps.push(s.docs.map((d) => d.id + ":" + d.data().title).join(","));
    const list = document.getElementById("list");
    list.textContent = "";
    for (const d of s.docs) { const li = document.createElement("li"); li.textContent = d.data().title; list.append(li); }
  }, (e) => { out.listenError = e.code; });
  await db.doc("tasks/b").set({ title: "two", n: 2 });
  await db.collection("tasks").doc("a").set({ title: "one", n: 1 });
  await db.doc("tasks/a").update({ title: "uno" });
  out.got = (await db.doc("tasks/a").get()).data();
  out.missing = (await db.doc("tasks/zz").get()).exists;
  try { await db.doc("tasks/a").set([1]); } catch (e) { out.arrayCode = e.code; }
  try { await db.collection("tasks").limit(5000).get(); } catch (e) { out.limitCode = e.code; }
  try { await db.doc("tasks/quota").set({ big: true }); } catch (e) { out.quotaCode = e.code; }
  out.ready = true;
})().catch((e) => { window.out.crash = String(e && e.stack || e); });
</script></body></html>`;

const SHARED = `<!doctype html><html><head><title>Shared</title></head><body><script>
(async () => {
  window.out = {
    db: await claude.use("db"),
    user: await claude.use("user"),
    assets: await claude.use("assets"),
    sample: await claude.use("sample"),
    perm: typeof (await claude.use("permissions")).state,
  };
})();
</script></body></html>`;

// a page that is its own record: its items are data in the page, and
// adding one publishes the page again, regenerated from them
function listMain() {
  const MAIN = document.currentScript.textContent;
  const state = JSON.parse(document.getElementById("state").textContent);
  for (const it of state.items) { const li = document.createElement("li"); li.textContent = it; document.getElementById("list").append(li); }
  const doc = (s) => "<!doctype html><html><head><title>List</title></head><body><script id=state type=application/json>" + JSON.stringify(s).replace(/</g, "\\u003c") + "</scr" + "ipt><ul id=list></ul><artifact-sync id=sy><b>x</b></artifact-sync><script>" + MAIN + "</scr" + "ipt></body></html>";
  window.loadedAt = Math.random();
  (async () => {
    const artifact = await claude.use("artifact");
    const out = { items: state.items };
    out.same = artifact === (await claude.use("self"));
    out.frozen = Object.isFrozen(artifact);
    out.perm = await (await claude.use("permissions")).state("artifact");
    out.display = getComputedStyle(document.getElementById("sy")).display;
    try { await artifact.edit(); } catch (e) { out.editCode = e.code; }
    try { await artifact.sync(() => {}); } catch (e) { out.syncCode = e.code; }
    out.meta = await artifact.metadata(["aaaaaaaaaaaaaaaaaaaaaaaaaa", "nope"]);
    window.add = (item) => artifact.publish(doc({ items: [...state.items, item] }));
    window.save = (files) => artifact.publish(files);
    window.out = out;
  })().catch((e) => { window.out = { crash: String(e && e.stack || e) }; });
}
const LIST_MAIN = "(" + String(listMain) + ")();";
const listDoc = (state) => "<!doctype html><html><head><title>List</title></head><body><script id=state type=application/json>" + JSON.stringify(state).replace(/</g, "\\u003c") + "</script><ul id=list></ul><artifact-sync id=sy><b>x</b></artifact-sync><script>" + LIST_MAIN + "</script></body></html>";

const READ_ONLY = `<!doctype html><html><head><title>Read only</title></head><body><script>
(async () => {
  const artifact = await claude.use("artifact");
  const out = { served: !!artifact };
  try { await artifact.publish("<!doctype html><p>x"); } catch (e) { out.publish = e.code; }
  try { await artifact.publish({ "a.txt": "a" }); } catch (e) { out.files = e.code; }
  try { await artifact.sync(() => {}); } catch (e) { out.sync = e.code; }
  window.out = out;
})().catch((e) => { window.out = { crash: String(e && e.stack || e) }; });
</script></body></html>`;

// a gallery: an upload shown in an <img> from the url it gave, listed,
// then deleted; and the calls a page gets wrong, refused before any
// request
const GALLERY = `<!doctype html><html><head><title>Gallery</title></head><body><img id="pic"><script>
(async () => {
  const assets = await claude.use("assets");
  const out = { served: !!assets, frozen: Object.isFrozen(assets) };
  const png = Uint8Array.from(atob("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="), (c) => c.charCodeAt(0));
  out.up = await assets.upload(new Blob([png], { type: "image/png" }));
  out.font = await assets.upload(new Blob(["wOF2"]), { type: "font/woff2" });
  const pic = document.getElementById("pic");
  await new Promise((ok, no) => { pic.onload = ok; pic.onerror = () => no(new Error("the img didn't load")); pic.src = out.up.url; });
  out.width = pic.naturalWidth;
  out.list = await assets.list();
  out.del = await assets.delete(out.up.url);
  out.again = await assets.delete(out.up.id);
  const code = (p) => p.then(() => "resolved", (e) => e.name + ":" + e.code);
  out.notBlob = await code(assets.upload("text"));
  out.empty = await code(assets.upload(new Blob([])));
  out.noType = await code(assets.upload(new Blob(["x"])));
  out.badOpts = await code(assets.upload(new Blob(["x"]), { type: 1 }));
  out.badRef = await code(assets.delete(42));
  out.html = await code(assets.upload(new Blob(["<p>"], { type: "text/html" })));
  out.gone = await code(assets.upload(new Blob(["x"], { type: "text/plain" }), { type: "text/x-gone" }));
  window.out = out;
})().catch((e) => { window.out = { crash: String(e && e.stack || e) }; });
</script></body></html>`;

// a page asking the model: an answer streamed, JSON, a tool it runs
// between rounds, an image, and what is refused or cut off
const ADD = { name: "add", description: "Adds two numbers", inputSchema: { type: "object", properties: { a: { type: "number" }, b: { type: "number" } } } };
const SAMPLER = `<!doctype html><html><head><title>Sampler</title></head><body><script>
(async () => {
  const sample = await claude.use("sample");
  const out = { served: typeof sample, frozen: Object.isFrozen(sample), same: sample === (await claude.use("sample")) };
  const code = (p) => p.then((v) => "resolved:" + JSON.stringify(v), (e) => e.code + (e.text ? ":" + e.text : ""));
  out.limits = JSON.parse(JSON.stringify(await sample.limits()));
  if (location.pathname === "/a/sampler-noimg/") {
    out.images = await code(sample("look", { images: [new Blob(["x"], { type: "image/png" })] }));
    window.out = out;
    return;
  }
  const seen = [];
  out.plain = await sample("Say hi", { onText: (e) => seen.push(e.text + "|" + e.delta) });
  out.seen = seen;
  out.plainFrozen = Object.isFrozen(out.plain);
  out.json = await sample.json("give json", { modelTier: "quick" });
  const add = Object.assign({}, ${JSON.stringify(ADD)}, { execute: (i) => ({ sum: i.a + i.b }) });
  out.tooled = await sample([{ role: "user", content: "add 2" }, { role: "user", content: "and 3" }], { tools: [add] });
  const png = Uint8Array.from(atob("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="), (c) => c.charCodeAt(0));
  out.image = (await sample("what is this", { images: [new Blob([png], { type: "image/png" })] })).text;
  out.empty = await code(sample("  "));
  out.endsAssistant = await code(sample([{ role: "user", content: "a" }, { role: "assistant", content: "b" }]));
  out.cacheTools = await code(sample("x", { tools: [add], cache: true }));
  out.badTier = await code(sample("x", { modelTier: "huge" }));
  out.notImage = await code(sample("x", { images: [new Blob(["x"], { type: "text/plain" })] }));
  out.limited = await code(sample("rate"));
  out.midError = await code(sample("mid"));
  out.badJSON = await code(sample.json("not json"));
  const ac = new AbortController();
  out.aborted = await code(sample("slow", { signal: ac.signal, onText: () => ac.abort() }));
  const pre = new AbortController();
  pre.abort();
  out.preAborted = await code(sample("x", { signal: pre.signal }));
  window.out = out;
})().catch((e) => { window.out = { crash: String(e && e.stack || e) }; });
</script></body></html>`;

// a store like magpie's, small: documents by path, a write told to every
// open events stream
function fakeMagpie(t) {
  const docs = new Map(), streams = new Set(), calls = [];
  // the list's versions, and its own streams; quiet holds what they'd hear
  const list = { pages: [listDoc({ items: [] })], calls: [], streams: new Set(), quiet: false, landOver: false };
  const newVersion = (page) => {
    list.pages.push(page);
    const n = list.pages.length;
    if (!list.quiet) for (const s of list.streams) s.write(`data: {"version":${n}}\n\n`);
    return n;
  };
  const tell = (p) => { for (const s of streams) s.write(`data: ${JSON.stringify({ paths: [p] })}\n\n`); };
  const err = (res, status, code) => { res.writeHead(status, { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" }); res.end(JSON.stringify({ error: { code, message: code } })); };
  const gallery = { calls: [], blobs: new Map() };
  const sampler = { calls: [] };
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    if (req.method === "GET" && url.pathname === "/a/gallery/") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP });
      return res.end(served(GALLERY, { api: "/a/gallery/_api", token: "tok", caps: { assets: {} }, viewer: { id: "u_0123456789abcdef01234567" }, version: 1, home: "/a/gallery/" }));
    }
    if (req.method === "POST" && url.pathname.startsWith("/a/gallery/_api/")) {
      const chunks = [];
      req.on("data", (c) => chunks.push(c));
      req.on("end", () => {
        const body = Buffer.concat(chunks);
        const what = url.pathname.split("/").pop();
        gallery.calls.push({ what, query: Object.fromEntries(url.searchParams), body, origin: req.headers.origin, type: req.headers["content-type"] });
        const ok = (v) => { res.writeHead(200, { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" }); res.end(JSON.stringify(v)); };
        if (what === "upload") {
          const t = url.searchParams.get("type");
          if (url.searchParams.get("t") !== "tok") return err(res, 403, "not_granted");
          if (t === "text/x-gone") return err(res, 404, "revoked");
          if (!["image/png", "font/woff2"].includes(t)) return err(res, 415, "unsupported_type");
          const id = String(gallery.blobs.size + 1).padStart(32, "a");
          gallery.blobs.set(id, { body, t });
          return ok({ id, url: "/_blob/" + id, sizeBytes: body.length, contentType: t });
        }
        const c = JSON.parse(body);
        if (c.t !== "tok") return err(res, 403, "not_granted");
        if (c.op === "list") return ok({ assets: [...gallery.blobs].map(([id, b]) => ({ id, url: "/_blob/" + id, contentType: b.t, sizeBytes: b.body.length, createdAt: "2026-11-24T00:00:00Z" })), usage: { files: gallery.blobs.size, bytes: 0, maxFiles: 1000, maxBytes: 1 << 30 } });
        const id = String(c.ref).replace(/^\/_blob\//, "");
        return ok({ deleted: gallery.blobs.delete(id) });
      });
      return;
    }
    if (req.method === "GET" && (url.pathname === "/a/sampler/" || url.pathname === "/a/sampler-noimg/")) {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP });
      const caps = url.pathname === "/a/sampler/" ? { sample: { images: true } } : { sample: {} };
      return res.end(served(SAMPLER, { api: url.pathname + "_api", token: "tok", caps, viewer: { id: "u_0123456789abcdef01234567" }, version: 1, home: url.pathname }));
    }
    if (req.method === "POST" && /^\/a\/sampler(-noimg)?\/_api\/sample$/.test(url.pathname)) {
      let body = "";
      req.on("data", (c) => (body += c));
      req.on("end", () => {
        const c = JSON.parse(body);
        sampler.calls.push({ c, origin: req.headers.origin, type: req.headers["content-type"] });
        if (c.t !== "tok") return err(res, 403, "not_granted");
        const last = c.messages.at(-1).content;
        const ask = typeof last === "string" ? last : "tool results";
        if (ask === "rate") return err(res, 429, "rate_limited");
        res.writeHead(200, { "Content-Type": "application/x-ndjson", "Access-Control-Allow-Origin": "*", "Cache-Control": "no-store" });
        const line = (v) => res.write(JSON.stringify(v) + "\n");
        const end = (stop, content) => { line({ end: { stop, tier: c.modelTier || "default", model: "claude-sonnet-5-5", content } }); res.end(); };
        const say = (t) => { line({ text: t }); return [{ type: "text", text: t }]; };
        if (ask === "Say hi") { line({ text: "Hel" }); line({ text: "lo" }); return end("end_turn", [{ type: "text", text: "Hello" }]); }
        if (ask === "give json") return end("end_turn", say('Here:\n```json\n{"a": [1, 2]}\n```'));
        if (ask === "add 2\n\nand 3") return end("tool_use", [...say("Let me add."), { type: "tool_use", id: "tu_1", name: "add", input: { a: 2, b: 3 } }]);
        if (ask === "tool results") return end("end_turn", say("It is " + JSON.parse(last[0].content).sum + "."));
        if (ask === "what is this") return end("end_turn", say(c.images ? "an image of " + c.images.length : "no image"));
        if (ask === "mid") { line({ text: "part" }); line({ error: { code: "upstream_error", message: "the vendor fell over" } }); return res.end(); }
        if (ask === "not json") return end("end_turn", say("nope"));
        if (ask === "slow") { line({ text: "a" }); req.on("close", () => res.end()); return; }
        end("end_turn", say("?"));
      });
      return;
    }
    if (req.method === "GET" && url.pathname.startsWith("/_blob/")) {
      const b = gallery.blobs.get(url.pathname.slice(7));
      gallery.calls.push({ what: "blob", origin: req.headers.origin });
      if (!b) { res.writeHead(404); return res.end(); }
      res.writeHead(200, { "Content-Type": b.t, "X-Content-Type-Options": "nosniff", "Access-Control-Allow-Origin": "*", "Content-Security-Policy": "sandbox" });
      return res.end(b.body);
    }
    if (req.method === "GET" && url.pathname === "/a/board/") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP, "Vary": "Sec-Fetch-Mode" });
      return res.end(served(PAGE, { api: "/a/board/_api", token: "tok", caps: { db: {}, user: {} }, viewer: { id: "u_0123456789abcdef01234567" } }));
    }
    if (req.method === "GET" && url.pathname === "/a/list/") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP });
      return res.end(served(list.pages.at(-1), { api: "/a/list/_api", token: "tok", caps: { artifact: {} }, viewer: { id: "u_0123456789abcdef01234567" }, version: list.pages.length, home: "/a/list/" }));
    }
    if (req.method === "GET" && url.pathname === "/a/ro/") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP });
      return res.end(served(READ_ONLY, { api: "/a/ro/_api", token: "tok", caps: { artifact: { publish: false } }, viewer: { id: "u_0123456789abcdef01234567" }, version: 1, home: "/a/ro/" }));
    }
    if (req.method === "GET" && url.pathname === "/a/list/_api/events") {
      if (url.searchParams.get("t") !== "tok") return err(res, 403, "not_granted");
      res.writeHead(200, { "Content-Type": "text/event-stream", "Access-Control-Allow-Origin": "*", "Cache-Control": "no-store" });
      let hello = "retry: 2000\n: magpie\n\n";
      if (+url.searchParams.get("v") < list.pages.length) hello += `data: {"version":${list.pages.length}}\n\n`;
      res.write(hello);
      list.streams.add(res);
      req.on("close", () => list.streams.delete(res));
      return;
    }
    if (req.method === "POST" && (url.pathname === "/a/list/_api/publish" || url.pathname === "/a/list/_api/meta" || url.pathname.startsWith("/a/ro/_api/"))) {
      let body = "";
      req.on("data", (c) => (body += c));
      req.on("end", () => {
        const c = JSON.parse(body);
        list.calls.push({ c, at: url.pathname, origin: req.headers.origin, type: req.headers["content-type"] });
        const ok = (v) => { res.writeHead(200, { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" }); res.end(JSON.stringify(v)); };
        if (c.t !== "tok" || url.pathname.startsWith("/a/ro/")) return err(res, 403, "not_granted");
        if (url.pathname.endsWith("/meta")) return ok({ artifacts: { aaaaaaaaaaaaaaaaaaaaaaaaaa: { id: "aaaaaaaaaaaaaaaaaaaaaaaaaa", title: "Other", type: null } } });
        const every = c.files && Object.values(c.files).every((e) => e && "ifMatch" in e);
        if (every && list.landOver) newVersion(list.pages.at(-1)); // another's save, first
        const live = list.pages.length;
        if (!every && c.base !== live) {
          res.writeHead(409, { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" });
          return res.end(JSON.stringify({ error: { code: "conflict", message: "conflict", live: String(live) } }));
        }
        const n = newVersion(c.html || list.pages.at(-1));
        const out = { version: String(n) };
        if (every) {
          out.shas = Object.fromEntries(Object.keys(c.files).map((p) => [p, "s-" + p]));
          out.changed = list.landOver ? [{ path: "other.txt", sha256: "s-other" }] : [];
        }
        ok(out);
      });
      return;
    }
    if (req.method === "GET" && url.pathname === "/a/shared/") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP });
      return res.end(served(SHARED, { api: "", token: "", caps: {}, viewer: { id: "" }, shared: true }));
    }
    if (req.method === "GET" && url.pathname === "/a/board/_api/events") {
      if (url.searchParams.get("t") !== "tok") return err(res, 403, "not_granted");
      res.writeHead(200, { "Content-Type": "text/event-stream", "Access-Control-Allow-Origin": "*", "Cache-Control": "no-store" });
      res.write("retry: 2000\n: magpie\n\n");
      streams.add(res);
      req.on("close", () => streams.delete(res));
      return;
    }
    if (req.method === "POST" && url.pathname === "/a/board/_api/db") {
      let body = "";
      req.on("data", (c) => (body += c));
      req.on("end", () => {
        const c = JSON.parse(body);
        calls.push({ c, origin: req.headers.origin, type: req.headers["content-type"] });
        if (c.t !== "tok") return err(res, 403, "not_granted");
        const ok = (v) => { res.writeHead(200, { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" }); res.end(JSON.stringify(v)); };
        const now = new Date().toISOString();
        if (c.op === "get") return ok({ doc: docs.get(c.path) || null });
        if (c.op === "query") {
          if (c.limit > 1000) return err(res, 400, "invalid_argument");
          const rows = [...docs.values()].filter((d) => d.path.startsWith(c.collection + "/") && d.path.split("/").length === c.collection.split("/").length + 1);
          if (c.orderBy) rows.sort((a, b) => (a.data[c.orderBy] - b.data[c.orderBy]) * (c.desc ? -1 : 1));
          return ok({ docs: rows });
        }
        if (c.path === "tasks/quota") return err(res, 400, "quota_exceeded");
        const was = docs.get(c.path);
        if (c.op === "set" || c.op === "update") {
          const data = c.op === "update" ? { ...was.data, ...c.data } : c.data;
          docs.set(c.path, { path: c.path, id: c.path.split("/").pop(), data, version: (was ? was.version : 0) + 1, updated: now });
        }
        if (c.op === "delete") docs.delete(c.path);
        tell(c.path);
        ok({});
      });
      return;
    }
    res.writeHead(404);
    res.end();
  });
  t.after(() => { for (const s of [...streams, ...list.streams]) s.destroy(); server.close(); });
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve({
    base: `http://127.0.0.1:${server.address().port}`,
    calls,
    list,
    gallery,
    sampler,
    // another's publish of the list: the agent's, or another view's
    publishList(state) { return newVersion(listDoc(state)); },
    // the agent's write, through artifact_data
    agentSet(p, data) { const was = docs.get(p); docs.set(p, { path: p, id: p.split("/").pop(), data, version: (was ? was.version : 0) + 1, updated: new Date().toISOString() }); tell(p); },
  })));
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`a claude.ai page runs on magpie's runtime (${engine})`, async (t) => {
    const site = await fakeMagpie(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(site.base + "/a/board/");
    await page.waitForFunction(() => window.out && (window.out.ready || window.out.crash), null, { timeout: 15000 });
    const out = await page.evaluate(() => window.out);
    assert.equal(out.crash, undefined);
    assert.equal(await page.evaluate(() => window.early), true, "window.claude is there before the page's scripts");
    assert.equal(await page.evaluate(() => origin), "null", "the page runs at an opaque origin");
    assert.equal(out.sameUse, true);
    assert.equal(out.room, null);
    assert.equal(out.nope, null);
    assert.equal(out.frozen, true);
    assert.equal(out.typeError, true);
    assert.equal(out.id, "u_0123456789abcdef01234567");
    assert.equal(out.owner, true);
    assert.equal(out.perm, "granted");
    assert.deepEqual(out.got, { title: "uno", n: 1 });
    assert.equal(out.missing, false);
    assert.equal(out.arrayCode, "invalid_argument");
    assert.equal(out.limitCode, "invalid_argument");
    assert.equal(out.quotaCode, "quota_exceeded");
    assert.equal(out.listenError, undefined);
    // every call is a simple request from the page's opaque origin, with its token
    assert.ok(site.calls.length > 0);
    for (const c of site.calls) {
      assert.equal(c.origin, "null");
      assert.match(c.type, /^text\/plain/);
      assert.equal(c.c.t, "tok");
    }
    await page.waitForFunction(() => document.querySelectorAll("#list li").length === 2, null, { timeout: 10000 });
    assert.deepEqual(await page.locator("#list li").allTextContents(), ["uno", "two"]);
    // the agent writes; the stream tells the page, and its view follows
    site.agentSet("tasks/c", { title: "three", n: 0 });
    await page.waitForFunction(() => document.querySelector("#list li")?.textContent === "three", null, { timeout: 10000 });
    assert.deepEqual(await page.locator("#list li").allTextContents(), ["three", "uno", "two"]);
    const snaps = await page.evaluate(() => window.out.snaps);
    assert.equal(snaps.at(-1), "c:three,a:uno,b:two");
    assert.deepEqual(errors, []);
  });

  test(`a page publishes itself (${engine})`, async (t) => {
    const site = await fakeMagpie(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const ctx = await browser.newContext();
    const a = await ctx.newPage(), b = await ctx.newPage();
    const errors = [];
    for (const p of [a, b]) {
      p.on("pageerror", (e) => errors.push(String(e)));
      await p.goto(site.base + "/a/list/");
      await p.waitForFunction(() => window.out, null, { timeout: 15000 });
    }
    const items = (p, want) => p.waitForFunction((w) => window.out && window.out.items && window.out.items.join(",") === w, want, { timeout: 15000 });
    const out = await a.evaluate(() => window.out);
    assert.equal(out.crash, undefined);
    assert.equal(out.same, true, "self is the same namespace");
    assert.equal(out.frozen, true);
    assert.equal(out.perm, "granted");
    assert.equal(out.display, "contents");
    assert.equal(out.editCode, "not_granted");
    assert.equal(out.syncCode, "invalid_content");
    assert.deepEqual(out.meta, { aaaaaaaaaaaaaaaaaaaaaaaaaa: { id: "aaaaaaaaaaaaaaaaaaaaaaaaaa", title: "Other", type: null }, nope: { id: "", title: "", type: null } });

    // the whole page: this view and the other reload to it
    assert.deepEqual(await a.evaluate(() => window.add("one")), { version: "2" });
    await items(a, "one");
    await items(b, "one");
    const pub = site.list.calls.find((c) => c.at.endsWith("/publish"));
    assert.equal(pub.c.base, 1);
    assert.equal(pub.origin, "null");
    assert.match(pub.type, /^text\/plain/);
    assert.match(pub.c.html, /^<!doctype html>/);

    // another's version won while b didn't hear it: b's edit is refused,
    // and b goes to the winner
    site.list.quiet = true;
    site.publishList({ items: ["one", "agent"] });
    const lost = await b.evaluate(() => window.add("late").then(() => null, (e) => ({ name: e.name, code: e.code, live: e.live })));
    assert.deepEqual(lost, { name: "ArtifactError", code: "conflict", live: "3" });
    await items(b, "one,agent");
    site.list.quiet = false;
    await a.reload();
    await items(a, "one,agent");

    // files: b keeps running and builds on the version it made; a reloads
    const [aAt, bAt] = [await a.evaluate(() => window.loadedAt), await b.evaluate(() => window.loadedAt)];
    const saved = await b.evaluate(() => window.save({
      "data/a.json": '{"a":1}',
      "img/dot.png": new Blob([new Uint8Array([137, 80, 78, 71])], { type: "image/png" }),
      "old.txt": null,
      "x.css": { content: "b{}", contentType: "text/css" },
    }));
    assert.deepEqual(saved, { version: "4" });
    let c = site.list.calls.at(-1).c;
    assert.equal(c.base, 3);
    assert.deepEqual(c.files, { "data/a.json": { text: '{"a":1}' }, "img/dot.png": { base64: "iVBORw==", contentType: "image/png" }, "old.txt": null, "x.css": { text: "b{}", contentType: "text/css" } });
    assert.deepEqual(await b.evaluate(() => window.save({ "data/a.json": '{"a":2}' })), { version: "5" });
    assert.equal(site.list.calls.at(-1).c.base, 4);
    await a.waitForFunction((at) => window.loadedAt !== at && window.out, aAt, { timeout: 15000 });
    assert.equal(await b.evaluate(() => window.loadedAt), bAt, "the view that saved files kept running");

    // with ifMatch on every file another's save may come first: the
    // answer says what it changed, and the view reloads to bring it
    const held = await b.evaluate(() => window.save({ "data/a.json": { content: '{"a":3}', ifMatch: "s-a" }, "new.txt": { content: "n", ifMatch: null } }));
    assert.deepEqual(held, { version: "6", shas: { "data/a.json": "s-data/a.json", "new.txt": "s-new.txt" }, changed: [] });
    assert.equal(await b.evaluate(() => window.loadedAt), bAt);
    site.list.landOver = true;
    const over = await b.evaluate(() => window.save({ "data/a.json": { content: '{"a":4}', ifMatch: "s-a" } }));
    assert.deepEqual(over, { version: "8", shas: { "data/a.json": "s-data/a.json" }, changed: [{ path: "other.txt", sha256: "s-other" }] });
    await b.waitForFunction((at) => window.loadedAt !== at && window.out, bAt, { timeout: 15000 });
    assert.deepEqual(errors, []);
  });

  test(`publish: false keeps a page from saving itself (${engine})`, async (t) => {
    const site = await fakeMagpie(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    await page.goto(site.base + "/a/ro/");
    await page.waitForFunction(() => window.out, null, { timeout: 15000 });
    assert.deepEqual(await page.evaluate(() => window.out), { served: true, files: "not_granted", publish: "not_granted", sync: "not_granted" });
    assert.equal(site.list.calls.length, 0, "nothing was sent");
  });

  test(`a page uploads, shows, lists and deletes its assets (${engine})`, async (t) => {
    const site = await fakeMagpie(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(site.base + "/a/gallery/");
    await page.waitForFunction(() => window.out, null, { timeout: 15000 });
    const out = await page.evaluate(() => window.out);
    assert.equal(out.crash, undefined);
    assert.equal(out.served, true);
    assert.equal(out.frozen, true);
    const id = "1".padStart(32, "a");
    assert.deepEqual(out.up, { id, url: "/_blob/" + id, sizeBytes: 70, contentType: "image/png" });
    assert.equal(out.font.contentType, "font/woff2", "options.type over an empty blob.type");
    assert.equal(out.width, 1, "the <img> showed the upload from its url");
    assert.equal(out.list.assets.length, 2);
    assert.equal(out.list.usage.maxFiles, 1000);
    assert.deepEqual(out.del, { deleted: true });
    assert.deepEqual(out.again, { deleted: false });
    for (const k of ["notBlob", "empty", "noType", "badOpts", "badRef"]) assert.equal(out[k], "AssetsError:invalid_request", k);
    assert.equal(out.html, "AssetsError:unsupported_type");
    assert.equal(out.gone, "AssetsError:quota_or_state", "a gone artifact is the state half of quota_or_state");
    // the file went as its bytes, as text/plain from the opaque origin,
    // its type and token in the url; the page's mistakes sent nothing
    const ups = site.gallery.calls.filter((c) => c.what === "upload");
    assert.equal(ups.length, 4);
    assert.equal(ups[0].body.toString("base64"), "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==");
    for (const c of ups) {
      assert.equal(c.origin, "null");
      assert.equal(c.type, "text/plain");
      assert.equal(c.query.t, "tok");
    }
    assert.deepEqual(ups.map((c) => c.query.type), ["image/png", "font/woff2", "text/html", "text/x-gone"]);
    const dels = site.gallery.calls.filter((c) => c.what === "assets").map((c) => JSON.parse(c.body));
    assert.deepEqual(dels.map((c) => c.op + ":" + (c.ref || "")), ["list:", "delete:/_blob/" + id, "delete:" + id]);
    assert.ok(site.gallery.calls.some((c) => c.what === "blob"));
    assert.deepEqual(errors, []);
  });

  test(`a page asks the model, runs its tools and is refused what it gets wrong (${engine})`, async (t) => {
    const site = await fakeMagpie(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(site.base + "/a/sampler/");
    await page.waitForFunction(() => window.out, null, { timeout: 15000 });
    const out = await page.evaluate(() => window.out);
    assert.equal(out.crash, undefined);
    assert.equal(out.served, "function");
    assert.equal(out.frozen, true);
    assert.equal(out.same, true);
    assert.deepEqual(out.limits, { maxPromptBytes: 262144, tools: { maxCount: 32 }, modelTiers: ["default", "complex", "quick"], images: { maxCount: 8, maxInputBytes: 20000000, mediaTypes: ["image/jpeg", "image/png", "image/webp", "image/gif"] } });
    // the answer streamed, onText given the whole of it each time
    assert.deepEqual(out.plain, { text: "Hello", truncated: false, modelTierApplied: "default", modelApplied: "claude-sonnet-5-5" });
    assert.deepEqual(out.seen, ["Hel|Hel", "Hello|lo"]);
    assert.equal(out.plainFrozen, true);
    assert.deepEqual(out.json, { a: [1, 2] }, "one fenced block's JSON");
    // the tool ran here, between rounds; the rounds' text joined
    assert.equal(out.tooled.text, "Let me add.\n\nIt is 5.");
    assert.equal(out.image, "an image of 1");
    assert.equal(out.empty, "invalid_request");
    assert.equal(out.endsAssistant, "invalid_request");
    assert.equal(out.cacheTools, "invalid_request");
    assert.equal(out.badTier, "invalid_request");
    assert.equal(out.notImage, "image_rejected");
    assert.equal(out.limited, "rate_limited");
    assert.equal(out.midError, "upstream_error:part", "an error after text keeps what came");
    assert.equal(out.badJSON, "invalid_json:nope");
    assert.equal(out.aborted, "cancelled:a");
    assert.equal(out.preAborted, "cancelled");
    // every round went as text/plain from the opaque origin, with the
    // token; what the page got wrong sent nothing
    const calls = site.sampler.calls;
    for (const c of calls) {
      assert.equal(c.origin, "null");
      assert.equal(c.type, "text/plain");
      assert.equal(c.c.t, "tok");
    }
    const asks = calls.map((c) => { const l = c.c.messages.at(-1).content; return typeof l === "string" ? l : "tool results"; });
    assert.deepEqual(asks, ["Say hi", "give json", "add 2\n\nand 3", "tool results", "what is this", "rate", "mid", "not json", "slow"]);
    assert.equal(calls[0].c.json, false);
    assert.equal(calls[1].c.json, true);
    assert.equal(calls[1].c.modelTier, "quick");
    // the tool round: offered with its schema, never cached, and the
    // second round carries the model's turn and the tool's result
    assert.deepEqual(calls[2].c.tools, [ADD]);
    assert.deepEqual(calls[2].c.cache, { off: true });
    assert.equal(calls[2].c.last, false);
    assert.equal(calls[2].c.messages.length, 1, "same-role turns joined");
    const second = calls[3].c.messages;
    assert.equal(second.length, 3);
    assert.equal(second[1].role, "assistant");
    assert.equal(second[1].content[1].type, "tool_use");
    assert.deepEqual(second[2].content, [{ type: "tool_result", tool_use_id: "tu_1", content: '{"sum":5}', is_error: false }]);
    // the image went as its bytes: a 1x1 PNG needs no downsizing
    assert.deepEqual(calls[4].c.images, [{ type: "image/png", data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==" }]);
    assert.deepEqual(errors, []);
  });

  test(`a page that doesn't declare images is refused them before any request (${engine})`, async (t) => {
    const site = await fakeMagpie(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    await page.goto(site.base + "/a/sampler-noimg/");
    await page.waitForFunction(() => window.out, null, { timeout: 15000 });
    const out = await page.evaluate(() => window.out);
    assert.equal(out.images, "images_unavailable");
    assert.equal(out.limits.images, undefined);
    assert.equal(site.sampler.calls.length, 0);
  });

  test(`a shared copy resolves every capability null (${engine})`, async (t) => {
    const site = await fakeMagpie(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    await page.goto(site.base + "/a/shared/");
    await page.waitForFunction(() => window.out, null, { timeout: 15000 });
    assert.deepEqual(await page.evaluate(() => window.out), { db: null, user: null, assets: null, sample: null, perm: "function" });
    assert.equal(site.calls.length, 0);
  });
}
