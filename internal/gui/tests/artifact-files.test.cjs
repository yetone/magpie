// Run with Node's test runner and Playwright on the module path; see README.md.
// files from a page in magpie's viewer shell (internal/artifact/runtime.js,
// shell.js): the shell asks the viewer once for each artifact, naming the
// folder, and makes the calls with its own token; a download is asked
// about file by file. The question, Don't allow, the Permissions panel,
// list, read, download, the sample tools and the errors reach the page.
// In both engines, at a narrow width, in every language. The route itself
// is held by TestArtifactFiles* in internal/gateway.
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

const PAGE = `<!doctype html><html><head><title>Plan</title></head><body>
<button id=go>Open</button>
<script>
window.out = {};
(async () => {
  const f = await claude.use("files");
  const p = await claude.use("permissions");
  window.p = p;
  out.served = !!f;
  out.frozen = Object.isFrozen(f);
  out.verbs = f ? Object.keys(f).sort() : [];
  if (!f) return;
  window.f = f;
  window.ans = (q) => q.then((r) => ({ ok: r && r.bytes ? { text: new TextDecoder().decode(r.bytes), uint8: r.bytes instanceof Uint8Array, size: r.size, revision: r.revision } : JSON.parse(JSON.stringify(r)) }), (e) => Object.assign({ code: e.code, name: e.name }, e.retryAfterMs !== undefined ? { retryAfterMs: e.retryAfterMs } : {}));
  out.ready = true;
})().catch((e) => { out.crash = String(e && e.stack || e); });
</script></body></html>`;

const BIG = "a" + "é".repeat(7000); // 14001 bytes: the first 12 KB ends inside an é
const FILES = {
  "README.md": Buffer.from("# Plan\nhello"),
  "src/main.go": Buffer.from("package main\n"),
  "src/lib/util.go": Buffer.from("package lib\n"),
  "logo.png": Buffer.from([0x89, 0x50, 0x4e, 0x47, 0, 0, 0, 13]),
  "big.txt": Buffer.from(BIG),
};

