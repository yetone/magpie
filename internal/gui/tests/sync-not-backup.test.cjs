// Run with Node's test runner and Playwright on the module path; see README.md.
// JasonLeeForOnly on Discord: WebDAV sync said "not a magpie backup" for
// versions, and nothing on the page got past it. When the server holds an
// empty, cut-short or other app's file where the backup should be, the
// status says so in the reader's language and an Upload row offers this
// computer's setup in its place, asked first; a web page answered instead
// of the file is said with its title and redirect, and offers no upload.
// In English, Chinese, Traditional Chinese, Japanese and German, Chromium
// and WebKit, at 900 and 440px. The API is faked; nothing is synced.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const EN = "the file magpie/magpie.magpie-backup on the WebDAV server is empty (0 bytes): a write to it didn't finish.";

function serve(lang, state) {
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
    if (pathname === "/api/davsync") return json(state.sync);
    if (pathname === "/api/davsync/upload") {
      state.uploads++;
      state.sync = { ...base, last: new Date().toISOString() };
      return json(state.sync);
    }
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { empty: "the file on the server is empty (0 bytes)", cut: "cut short (5.4 KB)", upload: "Upload…", go: "Upload this computer's setup", page: "web page (“Nextcloud”)", synced: "Synced" },
  zh: { empty: "服务器上的文件是空的（0 字节）", cut: "不完整的 magpie 备份（5.4 KB）", upload: "上传…", go: "上传本机的配置", page: "网页（“Nextcloud”）", synced: "已同步" },
  "zh-TW": { empty: "伺服器上的檔案是空的（0 位元組）", cut: "不完整的 magpie 備份（5.4 KB）", upload: "上傳…", go: "上傳本機的設定", page: "網頁（「Nextcloud」）", synced: null },
  ja: { empty: "サーバー上のファイルが空です（0 バイト）", cut: "途中で切れた magpie バックアップです（5.4 KB）", upload: "アップロード…", go: "このコンピューターの設定をアップロード", page: "Web ページ（「Nextcloud」）", synced: null },
  de: { empty: "die Datei auf dem Server ist leer (0 Byte)", cut: "abgeschnittenes magpie-Backup (5.4 KB)", upload: "Hochladen…", go: "Einrichtung dieses Computers hochladen", page: "Webseite („Nextcloud“)", synced: null },
};

const base = { on: true, kind: "webdav", url: "https://dav.jianguoyun.com/dav/", user: "me", passwordSet: true, passphraseSet: true, keys: true, agents: true, library: true, auto: 3 };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 440]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a server file that isn't a backup is said, and can be replaced`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const c of [
          { name: "empty", sync: { ...base, error: EN, serverFile: { what: "empty", size: 0, replace: true } }, said: [w.empty], upload: true },
          { name: "cut", sync: { ...base, error: EN, serverFile: { what: "cut", size: 5432, replace: true } }, said: [w.cut], upload: true },
          { name: "page", sync: { ...base, error: EN, serverFile: { what: "page", size: 900, title: "Nextcloud", moved: "cloud.example.com/login" } }, said: [w.page, "cloud.example.com/login"], upload: false },
          { name: "plain error", sync: { ...base, error: "the WebDAV server refused the user name or password (HTTP 401)" }, said: ["HTTP 401"], upload: false },
        ]) {
          const state = { sync: c.sync, uploads: 0 };
          const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, state));
          page.setDefaultTimeout(5000);
          await page.goto("http://magpie.test/");
          await page.locator("#prefs").click();
          await page.locator("#setTab-sync").click();
          const first = page.locator("#syncList .row.pref").first();
          await first.waitFor({ state: "visible" });
          const said = await first.locator(".who > .sub").first().textContent();
          for (const s of c.said) assert.ok(said.includes(s), `${c.name}: ${JSON.stringify(s)} in ${said}`);
          const whole = await first.locator(".who > .sub").first().evaluate((e) => ({ sw: e.scrollWidth, cw: e.clientWidth, right: e.getBoundingClientRect().right }));
          assert.ok(whole.sw <= whole.cw + 1 && whole.right <= width, `${c.name}: the reason is shown whole: ${JSON.stringify(whole)}`);
          if (c.sync.serverFile && lang !== "en") assert.ok(!said.includes("magpie.magpie-backup on the WebDAV"), `${c.name}: said in ${lang}, not the English error: ${said}`);
          const up = page.locator("#syncList button", { hasText: w.upload });
          assert.equal(await up.count(), c.upload ? 1 : 0, `${c.name}: upload offered`);
          if (c.upload) {
            const top = () => page.evaluate(() => document.querySelector("#view-settings").scrollTop);
            const before = await top();
            await up.click();
            const ask = page.locator(".upload-ask");
            await ask.waitFor({ state: "visible" });
            assert.equal(state.uploads, 0, "nothing is uploaded before it is confirmed");
            assert.equal(await ask.locator(".lib-confirm").count(), c.name === "cut" ? 3 : 2, `${c.name}: what a cut file loses is said only for it`);
            const fit = await ask.evaluate((e) => e.getBoundingClientRect().right);
            assert.ok(fit <= width, `the ask is inside the window: ${fit}`);
            if (process.env.ARTIFACT_DIR) {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${c.name}-upload-ask.png`) });
            }
            await ask.locator("button", { hasText: w.go }).click();
            await ask.waitFor({ state: "detached" });
            assert.equal(state.uploads, 1, "the upload was asked for");
            const after = await first.locator(".who > .sub").first().textContent();
            if (w.synced) assert.ok(after.includes(w.synced), `synced after: ${after}`);
            assert.equal(await up.count(), 0, "no upload row once synced");
            assert.equal(await top(), before, "nothing scrolled");
          }
          const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          assert.ok(over <= 0, `no sideways scroll: ${over}`);
          assert.deepEqual(errors, []);
          await page.close();
        }
      });
    }
  }
}
