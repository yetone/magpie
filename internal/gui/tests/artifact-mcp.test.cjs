// Run with Node's test runner and Playwright on the module path; see README.md.
// mcp from a page in magpie's viewer shell (internal/artifact/runtime.js,
// shell.js): the shell asks the viewer once for each server and artifact,
// and makes the calls with its own token; the page keeps the cache and the
// watches. The question, Don't allow, the Permissions panel, a watch that
// never asks, the cache, the errors and magpie's refusals reach the page.
// In both engines, at a narrow width, in every language. The route itself
// is held by TestArtifactMCP* in internal/gateway.
const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const dir = path.resolve(__dirname, "../../artifact");
const runtime = fs.readFileSync(path.join(dir, "runtime.js"), "utf8").replace(/\n+$/, "");
const shellJS = fs.readFileSync(path.join(dir, "shell.js"), "utf8").replace(/\n+$/, "");
const shellCSS = fs.readFileSync(path.join(dir, "shell.css"), "utf8");
const PAGE_CSP = "sandbox allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads";
const shellCSPSource = /func ShellCSP\(nonce string\) string \{\n\treturn (".*")\n\}/.exec(fs.readFileSync(path.join(dir, "shell.go"), "utf8"));
assert.ok(shellCSPSource, "ShellCSP in shell.go");
function shellCSP(nonce) {
  return shellCSPSource[1].split(/" \+ nonce \+ "/).map((s) => s.replace(/^"|"$/g, "")).join(nonce);
}
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&#34;");
function shellPage(cfg) {
  const nonce = crypto.randomBytes(18).toString("base64url");
  const json = JSON.stringify(cfg).replace(/</g, "\\u003c").replace(/>/g, "\\u003e").replace(/&/g, "\\u0026");
  const html = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name="referrer" content="no-referrer"><title>${esc(cfg.title)}</title><style nonce="${nonce}">${shellCSS}</style></head><body><iframe id="page" title="${esc(cfg.title)}" allow="autoplay; clipboard-write; fullscreen"></iframe><div id="ui"></div><script nonce="${nonce}">${shellJS}(${json});</script></body></html>`;
  return { html, nonce };
}
function served(page, cfg) {
  const tag = "<script>" + runtime + "(" + JSON.stringify(cfg).replace(/</g, "\\u003c") + ");</script>";
  return page.replace(/<head>/i, "<head>" + tag);
}

const PAGE = `<!doctype html><html><head><title>Issues</title></head><body>
<button id=go>Load</button><input id=box>
<script>
window.out = {};
window.log = [];
(async () => {
  const m = await claude.use("mcp");
  const p = await claude.use("permissions");
  window.p = p;
  out.served = !!m;
  out.frozen = Object.isFrozen(m);
  out.verbs = m ? Object.keys(m).sort() : [];
  if (!m) return;
  window.m = m;
  window.ans = (q) => q.then((r) => ({ ok: JSON.parse(JSON.stringify(r)) }), (e) => JSON.parse(JSON.stringify({ code: e.code, name: e.name, server: e.server, retryable: e.retryable, retryAfterMs: e.retryAfterMs, result: e.result })));
  out.ready = true;
})().catch((e) => { out.crash = String(e && e.stack || e); });
</script></body></html>`;

const MANIFEST = { servers: [
  { server: "github", tools: ["list_issues", "create_issue"] },
  { server: "Linear", tools: ["list_issues"] },
] };

// a fake magpie: the shell, the page, an empty comments store, and the
// MCP route with github (list_issues reads, create_issue writes); Linear
// is in the manifest and not in the Library
async function fakeMagpie(t, lang, caps) {
  const streams = new Set(), calls = [];
  const site = { calls, n: 0, fail: null, hold: null };
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    if (req.method === "GET" && url.pathname === "/a/x/") {
      const origin = "http://" + req.headers.host;
      if (req.headers["sec-fetch-dest"] === "document") {
        const { html, nonce } = shellPage({ api: "/a/x/_api", token: "stok", id: "x", title: "Issues", src: "/a/x/", version: 1, origin, lang });
        res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": shellCSP(nonce), "Cache-Control": "no-store" });
        return res.end(html);
      }
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": PAGE_CSP });
      return res.end(served(PAGE, { api: "/a/x/_api", token: "ptok", caps, viewer: { id: "u_0123456789abcdef01234567" }, version: 1, home: "/a/x/", origin }));
    }
    if (req.method === "GET" && url.pathname === "/a/x/_api/shell") {
      res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store" });
      res.write('retry: 2000\n\ndata: {"comments":true}\n\n');
      streams.add(res);
      req.on("close", () => streams.delete(res));
      return;
    }
    if (req.method === "POST" && (url.pathname === "/a/x/_api/comments" || url.pathname === "/a/x/_api/mcp")) {
      const chunks = [];
      req.on("data", (c) => chunks.push(c));
      req.on("end", async () => {
        const json = (code, v) => { res.writeHead(code, { "Content-Type": "application/json" }); res.end(JSON.stringify(v)); };
        if (url.pathname.endsWith("/comments")) return json(200, { threads: [], version: 1, agent: { can: "no_session" }, me: "u_0123456789abcdef01234567" });
        const b = JSON.parse(Buffer.concat(chunks).toString());
        if (b.op === "list") {
          if (site.off) return json(200, { manifest: MANIFEST.servers, servers: [], off: site.off });
          return json(200, { manifest: MANIFEST.servers, servers: [{ server: "github", kind: "connector", authStatus: "connected", tools: [
            { name: "list_issues", description: "Lists issues.", annotations: { readOnlyHint: true } },
            { name: "create_issue", description: "Opens an issue.", annotations: { readOnlyHint: false } },
          ] }] });
        }
        if (b.op === "describe") return json(200, { name: b.tool, description: "Lists issues.", inputSchema: { type: "object", properties: { q: { type: "string" } } } });
        calls.push({ token: b.t, version: b.version, server: b.server, tool: b.tool, input: b.input, watch: b.watch });
        if (b.server === "Linear") return json(404, { error: { code: "server_not_connected", message: "no Linear here", server: "Linear" } });
        if (site.hold) await site.hold;
        if (site.fail) return json(site.fail[0], { error: Object.assign({ message: "magpie says " + site.fail[1], server: b.server }, site.fail[2] || {}, { code: site.fail[1] }) });
        const n = ++site.n;
        const ro = b.tool === "list_issues";
        json(200, { annotations: { readOnlyHint: ro }, result: { content: [{ type: "text", text: JSON.stringify({ n, q: (b.input || {}).q || "" }) }] } });
      });
      return;
    }
    res.writeHead(404);
    res.end();
  });
  t.after(() => { for (const s of streams) s.destroy(); server.close(); });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  site.base = `http://127.0.0.1:${server.address().port}`;
  return site;
}