// a fake magpie: the shell, the page, an empty comments store, and the
// files route over FILES, in the folder /Users/u/proj
async function fakeMagpie(t, lang, caps) {
  const streams = new Set(), calls = [];
  const site = { calls, rev: "r1" };
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    if (req.method === "GET" && url.pathname === "/a/x/") {
      const origin = "http://" + req.headers.host;
      if (req.headers["sec-fetch-dest"] === "document") {
        const { html, nonce } = shellPage({ api: "/a/x/_api", token: "stok", id: "x", title: "Plan", src: "/a/x/", version: 1, origin, lang });
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
    if (req.method === "POST" && (url.pathname === "/a/x/_api/comments" || url.pathname === "/a/x/_api/files")) {
      const chunks = [];
      req.on("data", (c) => chunks.push(c));
      req.on("end", () => {
        const json = (code, v) => { res.writeHead(code, { "Content-Type": "application/json" }); res.end(JSON.stringify(v)); };
        if (url.pathname.endsWith("/comments")) return json(200, { threads: [], version: 1, agent: { can: "no_session" }, me: "u_0123456789abcdef01234567" });
        const b = JSON.parse(Buffer.concat(chunks).toString());
        calls.push(b);
        if (b.t !== "stok") return json(403, { error: { code: "not_granted", message: "not the shell's token" } });
        if (b.op === "where") return json(200, { folder: "/Users/u/proj", name: "proj" });
        if (b.op === "list") {
          const under = b.path ? b.path + "/" : "";
          const seen = new Set();
          for (const p of Object.keys(FILES).sort()) {
            if (!p.startsWith(under)) continue;
            const rest = p.slice(under.length).split("/");
            for (let i = 1; i <= (b.recursive ? rest.length : 1); i++) seen.add(under + rest.slice(0, i).join("/") + (i < rest.length ? "/" : ""));
          }
          const entries = [...seen].sort().map((p) => (p.endsWith("/") ? { path: p.slice(0, -1), isDirectory: true, size: 0 } : { path: p, isDirectory: false, size: FILES[p].length }));
          if (b.path === "src" && !b.cursor) return json(200, { entries: entries.slice(0, 1), cursor: entries[0].path });
          if (b.path === "src") return json(200, { entries: entries.filter((e) => e.path > b.cursor) });
          return json(200, { entries });
        }
        const file = FILES[b.path];
        if (!file) return json(404, { error: { code: "not_found", message: b.path + " isn't in the folder" } });
        if (b.op === "read") {
          const off = b.offset || 0, n = b.length === undefined ? file.length : b.length;
          res.writeHead(200, { "Content-Type": "application/octet-stream", "X-Magpie-Size": String(file.length), "X-Magpie-Revision": site.rev });
          return res.end(file.subarray(off, off + n));
        }
        if (b.op === "stat") return json(200, { name: path.posix.basename(b.path), size: file.length, revision: site.rev });
        if (b.op === "save") {
          if (b.revision !== site.rev) return json(409, { error: { code: "changed", message: "it changed" } });
          res.writeHead(200, { "Content-Type": "application/octet-stream" });
          return res.end(file);
        }
        json(400, { error: { code: "bad_request", message: "no such op" } });
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
  const site = await fakeMagpie(t, opts.lang || "en", opts.caps || { files: {} });
  const browser = await launch(engine);
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: opts.viewport || { width: 1200, height: 800 }, acceptDownloads: true });
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(site.base + "/a/x/");
  if (opts.grant) {
    await page.evaluate((g) => localStorage.setItem("magpie.artifact.x.files", g), opts.grant);
    await page.reload();
  }
  const frame = await (await page.waitForSelector("#page")).contentFrame();
  await frame.waitForFunction(() => window.out && (window.out.ready || window.out.crash || window.out.served === false), null, { timeout: 15000 });
  return { page, frame, errors, site };
}
const ask = (page) => page.locator(".pop.save.in[role=alertdialog]");
// the page's call, started and left running
const run = (frame, js) => frame.evaluate((js) => { window.res = undefined; window.ans((0, eval)(js)).then((r) => { window.res = r; }); }, js);
const result = async (frame) => (await frame.waitForFunction(() => window.res)).jsonValue();
async function answer(page, name) {
  await ask(page).waitFor();
  await page.waitForTimeout(450);
  await ask(page).getByRole("button", { name: name || "Allow", exact: true }).click();
}
const ops = (site) => site.calls.filter((c) => c.op !== "where").map((c) => c.op);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`a page lists and reads the folder, asked about once (${engine})`, async (t) => {
    const { page, frame, errors, site } = await open(t, engine);
    const out = await frame.evaluate(() => window.out);
    assert.equal(out.crash, undefined);
    assert.deepEqual([out.served, out.frozen, out.verbs], [true, true, ["asSampleTools", "download", "list", "read"]]);
    assert.equal(await frame.evaluate(() => p.state("files")), "prompt");

    await run(frame, `f.list()`);
    await ask(page).waitFor();
    assert.equal(await ask(page).locator(".ask").textContent(), "Let “Plan” read the files in proj?");
    assert.equal(await ask(page).locator(".size").textContent(), "/Users/u/proj");
    assert.deepEqual(ops(site), [], "nothing is read before the viewer says yes");
    await answer(page);
    assert.deepEqual((await result(frame)).ok.entries.map((e) => e.path + (e.isDirectory ? "/" : "")), ["README.md", "big.txt", "logo.png", "src/"]);
    assert.equal(await page.evaluate(() => localStorage.getItem("magpie.artifact.x.files")), "granted");
    assert.equal(await frame.evaluate(() => p.state("files")), "granted");
    assert.ok(site.calls.every((c) => c.t === "stok" && c.version === 0), "the shell's token, never the page's");

    // asked once: the next calls run at once
    const all = await frame.evaluate(() => f.list({ path: "src", recursive: true }));
    assert.deepEqual(all, { entries: [{ path: "src/lib", isDirectory: true, size: 0 }], cursor: "src/lib" });
    const more = await frame.evaluate(() => f.list({ path: "src", recursive: true, cursor: "src/lib" }));
    assert.deepEqual(more.entries.map((e) => e.path), ["src/lib/util.go", "src/main.go"]);
    assert.equal(more.cursor, undefined);
    await run(frame, `f.read("README.md", {offset: 2, length: 4})`);
    assert.deepEqual((await result(frame)).ok, { text: "Plan", uint8: true, size: 12, revision: "r1" });
    await run(frame, `f.read("README.md")`);
    assert.equal((await result(frame)).ok.text, "# Plan\nhello");
    assert.equal(await ask(page).count(), 0);
    assert.deepEqual(errors, []);
  });

  test(`Don't allow is kept, and Permissions changes it (${engine})`, async (t) => {
    const { page, frame, site } = await open(t, engine);
    await run(frame, `f.list()`);
    await answer(page, "Don't allow");
    assert.deepEqual(await result(frame), { code: "not_granted", name: "FilesError" });
    assert.equal(await frame.evaluate(() => p.state("files")), "denied");
    await run(frame, `f.read("README.md")`);
    assert.equal((await result(frame)).code, "not_granted");
    assert.equal(await ask(page).count(), 0, "a no is never asked again");
    assert.deepEqual(ops(site), []);
    // the panel has a Files row
    await frame.evaluate(() => { document.getElementById("go").onclick = () => { window.managed = p.manage().then(() => "closed"); }; });
    await frame.locator("#go").click();
    const panel = page.locator(".pop.save.perms.in");
    await panel.waitFor();
    assert.deepEqual(await panel.locator(".perm > span").allTextContents(), ["Files"]);
    assert.equal(await panel.locator(".perm button.on").textContent(), "Don't allow");
    await panel.getByRole("button", { name: "Ask", exact: true }).click();
    await panel.getByRole("button", { name: "Close" }).click();
    assert.equal(await frame.evaluate(() => window.managed), "closed");
    assert.equal(await frame.evaluate(() => p.state("files")), "prompt");
    // closed unanswered: a short wait, then asked again
    await run(frame, `f.list()`);
    await ask(page).waitFor();
    await page.keyboard.press("Escape");
    const dismissed = await result(frame);
    assert.equal(dismissed.code, "rate_limited");
    assert.ok(dismissed.retryAfterMs > 0 && dismissed.retryAfterMs <= 3000, String(dismissed.retryAfterMs));
    await run(frame, `f.list()`);
    assert.equal((await result(frame)).code, "rate_limited", "not asked again at once");
    assert.equal(await ask(page).count(), 0);
    await frame.waitForTimeout(3100);
    // request asks for it
    const asked = frame.evaluate(() => p.request(["files"]));
    await answer(page);
    assert.deepEqual(await asked, { files: "granted" });
    assert.ok((await frame.evaluate(() => window.ans(f.list()))).ok);
    assert.equal(await ask(page).count(), 0);
  });

  test(`a download asks file by file and saves the revision it named (${engine})`, async (t) => {
    const { page, frame, site } = await open(t, engine, { grant: "granted" });
    await run(frame, `f.download("src/main.go")`);
    await ask(page).waitFor();
    assert.equal(await ask(page).locator(".ask").textContent(), "Save “main.go”?");
    assert.ok(!ops(site).includes("save"), "nothing is sent before the viewer says yes");
    await page.waitForTimeout(450);
    const [dl] = await Promise.all([page.waitForEvent("download"), ask(page).getByRole("button", { name: "Save" }).click()]);
    assert.deepEqual((await result(frame)).ok, { status: "saved" });
    assert.equal(dl.suggestedFilename(), "main.go");
    assert.equal(fs.readFileSync(await dl.path(), "utf8"), "package main\n");
    assert.deepEqual(site.calls.filter((c) => c.op === "save").map((c) => [c.path, c.revision]), [["src/main.go", "r1"]]);
    // Cancel is declined, and nothing is sent
    await run(frame, `f.download("README.md")`);
    await answer(page, "Cancel");
    assert.deepEqual(await result(frame), { code: "declined", name: "FilesError" });
    assert.equal(site.calls.filter((c) => c.op === "save").length, 1);
    // changed between the question and the save
    await run(frame, `f.download("README.md")`);
    await ask(page).waitFor();
    site.rev = "r2";
    await answer(page, "Save");
    assert.equal((await result(frame)).code, "changed");
  });

  test(`errors reach the page as FilesError with their codes (${engine})`, async (t) => {
    const { frame, site } = await open(t, engine, { grant: "granted" });
    const codes = await frame.evaluate(async () => [
      (await window.ans(f.read(""))).code,
      (await window.ans(f.read(7))).code,
      (await window.ans(f.read("a", { length: 26 << 20 }))).code,
      (await window.ans(f.read("a", { offset: -1 }))).code,
      (await window.ans(f.list("src"))).code,
      (await window.ans(f.download(""))).code,
    ]);
    assert.deepEqual(codes, ["bad_request", "bad_request", "bad_request", "bad_request", "bad_request", "bad_request"]);
    assert.deepEqual(ops(site), [], "refused before it reaches magpie");
    assert.deepEqual(await frame.evaluate(() => window.ans(f.read("gone.txt"))), { code: "not_found", name: "FilesError" });
    assert.equal((await frame.evaluate(() => window.ans(f.download("gone.txt")))).code, "not_found");
  });

  test(`the sample tools list and read text, a part at a time (${engine})`, async (t) => {
    const { frame } = await open(t, engine, { grant: "granted" });
    const got = await frame.evaluate(async () => {
      const tools = await f.asSampleTools();
      const by = Object.fromEntries(tools.map((x) => [x.name, x]));
      const first = await by.read_project_file.execute({ path: "big.txt" });
      const next = Number(/offset (\d+)\)$/.exec(first)[1]);
      return {
        names: tools.map((x) => x.name),
        frozen: Object.isFrozen(tools) && Object.isFrozen(tools[0]),
        top: await by.list_project_files.execute({}),
        src: await by.list_project_files.execute({ path: "src" }),
        readme: await by.read_project_file.execute({ path: "README.md" }),
        png: await by.read_project_file.execute({ path: "logo.png" }),
        first, next,
        rest: await by.read_project_file.execute({ path: "big.txt", offset: next }),
      };
    });
    assert.deepEqual(got.names, ["list_project_files", "read_project_file"]);
    assert.ok(got.frozen);
    assert.equal(got.top, "README.md (12 bytes)\nbig.txt (14001 bytes)\nlogo.png (8 bytes)\nsrc/");
    assert.equal(got.src, 'src/lib/\n(more: call again with cursor "src/lib")');
    assert.equal(got.readme, "# Plan\nhello");
    assert.equal(got.png, "(logo.png isn't a text file: 8 bytes)");
    // the first part ends before the é the 12 KB cut in two
    assert.equal(got.next, 12287);
    assert.equal(got.first, BIG.slice(0, 6144) + "\n(1714 more bytes: call again with offset 12287)");
    assert.equal(BIG.slice(0, 6144) + got.rest, BIG);
  });

  test(`a page without files gets null (${engine})`, async (t) => {
    const { frame } = await open(t, engine, { caps: { comments: {} } });
    assert.equal(await frame.evaluate(() => window.out.served), false);
    assert.equal(await frame.evaluate(() => p.state("files")), "unavailable");
  });

  test(`the question and the panel fit a narrow window (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine, { viewport: { width: 320, height: 568 } });
    await run(frame, `f.list()`);
    await ask(page).waitFor();
    await page.waitForTimeout(250);
    const q = await ask(page).boundingBox();
    assert.ok(q.x >= 0 && q.x + q.width <= 320 && q.y >= 0 && q.y + q.height <= 568, `the question inside the window: ${JSON.stringify(q)}`);
    await answer(page);
    await result(frame);
    await frame.evaluate(() => { document.getElementById("go").onclick = () => { p.manage(); }; });
    await frame.locator("#go").click();
    const panel = page.locator(".pop.save.perms.in");
    await panel.waitFor();
    await page.waitForTimeout(250);
    const b = await panel.boundingBox();
    assert.ok(b.x >= 0 && b.x + b.width <= 320 && b.y >= 0 && b.y + b.height <= 568, `the panel inside the window: ${JSON.stringify(b)}`);
    const r = await panel.locator(".perm").boundingBox();
    assert.ok(r.x >= b.x && r.x + r.width <= b.x + b.width + 0.5, `the row inside the panel: ${JSON.stringify([r, b])}`);
  });

  const WORDS = {
    zh: ["允许“Plan”读取 proj 里的文件？", "允许"],
    "zh-TW": ["允許「Plan」讀取 proj 裡的檔案？", "允許"],
    ja: ["「Plan」に proj のファイルの読み取りを許可しますか？", "許可"],
    de: ["„Plan“ die Dateien in proj lesen lassen?", "Erlauben"],
  };
  for (const [lang, [question, yes]] of Object.entries(WORDS)) {
    test(`the question speaks ${lang} (${engine})`, async (t) => {
      const { page, frame } = await open(t, engine, { lang });
      await run(frame, `f.list()`);
      await ask(page).waitFor();
      assert.equal(await ask(page).locator(".ask").textContent(), question);
      await answer(page, yes);
      assert.ok((await result(frame)).ok);
    });
  }
}
