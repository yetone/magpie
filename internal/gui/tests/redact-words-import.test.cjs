// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Privacy's Masked words and Never mask take many words at once
// (acmilan_ on Discord: adding them one by one was slow). Import… opens a
// box under the list; what is pasted there, a word a line or split at
// commas, is added after the words the list has, a word already in it or
// pasted twice passed over, and the status says how many were added. A
// redraw keeps the box and what it holds; Escape and Cancel close it and
// save nothing; a save that fails says no count. Copy puts the list on the
// clipboard a word a line. Every language, Chromium and WebKit, narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, box) {
  return async (r) => {
    const req = r.request(), url = new URL(req.url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: box.settings });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        if (box.fail) return r.fulfill({ status: 500, contentType: "text/plain", body: "disk full" });
        box.settings = JSON.parse(req.postData());
        box.saved.push(box.settings.redactWords);
      }
      return json(box.settings);
    }
    if (url.pathname === "/api/settings/redact-allow") {
      const b = req.postDataJSON();
      box.allowed.push(b.allow);
      box.settings = { ...box.settings, redactAllow: b.allow };
      return json(box.settings);
    }
    if (url.pathname === "/api/copy") {
      box.copied.push(req.postDataJSON().text);
      return json({});
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const ROW = (key) => `.row:has(> .word-list[data-list="${key}"])`;
const LIST = (key) => `.word-list[data-list="${key}"]`;
const words = (page, key) => page.evaluate((sel) => [...document.querySelectorAll(`${sel} input[data-word]`)].map((i) => i.dataset.word), LIST(key));
const drawn = (page, key, want) => page.waitForFunction(([sel, want]) =>
  JSON.stringify([...document.querySelectorAll(`${sel} input[data-word]`)].map((i) => i.dataset.word)) === want, [LIST(key), JSON.stringify(want)], { timeout: 5000 });
const until = async (ok, what) => {
  for (let n = 0; n < 100 && !ok(); n++) await new Promise((r) => setTimeout(r, 50));
  assert(ok(), what);
};
// the window's own word for it
const said = (page, s, vars) => page.evaluate(([s, vars]) => t(s, vars), [s, vars]);
// nothing in the list's row past its section's edge, and no sideways scroll
const fits = (page, key) => page.evaluate((sel) => {
  const l = document.querySelector(sel).parentElement.getBoundingClientRect();
  const over = [...document.querySelectorAll(`${sel} input, ${sel} button, ${sel} textarea, ${sel} .hint`)]
    .filter((n) => n.getClientRects().length).filter((n) => { const b = n.getBoundingClientRect(); return b.left < l.left - 1 || b.right > l.right + 1; })
    .map((n) => n.className);
  return { over, page: document.documentElement.scrollWidth > document.documentElement.clientWidth };
}, ROW(key));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang} 400px: masked words and never-mask values are imported in bulk and copied out`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 400, height: 1400 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      const box = { saved: [], allowed: [], copied: [], fail: false,
        settings: { lang, theme: "light", redact: true, redactPersonal: false, redactWords: ["Nightjar", "百度"], redactAllow: ["ops@company.com"] } };
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, box));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=privacy");
      await page.locator(LIST("words")).waitFor();
      await settingsDrawn(page);

      // closed until asked for, and nothing native
      const area = page.locator(`${LIST("words")} textarea.word-import-in`);
      assert.equal(await area.isVisible(), false, "the import box is closed at first");
      assert.equal(await page.locator(`${ROW("words")} select`).count(), 0);
      const open = page.locator(`${ROW("words")} .word-import-open`);
      assert.equal((await open.textContent()).trim(), await said(page, "Import…"));
      await reach(page, open);
      await open.click();
      await page.waitForFunction((sel) => document.activeElement?.matches(`${sel} textarea.word-import-in`), LIST("words"));
      assert.deepEqual(await fits(page, "words"), { over: [], page: false }, "the open box fits a narrow window");

      // pasted: lines, commas (either width), blanks, a word twice and two
      // the list has; the count is of the new ones
      await area.fill("Nightjar\nKestrel, Osprey，百度\n\n  Kestrel \nops.example.com\n");
      const count = page.locator(`${LIST("words")} .word-import-count`);
      assert.equal(await count.textContent(), await said(page, "New words: {n}", { n: 3 }));
      if (lang === "en") assert.equal(await count.textContent(), "New words: 3");

      // a redraw keeps the box open with what it holds
      await page.evaluate(() => renderSettings());
      await page.locator(`${LIST("words")} textarea.word-import-in`).waitFor({ state: "visible" });
      assert.equal(await area.inputValue(), "Nightjar\nKestrel, Osprey，百度\n\n  Kestrel \nops.example.com\n");
      assert.equal(box.saved.length, 0, "typing in the box saves nothing");

      const go = page.locator(`${LIST("words")} .word-import-bar button.primary`);
      await reach(page, go);
      await go.click();
      const five = ["Nightjar", "百度", "Kestrel", "Osprey", "ops.example.com"];
      await until(() => JSON.stringify(box.saved.at(-1)) === JSON.stringify(five), `saved ${JSON.stringify(box.saved)}`);
      assert.equal(box.saved.length, 1, "one save for the whole import");
      await drawn(page, "words", five);
      const done = await said(page, "Words added: {n} · already in the list: {m}", { n: 3, m: 2 });
      await page.waitForFunction((want) => document.querySelector("#status").textContent === want, done);
      if (lang === "en") assert.equal(done, "Words added: 3 · already in the list: 2");
      assert.equal(await area.isVisible(), false, "the box closes once imported");

      // only words the list has: nothing to import; Escape closes, saves nothing
      await open.click();
      await area.fill("kestrel\nKestrel");
      assert.equal(await count.textContent(), await said(page, "New words: {n}", { n: 1 }), "a word in another case is a word of its own");
      await area.fill("Kestrel, Osprey");
      assert.equal(await count.textContent(), await said(page, "No new words: each is in the list already"));
      assert.equal(await go.isDisabled(), true);
      await area.press("Escape");
      assert.equal(await area.isVisible(), false);
      // and Cancel closes it too, emptied for next time
      await open.click();
      await area.fill("Plover");
      await page.locator(`${LIST("words")} .word-import-bar button:not(.primary)`).click();
      assert.equal(await area.isVisible(), false);
      await open.click();
      assert.equal(await area.inputValue(), "");
      await area.press("Escape");
      assert.equal(box.saved.length, 1, "closing saves nothing");

      // a save that fails says no count
      box.fail = true;
      await open.click();
      await area.fill("Plover");
      await area.press(process.platform === "darwin" ? "Meta+Enter" : "Control+Enter");
      await page.waitForFunction(() => document.querySelector("#status").classList.contains("err"));
      assert.notEqual(await page.locator("#status").textContent(), await said(page, "Words added: {n}", { n: 1 }));
      box.fail = false;
      await drawn(page, "words", five);

      // Copy: the list a word a line, as Import takes it
      const copy = page.locator(`${ROW("words")} .word-copy`);
      await reach(page, copy);
      await copy.click();
      await until(() => box.copied.length === 1, "copied once");
      assert.equal(box.copied[0], five.join("\n"));
      await page.waitForFunction((want) => document.querySelector("#status").textContent === want, await said(page, "Words copied: {n}", { n: 5 }));

      // Never mask takes an import the same way
      const allowOpen = page.locator(`${ROW("allow")} .word-import-open`);
      await reach(page, allowOpen);
      await allowOpen.click();
      const allowArea = page.locator(`${LIST("allow")} textarea.word-import-in`);
      await allowArea.fill("ops@company.com, build-01\nbuild-02");
      const allowGo = page.locator(`${LIST("allow")} .word-import-bar button.primary`);
      await reach(page, allowGo);
      await allowGo.click();
      await until(() => box.allowed.length === 1, "never-mask saved once");
      assert.deepEqual(box.allowed[0], ["ops@company.com", "build-01", "build-02"]);
      await drawn(page, "allow", ["ops@company.com", "build-01", "build-02"]);
      assert.deepEqual(await fits(page, "allow"), { over: [], page: false });

      // what was saved is drawn again on the next load
      await page.reload();
      await page.locator(LIST("words")).waitFor();
      await settingsDrawn(page);
      assert.deepEqual(await words(page, "words"), five);
      assert.deepEqual(errors, []);
    });
  }
}
