// Run with Node's test runner and Playwright on the module path; see README.md.
// Hu9956, #844: a restart to update went ahead on the click, and what it
// brought was only to be found in Settings › About › What's new. Now the
// header's Update pill and Settings' Restart to update first show what
// changed since this version, every release up to the new one, newest first,
// and restart only on that sheet's Restart to update; Later leaves it be.
// The update's own notes show at once, the full list once it comes. No click
// moves the page; no coloured left border. English and Chinese; no backend.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { pill: "Update", restart: "Restart to update", later: "Later", head: "Update to v0.1.402", newer: "Update to v0.1.403", since: "What changed since v0.1.400", span: "2 releases · v0.1.401 – v0.1.402", span3: "3 releases · v0.1.401 – v0.1.403", spanMore: "156 releases · the newest 3 shown, v0.1.401 – v0.1.403", version: "Version" },
  zh: { pill: "更新", restart: "重启以更新", later: "稍后", head: "更新到 v0.1.402", newer: "更新到 v0.1.403", since: "自 v0.1.400 以来的更新内容", span: "2 个版本 · v0.1.401 – v0.1.402", span3: "3 个版本 · v0.1.401 – v0.1.403", spanMore: "156 个版本 · 显示最新 3 个，v0.1.401 – v0.1.403", version: "版本" },
};

