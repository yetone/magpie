// Run with Node's test runner and Playwright on the module path; see README.md.
// #5715 (JJasonsun on Discord): a library past a sync's size limit synced
// "fine" while its big skills never reached the other computer. Settings ›
// Sync now says it in red under the status: the files too big to go, and
// the skills a sync couldn't fetch from their source here, each on a line
// of its own, wrapped whole. A sync that failed says only that, and one
// with nothing left out has no such line. Library › Skills tags a skill
// not fetched yet "Not fetched yet", its tip saying Update tries again. In
// English, Chinese, Japanese and German, Chromium and WebKit, at 900 and
// 440px. The API is faked; nothing is synced.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/jason";
const FETCH = "pdf: not fetched from its source on this computer yet (Update tries again): git isn't installed";

const library = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills` }],
  instructions: { agents: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], newSkills: [], checkDue: false,
  skills: [
    { name: "pdf", description: "PDFs", kind: "github", source: "https://github.com/anthropics/skills/tree/HEAD/skills/pdf", agents: ["claude"], pending: true },
    { name: "docx", description: "Word files", kind: "github", source: "https://github.com/anthropics/skills/tree/HEAD/skills/docx", agents: ["claude"] },
  ],
};

function serve(lang, sync) {
  const settings = {
    theme: "light", lang, tray: "panel", version: "test", dir: "/tmp/magpie",
    gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [],
    fx: { rate: 7.2, stale: false },
  };
  return async (route) => {
    const pathname = new URL(route.request().url()).pathname;
    const json = (data) => route.fulfill({ json: data });
    if (pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (pathname === "/api/settings") return json(settings);
    if (pathname === "/api/usage/quotas") return json([]);
    if (pathname === "/api/groups") return json({ groups: [], models: [] });
    if (pathname === "/api/plugins") return json({ plugins: [] });
    if (pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (pathname === "/api/davsync") return json(sync);
    if (pathname === "/api/library") return json(library);
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { left: "Files too big to sync stay on this computer only, in: mine, big-notes", tag: "Not fetched yet", tip: "Not fetched on this computer yet: Update tries again" },
  zh: { left: "太大无法同步的文件只留在这台电脑上，位于：mine、big-notes", tag: "尚未获取", tip: "尚未在这台电脑上获取：点「更新」重试" },
  ja: { left: "大きすぎて同期できないファイルはこのコンピューターにだけ残ります：mine、big-notes", tag: "未取得", tip: "このコンピューターではまだ取得していません：「アップデート」で再試行" },
  de: { left: "Zu große Dateien zum Synchronisieren bleiben nur auf diesem Computer, in: mine, big-notes", tag: "Noch nicht geladen", tip: "Auf diesem Computer noch nicht geladen: „Aktualisieren“ versucht es erneut" },
};

const base = { on: true, kind: "webdav", url: "https://dav.jianguoyun.com/dav/", user: "me", passwordSet: true, passphraseSet: true, keys: true, agents: true, library: true, usage: true };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 440]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: what a sync left out of the library is said`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const last = new Date(Date.now() - 60000).toISOString();
        for (const c of [
          { name: "left out", sync: { ...base, last, libraryLeft: ["mine", "big-notes"], libraryProblems: [FETCH] }, want: [w.left, FETCH] },
          { name: "not fetched", sync: { ...base, last, libraryProblems: [FETCH] }, want: [FETCH] },
          { name: "all synced", sync: { ...base, last }, want: [] },
          { name: "sync failed", sync: { ...base, error: "the WebDAV server refused the user name or password (HTTP 401)", libraryLeft: ["mine"] }, want: [] },
        ]) {
          const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, c.sync));
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          await page.goto("http://magpie.test/");
          await page.locator("#prefs").click();
          await openPart(page, "sync");
          await page.locator("#syncList .sync-preview-row").waitFor({ state: "visible" });
          const line = page.locator("#syncList .sync-lib");
          if (!c.want.length) {
            assert.equal(await line.count(), 0, `${c.name}: no library line`);
          } else {
            assert.equal(await line.count(), 1, `${c.name}: one library line`);
            assert.deepEqual(await line.locator(":scope > div").allTextContents(), c.want, c.name);
            const [color, red] = await page.evaluate(() => {
              const probe = document.createElement("i");
              probe.style.color = "var(--red)";
              document.body.append(probe);
              const r = getComputedStyle(probe).color;
              probe.remove();
              return [getComputedStyle(document.querySelector("#syncList .sync-lib")).color, r];
            });
            assert.equal(color, red, `${c.name}: the line is red`);
            const fit = await line.evaluate((e) => ({ sw: e.scrollWidth, cw: e.clientWidth, right: e.getBoundingClientRect().right }));
            assert.ok(fit.sw <= fit.cw + 1, `${c.name}: not cut off: ${JSON.stringify(fit)}`);
            assert.ok(fit.right <= width, `${c.name}: inside the window: ${JSON.stringify(fit)}`);
            const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
            assert.ok(over <= 0, `no sideways scroll: ${over}`);
            if (process.env.ARTIFACT_DIR && c.name === "left out") {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              await page.locator("#syncList").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-sync-library-left.png`) });
            }
          }
          if (c.name === "left out") {
            // the skill not fetched yet is tagged in Library › Skills
            await page.evaluate(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
            await page.goto("http://magpie.test/");
            await page.locator('button[data-view="library"]').click();
            const v = page.locator("#view-library");
            const tag = v.locator(".lib-skill .lib-tag.lib-pending");
            await tag.first().waitFor();
            assert.equal(await tag.count(), 1, "only pdf is tagged");
            const row = v.locator(".lib-skill", { has: page.locator(".lib-tag.lib-pending") });
            assert.match(await row.locator(".name").first().textContent(), /^pdf/);
            assert.equal((await tag.textContent()).trim(), w.tag);
            assert.equal(await tag.getAttribute("title"), w.tip);
          }
          assert.deepEqual(errors, []);
          await page.close();
        }
      });
    }
  }
}
