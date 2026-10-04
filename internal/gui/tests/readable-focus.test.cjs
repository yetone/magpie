// Readable instructions, selectable model IDs and keyboard focus with the real assets.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");
const settings = { theme: "light", lang: "en", tray: "panel", textSize: 100, currency: "usd",
  dir: "/config/magpie", gateway: "http://127.0.0.1:3999", version: "0.1.900",
  proxy: "", proxyNow: "none", proxySource: "none", redactWords: [], otel: {},
  visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
  fx: { rate: 7.2, at: new Date().toISOString(), stale: false } };
const providers = { providers: [{ id: "acme", name: "Acme", icon: "generic", agents: [],
  chat: "https://acme.test/v1", headers: {}, fallback: [], key: { set: true, masked: "sk-…1234" },
  models: [{ id: "example", name: "Example model", on: true }, { id: "second", name: "Second model", on: true }] }],
  presets: [], excluded: [], gateway: { running: true, window: true, mine: false, url: settings.gateway, calls: [] } };
async function serve(route) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
  if (url.pathname === "/api/settings") return json(settings);
  if (url.pathname === "/api/providers") return json(providers);
  if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
  if (url.pathname === "/api/usage/quotas") return json([]);
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
}

async function contrast(locator) {
  return locator.evaluate((element) => {
    const luminance = (color) => color.match(/[\d.]+/g).slice(0, 3).map((value) => {
      const n = Number(value) / 255; return n <= .04045 ? n / 12.92 : ((n + .055) / 1.055) ** 2.4;
    }).reduce((sum, n, i) => sum + n * [.2126, .7152, .0722][i], 0);
    let parent = element, background;
    while (parent) {
      background = getComputedStyle(parent).backgroundColor;
      if (background !== "transparent" && background !== "rgba(0, 0, 0, 0)") break;
      parent = parent.parentElement;
    }
    const fg = luminance(getComputedStyle(element).color), bg = luminance(background);
    return (Math.max(fg, bg) + .05) / (Math.min(fg, bg) + .05);
  });
}