function server(lang, ctl) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    const settings = { theme: "light", lang, version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", fx: { rate: 7.2, at: new Date().toISOString() } };
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/update") return json({ state: "ready", current: "0.1.400", latest: "0.1.402", notes: "- Two: the pill asks first", url: "https://example.test/v0.1.402" });
    if (url.pathname === "/api/update/notes") {
      ctl.asked.push(url.searchParams.get("lang"));
      await new Promise((r) => setTimeout(r, 200));
      // #910: the feed asked again names a release out since the download
      // a line two releases both carry is said once, at the newest
      if (ctl.newer) return json({ latest: "0.1.403", releases: [{ version: "0.1.403", notes: "- Three: out since\n- One: a fix" }, { version: "0.1.402", notes: "- Two: the pill asks first" }, { version: "0.1.401", notes: "- One: a fix" }], ...(ctl.total ? { total: ctl.total } : {}) });
      if (ctl.one) return json({ latest: "0.1.402", releases: [{ version: "0.1.402", notes: "- Two: the pill asks first" }] });
      return json({ latest: "0.1.402", releases: [{ version: "0.1.402", notes: "- Two: the pill asks first" }, { version: "0.1.401", notes: "- One: a fix" }] });
    }
    if (url.pathname === "/api/update/install") {
      ctl.installs.push(req.postDataJSON() || {});
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const scrolls = (page) => page.evaluate(() => [window.scrollY, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)].join(","));
// the sheet's releases: each version with its notes
// the releases' lines, merged under one list (the owner), each with the
// version it came in
const shown = (page) => page.$$eval("#modal .update-ask .wn-merged li", (ls) => ls.map((l) => [l.querySelector(".wn-v")?.textContent, l.firstChild.textContent.trim()]));
const lines = (n) => document.querySelectorAll("#modal .update-ask .wn-merged li").length === n;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: what an update brings is shown before it restarts`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const w = words[lang], ctl = { installs: [], asked: [] }, errors = [];
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 640 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      // WebKit's notice when a layout outruns its observers, not a fault
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, ctl));
      await page.goto("http://magpie.test/?view=agents");
      const pill = page.locator("#update");
      await pill.waitFor({ state: "visible" });
      const before = await scrolls(page);

      // the pill: the sheet, not a restart
      await pill.click();
      const ask = page.locator("#modal .update-ask");
      await ask.waitFor();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.head);
      assert.equal((await ask.locator(".wn-since").textContent()).trim(), w.since);
      await page.waitForFunction(lines, 2);
      assert.deepEqual(await shown(page), [["v0.1.402", "Two: the pill asks first"], ["v0.1.401", "One: a fix"]]);
      assert.equal(await ask.locator(".wn-span").textContent(), w.span);
      assert.deepEqual(ctl.asked, [lang], "the notes are asked for in the page's language");
      assert.deepEqual(await ask.locator(".bar button").allTextContents(), [w.later, w.restart]);
      for (const n of [ask, ask.locator(".wn-rel").first()]) assert.equal(await n.evaluate((e) => getComputedStyle(e).borderLeftStyle), "none");
      assert.deepEqual(ctl.installs, [], "nothing restarts until it's asked to");
      if (process.env.SHOT) await page.screenshot({ path: `${process.env.SHOT}/${engine}-${lang}.png` });

      // Later: the sheet goes, nothing restarts
      await ask.locator(".bar button", { hasText: w.later }).click();
      await ask.waitFor({ state: "detached" });
      await page.waitForTimeout(150);
      assert.deepEqual(ctl.installs, []);

      // the pill again, then the sheet's Restart to update
      await pill.click();
      await ask.locator(".bar button.primary").click();
      for (let i = 0; i < 50 && !ctl.installs.length; i++) await page.waitForTimeout(20);
      assert.equal(ctl.installs.length, 1, "the sheet's Restart to update restarts");
      await ask.waitFor({ state: "detached" });
      assert.equal(await scrolls(page), before, "no click moves the page");

      // Settings › About's Restart to update asks the same
      await page.goto("http://magpie.test/?view=settings&tab=about");
      const row = page.locator("#about .row.pref", { has: page.locator(".name", { hasText: w.version }) }).first();
      await row.locator("button", { hasText: w.restart }).click();
      await ask.waitFor();
      await page.waitForFunction(lines, 2);
      assert.equal(ctl.installs.length, 1, "the row's click asks first");
      await ask.locator(".bar button.primary").click();
      for (let i = 0; i < 50 && ctl.installs.length < 2; i++) await page.waitForTimeout(20);
      assert.equal(ctl.installs.length, 2);
      assert.deepEqual(errors, []);
    });

    // one release reads as before: its version over its notes, nothing merged
    test(`${engine} ${lang}: one release is shown by itself`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const ctl = { installs: [], asked: [], one: true }, errors = [];
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 640 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      // WebKit's notice when a layout outruns its observers, not a fault
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, ctl));
      await page.goto("http://magpie.test/?view=agents");
      const pill = page.locator("#update");
      await pill.waitFor({ state: "visible" });
      await pill.click();
      const ask = page.locator("#modal .update-ask");
      await ask.waitFor();
      await page.waitForFunction(() => document.querySelector("#modal .update-ask .wn-ver"));
      for (let i = 0; i < 50 && !ctl.asked.length; i++) await page.waitForTimeout(20);
      await page.waitForTimeout(400);
      assert.deepEqual(await ask.locator(".wn-ver b").allTextContents(), ["v0.1.402"]);
      assert.equal((await ask.locator(".wn-notes").textContent()).trim(), "Two: the pill asks first");
      assert.equal(await ask.locator(".wn-merged, .wn-span").count(), 0);
      assert.deepEqual(errors, []);
    });

    // Moody-Sin, #910: an update downloaded hours ago held the sheet to its
    // version with newer ones out; the notes' answer asks the feed again,
    // and the sheet names the newest, with every release up to it
    test(`${engine} ${lang}: the sheet names a release out since the download`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const w = words[lang], ctl = { installs: [], asked: [], newer: true }, errors = [];
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 640 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      // WebKit's notice when a layout outruns its observers, not a fault
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, ctl));
      await page.goto("http://magpie.test/?view=agents");
      const pill = page.locator("#update");
      await pill.waitFor({ state: "visible" });
      await pill.click();
      const ask = page.locator("#modal .update-ask");
      await ask.waitFor();
      await page.waitForFunction(lines, 3);
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.newer);
      assert.equal((await ask.locator(".wn-since").textContent()).trim(), w.since);
      assert.deepEqual(await shown(page), [["v0.1.403", "Three: out since"], ["v0.1.403", "One: a fix"], ["v0.1.402", "Two: the pill asks first"]]);
      assert.equal(await ask.locator(".wn-span").textContent(), w.span3);
      assert.deepEqual(errors, []);
    });

    // sujingshen on X: v0.1.2606 to v0.1.2762 read "40 releases", the
    // newest 40 the app keeps (MostNotes); the line says how many in all
    test(`${engine} ${lang}: a list cut short says how many releases there are`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const w = words[lang], ctl = { installs: [], asked: [], newer: true, total: 156 }, errors = [];
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 640 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, ctl));
      await page.goto("http://magpie.test/?view=agents");
      const pill = page.locator("#update");
      await pill.waitFor({ state: "visible" });
      await pill.click();
      const ask = page.locator("#modal .update-ask");
      await ask.waitFor();
      await page.waitForFunction(lines, 3);
      assert.equal(await ask.locator(".wn-span").textContent(), w.spanMore);
      assert.deepEqual(errors, []);
    });
  }
}
