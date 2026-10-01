// Settings' WebDAV/S3 row against a setup changed behind the window (#305):
// the terminal's magpie webdav / magpie s3 writes sync.json under a page
// that has already read it, and the page kept the first view it ever read,
// so its row, its status and the form's ticks showed what the process had
// started with until a restart. Coming back to the settings page or the
// window's focus re-reads it now (a form open is never rebuilt under
// whoever is typing in it, #104), and the off-state description names only
// what the view's toggles turn on, as the CLI does. The API is faked;
// nothing is synced.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, syncRef) {
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
    if (pathname === "/api/davsync") return json(syncRef.value);
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a setup changed behind the window reaches the page again`, async (t) => {
      const zh = lang === "zh";
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", (error) => errors.push(error.message));
      // what /api/davsync answers, changed between reads as the terminal
      // changes sync.json behind the window
      const sync = { value: { on: false, keys: true, agents: true, library: true } };
      await page.route("**/*", serve(lang, sync));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-webdav-settings-refresh.png`), fullPage: true });
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
      // coming back is read again, so redraws that follow it settle before
      // an assertion: polled rather than waited for
      const settle = async (fn) => {
        for (let i = 0; i < 50; i++) {
          if (await fn()) return true;
          await page.waitForTimeout(60);
        }
        return false;
      };
      const focus = () => page.evaluate(() => window.dispatchEvent(new Event("focus")));
      const seen = () => page.evaluate(() => document.visibilityState === "visible");
      assert.equal(await seen(), true, "the page must be visible, as a window's is");

      // off, nothing changed by anything: the description of the whole lot
      assert.equal(await first.locator(".name").textContent(), zh ? "WebDAV / S3 同步" : "WebDAV or S3 sync");
      assert.equal(await first.locator(".sub").textContent(), zh
        ? "让每台电脑上的供应商（含 API Key）、设置、Profile、agent 的模型、资源库保持一致"
        : "Keeps providers with their API keys, settings, profiles, agents' models, library the same on every computer");

      // an open form is someone typing: a focus while one is open must not
      // rebuild it under them (#104)
      await first.locator(".val button").click();
      const form = list.locator(".sync-form");
      await form.waitFor({ state: "visible" });
      const address = form.locator(":scope > label:visible", { hasText: zh ? "地址" : "Address" }).locator("xpath=following-sibling::div[1]").locator("input");
      await address.fill("https://dav.example.com/dav/");
      await address.evaluate((e) => { e.dataset.typed = "1"; });
      await focus();
      await page.waitForTimeout(600);
      assert.equal(await form.isVisible(), true, "the open form must stay");
      assert.equal(await address.evaluate((e) => e.dataset.typed), "1", "the form must be the same element, not redrawn");
      assert.equal(await address.inputValue(), "https://dav.example.com/dav/", "what was typed must stay");

      // what the terminal leaves behind: on, with agents and the library
      // taken out. The focus of a window back on the page re-reads it.
      await first.locator(".val button", { hasText: zh ? "关闭" : "Close" }).click();
      await form.waitFor({ state: "detached" });
      sync.value = { on: true, kind: "webdav", url: "https://dav.example.com/dav/", user: "me", passwordSet: true, passphraseSet: true,
        keys: true, agents: false, library: false };
      await focus();
      assert.equal(await settle(async () => (await first.locator(".sub").textContent()) === (zh ? "尚未同步 · dav.example.com" : "Not synced yet · dav.example.com")), true,
        "the row must say what the CLI left, not what the page first read");
      assert.equal(await first.locator(".name").textContent(), zh ? "WebDAV 同步" : "WebDAV sync");

      // the form's ticks come from that fresh view
      await first.locator(".val button", { hasText: zh ? "编辑" : "Edit" }).click();
      await form.waitFor({ state: "visible" });
      const tick = (word) => form.locator("label.tick", { hasText: word }).locator("input");
      assert.equal(await tick("API Key").isChecked(), true);
      assert.equal(await tick(zh ? "agent" : "Agents").isChecked(), false, "agents' models must be unticked, as the CLI left it");
      assert.equal(await tick(zh ? "资源库" : "Library").isChecked(), false, "the library must be unticked, as the CLI left it");
      await first.locator(".val button", { hasText: zh ? "关闭" : "Close" }).click();
      await form.waitFor({ state: "detached" });

      // S3 behind the window: the same re-read, and the row keeps its kind
      sync.value = { on: true, kind: "s3", url: "s3://bucket/prefix", endpoint: "https://s3.example.com", region: "auto",
        user: "key-id", passwordSet: true, passphraseSet: true, keys: true, agents: true, library: true };
      await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
      assert.equal(await settle(async () => (await first.locator(".sub").textContent()) === (zh ? "尚未同步 · s3://bucket/prefix · s3.example.com" : "Not synced yet · s3://bucket/prefix · s3.example.com")), true,
        "an S3 setup changed behind the window must reach the page too");
      assert.equal(await first.locator(".name").textContent(), zh ? "S3 同步" : "S3 sync");

      // off again, keys and agents out: the description names what is on,
      // as the CLI's list does
      sync.value = { on: false, keys: false, agents: false, library: true };
      await focus();
      assert.equal(await settle(async () => (await first.locator(".sub").textContent()) === (zh
        ? "让每台电脑上的供应商（不含 API Key）、设置、Profile、资源库保持一致"
        : "Keeps providers without their API keys, settings, profiles, library the same on every computer")), true,
        "the description must be built from the view's toggles");
      assert.equal(await first.locator(".name").textContent(), zh ? "WebDAV / S3 同步" : "WebDAV or S3 sync");

      // a view that says nothing of the toggles (a read that failed) is
      // the whole lot, as Status' off view is
      sync.value = { on: false };
      await focus();
      assert.equal(await settle(async () => (await first.locator(".sub").textContent()) === (zh
        ? "让每台电脑上的供应商（含 API Key）、设置、Profile、agent 的模型、资源库保持一致"
        : "Keeps providers with their API keys, settings, profiles, agents' models, library the same on every computer")), true,
        "a view with no toggles must read as the defaults");

      assert.deepEqual(errors, []);
    });
  }
}
