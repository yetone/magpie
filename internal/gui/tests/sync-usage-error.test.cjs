// Run with Node's test runner and Playwright on the module path; see README.md.
// #1259: a sync whose usage couldn't be shared read "已同步 · dav.jianguoyun.com
// · 用量未能共享：…" in one grey line, as if the whole sync went fine. The
// usage failure is now a line of its own under the status, in red, wrapped
// whole at 440px; the status line names only the sync. Without a usage
// error there is no such line, and a sync that failed says only that. In
// English, Chinese, Japanese and German, Chromium and WebKit, at 900 and
// 440px. The API is faked; nothing is synced.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const CUT = "the WebDAV server kept 0 of 6869 bytes of 5f0c-2026-08-29.magpie-usage: the write was cut short — sync again, and if it keeps happening the network to the server is dropping long uploads";

function serve(lang, sync) {
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
    if (pathname === "/api/davsync") return json(sync);
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { usage: "Couldn't share usage: ", sync: "Couldn't sync: " },
  zh: { usage: "用量未能共享：", sync: "同步失败：" },
  ja: { usage: "使用量を共有できませんでした：", sync: null },
  de: { usage: "Nutzung konnte nicht geteilt werden: ", sync: null },
};

const base = { on: true, kind: "webdav", url: "https://dav.jianguoyun.com/dav/", user: "me", passwordSet: true, passphraseSet: true, keys: true, agents: true, library: true, usage: true };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 440]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: usage that couldn't be shared is its own red line`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const c of [
          { name: "usage failed", sync: { ...base, last: new Date(Date.now() - 60000).toISOString(), usageError: CUT } },
          { name: "all shared", sync: { ...base, last: new Date(Date.now() - 60000).toISOString() } },
          { name: "sync failed", sync: { ...base, error: "the WebDAV server refused the user name or password (HTTP 401)", usageError: CUT } },
        ]) {
          const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, c.sync));
          page.setDefaultTimeout(5000);
          await page.goto("http://magpie.test/");
          await page.locator("#prefs").click();
          await page.locator("#setTab-sync").click();
          const first = page.locator("#syncList .row.pref").first();
          await first.waitFor({ state: "visible" });
          const status = first.locator(".who > .sub").first();
          const line = first.locator(".sync-usage-err");
          const said = await status.textContent();
          assert.ok(!said.includes("6869"), `${c.name}: the status line carries no usage error: ${said}`);
          if (c.name === "usage failed") {
            assert.equal(await line.count(), 1, "the usage failure has a line");
            assert.equal(await line.textContent(), w.usage + CUT);
            const [color, statusColor, red] = await page.evaluate(() => {
              const l = document.querySelector("#syncList .sync-usage-err");
              const s = l.parentElement.querySelector(".sub");
              const probe = document.createElement("i");
              probe.style.color = "var(--red)";
              document.body.append(probe);
              const r = getComputedStyle(probe).color;
              probe.remove();
              return [getComputedStyle(l).color, getComputedStyle(s).color, r];
            });
            assert.equal(color, red, "the usage line is red");
            assert.notEqual(statusColor, red, "the status beside it isn't");
            // shown whole: wrapped, not cut off, and inside the window
            const fit = await line.evaluate((e) => ({ sw: e.scrollWidth, cw: e.clientWidth, right: e.getBoundingClientRect().right, h: e.getBoundingClientRect().height }));
            assert.ok(fit.sw <= fit.cw + 1, `the line isn't cut off: ${JSON.stringify(fit)}`);
            assert.ok(fit.right <= width, `the line is inside the window: ${JSON.stringify(fit)}`);
            assert.ok(fit.h > 0);
            const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
            assert.ok(over <= 0, `no sideways scroll: ${over}`);
            if (process.env.ARTIFACT_DIR) {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              await first.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-sync-usage-error.png`) });
            }
          } else {
            assert.equal(await line.count(), 0, `${c.name}: no usage line`);
            if (c.name === "sync failed" && w.sync) assert.ok(said.startsWith(w.sync), said);
          }
          assert.deepEqual(errors, []);
          await page.close();
        }
      });
    }
  }
}