async function selectText(page, locator) {
  await page.mouse.move(600, 300);
  for (let i = 0; i < 12; i++) {
    const box = await locator.boundingBox();
    if (box.y >= 60 && box.y + box.height < 560) break;
    await page.mouse.wheel(0, box.y < 60 ? -200 : 200);
    await page.waitForTimeout(100);
  }
  await page.waitForTimeout(350);
  const rect = await locator.evaluate((element) => {
    const range = document.createRange(); range.selectNodeContents(element);
    const r = range.getBoundingClientRect();
    return { left: r.left, right: r.right, y: r.top + r.height / 2 };
  });
  await page.mouse.move(rect.left + .5, rect.y);
  await page.mouse.down();
  await page.mouse.move(rect.right - .5, rect.y, { steps: 12 });
  await page.mouse.up();
  return page.evaluate(() => getSelection().toString());
}

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  test(`${engine}: readable text, selection and keyboard focus`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 1100, height: 600 }, reducedMotion: "reduce" });
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve);

    await t.test("connection instructions and editor help are readable in both themes", async () => {
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#gwModels .name").first().waitFor();
      for (const theme of ["light", "dark"]) {
        await page.evaluate((value) => { document.documentElement.dataset.theme = value; }, theme);
        for (const selector of [".connect label", ".connect .hint", "#gwModels .sub"]) {
          const ratio = await contrast(page.locator(selector).first());
          assert.ok(ratio >= 4.5, `${theme} ${selector}: contrast ${ratio}`);
        }
      }
      await page.locator('#nav button[data-view="providers"]').click();
      await page.locator("#providers .row.provider").click();
      for (const theme of ["light", "dark"]) {
        await page.evaluate((value) => { document.documentElement.dataset.theme = value; }, theme);
        for (const selector of ["#modal .editor label", "#modal .editor .hint"]) {
          const ratio = await contrast(page.locator(selector).first());
          assert.ok(ratio >= 4.5, `${theme} ${selector}: contrast ${ratio}`);
        }
      }
      await page.goto("http://magpie.test/?view=settings");
      const description = page.locator(".row.pref .sub").filter({ hasText: /\S/ }).first();
      await description.waitFor();
      for (const theme of ["light", "dark"]) {
        await page.evaluate((value) => { document.documentElement.dataset.theme = value; }, theme);
        assert.ok(await contrast(description) >= 4.5, `${theme}: settings instructions are readable`);
      }
    });

    await t.test("selecting a model ID does not activate or redraw its row", async () => {
      await page.goto("http://magpie.test/?view=gateway");
      const name = page.locator("#gwModels .name").nth(1);
      await name.waitFor();
      const picked = await page.locator("#gwModels .selected .name").textContent();
      assert.equal(await selectText(page, name), await name.textContent());
      assert.equal(await page.locator("#gwModels .selected .name").textContent(), picked);
      assert.equal(await page.locator("body").evaluate((e) => getComputedStyle(e).userSelect), "none");
      await page.evaluate(() => { getSelection().removeAllRanges(); status("Connection refused: acme.test", "err", 60000); });
      assert.equal(await selectText(page, page.locator("#status")), "Connection refused: acme.test");
      await name.click();
      assert.equal(await page.locator("#gwModels .selected .name").textContent(), await name.textContent(), "an ordinary click still picks the model");
      await page.locator('#nav button[data-view="providers"]').click();
      await page.locator("#providers .row.provider").click();
      await page.getByRole("button", { name: "Names & levels", exact: true }).click();
      const id = page.locator(".mname .mwho code").first();
      await id.scrollIntoViewIfNeeded();
      assert.equal(await selectText(page, id), await id.textContent(), "model IDs in the editor can be selected too");
      assert.ok(await contrast(id) >= 4.5);
    });

    await t.test("keyboard focus can enter the view without allowing pointer clicks to scroll", async () => {
      await page.goto("http://magpie.test/?view=settings");
      await page.locator("#setTab-general").waitFor();
      await page.locator("#view-settings").evaluate((v) => {
        const first = document.createElement("button"); first.id = "focus-start"; first.textContent = "Edit";
        const spacer = document.createElement("div"); spacer.style.cssText = "flex:none;height:1400px";
        const field = document.createElement("input"); field.id = "focus-end";
        first.onclick = () => field.focus();
        v.replaceChildren(first, spacer, field);
      });
      const start = page.locator("#focus-start"), end = page.locator("#focus-end");
      const scroll = () => page.locator("#view-settings").evaluate((v) => v.scrollTop);
      const visibleFocus = () => end.evaluate((f) => {
        const r = f.getBoundingClientRect(), v = f.closest(".view").getBoundingClientRect();
        // Native scrolling rounds scrollTop to CSS pixels in these engines.
        return document.activeElement === f && r.top >= v.top - 2 && r.bottom <= v.bottom + 2;
      });
      const focusDetails = () => end.evaluate((f) => ({ focus: document.activeElement.id, scroll: f.closest(".view").scrollTop, top: f.getBoundingClientRect().top, bottom: f.getBoundingClientRect().bottom, view: f.closest(".view").getBoundingClientRect().bottom }));
      await start.focus();
      await page.keyboard.press("Tab");
      await page.waitForTimeout(600);
      assert.equal(await visibleFocus(), true, "Tab's focused field stays in sight: " + JSON.stringify(await focusDetails()));
      await page.keyboard.press("Shift+Tab");
      await page.waitForTimeout(600);
      assert.equal(await scroll(), 0, "Shift+Tab returns to the first control");
      await start.click();
      await page.waitForTimeout(600);
      assert.equal(await scroll(), 0, "a pointer click keeps the reader's position");
      for (const key of ["Enter", "Space"]) {
        await start.focus();
        await page.keyboard.press(key);
        await page.waitForTimeout(600);
        assert.equal(await visibleFocus(), true, `${key}: the field focused by the keyboard action is in sight: ${JSON.stringify(await focusDetails())}`);
        await page.keyboard.press("Shift+Tab");
        await page.waitForTimeout(600);
      }
    });
    assert.deepEqual(errors, []);
  });
}
