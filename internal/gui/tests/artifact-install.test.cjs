// Run with Node's test runner and Playwright on the module path; see README.md.
// Install as app in magpie's viewer shell (internal/artifact/shell.js,
// app.go): the newest page registers its service worker, which keeps
// the window the shell once it answers it, and shows an
// install button when the browser offers to install it, and the button
// hands the offer to the browser. Library's Install as app opens the page
// with #install, which asks first, or says how in a browser with no
// offer. A pinned version does none of it. In both engines, at a narrow
// width, in every language. The real manifest, icons and worker, and a
// real install offer, are in the gateway's tests and the e2e run.
const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const dir = path.resolve(__dirname, "../../artifact");
const shellJS = fs.readFileSync(path.join(dir, "shell.js"), "utf8").replace(/\n+$/, "");
const shellCSS = fs.readFileSync(path.join(dir, "shell.css"), "utf8");
const shellCSPSource = /func ShellCSP\(nonce string\) string \{\n\treturn (".*")\n\}/.exec(fs.readFileSync(path.join(dir, "shell.go"), "utf8"));
assert.ok(shellCSPSource, "ShellCSP in shell.go");
function shellCSP(nonce) {
  return shellCSPSource[1].split(/" \+ nonce \+ "/).map((s) => s.replace(/^"|"$/g, "")).join(nonce);
}
// the real worker, from AppWorkerJS in app.go, its offline page a stand-in
const workerSource = /b\.WriteString\(`("use strict";\nconst OFF = )`\)[\s\S]*?b\.WriteString\(`(;\n[\s\S]*?)`\)\n\treturn b\.Bytes\(\)/.exec(fs.readFileSync(path.join(dir, "app.go"), "utf8"));
assert.ok(workerSource, "AppWorkerJS in app.go");
const worker = workerSource[1] + JSON.stringify("<!doctype html><title>off</title><h1>magpie isn't running</h1>") + workerSource[2];
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&#34;");
// the shell as ShellPage writes it, its app links included
function shellPage(cfg) {
  const nonce = crypto.randomBytes(18).toString("base64url");
  const json = JSON.stringify(cfg).replace(/</g, "\\u003c").replace(/>/g, "\\u003e").replace(/&/g, "\\u0026");
  const links = `<link rel="icon" href="${cfg.app}/app-icon-192.png">` + (cfg.pinned ? "" : `<link rel="manifest" href="${cfg.app}/app.webmanifest">`);
  const html = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name="referrer" content="no-referrer"><title>${esc(cfg.title)}</title>${links}<style nonce="${nonce}">${shellCSS}</style></head><body><iframe id="page" title="${esc(cfg.title)}" allow="clipboard-write; fullscreen"></iframe><div id="ui"></div><script nonce="${nonce}">${shellJS}(${json});</script></body></html>`;
  return { html, nonce };
}

// a fake magpie: the shell, a page, an empty comments store, and the
// app's files
async function fakeMagpie(t, lang, pinned) {
  const streams = new Set(), state = { down: false };
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    // magpie gone: nothing answers
    if (state.down) return req.socket.destroy();
    if (req.method === "GET" && url.pathname === "/a/x/") {
      if (req.headers["sec-fetch-dest"] === "document") {
        const { html, nonce } = shellPage({ api: "/a/x/_api", token: "stok", id: "x", title: "Export", src: "/a/x/", version: 2, pinned, origin: "http://" + req.headers.host, lang, app: "/a/x/_api" });
        res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": shellCSP(nonce), "Cache-Control": "no-store" });
        return res.end(html);
      }
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      return res.end("<!doctype html><title>Export</title><p>Export");
    }
    if (req.method === "GET" && url.pathname === "/a/x/_api/app-sw.js") {
      res.writeHead(200, { "Content-Type": "text/javascript", "Service-Worker-Allowed": "/a/x/", "Cache-Control": "no-cache" });
      return res.end(worker);
    }
    if (req.method === "GET" && url.pathname === "/a/x/_api/app.webmanifest") {
      res.writeHead(200, { "Content-Type": "application/manifest+json" });
      return res.end(JSON.stringify({ id: "/a/x/", name: "Export", start_url: "/a/x/", scope: "/a/x/", display: "standalone", icons: [] }));
    }
    if (req.method === "GET" && url.pathname === "/a/x/_api/shell") {
      res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store" });
      res.write("retry: 2000\n\n");
      streams.add(res);
      req.on("close", () => streams.delete(res));
      return;
    }
    if (req.method === "POST" && url.pathname === "/a/x/_api/comments") {
      req.resume();
      req.on("end", () => {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ threads: [], version: 2, agent: { can: "no_session" }, me: "u_0123456789abcdef01234567" }));
      });
      return;
    }
    res.writeHead(404);
    res.end();
  });
  t.after(() => { for (const s of streams) s.destroy(); server.close(); });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  return { base: `http://127.0.0.1:${server.address().port}`, state };
}

