// Run with Node's test runner and Playwright on the module path; see README.md.
// downloads.save from a page in magpie's viewer shell
// (internal/artifact/runtime.js, shell.js): the runtime checks the call
// as contract 0.2.99 has it, the shell asks the viewer, one file at a
// time, and saves it only on their Save, under the cleaned name, with
// the bytes the page gave. Cancel, Escape and a second file are
// answered; a page posting to the shell itself is checked again. In both
// engines, at a narrow width, in every language. artifact-comments
// holds the shell's comments.
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
  const html = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name="referrer" content="no-referrer"><title>${esc(cfg.title)}</title><style nonce="${nonce}">${shellCSS}</style></head><body><iframe id="page" title="${esc(cfg.title)}" allow="clipboard-write; fullscreen"></iframe><div id="ui"></div><script nonce="${nonce}">${shellJS}(${json});</script></body></html>`;
  return { html, nonce };
}
function served(page, cfg) {
  const tag = "<script>" + runtime + "(" + JSON.stringify(cfg).replace(/</g, "\\u003c") + ");</script>";
  return page.replace(/<head>/i, "<head>" + tag);
}

const PAGE = `<!doctype html><html><head><title>Export</title></head><body>
<button id=go>Export</button><input id=box>
<script>
window.out = {};
(async () => {
  const d = await claude.use("downloads");
  out.served = !!d;
  out.frozen = Object.isFrozen(d);
  out.verbs = d ? Object.keys(d).sort() : [];
  window.save = (o) => d.save(o).then((r) => r, (e) => ({ code: e.code, name: e.name }));
  out.ready = true;
})().catch((e) => { out.crash = String(e && e.stack || e); });
</script></body></html>`;

// a fake magpie: the shell, the page, an empty comments store
async function fakeMagpie(t, lang, caps) {
  const streams = new Set();
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    if (req.method === "GET" && url.pathname === "/a/x/") {
      const origin = "http://" + req.headers.host;
      if (req.headers["sec-fetch-dest"] === "document") {
        const { html, nonce } = shellPage({ api: "/a/x/_api", token: "stok", id: "x", title: "Export", src: "/a/x/", version: 1, origin, lang });
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
    if (req.method === "POST" && url.pathname === "/a/x/_api/comments") {
      req.resume();
      req.on("end", () => {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ threads: [], version: 1, agent: { can: "no_session" }, me: "u_0123456789abcdef01234567" }));
      });
      return;
    }
    res.writeHead(404);
    res.end();
  });
  t.after(() => { for (const s of streams) s.destroy(); server.close(); });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  return { base: `http://127.0.0.1:${server.address().port}` };
}

