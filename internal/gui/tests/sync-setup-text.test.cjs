// Run with Node's test runner and Playwright on the module path; see README.md.
// A sync setup copied as text (WJaunita45604 on X): Copy setup in the open
// sync form copies the text magpie seals with the passphrase; on another
// computer, the text pasted into the form asks for that passphrase, says a
// wrong one in the reader's language, and with the right one fills the
// form, the kind picked from the server's address. Nothing is saved until
// Turn on. The API is faked; nothing is synced or sealed here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const TEXT = "magpie-sync:eyJmb3JtYXQiOiJtYWdwaWUtZGF0YSJ9AbCdEf0123456789";
const opened = { url: "s3://bucket/team", user: "AKIAEXAMPLE", password: "s3-secret-Qx7", endpoint: "https://acct.r2.cloudflarestorage.com", region: "auto", pathStyle: true, keys: true, agents: true, library: false, memory: true, signIns: true };

function serve(lang, on, posts) {
  const settings = { theme: "light", lang, tray: "panel", version: "test", dir: "/tmp/magpie", gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [], fx: { rate: 7.2, stale: false } };
  const sync = on ? { on: true, kind: "webdav", url: "https://dav.example.com/dav/", user: "me", passwordSet: true, passphraseSet: true, keys: true, agents: true, library: true, memory: true, last: "2026-11-13T08:00:00Z" }
    : { on: false, keys: true, agents: true, library: true, memory: true };
  return async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (pathname === "/api/settings") return json(settings);
    if (pathname === "/api/usage/quotas") return json([]);
    if (pathname === "/api/groups") return json({ groups: [], models: [] });
    if (pathname === "/api/plugins") return json({ plugins: [] });
    if (pathname === "/api/davsync") return json(sync);
    if (pathname.startsWith("/api/davsync/") || pathname === "/api/copy") {
      const body = request.postDataJSON();
      posts.push({ path: pathname, body });
      if (pathname === "/api/davsync/setup-text") return json({ text: TEXT });
      if (pathname === "/api/copy") return json({});
      if (pathname === "/api/davsync/setup-open") {
        if (body.passphrase !== "correct horse") return json({ error: "the passphrase doesn't open this setup: type the one sync uses on the other computer" }, 400);
        return json(opened);
      }
      if (pathname === "/api/davsync/save") return json({ ...sync, on: true, kind: "s3", url: body.url });
      return json(sync);
    }
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });
const press = (loc) => loc.evaluate((b) => b.click());

async function open(engine, lang, width, on, t) {
  const browser = await launch(engine);
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, on, posts));
  await page.goto("http://magpie.test/");
  await page.locator("#prefs").click();
  await openPart(page, "sync");
  const first = page.locator("#syncList .row.pref").first();
  await first.waitFor({ state: "visible" });
  await press(first.locator(".val button").last());
  const form = page.locator("#syncList .sync-form");
  await form.waitFor({ state: "visible" });
  return { page, form, errors, posts };
}

