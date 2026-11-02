// Run with Node's test runner and Playwright on the module path; see README.md.
// yapechan on Discord: after an import or a WebDAV sync no providers showed
// on the other computer, and nothing said why. A backup or a sync carries a
// subscription's settings, never its sign-in, so a subscription shows only
// once signed in to there. The sync's status and the import's result now
// name the subscriptions not signed in on this computer (by the name the
// Add sheet gives them), and the import says which plugins it added and
// which it couldn't. In English, Chinese, Japanese and German, Chromium and
// WebKit, at 900 and 440px. 悠悠哥 on Discord asked why sync said ChatGPT
// wasn't signed in: the line says the synced settings use it, and that one
// not used here needs nothing (zh-TW too, 1100 and 420px). The API is faked; nothing is synced.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, sync, imported) {
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
    if (pathname === "/api/backup/import") return json(imported);
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { sync: "Your synced settings use Claude, Cursor, which isn't signed in on this computer. Sync carries settings, not sign-ins", backup: "This backup's settings use Claude, Cursor, which isn't signed in on this computer. A backup carries settings, not sign-ins", added: "Plugins added: ", failed: "Couldn't add the plugin github:x/y: " },
  zh: { sync: "同步过来的设置用到了 Claude, Cursor，但本机尚未登录。同步只带设置", backup: "这份备份的设置用到了 Claude, Cursor，但本机尚未登录。备份只带设置", added: "已添加插件：", failed: "无法添加插件 github:x/y：" },
  "zh-TW": { sync: "同步過來的設定用到了 Claude, Cursor，但本機尚未登入。同步只帶設定", backup: "這份備份的設定用到了 Claude, Cursor，但本機尚未登入。備份只帶設定", added: null, failed: null },
  ja: { sync: "同期した設定は Claude, Cursor を使っていますが、このコンピュータではまだサインインしていません。", backup: "このバックアップの設定は Claude, Cursor を使っていますが、このコンピュータではまだサインインしていません。", added: "追加したプラグイン：", failed: "プラグイン github:x/y を追加できませんでした：" },
  de: { sync: "Deine synchronisierten Einstellungen nutzen Claude, Cursor, auf diesem Computer aber nicht angemeldet.", backup: "Die Einstellungen dieser Sicherung nutzen Claude, Cursor, auf diesem Computer aber nicht angemeldet.", added: "Hinzugefügte Erweiterungen: ", failed: "Die Erweiterung github:x/y konnte nicht hinzugefügt werden: " },
};

const base = { on: true, kind: "webdav", url: "https://dav.example.invalid/dav/", user: "me", passwordSet: true, passphraseSet: true, keys: true, agents: true, library: true, usage: true, last: new Date(Date.now() - 60000).toISOString() };
const imported = { Added: 3, Replaced: 0, Profiles: 0, Agents: 0, NeedKey: ["Acme"], SignIn: ["claude", "cursor"], Plugins: ["@magpie-community/opencode-cursor-auth"], PluginErrors: { "github:x/y": "no such repository" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 900, 440, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: the subscriptions to sign in to here are named`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const signIn of [["claude", "cursor"], undefined]) {
          const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, { ...base, signIn }, imported));
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          await page.goto("http://magpie.test/");
          await page.locator("#prefs").click();
          await openPart(page, "sync");
          const first = page.locator("#syncList .row.pref").first();
          await first.waitFor({ state: "visible" });
          const line = first.locator(".sync-sign-in");
          if (!signIn) {
            assert.equal(await line.count(), 0, "no line when every subscription is signed in");
            assert.deepEqual(errors, []);
            await page.close();
            continue;
          }
          assert.deepEqual(errors, [], "no page errors");
          assert.equal(await line.count(), 1, "the sync names what to sign in to");
          const said = await line.textContent();
          assert.ok(said.startsWith(w.sync), said);
          const fit = await line.evaluate((e) => ({ sw: e.scrollWidth, cw: e.clientWidth, right: e.getBoundingClientRect().right }));
          assert.ok(fit.sw <= fit.cw + 1 && fit.right <= width, `the line is shown whole: ${JSON.stringify(fit)}`);

          // the import's result: Import is the last of Sync's own rows,
          // Back up by itself's come after it
          const open = page.locator("#syncList .row.pref:not(.auto-backup)").last().locator("button");
          await reach(page, open);
          await open.click();
          const form = page.locator("#syncList .sync-form");
          await form.locator("input[type=file]").setInputFiles({ name: "magpie.magpie-backup", mimeType: "application/octet-stream", buffer: Buffer.from("sealed") });
          await form.locator("input[type=password]").fill("correct horse");
          // at 440px in German the form's foot is under the window's
          const go = form.locator("button.primary");
          await reach(page, go);
          await go.click();
          const done = form.locator(".done");
          await done.first().waitFor({ state: "visible" });
          const lines = await done.allTextContents();
          assert.ok(lines.some((l) => l.startsWith(w.backup)), `import names what to sign in to: ${lines.join(" | ")}`);
          // zh-TW: the plugin lines' own words aren't this test's; their presence is
          if (w.added) assert.ok(lines.includes(w.added + "@magpie-community/opencode-cursor-auth"), lines.join(" | "));
          else assert.ok(lines.some((l) => l.endsWith("@magpie-community/opencode-cursor-auth")), lines.join(" | "));
          if (w.failed) assert.ok(lines.includes(w.failed + "no such repository"), lines.join(" | "));
          else assert.ok(lines.some((l) => l.endsWith("no such repository")), lines.join(" | "));
          assert.ok(!lines.some((l) => /: ,|：,|:\s*$/.test(l)), `no empty name: ${lines.join(" | ")}`);
          const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          assert.ok(over <= 0, `no sideways scroll: ${over}`);
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.locator("#syncList").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-sync-sign-in.png`) });
          }
          assert.deepEqual(errors, []);
          await page.close();
        }
      });
    }
  }
}
