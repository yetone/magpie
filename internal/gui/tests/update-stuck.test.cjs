// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → Version and the Update pill, for a magpie that can't replace
// itself where it runs: a magpie.exe kept at C:\ (#1277) said only that a new
// version was out, and its Download opened the release page with nothing
// saying why. Both now say the folder magpie may not write to, in English,
// Chinese, Japanese and German, at a narrow window too. In a container (the
// Docker image's /magpie, run as nonroot) they say to pull the new
// version's image, by its own tag, rather than to move magpie to another
// folder. No backend, the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const DIR = "C:\\";

const words = {
  en: { version: "Version", why: "magpie can't write to the folder it runs from (C:\\), so it can't update itself; move it to a folder you can write to and open it from there." },
  zh: { version: "版本", why: "magpie 无法写入它所在的文件夹（C:\\），所以无法自动更新；请把它移到你有写入权限的文件夹，再从那里打开。" },
  ja: { why: "magpie は実行中のフォルダ（C:\\）に書き込めないため、自動アップデートできません。書き込み可能なフォルダに移動し、そこから開いてください。" },
  de: { why: "magpie kann nicht in den Ordner schreiben, aus dem es läuft (C:\\), und kann sich daher nicht selbst aktualisieren; verschieben Sie es in einen Ordner mit Schreibrechten und öffnen Sie es von dort." },
};

const folder = { state: "available", current: "0.1.400", latest: "0.1.401", url: "https://github.com/yetone/magpie-releases/releases/tag/v0.1.401", stuck: "not-writable", stuckDir: DIR };
// on the Daily channel :latest held another release than the one out
// (fishmagic80 on X): the hint names the version's own image
const container = { state: "available", current: "0.1.400", latest: "0.1.401", channel: "daily", url: "https://github.com/yetone/magpie-releases/releases/tag/v0.1.401", stuck: "container", image: "ghcr.io/yetone/magpie:0.1.401" };
const pull = {
  en: "magpie runs in a container, so it can't update itself; pull the image of 0.1.401 (docker pull ghcr.io/yetone/magpie:0.1.401) and recreate the container from it.",
  zh: "magpie 运行在容器里，无法自动更新；请拉取 0.1.401 的镜像（docker pull ghcr.io/yetone/magpie:0.1.401），并用它重新创建容器。",
  "zh-TW": "magpie 在容器中執行，無法自動更新；請拉取 0.1.401 的映像（docker pull ghcr.io/yetone/magpie:0.1.401），並用它重新建立容器。",
  ja: "magpie はコンテナで実行されているため、自動アップデートできません。0.1.401 のイメージを取得し（docker pull ghcr.io/yetone/magpie:0.1.401）、そのイメージでコンテナを作り直してください。",
  de: "magpie läuft in einem Container und kann sich daher nicht selbst aktualisieren; laden Sie das Image von 0.1.401 (docker pull ghcr.io/yetone/magpie:0.1.401) und erstellen Sie den Container damit neu.",
};
const cases = [
  { name: "a magpie that can't write to its folder says why it can't update", update: folder, langs: ["en", "zh", "ja", "de"], why: (lang) => words[lang].why },
  { name: "a magpie in a container says to pull the new image", update: container, langs: ["en", "zh", "zh-TW", "ja", "de"], why: (lang) => pull[lang] },
];

function server(lang, ctl, update) {
  const settings = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3999",
    proxyNow: "none", proxySource: "none", login: false, redactWords: [],
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/update" || url.pathname === "/api/update/check") return json({ ...update });
    if (url.pathname === "/api/update/install") { ctl.installs++; return json({ ...update }); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const c of cases) test(engine + ": " + c.name, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of c.langs) {
      for (const width of [1000, 440]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const ctl = { installs: 0 };
          const errors = [];
          const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
          await page.route("**/*", server(lang, ctl, c.update));
          await page.goto("http://magpie.test/?view=settings&tab=about");
          const want = c.why(lang);
          // the Version row: the version out, then why it can't be put in here
          // (the row is drawn again as the answer is read, so it is found
          // and read in one go)
          const sub = await (await page.waitForFunction((w) => [...document.querySelectorAll("#about .row.pref .sub")].find((s) => s.textContent.includes(w))?.textContent, want)).jsonValue();
          assert.ok(sub.includes("0.1.401"), sub);
          // the pill's tooltip says it too, before the click opens the page
          await page.waitForFunction(() => !document.querySelector("#update")?.hidden);
          const title = await page.locator("#update").getAttribute("title");
          assert.ok(title.startsWith(want), title);
          // nothing is cut off or pushes the page sideways at a narrow width
          const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
          assert.deepEqual(errors, []);
          await page.close();
        });
      }
    }
  });
}

// A magpie a package manager installed (the AUR's magpie-bin, shuhai on
// Discord) leaves its updates to the manager: the Version row says the
// version is out and the command that updates the package, with no
// Download (the release's binary isn't for this install) and no Update
// pill (the package can come days after the release).
const packaged = { state: "available", current: "0.1.400", latest: "0.1.401", url: "https://github.com/yetone/magpie-releases/releases/tag/v0.1.401", stuck: "package", packaged: { manager: "pacman", package: "magpie-bin", command: "yay -S magpie-bin" } };
const byManager = {
  en: "pacman installed magpie (magpie-bin), so pacman updates it, not magpie: yay -S magpie-bin",
  zh: "pacman 安装了 magpie（magpie-bin），所以由 pacman 更新它，而不是 magpie 自己：yay -S magpie-bin",
  "zh-TW": "pacman 安裝了 magpie（magpie-bin），所以由 pacman 更新它，而不是 magpie 自己：yay -S magpie-bin",
  ja: "magpie は pacman でインストールされている（magpie-bin）ため、magpie ではなく pacman がアップデートします：yay -S magpie-bin",
  de: "pacman hat magpie installiert (magpie-bin), daher aktualisiert pacman es, nicht magpie: yay -S magpie-bin",
};
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a packaged magpie names its package manager's command", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1000, 440]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const ctl = { installs: 0 };
          const errors = [];
          const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
          await page.route("**/*", server(lang, ctl, packaged));
          await page.goto("http://magpie.test/?view=settings&tab=about");
          const want = byManager[lang];
          const sub = await (await page.waitForFunction((w) => [...document.querySelectorAll("#about .row.pref .sub")].find((s) => s.textContent.includes(w))?.textContent, want)).jsonValue();
          assert.ok(sub.includes("0.1.401"), sub);
          const row = page.locator("#about .row.pref", { has: page.locator(".sub", { hasText: want }) });
          const buttons = await row.locator("button").allTextContents();
          const download = { en: "Download", zh: "下载", "zh-TW": "下載", ja: "ダウンロード", de: "Herunterladen" }[lang];
          assert.ok(!buttons.some((b) => b.trim() === download), buttons.join("|"));
          // no Update pill, once the pill has read the update too (a fetch
          // of /api/update beside the row's)
          await page.waitForFunction(() => performance.getEntriesByType("resource").filter((e) => new URL(e.name).pathname === "/api/update").length >= 2);
          await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
          assert.equal(await page.locator("#update").isHidden(), true);
          const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
          assert.equal(ctl.installs, 0);
          assert.deepEqual(errors, []);
          await page.close();
        });
      }
    }
  });
}