const said = (page, s) => page.evaluate((x) => t(x), s);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, width] of [["en", 900], ["zh", 900], ["zh-TW", 900], ["ja", 900], ["de", 900], ["en", 440], ["zh", 440]]) {
    test(`${engine} ${lang} ${width}px: Copy setup copies the sealed text`, async (t) => {
      const { page, form, errors, posts } = await open(engine, lang, width, true, t);
      const copy = form.locator(".bar .sync-copy");
      assert.equal((await copy.textContent()).trim(), await said(page, "Copy setup"));
      if (lang !== "en") assert.notEqual((await copy.textContent()).trim(), "Copy setup");
      await press(copy);
      for (let i = 0; i < 40 && !posts.some((p) => p.path === "/api/copy"); i++) await page.waitForTimeout(50);
      assert.deepEqual(posts.find((p) => p.path === "/api/copy")?.body, { text: TEXT });
      assert.ok(await form.evaluate((f) => f.scrollWidth <= f.clientWidth + 1), "the form's bar fits");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang} ${width}px: a pasted setup fills the form with its passphrase`, async (t) => {
      const { page, form, errors, posts } = await open(engine, lang, width, false, t);
      // no Copy setup while sync is off: there is nothing to copy
      assert.equal(await form.locator(".bar .sync-copy").count(), 0);
      const address = form.locator("input").first();
      const phrase = form.locator("input[type=password]").last();
      const note = form.locator(".editor-error");
      const pasted = await address.evaluate((i, s) => {
        i.focus();
        const dt = new DataTransfer();
        dt.setData("text/plain", s);
        const e = new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true });
        i.dispatchEvent(e);
        return e.defaultPrevented;
      }, "on the other computer:\n" + TEXT.slice(0, 30) + "\n" + TEXT.slice(30));
      assert.equal(pasted, true);
      assert.equal(await address.inputValue(), "", "the text isn't typed into the address");
      const ask = await said(page, "Type the passphrase sync uses on the other computer: the pasted setup is sealed with it");
      if (lang !== "en") assert.notEqual(ask, "Type the passphrase sync uses on the other computer: the pasted setup is sealed with it");
      assert.equal(await note.textContent(), ask);
      assert.ok(await note.evaluate((n) => n.classList.contains("note")));
      assert.ok(!posts.some((p) => p.path === "/api/davsync/setup-open"), "nothing asked without a passphrase");

      await phrase.fill("wrong horse");
      await phrase.press("Enter");
      const wrong = await said(page, "the passphrase doesn't open this setup: type the one sync uses on the other computer");
      if (lang !== "en") assert.notEqual(wrong, "the passphrase doesn't open this setup: type the one sync uses on the other computer");
      for (let i = 0; i < 40 && (await note.textContent()) !== wrong; i++) await page.waitForTimeout(50);
      assert.equal(await note.textContent(), wrong);
      assert.ok(await note.evaluate((n) => !n.classList.contains("note")));

      // the right one, then straight to Turn on: the setup is opened first
      await phrase.fill("correct horse");
      await press(form.locator(".bar .primary"));
      for (let i = 0; i < 60 && !posts.some((p) => p.path === "/api/davsync/save"); i++) await page.waitForTimeout(50);
      const save = posts.find((p) => p.path === "/api/davsync/save")?.body;
      assert.ok(save, "saved");
      assert.deepEqual([save.url, save.user, save.password, save.endpoint, save.region, save.pathStyle, save.passphrase, save.library, save.signIns],
        ["s3://bucket/team", "AKIAEXAMPLE", "s3-secret-Qx7", "https://acct.r2.cloudflarestorage.com", "auto", true, "correct horse", false, true]);
      const asks = posts.filter((p) => p.path === "/api/davsync/setup-open");
      assert.ok(asks.length >= 2 && asks.every((p) => p.body.text.includes(TEXT.slice(30))));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang} ${width}px: the pasted setup shows S3 picked before Turn on`, async (t) => {
      const { page, form, errors } = await open(engine, lang, width, false, t);
      const phrase = form.locator("input[type=password]").last();
      await phrase.fill("correct horse");
      await phrase.evaluate((i, s) => {
        const dt = new DataTransfer();
        dt.setData("text/plain", s);
        i.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
      }, TEXT);
      const filled = await said(page, "Filled in from the pasted setup.");
      for (let i = 0; i < 40 && (await form.locator(".editor-error").textContent()) !== filled; i++) await page.waitForTimeout(50);
      assert.equal(await form.locator(".editor-error").textContent(), filled);
      assert.equal(await form.locator(".segs .opt.on").getAttribute("data-seg"), "s3");
      const values = await form.locator("input:visible").evaluateAll((is) => is.map((i) => i.type === "checkbox" ? i.checked : i.value));
      for (const v of ["https://acct.r2.cloudflarestorage.com", "bucket", "team", "auto", "AKIAEXAMPLE", "s3-secret-Qx7", "correct horse"]) assert.ok(values.includes(v), v);
      assert.deepEqual(errors, []);
    });
  }
}