const launch = (engine) => (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
async function open(t, engine, opts) {
  opts = opts || {};
  const site = await fakeMagpie(t, opts.lang || "en", !!opts.pinned);
  const browser = await launch(engine);
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: opts.viewport || { width: 1200, height: 800 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(site.base + "/a/x/" + (opts.hash || ""));
  await page.waitForSelector(".bar");
  return { page, errors, site };
}
// the browser's offer to install, as Chromium makes it: prompt() counted
const offer = (page) => page.evaluate(() => {
  window.prompted = 0;
  const e = new Event("beforeinstallprompt", { cancelable: true });
  e.prompt = () => { window.prompted++; return Promise.resolve(); };
  dispatchEvent(e);
  return e.defaultPrevented;
});
const installBtn = (page) => page.locator('.bar button[aria-label="Install as app"]');
const ask = (page) => page.locator(".pop.save.in");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`the newest page registers its worker and offers the browser's install (${engine})`, async (t) => {
    const { page, errors, site } = await open(t, engine);
    const scope = await page.evaluate(async () => {
      if (!navigator.serviceWorker) return "none";
      const r = await navigator.serviceWorker.ready;
      return new URL(r.scope).pathname;
    });
    assert.equal(scope, "/a/x/");
    // the worker answers the window now, and it is still the shell, not
    // the page alone (a worker's own fetch goes as Sec-Fetch-Dest empty)
    await page.reload();
    await page.waitForSelector(".bar");
    assert.equal(await page.evaluate(() => !!navigator.serviceWorker.controller), true);
    assert.equal(await page.locator("iframe#page").count(), 1);
    // magpie gone, the window says so instead of the browser's error
    site.state.down = true;
    await page.reload().catch(() => {});
    assert.equal(await page.locator("h1").textContent(), "magpie isn't running");
    site.state.down = false;
    await page.reload();
    await page.waitForSelector(".bar");
    assert.equal(await installBtn(page).count(), 0, "no button before the browser offers");
    assert.equal(await offer(page), true, "the shell keeps the offer for its button");
    await installBtn(page).waitFor();
    await installBtn(page).click();
    assert.equal(await page.evaluate(() => window.prompted), 1);
    assert.equal(await installBtn(page).count(), 0, "an offer is used once");
    await offer(page);
    await installBtn(page).waitFor();
    await page.evaluate(() => dispatchEvent(new Event("appinstalled")));
    assert.equal(await installBtn(page).count(), 0, "installed, the button goes");
    assert.deepEqual(errors, []);
  });

  test(`a pinned version neither registers nor offers (${engine})`, async (t) => {
    const { page } = await open(t, engine, { pinned: true });
    await offer(page);
    await page.waitForTimeout(300);
    assert.equal(await installBtn(page).count(), 0);
    assert.equal(await page.evaluate(async () => navigator.serviceWorker ? (await navigator.serviceWorker.getRegistrations()).length : 0), 0);
    assert.equal(await page.locator('link[rel="manifest"]').count(), 0);
  });

  test(`#install asks first, and Install hands it to the browser (${engine})`, async (t) => {
    const { page, errors } = await open(t, engine, { hash: "#install" });
    assert.equal(new URL(page.url()).hash, "", "a reload doesn't ask again");
    await offer(page);
    await ask(page).waitFor();
    assert.equal(await ask(page).locator(".ask").textContent(), "Install “Export”?");
    assert.equal(await ask(page).locator(".size").textContent(), "It opens in its own window, always at its newest version.");
    await ask(page).getByRole("button", { name: "Install" }).click();
    assert.equal(await page.evaluate(() => window.prompted), 1);
    await page.waitForFunction(() => !document.querySelector(".pop.save"));
    assert.deepEqual(errors, []);
  });

  test(`#install with no offer says how in this browser, and Escape closes it (${engine})`, async (t) => {
    const { page } = await open(t, engine, { hash: "#install" });
    await ask(page).waitFor({ timeout: 6000 });
    const said = await ask(page).locator(".ask").textContent();
    assert.equal(said, engine === "webkit" ? "In Safari, choose File › Add to Dock." : "Use the app button in the address bar.");
    await page.keyboard.press("Escape");
    await page.waitForFunction(() => !document.querySelector(".pop.save"));
    // Cancel on the asking one
    // the page opened again, as Library opens it
    const at = page.url();
    await page.goto("about:blank");
    await page.goto(at + "#install");
    await page.waitForSelector(".bar");
    await offer(page);
    await ask(page).waitFor();
    await ask(page).getByRole("button", { name: "Cancel" }).click();
    await page.waitForFunction(() => !document.querySelector(".pop.save"));
    assert.equal(await page.evaluate(() => window.prompted), 0);
  });

  test(`the question fits a narrow window (${engine})`, async (t) => {
    const { page } = await open(t, engine, { hash: "#install", viewport: { width: 320, height: 560 } });
    await offer(page);
    await ask(page).waitFor();
    const box = await ask(page).boundingBox();
    assert.ok(box.x >= 0 && box.x + box.width <= 320, JSON.stringify(box));
  });

  const words = {
    zh: ["安装“Export”？", "它在自己的窗口里打开，总是最新版本。", "安装为应用"],
    "zh-TW": ["安裝「Export」？", "它在自己的視窗裡開啟，永遠是最新版本。", "安裝為應用程式"],
    ja: ["「Export」をインストールしますか？", "専用のウインドウで、常に最新のバージョンで開きます。", "アプリとしてインストール"],
    de: ["„Export“ installieren?", "Sie öffnet sich in einem eigenen Fenster, immer in der neuesten Version.", "Als App installieren"]
  };
  for (const [lang, [q, sub, btn]] of Object.entries(words)) {
    test(`the shell speaks ${lang} (${engine})`, async (t) => {
      const { page } = await open(t, engine, { lang, hash: "#install" });
      await offer(page);
      await ask(page).waitFor();
      assert.equal(await ask(page).locator(".ask").textContent(), q);
      assert.equal(await ask(page).locator(".size").textContent(), sub);
      await page.keyboard.press("Escape");
      await page.locator(`.bar button[aria-label="${btn}"]`).waitFor();
    });
  }
}
