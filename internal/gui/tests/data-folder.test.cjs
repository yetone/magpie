// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Sync's Data folder (CHENGJIAHEGOXIS and buhaodeshiyehao on X:
// magpie's files on another disk). The row shows where magpie's files are,
// with Show and Move…; Move… says what a move does in the app's own dialog
// before anything is picked, Cancel posts nothing, and Choose a folder…
// posts the move, the button then saying magpie restarts. A move that
// can't restart (another magpie serving) says it waits, with Cancel; once
// moved, the row shows the folder on the disk and Move back, and a move
// that failed says why. In all five languages, at 1100 and 420px,
// Chromium and WebKit, nothing wider than the window. The API is faked;
// nothing is moved.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart } = require("./reader.cjs");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const tr = (lang, k, vars = {}) => {
  const s = lang === "en" ? k : I18N[lang][k];
  assert.equal(typeof s, "string", `${lang} has ${JSON.stringify(k)}`);
  return s.replace(/\{(\w+)\}/g, (_, v) => vars[v]);
};

const home = { config: "~/.config/magpie", cache: "~/.cache/magpie" };
const disk = "/Volumes/Samsung T7 Shield with a long name to wrap/magpie";

function serve(lang, posts, state) {
  const settings = {
    theme: "light", lang, tray: "panel", version: "test", dir: "/tmp/magpie",
    gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [],
    fx: { rate: 7.2, stale: false },
  };
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
    if (pathname === "/api/davsync") return json({ on: false });
    if (pathname === "/api/autobackup") return json({ on: false, hours: 24, keep: 7, dir: "~/backups", backups: [] });
    if (pathname === "/api/datafolder") return json(state.view);
    if (pathname.startsWith("/api/datafolder/")) {
      const action = pathname.slice("/api/datafolder/".length);
      posts.push(action);
      if (action === "move") {
        state.view = { ...state.view, pending: disk, other: state.other };
        return json({ view: state.view, restart: !state.other });
      }
      if (action === "cancel") state.view = { ...state.view, pending: undefined, other: undefined };
      return json({ view: state.view });
    }
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const w = (k, vars) => tr(lang, k, vars);
      test(`${engine} ${width}px ${lang}: Data folder moves after saying so, and says where it is`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        const errors = [], posts = [];
        const state = { view: { ...home }, other: false };
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts, state));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-data-folder.png`), fullPage: true });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);

        await page.goto("http://magpie.test/");
        await page.locator("#prefs").click();
        await openPart(page, "sync");
        const list = page.locator("#syncList");
        const row = list.locator(".row.pref.data-folder");
        await row.waitFor({ state: "visible" });
        // the app opened again on the server's view now
        const reopen = async () => {
          await page.reload();
          await page.locator("#prefs").click();
          await openPart(page, "sync");
          await row.waitFor({ state: "visible" });
        };
        assert.equal(await row.locator(".name").textContent(), w("Data folder"));
        assert.equal(await row.locator(".data-folder-dir").textContent(), home.config);
        for (const b of ["Show", "Move…"]) await row.locator("button", { hasText: w(b) }).waitFor();
        assert.equal(await row.locator("button", { hasText: w("Move back") }).count(), 0, "nothing to move back from");

        // Move… says what it does first; Cancel posts nothing
        await row.locator("button", { hasText: w("Move…") }).click();
        const ask = page.locator(".data-folder-ask");
        await ask.waitFor({ state: "visible" });
        assert.equal(await ask.locator(".ehead b").textContent(), w("Move magpie's files to another disk?"));
        await ask.locator("button", { hasText: w("Cancel") }).click();
        await ask.waitFor({ state: "detached" });
        assert.deepEqual(posts, []);

        // Choose a folder… moves: magpie restarts for it
        await row.locator("button", { hasText: w("Move…") }).click();
        await ask.locator("button", { hasText: w("Choose a folder…") }).click();
        await page.waitForFunction((s) => document.querySelector(".data-folder-ask .primary")?.textContent === s, w("Restarting…"));
        assert.deepEqual(posts, ["move"]);
        await page.evaluate(() => closeConfirmAsk());

        // another magpie serving: the move waits, said, with Cancel
        state.view = { ...home };
        state.other = true;
        await reopen();
        await row.locator("button", { hasText: w("Move…") }).click();
        await ask.locator("button", { hasText: w("Choose a folder…") }).click();
        await ask.waitFor({ state: "detached" });
        await page.waitForFunction(() => document.querySelectorAll("#syncList .data-folder-said").length === 2);
        assert.deepEqual(await row.locator(".data-folder-said").allTextContents(), [
          w("Moves to {dir} when magpie starts again", { dir: disk }),
          w("Another magpie serves the gateway (magpie serve): stop it, then open magpie again to move them"),
        ]);
        assert.equal(await row.locator("button", { hasText: w("Move…") }).count(), 0, "no second move while one waits");
        await row.locator("button", { hasText: w("Cancel") }).click();
        await page.waitForFunction(() => document.querySelectorAll("#syncList .data-folder-said").length === 0);
        assert.deepEqual(posts, ["move", "move", "cancel"]);

        // moved: the folder on the disk, and Move back, which asks too;
        // the last failure said under it
        state.view = { ...home, configAt: disk + "/config", cacheAt: disk + "/cache", moved: disk, error: "/Volumes/X/magpie has files in it already: pick another folder, or empty it" };
        await reopen();
        assert.equal(await row.locator(".data-folder-dir").textContent(), disk);
        const bad = row.locator(".data-folder-said.bad");
        assert.equal(await bad.textContent(), w("The last move wasn't made: {error}", { error: state.view.error }));
        await row.locator("button", { hasText: w("Move back") }).click();
        await ask.waitFor({ state: "visible" });
        assert.equal(await ask.locator(".ehead b").textContent(), w("Move magpie's files back to this computer?"));
        assert.match(await ask.textContent(), new RegExp(home.config.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
        await ask.locator("button", { hasText: w("Cancel") }).click();
        await ask.waitFor({ state: "detached" });

        // nothing wider than the window, the long folder wrapping
        const wide = await page.evaluate(() => {
          const out = [];
          if (document.scrollingElement.scrollWidth > innerWidth) out.push("page " + document.scrollingElement.scrollWidth);
          for (const r of document.querySelectorAll("#syncList .row.pref")) {
            const b = r.getBoundingClientRect();
            if (b.right > innerWidth + 0.5 || r.scrollWidth > r.clientWidth + 1) out.push(r.querySelector(".name")?.textContent + " " + b.right + "/" + r.scrollWidth + ">" + r.clientWidth);
          }
          return out;
        });
        assert.deepEqual(wide, [], "rows wider than the window");
        assert.deepEqual(errors, []);
      });
    }
  }
}
