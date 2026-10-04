// UI safety and Web conventions, using isolated APIs and the real frontend.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");

function fixture(lang, posts) {
  let settings = { theme: "light", lang, tray: "panel", currency: "usd", textSize: 100,
    version: "0.1.700", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3999",
    proxy: "", proxyNow: "none", proxySource: "none", redactWords: [], visionModels: [], imageGenModels: [],
    workbuddyCheckins: [], lanURLs: [], otel: {}, fx: { rate: 7.2, at: new Date().toISOString(), stale: false } };
  const relay = { id: "relay", name: "Relay", icon: "generic", host: "relay.test", chat: "https://relay.test/v1",
    responses: "", anthropic: "", models: [], agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true };
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ ...settings, web: true })};` });
    if (req.method() === "POST") posts.push({ path: url.pathname, body: req.postDataJSON() });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") settings = { ...settings, ...req.postDataJSON() };
      return json(settings);
    }
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/provider/delete") { providers.providers = []; return json(providers); }
    if (url.pathname === "/api/provider/save") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try { await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] }); }
    catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function answer(page, action, accept, text, escape = false) {
  await action();
  const dialog = page.getByRole("alertdialog");
  await dialog.waitFor();
  assert.match(await dialog.textContent(), text);
  for (const theme of ["light", "dark"]) {
    const layout = await dialog.evaluate((element, theme) => {
      const root = document.documentElement, previous = root.dataset.theme;
      root.dataset.theme = theme;
      const rect = element.getBoundingClientRect();
      const style = getComputedStyle(element.querySelector("button:last-child"));
      const luminance = (color) => color.match(/[\d.]+/g).slice(0, 3).map((value) => {
        const n = Number(value) / 255; return n <= .04045 ? n / 12.92 : ((n + .055) / 1.055) ** 2.4;
      }).reduce((sum, n, i) => sum + n * [.2126, .7152, .0722][i], 0);
      const fg = luminance(style.color), bg = luminance(style.backgroundColor);
      const result = { x: rect.left + rect.width / 2 - innerWidth / 2, y: rect.top + rect.height / 2 - innerHeight / 2,
        contrast: (Math.max(fg, bg) + .05) / (Math.min(fg, bg) + .05) };
      if (previous === undefined) delete root.dataset.theme; else root.dataset.theme = previous;
      return result;
    }, theme);
    assert.ok(Math.abs(layout.x) <= 1 && Math.abs(layout.y) <= 1, `${theme}: confirmation is centered`);
    assert.ok(layout.contrast >= 4.5, `${theme}: destructive button contrast ${layout.contrast}`);
  }
  assert.equal(await dialog.evaluate((element) => element.contains(document.activeElement)), true);
  await dialog.locator("button").last().focus();
  await page.keyboard.press("Tab");
  assert.equal(await dialog.evaluate((element) => element.contains(document.activeElement)), true, "confirmation traps focus");
  if (accept) await dialog.locator("button").last().click();
  else if (escape) await page.keyboard.press("Escape");
  else await dialog.getByRole("button", { name: /^(Cancel|取消)$/ }).click();
  await dialog.waitFor({ state: "detached" });
}

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: drafts, dialogs, history, explicit saves and deletion`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const posts = [], errors = [];
      page.on("dialog", (dialog) => { errors.push("Unexpected native dialog: " + dialog.type()); dialog.dismiss(); });
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const dirtyText = lang === "zh" ? /放弃未保存/ : /Discard unsaved/;
      const saveName = lang === "zh" ? "保存" : "Save";
      const cancelName = lang === "zh" ? "取消" : "Cancel";
      const row = page.locator("#providers .row.provider");
      await row.click();
      const modal = page.locator("#modal");
      await page.getByRole("dialog").waitFor();
      assert.equal(await page.getByRole("dialog").getAttribute("aria-modal"), "true");
      assert.equal(await page.locator("header").evaluate((e) => e.inert), true);
      assert.ok(await page.locator("#modal label").evaluateAll((labels) => labels.filter((l) => l.htmlFor).every((l) => !!l.control)), "labels refer to controls");
      // Focus cycles in both directions, never entering the dimmed background.
      await page.locator("#modal .bar button:last-child").focus();
      await page.keyboard.press("Tab");
      assert.equal(await page.evaluate(() => !!document.activeElement.closest("#modal")), true);
      await page.keyboard.press("Shift+Tab");
      assert.equal(await page.evaluate(() => !!document.activeElement.closest("#modal")), true);
      await modal.getByRole("button", { name: cancelName, exact: true }).click();
      await modal.waitFor({ state: "hidden" }); // untouched form needs no confirmation
      await row.click();
      const url = modal.locator('input[type="url"]').first();
      const initial = await url.inputValue();
      await url.fill("https://changed.test/v1");
      await answer(page, () => url.press("Escape"), false, dirtyText);
      assert.equal(await url.inputValue(), "https://changed.test/v1");
      assert.equal(await modal.isVisible(), true);
      // Clicking the scrim is also guarded.
      await answer(page, () => page.mouse.click(2, 2), false, dirtyText, true);
      assert.equal(await modal.isVisible(), true);
      // A reverted field is clean; saving commits without a discard prompt.
      await url.fill(initial);
      await modal.getByRole("button", { name: cancelName, exact: true }).click();
      await modal.waitFor({ state: "hidden" });
      await row.click();
      await modal.locator('input[type="url"]').first().fill("https://saved.test/v1");
      await modal.getByRole("button", { name: saveName, exact: true }).click();
      await modal.waitFor({ state: "hidden" });
      assert.equal(posts.filter((p) => p.path === "/api/provider/save").length, 1);
      await row.click();
      await modal.locator('input[type="url"]').first().fill("https://discarded.test/v1");
      await answer(page, () => modal.getByRole("button", { name: cancelName, exact: true }).click(), true, dirtyText);
      await modal.waitFor({ state: "hidden" });
      // Removal never mutates anything when declined.
      await row.click();
      const remove = modal.getByRole("button", { name: lang === "zh" ? "移除" : "Remove", exact: true }).first();
      await answer(page, () => remove.click(), false, /Relay/);
      assert.equal(posts.filter((p) => p.path === "/api/provider/delete").length, 0);
      assert.equal(await modal.isVisible(), true);
      await answer(page, () => remove.click(), true, /Relay/);
      await modal.waitFor({ state: "hidden" });
      assert.equal(posts.filter((p) => p.path === "/api/provider/delete").length, 1);
      // Normal Web navigation supports back and forward.
      await page.locator('#nav button[data-view="agents"]').click();
      await page.locator('#nav button[data-view="gateway"]').click();
      await page.goBack();
      await page.waitForFunction(() => !document.querySelector("#view-agents").hidden);
      await page.goForward();
      await page.waitForFunction(() => !document.querySelector("#view-gateway").hidden);
      assert.equal(await page.locator('#nav button[data-view="gateway"]').getAttribute("aria-current"), "page");
      // Text settings don't save on blur, and leaving requires confirmation.
      await page.locator("#prefs").click();
      await page.locator("#setTab-privacy").click();
      const words = page.locator('input[data-setting="redactWords"]');
      await words.fill("private-host");
      await words.press("Tab");
      assert.equal(posts.filter((p) => p.path === "/api/settings").length, 0);
      await answer(page, () => page.locator('#nav button[data-view="agents"]').click(), false, dirtyText);
      assert.equal(await words.inputValue(), "private-host");
      const response = page.waitForResponse((r) => r.url().endsWith("/api/settings") && r.request().method() === "POST");
      await words.locator("..").getByRole("button", { name: saveName, exact: true }).click();
      await response;
      await page.waitForFunction(() => document.querySelector('input[data-setting="redactWords"]').value === "private-host");
      assert.deepEqual(posts.find((p) => p.path === "/api/settings").body.redactWords, ["private-host"]);
      // Browser back can be declined without losing the current form or URL.
      await words.fill("another-host");
      assert.equal(await page.evaluate(() => {
        const leaving = new Event("beforeunload", { cancelable: true });
        window.dispatchEvent(leaving);
        return leaving.defaultPrevented;
      }), true, "unsaved settings guard browser leave");
      await answer(page, () => page.goBack(), false, dirtyText);
      await page.waitForFunction(() => location.search.includes("view=settings"));
      assert.equal(await words.inputValue(), "another-host");
      await answer(page, () => page.locator('#nav button[data-view="agents"]').click(), true, dirtyText);
      await page.waitForFunction(() => !document.querySelector("#view-agents").hidden);
      // Sync credentials use the same discard rule, including Close.
      await page.locator("#prefs").click();
      await page.locator("#setTab-sync").click();
      await page.locator("#syncList .row.pref").first().locator("button").click();
      const sync = page.locator("#syncList .sync-form");
      const address = sync.locator('input[type="text"]').first();
      await address.fill("https://dav.changed.test/dav/");
      await answer(page, () => sync.getByRole("button", { name: cancelName, exact: true }).click(), false, dirtyText);
      assert.equal(await address.inputValue(), "https://dav.changed.test/dav/");
      await answer(page, () => sync.getByRole("button", { name: cancelName, exact: true }).click(), true, dirtyText);
      await sync.waitFor({ state: "detached" });
      assert.equal(posts.filter((p) => p.path.startsWith("/api/davsync/")).length, 0);
      await page.locator('#nav button[data-view="agents"]').click();
      // Focus-initiated scroll is not forced back to its earlier position.
      const scrolled = await page.evaluate(() => {
        const view = document.querySelector("#view-agents");
        const spacer = document.createElement("div"); spacer.style.cssText = "height:2000px;flex:none";
        const input = document.createElement("input");
        view.append(spacer, input); input.focus(); input.scrollIntoView({ block: "nearest" });
        return view.scrollTop;
      });
      assert.ok(scrolled > 0);
      assert.ok(await page.locator("#view-agents").evaluate((v) => v.scrollTop) > 0);
      await page.clock.install();
      await page.evaluate(() => status("Fixture failure", "err"));
      await page.clock.runFor(9000);
      assert.equal(await page.locator("#status").textContent(), "Fixture failure");
      assert.equal(await page.locator("#status").getAttribute("role"), "alert");
      await page.locator("#statusDismiss").click();
      assert.equal(await page.locator("#status").textContent(), "");
      assert.deepEqual(errors, []);
    });
  }
}