const launch = (engine) => (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
async function open(t, engine, opts) {
  opts = opts || {};
  const site = await fakeMagpie(t, opts.lang || "en", opts.caps || { downloads: {} });
  const browser = await launch(engine);
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: opts.viewport || { width: 1200, height: 800 }, acceptDownloads: true });
  // a shell slow to answer the page's hello, as on a busy computer
  if (opts.slowShell) await page.addInitScript((ms) => {
    if (window.top !== window) return;
    const on = window.addEventListener;
    window.addEventListener = function (type, fn, o) {
      if (type !== "message") return on.call(this, type, fn, o);
      return on.call(this, type, function (e) { if (e.data && e.data.type === "hello") setTimeout(() => fn.call(this, e), ms); else fn.call(this, e); }, o);
    };
  }, opts.slowShell);
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(site.base + "/a/x/");
  const frame = await (await page.waitForSelector("#page")).contentFrame();
  await frame.waitForFunction(() => window.out && (window.out.ready || window.out.crash || window.out.served === false), null, { timeout: 15000 });
  return { page, frame, errors };
}
const ask = (page) => page.locator(".pop.save.in");
// the page's save, started from a click in the page as a page would
async function start(frame, o) {
  await frame.evaluate((o) => {
    document.getElementById("go").onclick = () => { window.res = window.save(o.buf ? Object.assign({}, o, { data: new TextEncoder().encode(o.buf).buffer }) : o); };
  }, o);
  await frame.locator("#go").click();
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`the viewer saves a page's file (${engine})`, async (t) => {
    const { page, frame, errors } = await open(t, engine);
    const out = await frame.evaluate(() => window.out);
    assert.equal(out.crash, undefined);
    assert.deepEqual([out.served, out.frozen, out.verbs], [true, true, ["save"]]);
    await start(frame, { filename: "../plan  \u200b.csv", buf: "a,b\n1,2\n" });
    await ask(page).waitFor();
    assert.equal(await ask(page).locator(".ask").textContent(), "Save “_plan .csv”?");
    assert.match(await ask(page).locator(".size").textContent(), /^8\s?(B|byte)/);
    const [dl] = await Promise.all([page.waitForEvent("download"), ask(page).getByRole("button", { name: "Save" }).click()]);
    assert.equal(dl.suggestedFilename(), "_plan .csv");
    assert.equal(fs.readFileSync(await dl.path(), "utf8"), "a,b\n1,2\n");
    assert.deepEqual(await frame.evaluate(() => window.res), { status: "saved" });
    await page.waitForFunction(() => !document.querySelector(".pop.save"));
    assert.deepEqual(errors, []);
  });

  test(`the viewer says no, and one file waits at a time (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine);
    let saved = 0;
    page.on("download", () => saved++);
    await start(frame, { filename: "a.txt", data: "one" });
    await ask(page).waitFor();
    assert.deepEqual(await frame.evaluate(() => window.save({ filename: "b.txt", data: "two" })), { code: "rate_limited", name: "DownloadsError" });
    await ask(page).getByRole("button", { name: "Cancel" }).click();
    assert.deepEqual(await frame.evaluate(() => window.res), { code: "declined", name: "DownloadsError" });
    // Escape in the page declines it too
    await start(frame, { filename: "c.json", data: "{}" });
    await ask(page).waitFor();
    await frame.locator("#box").focus();
    await page.keyboard.press("Escape");
    assert.deepEqual(await frame.evaluate(() => window.res), { code: "declined", name: "DownloadsError" });
    await page.waitForFunction(() => !document.querySelector(".pop.save"));
    assert.equal(saved, 0);
  });

  test(`a bad call is refused before anyone is asked (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine);
    const codes = await frame.evaluate(async () => {
      const r = async (o) => (await window.save(o)).code;
      return {
        exe: await r({ filename: "run.exe", data: "x" }),
        none: await r({ filename: "README", data: "x" }),
        mov: await r({ filename: "clip.MOV", data: "x" }),
        name: await r({ filename: 7, data: "x" }),
        long: await r({ filename: "a".repeat(509) + ".txt", data: "x" }),
        empty: await r({ filename: "a.txt", data: "" }),
        obj: await r({ filename: "a.txt", data: { a: 1 } }),
        request: await r({ filename: "a.txt", data: "x", request: "r1" }),
        big: await r({ filename: "a.zip", data: new ArrayBuffer((64 << 20) + 1) }),
      };
    });
    assert.deepEqual(codes, { exe: "rejected_extension", none: "rejected_extension", mov: "not_signed", name: "bad_request", long: "bad_request", empty: "bad_request", obj: "bad_request", request: "request_unknown", big: "too_large" });
    // a page posting to the shell itself is checked there too
    const direct = await frame.evaluate(() => new Promise((res) => {
      addEventListener("message", (e) => { if (e.data && e.data.type === "saved" && e.data.req === 99) res(e.data.error); });
      parent.postMessage({ magpie: "artifact-shell", type: "save", req: 99, filename: "../../x.csv", size: 1, blob: new Blob(["x"]) }, "*");
    }));
    assert.equal(direct, "bad_request");
    assert.equal(await page.locator(".pop.save").count(), 0, "nobody was asked");
  });

  test(`a page without downloads gets null (${engine})`, async (t) => {
    const { frame } = await open(t, engine, { caps: { comments: {} } });
    assert.equal(await frame.evaluate(() => window.out.served), false);
  });

  test(`the prompt fits a narrow window (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine, { viewport: { width: 320, height: 568 } });
    await start(frame, { filename: "a-rather-long-name-for-a-file-that-the-page-exported-today.pdf", data: "%PDF" });
    await ask(page).waitFor();
    const b = await ask(page).boundingBox();
    assert.ok(b.x >= 0 && b.x + b.width <= 320 && b.y >= 0 && b.y + b.height <= 568, `inside the window: ${JSON.stringify(b)}`);
  });

  test(`a shell slow to answer still asks the viewer (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine, { slowShell: 4000 });
    await start(frame, { filename: "late.csv", data: "a,b" });
    await ask(page).waitFor({ timeout: 15000 });
    await ask(page).getByRole("button", { name: "Cancel", exact: true }).click();
    assert.equal((await frame.evaluate(() => window.res)).code, "declined");
  });

  const WORDS = { zh: ["保存“a.md”？", "保存", "取消"], "zh-TW": ["儲存「a.md」？", "儲存", "取消"], ja: ["「a.md」を保存しますか？", "保存", "キャンセル"], de: ["„a.md“ sichern?", "Sichern", "Abbrechen"] };
  for (const [lang, [q, yes, no]] of Object.entries(WORDS)) {
    test(`the prompt speaks ${lang} (${engine})`, async (t) => {
      const { page, frame } = await open(t, engine, { lang });
      await start(frame, { filename: "a.md", data: "# a" });
      await ask(page).waitFor();
      assert.equal(await ask(page).locator(".ask").textContent(), q);
      await ask(page).getByRole("button", { name: yes, exact: true }).waitFor();
      await ask(page).getByRole("button", { name: no, exact: true }).click();
      assert.equal((await frame.evaluate(() => window.res)).code, "declined");
    });
  }
}
