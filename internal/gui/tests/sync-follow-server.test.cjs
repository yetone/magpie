// Run with Node's test runner and Playwright on the module path; see README.md.
// A computer can follow the server: take its setup and send none of its
// own (Ramon317317 on X: one computer the others all take theirs from).
// With sync on, the This computer row in the sync list shows Both ways
// picked, and says changes go both ways. Picking Follows the server posts
// follow-server on, and the row then says changes made here are replaced
// and kept. A notice from a sync that replaced a change made here says it
// was because this computer follows the server. Picking Both ways again
// posts it off. No click moves the page; nothing is wider than the window
// at a phone's width. In English and Chinese, Chromium and WebKit. The API
// is faked; nothing is synced.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posts) {
  const settings = {
    theme: "light", lang, tray: "panel", version: "test", dir: "/tmp/magpie",
    gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [],
    fx: { rate: 7.2, stale: false },
  };
  let sync = { on: true, kind: "webdav", url: "https://dav.jianguoyun.com/dav/", user: "me", passwordSet: true, passphraseSet: true,
    keys: true, agents: true, library: true, auto: 3, last: new Date().toISOString() };
  return async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    const json = (data) => route.fulfill({ json: data });
    if (pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (pathname === "/api/settings") return json(settings);
    if (pathname === "/api/usage/quotas") return json([]);
    if (pathname === "/api/groups") return json({ groups: [], models: [] });
    if (pathname === "/api/plugins") return json({ plugins: [] });
    if (pathname === "/api/davsync") return json(sync);
    if (pathname.startsWith("/api/davsync/")) {
      const action = pathname.slice("/api/davsync/".length);
      posts.push({ action, body: request.postDataJSON() });
      if (action === "follow-server") {
        const on = request.postDataJSON().on;
        sync = { ...sync, followServer: on || undefined,
          notice: on ? { at: new Date().toISOString(), here: ["providers"], followed: true, saved: "/tmp/magpie/sync" } : undefined };
      }
      return json(sync);
    }
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    row: "This computer", both: "Both ways", follow: "Follows the server",
    bothSub: "Changes go to the other computers, and theirs come here",
    followSub: "Takes the server's setup and sends none. A change made here is replaced and kept in the sync folder.",
    note: "Replaced by the server's, as this computer follows it: providers",
  },
  zh: {
    row: "本机", both: "双向同步", follow: "跟随服务器",
    bothSub: "本机的改动同步到其他电脑，其他电脑的改动也同步到本机",
    followSub: "只采用服务器上的配置，不上传本机的。本机的改动会被替换，副本保存在同步文件夹中。",
    note: "本机跟随服务器，本机改过的供应商已被服务器上的替换",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [900, 400]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: this computer can follow the server, and sync both ways again`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-sync-follow-server.png`), fullPage: true });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);

        await page.goto("http://magpie.test/");
        await page.locator("#prefs").click();
        await openPart(page, "sync");
        const list = page.locator("#syncList");
        const row = list.locator(".row.pref").filter({ has: page.locator(".sync-follows") });
        await row.waitFor({ state: "visible" });
        assert.equal(await row.locator(".name").textContent(), w.row);
        assert.equal(await row.locator(".sub").textContent(), w.bothSub);
        assert.equal(await row.locator(".segs .opt.on").textContent(), w.both);
        await row.scrollIntoViewIfNeeded();
        await page.waitForTimeout(300);
        const top = () => page.evaluate(() => document.querySelector("#view-settings").scrollTop);

        // follow: posted on, said, and a sync's notice says why
        let at = await top();
        await row.locator(".segs .opt", { hasText: w.follow }).click();
        await page.waitForFunction((s) => document.querySelector("#syncList .sub.sync-follows")?.textContent === s, w.followSub);
        assert.deepEqual(posts.at(-1), { action: "follow-server", body: { on: true } });
        assert.equal(await row.locator(".segs .opt.on").textContent(), w.follow);
        assert.equal(await list.locator(".sync-note .sub").first().textContent(), w.note);
        assert.equal(await top(), at, "picking Follows the server didn't move the page");
        const wide = await page.evaluate(() => {
          const r = document.querySelector("#syncList .sub.sync-follows").closest(".row").getBoundingClientRect();
          return r.right - document.documentElement.clientWidth;
        });
        assert.ok(wide <= 1, `the row is ${wide}px wider than the window`);

        // picked again: nothing posted
        const n = posts.length;
        await row.locator(".segs .opt", { hasText: w.follow }).click();
        await page.waitForTimeout(200);
        assert.equal(posts.length, n, "picking the choice already made posts nothing");

        // both ways again
        at = await top();
        await row.locator(".segs .opt", { hasText: w.both }).click();
        await page.waitForFunction((s) => document.querySelector("#syncList .sub.sync-follows")?.textContent === s, w.bothSub);
        assert.deepEqual(posts.at(-1), { action: "follow-server", body: { on: false } });
        assert.equal(await top(), at, "picking Both ways didn't move the page");
        assert.equal(await list.locator("select").count(), 0, "no native select");
        assert.deepEqual(errors, []);
      });
    }
  }
}