const launch = (engine) => (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
async function open(t, engine, opts) {
  opts = opts || {};
  const site = await fakeMagpie(t, opts.lang || "en", opts.caps || { mcp: MANIFEST });
  const browser = await launch(engine);
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: opts.viewport || { width: 1200, height: 800 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(site.base + "/a/x/");
  if (opts.grant) {
    await page.evaluate((g) => localStorage.setItem("magpie.artifact.x.mcp:github", g), opts.grant);
    await page.reload();
  }
  const frame = await (await page.waitForSelector("#page")).contentFrame();
  await frame.waitForFunction(() => window.out && (window.out.ready || window.out.crash || window.out.served === false), null, { timeout: 15000 });
  return { page, frame, errors, site };
}
const ask = (page) => page.locator(".pop.save.in[role=alertdialog]");
// the page's call, started and left running
const call = (frame, server, tool, input, o) => frame.evaluate(([s, tl, i, o]) => { window.res = undefined; window.ans(m.callTool(s, tl, i, o)).then((r) => { window.res = r; }); }, [server, tool, input, o]);
const result = async (frame) => (await frame.waitForFunction(() => window.res)).jsonValue();
async function allow(page, name) {
  await ask(page).waitFor();
  await page.waitForTimeout(450);
  await ask(page).getByRole("button", { name: name || "Allow", exact: true }).click();
}
// a watch's events, as plain data, in window.events[name]
const watch = (frame, name, server, tool, input, o) => frame.evaluate(([n, s, tl, i, o]) => {
  window.events = window.events || {};
  const list = (window.events[n] = []);
  window.stops = window.stops || {};
  window.stops[n] = m.watchTool(s, tl, i, (ev) => list.push(ev.type === "data" ? { type: "data", payload: ev.result.payload, cache: ev.result.cache && { revalidating: ev.result.cache.revalidating, stored: typeof ev.result.cache.storedAt === "number" } } : { type: "error", code: ev.error.code }), o);
}, [name, server, tool, input, o]);
const events = (frame, name, n) => frame.waitForFunction(([name, n]) => window.events[name].length >= n && window.events[name], [name, n], { timeout: 15000 }).then((h) => h.jsonValue());

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`a page runs a tool, the server asked about once (${engine})`, async (t) => {
    const { page, frame, errors, site } = await open(t, engine);
    const out = await frame.evaluate(() => window.out);
    assert.equal(out.crash, undefined);
    assert.deepEqual([out.served, out.frozen, out.verbs], [true, true, ["callTool", "describeTool", "invalidate", "listTools", "server", "watchTool"]]);
    assert.deepEqual(await frame.evaluate(() => p.state()), { mcp: "prompt", "mcp:github": "prompt", "mcp:Linear": "prompt" });
    // not asked about yet: listed from the manifest, names only
    const listed = await frame.evaluate(() => m.listTools("github"));
    assert.deepEqual(listed, { servers: [{ server: "github", kind: "connector", authStatus: "unknown", tools: [{ name: "list_issues", description: "" }, { name: "create_issue", description: "" }] }] });
    assert.equal((await frame.evaluate(() => window.ans(m.describeTool("github", "list_issues")))).code, "not_in_manifest", "described only once allowed");

    await call(frame, "github", "list_issues", { q: "open" });
    await ask(page).waitFor();
    assert.equal(await ask(page).locator(".ask").textContent(), "Let “Issues” use github?");
    assert.equal(await ask(page).locator(".size").textContent(), "It can run list_issues, create_issue as you.");
    assert.equal(site.calls.length, 0, "nothing runs before the viewer says yes");
    await allow(page);
    const r = await result(frame);
    assert.deepEqual(r.ok.payload, { n: 1, q: "open" });
    assert.deepEqual(site.calls, [{ token: "stok", version: 0, server: "github", tool: "list_issues", input: { q: "open" }, watch: false }]);
    assert.equal(await page.evaluate(() => localStorage.getItem("magpie.artifact.x.mcp:github")), "granted");
    assert.deepEqual(await frame.evaluate(() => p.state()), { mcp: "prompt", "mcp:github": "granted", "mcp:Linear": "prompt" });

    // asked once: the next call runs at once; the handle answers the payload
    assert.deepEqual(await frame.evaluate(async () => (await m.server("github")).list_issues({ q: "x" })), { n: 2, q: "x" });
    assert.equal(await ask(page).count(), 0);
    const described = await frame.evaluate(() => m.describeTool("github", "list_issues"));
    assert.equal(described.name, "list_issues");
    assert.ok(described.inputSchema);
    // a write runs and is never cached: each call reaches magpie
    await frame.evaluate(() => m.callTool("github", "create_issue", { title: "a" }));
    await frame.evaluate(() => m.callTool("github", "create_issue", { title: "a" }));
    assert.equal(site.calls.filter((c) => c.tool === "create_issue").length, 2);
    // concurrent reads of one identity share a flight; key order is no matter
    const before = site.calls.length;
    const two = await frame.evaluate(() => Promise.all([m.callTool("github", "list_issues", { q: "a", s: 1 }), m.callTool("github", "list_issues", { s: 1, q: "a" })]).then((rs) => rs.map((r) => r.payload.n)));
    assert.equal(two[0], two[1]);
    assert.equal(site.calls.length, before + 1);
    // staleTime keeps a result fresh: the cache answers, marked so
    const c1 = await frame.evaluate(() => m.callTool("github", "list_issues", { q: "c" }, { cache: { staleTime: 60000 } }));
    const c2 = await frame.evaluate(() => m.callTool("github", "list_issues", { q: "c" }, { cache: { staleTime: 60000 } }));
    assert.equal(c1.payload.n, c2.payload.n);
    assert.equal(c2.cache.revalidating, false);
    assert.equal(typeof c2.cache.storedAt, "number");
    assert.equal(c1.cache, undefined);
    assert.deepEqual(errors, []);
  });

  test(`Don't allow is kept, and Permissions changes it (${engine})`, async (t) => {
    const { page, frame, site } = await open(t, engine);
    await call(frame, "github", "list_issues");
    await allow(page, "Don't allow");
    assert.deepEqual(await result(frame), { code: "not_in_manifest", name: "McpError", server: "github" });
    assert.equal(await frame.evaluate(() => p.state("mcp:github")), "denied");
    await call(frame, "github", "list_issues");
    assert.equal((await result(frame)).code, "not_in_manifest");
    assert.equal(await ask(page).count(), 0, "a no is never asked again");
    assert.deepEqual(await frame.evaluate(() => m.listTools()), { servers: [{ server: "Linear", kind: "connector", authStatus: "unknown", tools: [{ name: "list_issues", description: "" }] }] }, "a server said no to is not listed");
    assert.equal(site.calls.length, 0);
    // the panel: a row per manifest server
    await frame.evaluate(() => { document.getElementById("go").onclick = () => { window.managed = p.manage().then(() => "closed"); }; });
    await frame.locator("#go").click();
    const panel = page.locator(".pop.save.perms.in");
    await panel.waitFor();
    assert.deepEqual(await panel.locator(".perm > span").allTextContents(), ["github", "Linear"]);
    const gh = panel.locator(".perm").filter({ hasText: "github" });
    assert.equal(await gh.locator("button.on").textContent(), "Don't allow");
    await gh.getByRole("button", { name: "Ask", exact: true }).click();
    await panel.getByRole("button", { name: "Close" }).click();
    assert.equal(await frame.evaluate(() => window.managed), "closed");
    assert.equal(await frame.evaluate(() => p.state("mcp:github")), "prompt");
    // closed unanswered: retryable, and asked again on a later call
    await call(frame, "github", "list_issues");
    await ask(page).waitFor();
    await page.keyboard.press("Escape");
    assert.deepEqual(await result(frame), { code: "upstream_error", name: "McpError", server: "github", retryable: true, retryAfterMs: 3000 });
    assert.equal(site.calls.length, 0);
    // request asks for the servers named, in one question
    const asked = frame.evaluate(() => p.request(["mcp:github"]));
    await allow(page);
    assert.deepEqual(await asked, { "mcp:github": "granted" });
    await call(frame, "github", "list_issues");
    assert.ok((await result(frame)).ok);
    assert.equal(await ask(page).count(), 0);
  });

  test(`a watch never asks, replays the cache and follows invalidate (${engine})`, async (t) => {
    const { page, frame, site } = await open(t, engine);
    await watch(frame, "w0", "github", "list_issues", { q: "w" });
    assert.deepEqual(await events(frame, "w0", 1), [{ type: "error", code: "approval_required" }]);
    assert.equal(await ask(page).count(), 0);
    assert.equal(site.calls.length, 0);
    await frame.evaluate(() => window.stops.w0());

    await call(frame, "github", "list_issues", { q: "w" });
    await allow(page);
    const first = (await result(frame)).ok.payload.n;
    // the cache replays at once, stale, and the refetch follows
    await watch(frame, "w1", "github", "list_issues", { q: "w" });
    const ev = await events(frame, "w1", 2);
    assert.deepEqual(ev[0], { type: "data", payload: { n: first, q: "w" }, cache: { revalidating: true, stored: true } });
    assert.equal(ev[1].type, "data");
    assert.equal(ev[1].payload.n, first + 1);
    assert.equal(site.calls.at(-1).watch, true);
    // a call of the same identity feeds the watch
    await frame.evaluate(() => m.callTool("github", "list_issues", { q: "w" }));
    assert.equal((await events(frame, "w1", 3))[2].payload.n, first + 2);
    // invalidate refetches what is watched
    await frame.evaluate(() => m.invalidate("github", "list_issues"));
    assert.equal((await events(frame, "w1", 4))[3].payload.n, first + 3);
    // after unsubscribing, nothing more arrives
    await frame.evaluate(() => { window.stops.w1(); window.stops.w1(); });
    await frame.evaluate(() => m.invalidate("github"));
    await page.waitForTimeout(300);
    assert.equal((await frame.evaluate(() => window.events.w1.length)), 4);
    // a write is never watched, once its hints are known
    await frame.evaluate(() => m.callTool("github", "create_issue", {}));
    await watch(frame, "w2", "github", "create_issue", {});
    assert.deepEqual(await events(frame, "w2", 1), [{ type: "error", code: "bad_request" }]);
    assert.equal(await frame.evaluate(() => { try { m.watchTool("github", "list_issues", {}, null); return "ok"; } catch (e) { return e.name; } }), "TypeError");
  });

  test(`errors reach the page as McpError with their codes (${engine})`, async (t) => {
    const { page, frame, site } = await open(t, engine, { grant: "granted" });
    const codes = await frame.evaluate(async () => [
      (await window.ans(m.callTool("github", "list_issues", [1]))).code,
      (await window.ans(m.callTool("github", "list_issues", { f: { $file: "a" } }))).code,
      (await window.ans(m.callTool("github", "list_issues", { big: "x".repeat(1 << 20) }))).code,
      (await window.ans(m.callTool("", "list_issues"))).code,
      (await window.ans(m.callTool("github", "list_issues", {}, { cache: 7 }))).code,
      (await window.ans(m.server("Nope"))).code,
    ]);
    assert.deepEqual(codes, ["bad_request", "capability_disabled", "bad_request", "bad_request", "bad_request", "server_not_connected"]);
    assert.equal(site.calls.length, 0);
    // in the manifest, not in the Library: never asked about
    await call(frame, "Linear", "list_issues");
    assert.equal((await result(frame)).code, "server_not_connected");
    assert.equal(await ask(page).count(), 0);
    // a tool's failure, with its whole result
    site.fail = [200, "tool_error", { result: { isError: true, content: [{ type: "text", text: "no repo" }] } }];
    const te = await frame.evaluate(() => window.ans(m.callTool("github", "create_issue", {})));
    assert.equal(te.code, "tool_error");
    assert.deepEqual(te.result.content, [{ type: "text", text: "no repo" }]);
    // retryable, and a wait capped at 60s
    site.fail = [429, "rate_limited", { retryable: true, retryAfterMs: 120000 }];
    assert.deepEqual(await frame.evaluate(() => window.ans(m.callTool("github", "list_issues", { q: "r" }))), { code: "rate_limited", name: "McpError", server: "github", retryable: true, retryAfterMs: 60000 });
    // a denial drops what the cache held for the server
    site.fail = null;
    const kept = await frame.evaluate(() => m.callTool("github", "list_issues", { q: "k" }, { cache: { staleTime: 60000 } }));
    site.fail = [403, "blocked_by_policy"];
    assert.equal((await frame.evaluate(() => window.ans(m.callTool("github", "list_issues", { q: "z" })))).code, "blocked_by_policy");
    site.fail = null;
    const again = await frame.evaluate(() => m.callTool("github", "list_issues", { q: "k" }, { cache: { staleTime: 60000 } }));
    assert.notEqual(again.payload.n, kept.payload.n);
    // an abort cancels the page's call
    let go;
    site.hold = new Promise((r) => { go = r; });
    const cancelled = frame.evaluate(() => { const ac = new AbortController(); const q = window.ans(m.callTool("github", "list_issues", { q: "slow" }, { signal: ac.signal })); setTimeout(() => ac.abort(), 200); return q; });
    assert.equal((await cancelled).code, "cancelled");
    go();
    site.hold = null;
  });

  test(`with magpie's hub off the page hears so, and nobody is asked (${engine})`, async (t) => {
    const { page, frame, site } = await open(t, engine);
    site.off = "magpie's MCP hub is off, so it runs no server's tools: turn it on in magpie's Library → MCP";
    const r = await frame.evaluate(() => m.callTool("github", "list_issues", { q: "a" }).then(() => ({}), (e) => ({ code: e.code, message: e.message })));
    assert.equal(r.code, "server_not_connected");
    assert.equal(r.message, site.off);
    assert.equal(await ask(page).count(), 0);
    assert.equal(site.calls.length, 0);
  });

  test(`a page without mcp gets null (${engine})`, async (t) => {
    const { frame } = await open(t, engine, { caps: { comments: {} } });
    assert.equal(await frame.evaluate(() => window.out.served), false);
    assert.equal(await frame.evaluate(() => p.state("mcp:github")), "unavailable");
    assert.equal(await frame.evaluate(() => p.state("mcp")), "unavailable");
  });

  test(`the question and the panel fit a narrow window (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine, { viewport: { width: 320, height: 568 } });
    await call(frame, "github", "list_issues");
    await ask(page).waitFor();
    await page.waitForTimeout(250);
    const q = await ask(page).boundingBox();
    assert.ok(q.x >= 0 && q.x + q.width <= 320 && q.y >= 0 && q.y + q.height <= 568, `the question inside the window: ${JSON.stringify(q)}`);
    await allow(page);
    await result(frame);
    await frame.evaluate(() => { document.getElementById("go").onclick = () => { p.manage(); }; });
    await frame.locator("#go").click();
    const panel = page.locator(".pop.save.perms.in");
    await panel.waitFor();
    await page.waitForTimeout(250);
    const b = await panel.boundingBox();
    assert.ok(b.x >= 0 && b.x + b.width <= 320 && b.y >= 0 && b.y + b.height <= 568, `the panel inside the window: ${JSON.stringify(b)}`);
    for (const row of await panel.locator(".perm").all()) {
      const r = await row.boundingBox();
      assert.ok(r.x >= b.x && r.x + r.width <= b.x + b.width + 0.5, `a row inside the panel: ${JSON.stringify([r, b])}`);
    }
  });

  const WORDS = {
    zh: ["允许“Issues”使用 github？", "它能以你的身份运行 list_issues, create_issue。", "允许"],
    "zh-TW": ["允許「Issues」使用 github？", "它能以你的身分執行 list_issues, create_issue。", "允許"],
    ja: ["「Issues」に github の使用を許可しますか？", "あなたとして list_issues, create_issue を実行できます。", "許可"],
    de: ["„Issues“ github verwenden lassen?", "Die Seite kann list_issues, create_issue in deinem Namen ausführen.", "Erlauben"],
  };
  for (const [lang, [question, tools, yes]] of Object.entries(WORDS)) {
    test(`the question speaks ${lang} (${engine})`, async (t) => {
      const { page, frame } = await open(t, engine, { lang });
      await call(frame, "github", "list_issues");
      await ask(page).waitFor();
      assert.equal(await ask(page).locator(".ask").textContent(), question);
      assert.equal(await ask(page).locator(".size").textContent(), tools);
      await allow(page, yes);
      assert.ok((await result(frame)).ok);
    });
  }
}
