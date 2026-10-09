// Sync to an S3 bucket from Settings (#296): WebDAV or S3 picked in the
// form, the bucket's fields posted as the server takes them (s3://bucket/
// prefix, the access key as the user and its secret as the password), the
// row saying S3 once on, and what was typed for one kind kept on going to
// the other and back. The API is faked; nothing is synced.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posts) {
  const settings = {
    theme: "light", lang, tray: "panel", version: "test", dir: "/tmp/magpie",
    gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [],
    fx: { rate: 7.2, stale: false },
  };
  let sync = { on: false, keys: true, agents: true, library: true };
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
      sync = { on: true, kind: b.url.startsWith("s3://") ? "s3" : "webdav", url: b.url, user: b.user, endpoint: b.endpoint, region: b.region,
        pathStyle: b.pathStyle, passwordSet: true, passphraseSet: true, keys: b.keys, agents: b.agents, library: b.library };
      return json(sync);
    }
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: S3 sync settings`, async (t) => {
      const zh = lang === "zh";
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 900, height: 420 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      const errors = [], posts = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.route("**/*", serve(lang, posts));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-s3-sync.png`), fullPage: true });
        }
        await browser.close();
      });

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
      assert.equal(await first.locator(".name").textContent(), zh ? "WebDAV / S3 / GitHub 同步" : "WebDAV, S3 or GitHub sync");
      await first.locator(".val button").click();

      const form = list.locator(".sync-form");
      await form.waitFor({ state: "visible" });
      const labels = async () => (await form.locator(":scope > label:visible").allTextContents()).filter(Boolean);
      // WebDAV is the one picked at first, S3's fields not shown
      assert.deepEqual(await labels(), zh ? ["同步到", "地址", "用户", "密码", "口令", "同时同步"] : ["Sync to", "Address", "User", "Password", "Passphrase", "Also sync"]);
      await form.locator(":scope > div").nth(1).locator("input").fill("https://dav.example.com/dav/");

      // picking S3 moves nothing on the page
      const top = () => page.evaluate(() => document.querySelector("#view-settings").scrollTop);
      // at the page's end, then back up by the wheel till the choice is in sight
      await toEnd();
      const segTop = () => page.evaluate(() => document.querySelector(".sync-form .segs").getBoundingClientRect().top - document.querySelector("#view-settings").getBoundingClientRect().top);
      for (let i = 0; i < 40 && (await segTop()) < 40; i++) { await page.mouse.wheel(0, -40); await page.waitForTimeout(15); }
      await page.waitForTimeout(300);
      const before = await top();
      assert.ok(before > 0, "the settings page must be scrolled");
      await form.locator(".segs .opt", { hasText: "S3" }).click();
      await page.waitForTimeout(250);
      assert.equal(await top(), before, "picking S3 must not scroll the page");
      assert.deepEqual(await labels(), zh
        ? ["同步到", "终结点", "存储桶", "前缀", "区域", "Access Key", "Secret", "口令", "同时同步"]
        : ["Sync to", "Endpoint", "Bucket", "Prefix", "Region", "Access key", "Secret", "Passphrase", "Also sync"]);
      // every label fits its column
      for (const l of await form.locator(":scope > label:visible").all()) {
        assert.equal(await l.evaluate((e) => e.scrollWidth <= e.clientWidth + 1), true, await l.textContent());
      }

      const box = (label) => form.locator(":scope > label:visible", { hasText: new RegExp("^" + label + "$") }).locator("xpath=following-sibling::div[1]").locator("input");
      const L = zh ? { endpoint: "终结点", bucket: "存储桶", prefix: "前缀", region: "区域", key: "Access Key", secret: "Secret", phrase: "口令" }
        : { endpoint: "Endpoint", bucket: "Bucket", prefix: "Prefix", region: "Region", key: "Access key", secret: "Secret", phrase: "Passphrase" };
      await box(L.endpoint).fill("https://acct.r2.cloudflarestorage.com");
      await box(L.phrase).fill("correct horse");
      const save = form.locator(".bar .primary");
      await save.click();
      assert.equal(await form.locator(".editor-error").textContent(), zh ? "请填写存储桶名称" : "Name the bucket");
      assert.equal(posts.length, 0);

      await box(L.bucket).fill("magpie-sync");
      await box(L.prefix).fill("/team/");
      await box(L.region).fill("auto");
      await box(L.key).fill("AKIDEXAMPLE");
      await box(L.secret).fill("s3cr3t");
      await form.locator("label.tick").first().click(); // path-style
      // the WebDAV address typed before is still there, and isn't sent;
      // going back and forth, the shorter form too, moves nothing
      // the fields filled scrolled the choice out of sight: wheeled back to it
      await page.mouse.move(450, 300);
      for (let i = 0; i < 40 && (await segTop()) < 40; i++) { await page.mouse.wheel(0, -40); await page.waitForTimeout(15); }
      await page.waitForTimeout(300);
      const at = await top();
      assert.ok(at > 0, "the settings page must still be scrolled");
      await form.locator(".segs .opt", { hasText: "WebDAV" }).click();
      await page.waitForTimeout(250);
      assert.equal(await top(), at, "picking WebDAV must not scroll the page");
      assert.equal(await form.locator(":scope > div").nth(1).locator("input").inputValue(), "https://dav.example.com/dav/");
      await form.locator(".segs .opt", { hasText: "S3" }).click();
      await page.waitForTimeout(250);
      assert.equal(await top(), at, "picking S3 again must not scroll the page");
      await toEnd(); // the save button, at the form's foot, wheeled into sight
      await save.click();
      await form.waitFor({ state: "detached" });
      assert.deepEqual(posts, [{ url: "s3://magpie-sync/team", user: "AKIDEXAMPLE", password: "s3cr3t", endpoint: "https://acct.r2.cloudflarestorage.com",
        region: "auto", pathStyle: true, passphrase: "correct horse", keys: true, agents: true, library: true }]);

      assert.equal(await first.locator(".name").textContent(), zh ? "S3 同步" : "S3 sync");
      assert.equal(await first.locator(".sub").textContent(), (zh ? "尚未同步 · " : "Not synced yet · ") + "s3://magpie-sync/team · acct.r2.cloudflarestorage.com");

      // editing it again: S3 picked, its fields as saved, the secret kept
      const edit = first.locator(".val button").last();
      await toEnd();
      // with the page scrolled to its end the row can sit under the sticky
      // header: wheeled back until it is clear of it
      const under = () => edit.evaluate((b) => b.getBoundingClientRect().top < document.querySelector("header.top").getBoundingClientRect().bottom + 4);
      for (let i = 0; i < 40 && (await under()); i++) { await page.mouse.wheel(0, -60); await page.waitForTimeout(30); }
      await edit.click();
      await form.waitFor({ state: "visible" });
      assert.equal(await form.locator(".segs .opt.on").textContent(), zh ? "S3 · 使用中" : "S3 · on");
      assert.equal(await box(L.bucket).inputValue(), "magpie-sync");
      assert.equal(await box(L.prefix).inputValue(), "team");
      assert.equal(await box(L.region).inputValue(), "auto");
      assert.equal(await box(L.key).inputValue(), "AKIDEXAMPLE");
      assert.equal(await box(L.secret).inputValue(), "");
      assert.equal(await box(L.secret).getAttribute("placeholder"), zh ? "已保存 · 输入新的即可替换" : "saved · type a new one to replace it");
      assert.equal(await form.locator("label.tick input").first().isChecked(), true);

      await page.setViewportSize({ width: 520, height: 700 });
      assert.equal(await form.evaluate((e) => e.scrollWidth > e.clientWidth), false);
      assert.deepEqual(errors, []);
    });
  }
}
