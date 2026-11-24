// Run with Node's test runner and Playwright on the module path; see README.md.
// The page runtime (internal/artifact/runtime.js), as a page written for
// claude.ai's artifact runtime meets it: window.claude is there before the
// page's own scripts, use() resolves db and user later and null for what
// isn't served, a bad path throws a TypeError at once, a refused call
// rejects with its code, a query's onSnapshot shows the page's own writes
// and another's (the agent's, told on the events stream), and a shared
// copy resolves every capability null. Under the sandbox CSP magpie serves
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
    perm: typeof (await claude.use("permissions")).state,
  };
})();
</script></body></html>`;

// a store like magpie's, small: documents by path, a write told to every
// open events stream
function fakeMagpie(t) {
  const docs = new Map(), streams = new Set(), calls = [];
  const tell = (p) => { for (const s of streams) s.write(`data: ${JSON.stringify({ paths: [p] })}\n\n`); };
  const err = (res, status, code) => { res.writeHead(status, { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" }); res.end(JSON.stringify({ error: { code, message: code } })); };
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    if (req.method === "GET" && url.pathname === "/a/board/") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP, "Vary": "Sec-Fetch-Mode" });
      return res.end(served(PAGE, { api: "/a/board/_api", token: "tok", caps: { db: {}, user: {} }, viewer: { id: "u_0123456789abcdef01234567" } }));
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
  t.after(() => { for (const s of streams) s.destroy(); server.close(); });
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve({
    base: `http://127.0.0.1:${server.address().port}`,
    calls,
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

  test(`a shared copy resolves every capability null (${engine})`, async (t) => {
    const site = await fakeMagpie(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    await page.goto(site.base + "/a/shared/");
    await page.waitForFunction(() => window.out, null, { timeout: 15000 });
    assert.deepEqual(await page.evaluate(() => window.out), { db: null, user: null, perm: "function" });
    assert.equal(site.calls.length, 0);
  });
}
