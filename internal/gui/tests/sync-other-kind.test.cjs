// Run with Node's test runner and Playwright on the module path; see README.md.
// Moving sync between WebDAV and S3 keeps the other's settings (ARNO on
// Discord: trying S3 wiped the WebDAV address, user and password). With S3
// synced to and a WebDAV server kept, the row says the WebDAV settings are
// kept; the form marks S3 as the one on and says so; picking WebDAV shows
// the address and user kept, its password saved, says saving moves sync
// there, and its button says so; picking moves nothing on the page. Saving
// posts the WebDAV server, the password left empty for the one kept, and
// the row then says WebDAV sync, S3's settings kept. In English and
// Chinese, Chromium and WebKit. The API is faked; nothing is synced.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const DAV = { kind: "webdav", url: "https://dav.example.com/dav/", user: "me", passwordSet: true };
const S3 = { kind: "s3", url: "s3://bkt/team", user: "AKID", endpoint: "https://acct.r2.cloudflarestorage.com", region: "auto", passwordSet: true };

function serve(lang, posts) {
  const settings = {
    theme: "light", lang, tray: "panel", version: "test", dir: "/tmp/magpie",
    gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [],
    fx: { rate: 7.2, stale: false },
  };
  let sync = { on: true, ...S3, passphraseSet: true, keys: true, agents: true, library: true, other: DAV };
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
    if (pathname === "/api/davsync/save") {
      const b = request.postDataJSON();
      posts.push(b);
      // as the server does: the S3 bucket moved from is kept
      sync = { on: true, kind: "webdav", url: b.url, user: b.user, passwordSet: true, passphraseSet: true,
        keys: b.keys, agents: b.agents, library: b.library, last: new Date().toISOString(), other: S3 };
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
    s3Row: "S3 sync", davRow: "WebDAV sync", davKept: "WebDAV settings kept", s3Kept: "S3 settings kept",
    s3On: "S3 · on", here: "Syncing here now. The WebDAV settings are kept, not synced to: pick WebDAV to see them.",
    moving: "S3 is synced to now. Saving moves sync here; the S3 settings are kept for moving back.",
    save: "Save", move: "Move sync to WebDAV", address: "Address", user: "User", password: "Password",
    saved: "saved · type a new one to replace it",
  },
  zh: {
    s3Row: "S3 同步", davRow: "WebDAV 同步", davKept: "WebDAV 设置已保留", s3Kept: "S3 设置已保留",
    s3On: "S3 · 使用中", here: "当前正同步到这里。WebDAV 的设置仍然保留（不会同步到它），选 WebDAV 即可查看。",
    moving: "当前同步到 S3。保存后改为同步到这里；S3 的设置会保留，随时可以切回。",
    save: "保存", move: "改为同步到 WebDAV", address: "地址", user: "用户", password: "密码",
    saved: "已保存 · 输入新的即可替换",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: moving sync between WebDAV and S3 keeps the other's settings`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sync-other-kind.png`), fullPage: true });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);

      await page.goto("http://magpie.test/");
      await page.locator("#prefs").click();
      await page.locator("#setTab-sync").click();
      const list = page.locator("#syncList");
      const first = list.locator(".row.pref").first();
      await first.waitFor({ state: "visible" });
      // the sync rows are near the settings page's end: scrolled there by a
      // real wheel (the page keeps its place from any other scroll)
      const toEnd = async () => {
        await page.mouse.move(450, 300);
        const left = () => page.locator("#view-settings").evaluate((v) => v.scrollHeight - v.clientHeight - v.scrollTop);
        for (let i = 0; i < 120 && (await left()) > 0.5; i++) { await page.mouse.wheel(0, 80); await page.waitForTimeout(15); }
        await page.waitForTimeout(300);
      };
      await toEnd();
      assert.equal(await first.locator(".name").textContent(), w.s3Row);
      assert.ok((await first.locator(".sub").textContent()).endsWith(" · " + w.davKept), "the row says WebDAV's settings are kept");

      await first.locator(".val button").last().click();
      const form = list.locator(".sync-form");
      await form.waitFor({ state: "visible" });
      const hint = form.locator(".segs").locator("xpath=following-sibling::div[contains(@class,'hint')]");
      const save = form.locator(".bar .primary");
      assert.equal(await form.locator(".segs .opt.on").textContent(), w.s3On);
      assert.equal(await hint.textContent(), w.here);
      assert.equal(await save.textContent(), w.save);

      // WebDAV picked: its server as kept, and that saving moves sync there,
      // the page where it was
      await toEnd();
      const top = () => page.evaluate(() => document.querySelector("#view-settings").scrollTop);
      const segTop = () => page.evaluate(() => document.querySelector(".sync-form .segs").getBoundingClientRect().top - document.querySelector("#view-settings").getBoundingClientRect().top);
      for (let i = 0; i < 40 && (await segTop()) < 40; i++) { await page.mouse.wheel(0, -40); await page.waitForTimeout(15); }
      await page.waitForTimeout(300);
      const before = await top();
      await form.locator(".segs .opt", { hasText: "WebDAV" }).click();
      await page.waitForTimeout(250);
      assert.equal(await top(), before, "picking WebDAV must not scroll the page");
      const box = (label) => form.locator(":scope > label:visible", { hasText: new RegExp("^" + label + "$") }).locator("xpath=following-sibling::div[1]").locator("input");
      assert.equal(await box(w.address).inputValue(), DAV.url);
      assert.equal(await box(w.user).inputValue(), "me");
      assert.equal(await box(w.password).inputValue(), "");
      assert.equal(await box(w.password).getAttribute("placeholder"), w.saved);
      assert.equal(await hint.textContent(), w.moving);
      assert.equal(await save.textContent(), w.move);

      await save.click();
      await form.waitFor({ state: "detached" });
      assert.deepEqual(posts, [{ url: DAV.url, user: "me", password: "", passphrase: "", keys: true, agents: true, library: true }]);
      assert.equal(await first.locator(".name").textContent(), w.davRow);
      assert.ok((await first.locator(".sub").textContent()).endsWith(" · " + w.s3Kept), "the row says S3's settings are kept");
      assert.deepEqual(errors, []);
    });
  }
}
