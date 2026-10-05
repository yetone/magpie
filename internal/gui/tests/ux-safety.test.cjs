// Editor safety and accessible confirmations, using isolated APIs and the real frontend.
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
    responses: "", anthropic: "", models: [{ id: "fixture-model", name: "Fixture model", on: true }], agents: [], fallback: [], headers: { "X-Fixture": "kept" },
    key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true };
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang: settings.lang, theme: settings.theme, textSize: settings.textSize, web: true })};` });
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
    test(`${engine} ${lang}: drafts, accessible dialogs and immediate deletion`, async (t) => {
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
      const row = page.locator('#providers .row.provider[data-id="relay"]');
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
      await modal.getByRole("button", { name: lang === "zh" ? "名称与推理档位" : "Names & levels", exact: true }).click();
      await modal.getByRole("button", { name: cancelName, exact: true }).click();
      await modal.waitFor({ state: "hidden" }); // opening model settings alone is not an edit
      assert.equal(await page.getByRole("alertdialog").count(), 0);
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
      // Draft-only removals apply without a confirmation; canceling the draft restores them.
      await row.click();
      await modal.locator(".headers .pair .danger").click();
      assert.equal(await modal.locator(".headers .pair").count(), 0);
      assert.equal(await page.getByRole("alertdialog").count(), 0);
      await answer(page, () => modal.getByRole("button", { name: cancelName, exact: true }).click(), true, dirtyText);
      await modal.waitFor({ state: "hidden" });
      await row.click();
      assert.equal(await modal.locator(".headers .pair").count(), 1);
      await modal.locator('input[type="url"]').first().fill("https://leave.test/v1");
      assert.equal(await page.evaluate(() => {
        const leaving = new Event("beforeunload", { cancelable: true });
        window.dispatchEvent(leaving);
        return leaving.defaultPrevented;
      }), true, "a dirty editor guards browser leave");
      const leave = () => page.evaluate(() => { show("agents"); });
      await answer(page, leave, false, dirtyText);
      assert.equal(await modal.locator('input[type="url"]').first().inputValue(), "https://leave.test/v1");
      const fromModel = () => page.evaluate(() => { window.newGroupWith("relay/fixture-model", "Fixture model"); });
      await answer(page, fromModel, false, dirtyText);
      assert.equal(await page.locator("#view-providers").evaluate((e) => e.hidden), false, "Cancel keeps the provider page when creating a group from its model");
      assert.equal(await page.locator(".rt-gedit").count(), 0, "Cancel creates no routing draft behind the provider editor");
      assert.equal(await modal.locator('input[type="url"]').first().inputValue(), "https://leave.test/v1");
      await answer(page, leave, true, dirtyText);
      await modal.waitFor({ state: "hidden" });
      await page.waitForFunction(() => !document.querySelector("#view-agents").hidden);
      await page.locator('#nav button[data-view="providers"]').click();
      // Removal never mutates anything when declined.
      await row.click();
      const remove = modal.getByRole("button", { name: lang === "zh" ? "移除" : "Remove", exact: true }).first();
      await answer(page, async () => {
        await remove.click();
        const dialog = page.locator("dialog.action-confirm[open]");
        await dialog.waitFor();
        await page.evaluate(() => { renderProviders(); renderProviders(); });
        assert.equal(await dialog.evaluate((element) => element.inert), false, "redrawing the editor keeps the confirmation interactive");
        assert.equal(await page.locator("header").evaluate((element) => element.inert), true, "the background stays inert through redraws");
      }, false, /Relay/);
      assert.equal(posts.filter((p) => p.path === "/api/provider/delete").length, 0);
      assert.equal(await modal.isVisible(), true);
      await answer(page, () => remove.click(), true, /Relay/);
      await modal.waitFor({ state: "hidden" });
      assert.equal(posts.filter((p) => p.path === "/api/provider/delete").length, 1);
      assert.equal(await page.locator('#nav button[data-view="providers"]').getAttribute("aria-current"), "page");
      assert.deepEqual(errors, []);
    });
    test(`${engine} ${lang}: modal redraw keeps menus interactive and restores only its background`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      await page.route("**/*", fixture(lang, []));
      await page.goto("http://magpie.test/?view=providers");
      await page.evaluate(() => {
        const element = document.createElement("div");
        element.id = "already-inert";
        element.inert = true;
        document.body.append(element);
      });
      await page.locator('#providers .row.provider[data-id="relay"]').click();
      await page.getByRole("dialog").waitFor();
      await page.evaluate(() => {
        const element = document.createElement("div");
        element.id = "later-inert";
        element.inert = true;
        document.body.append(element);
        openProtoMenu(document.querySelector('#modal input[type="url"]'),
          [{ v: "one", name: "One" }, { v: "two", name: "Two" }], "one",
          (value) => { document.body.dataset.menuChoice = value; });
        // The provider list intentionally closes menus; redraw just the editor here.
        openModal(renderEditor(providers.providers.find((provider) => provider.id === "relay")));
      });
      const menu = page.getByRole("menu");
      assert.equal(await menu.evaluate((element) => element.inert), false);
      await menu.getByRole("menuitemradio", { name: "Two", exact: true }).click();
      assert.equal(await page.locator("body").getAttribute("data-menu-choice"), "two");
      await page.locator("#modal").getByRole("button", { name: lang === "zh" ? "取消" : "Cancel", exact: true }).click();
      await page.locator("#modal").waitFor({ state: "hidden" });
      assert.equal(await page.locator("header").evaluate((element) => element.inert), false);
      for (const id of ["already-inert", "later-inert"]) {
        assert.equal(await page.locator("#" + id).evaluate((element) => element.inert), true, `${id} keeps its own inert state`);
      }
    });
    test(`${engine} ${lang}: clicks reach the page during modal closing and a reopened editor protects it again`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 } });
      await page.route("**/*", fixture(lang, []));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator('#providers .row.provider[data-id="relay"]').click();
      const nav = page.locator('#nav button[data-view="providers"]');
      const point = await nav.boundingBox();
      await page.evaluate(() => {
        document.querySelector('#nav button[data-view="providers"]').addEventListener("click", () => {
          document.body.dataset.closeClick = "received";
          const modal = document.querySelector("#modal");
          document.body.dataset.clickedWhileClosing = String(!modal.hidden && modal.classList.contains("out"));
        }, { once: true, capture: true });
        cancelEdit();
        // Hold the real close animation at its start so the click cannot land after it finishes.
        const modal = document.querySelector("#modal");
        for (const a of [...modal.getAnimations(), ...modal.firstElementChild.getAnimations()]) a.pause();
      });
      await page.mouse.click(point.x + point.width / 2, point.y + point.height / 2);
      assert.equal(await page.locator("body").getAttribute("data-close-click"), "received", "the closing editor does not swallow the page's click");
      assert.equal(await page.locator("body").getAttribute("data-clicked-while-closing"), "true", "the click arrived before the close animation finished");
      await page.evaluate(() => {
        const modal = document.querySelector("#modal");
        for (const a of [...modal.getAnimations(), ...modal.firstElementChild.getAnimations()]) a.finish();
      });
      await page.locator("#modal").waitFor({ state: "hidden" });
      const row = page.locator('#providers .row.provider[data-id="relay"]');
      await row.click();
      const reopeningPoint = await row.boundingBox();
      const closing = await page.evaluate(() => {
        cancelEdit();
        const modal = document.querySelector("#modal");
        for (const a of [...modal.getAnimations(), ...modal.firstElementChild.getAnimations()]) a.pause();
        return !modal.hidden && modal.classList.contains("out");
      });
      assert.equal(closing, true);
      await page.mouse.click(reopeningPoint.x + reopeningPoint.width / 2, reopeningPoint.y + reopeningPoint.height / 2);
      assert.equal(await page.locator("header").evaluate((e) => e.inert), true, "reopening during close protects the background again");
      assert.equal(await page.locator("#modal").evaluate((m) => !m.hidden && !m.classList.contains("out")), true);
      await page.evaluate(() => {
        cancelEdit();
        const modal = document.querySelector("#modal");
        for (const a of [...modal.getAnimations(), ...modal.firstElementChild.getAnimations()]) a.finish();
      });
      await page.locator("#modal").waitFor({ state: "hidden" });
      assert.equal(await page.locator("header").evaluate((e) => e.inert), false, "the reopened editor also releases its background");
    });
  }
}
