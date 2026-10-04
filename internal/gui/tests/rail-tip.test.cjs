// Run with Node's test runner and Playwright on the module path; see README.md.
// The model picker's rail names an icon beside it, on the right, never over
// the icon below: the browser's own tooltip came up under the pointer, and
// Devin's name sat on ZCode's Z as if it were ZCode's (Elan on X). Hovering
// from one icon to the next moves the name with it; leaving, a click, Esc
// and closing the picker take it away. Keyboard focus shows it too. The
// picker is the one in a connected agent's opened row. In Chromium and
// WebKit, English and Chinese, light and dark.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const vendors = [["Grok (SuperGrok)", "xai"], ["Devin", "devin"], ["ZCode", "zcode"], ["Kimi", "kimi"]];
const options = vendors.flatMap(([g, ic]) => [1, 2, 3].map((n) => ({ value: `${ic}/m${n}`, ref: `${ic}/m${n}`, label: `${g} ${n}`, group: g, icon: ic })));
const state = (lang, theme) => ({
  agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", wired: true, fields: [{ key: "model", label: "model", value: "xai/m1", options }] }],
  profiles: [], settings: { lang, theme },
});

function serve(lang, theme) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state(lang, theme) });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// where the tip is, and whether it covers another rail icon than its own
const where = (page, name) => page.evaluate((name) => {
  const tip = document.querySelector(".rail-tip");
  if (!tip || !tip.classList.contains("on") || getComputedStyle(tip).visibility !== "visible") return null;
  const t = tip.getBoundingClientRect();
  const own = document.querySelector(`#pickerRail .rail-item[aria-label="${name}"]`).getBoundingClientRect();
  const over = [...document.querySelectorAll("#pickerRail .rail-item")].filter((b) => {
    const r = b.getBoundingClientRect();
    return r.right > t.left && r.left < t.right && r.bottom > t.top && r.top < t.bottom;
  }).map((b) => b.getAttribute("aria-label"));
  return { text: tip.textContent, left: t.left, ownRight: own.right, mid: t.top + t.height / 2, ownMid: own.top + own.height / 2, over,
    title: document.querySelector(`#pickerRail .rail-item[aria-label="${name}"]`).title, contrast: getComputedStyle(tip).color !== getComputedStyle(tip).backgroundColor };
}, name);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, theme] of [["en", "light"], ["zh", "dark"]]) {
    test(`${engine} ${lang} ${theme}: a rail icon's name sits beside it`, async () => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      try {
        const page = await browser.newPage({ viewport: { width: 1000, height: 700 }, colorScheme: theme });
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("http://magpie.test/**", serve(lang, theme));
        await page.goto("http://magpie.test/");
        await page.locator('#nav [data-view="agents"]').click();
        // connected, its model picked in its opened row
        await page.locator("#agents .ag-link").first().click();
        await page.locator('#agents [data-key="model"]').first().click();
        await page.locator("#pickerRail .rail-item").first().waitFor();
        const item = (n) => page.locator(`#pickerRail .rail-item[aria-label="${n}"]`);

        await item("Devin").hover();
        await page.waitForFunction(() => document.querySelector(".rail-tip.on"));
        await page.waitForTimeout(200);
        let w = await where(page, "Devin");
        assert.equal(w.text, "Devin");
        assert.equal(w.title, "", "no browser tooltip under the pointer");
        assert.deepEqual(w.over, [], "the name covers no rail icon (ZCode's Z below it)");
        assert(w.left >= w.ownRight, "it is to the right of its icon");
        assert(Math.abs(w.mid - w.ownMid) < 1.5, "level with its icon");
        assert(w.contrast);

        // next icon: the name moves at once, no wait
        await item("ZCode").hover();
        await page.waitForTimeout(60);
        w = await where(page, "ZCode");
        assert.equal(w?.text, "ZCode", "moving to the next icon names it at once");
        assert.deepEqual(w.over, []);

        // a click takes it away and switches the group
        await item("ZCode").click();
        assert.equal(await where(page, "ZCode"), null, "a click hides it");
        assert.equal(await page.locator("#pop li.group, #list li:not(.group)").first().isVisible(), true);

        // leaving the rail hides it
        await item("Kimi").hover();
        await page.waitForFunction(() => document.querySelector(".rail-tip.on"));
        await page.mouse.move(900, 650);
        await page.waitForTimeout(150);
        assert.equal(await where(page, "Kimi"), null, "leaving hides it");

        // Esc closes the picker and the name with it
        await item("Devin").hover();
        await page.waitForFunction(() => document.querySelector(".rail-tip.on"));
        await page.locator("#q").press("Escape");
        assert(await page.locator("#pop").isHidden());
        await page.waitForTimeout(150);
        assert.equal(await page.evaluate(() => !!document.querySelector(".rail-tip.on")), false, "closing the picker hides it");

        // keyboard focus names it too
        await page.mouse.move(900, 650);
        await page.locator('#agents [data-key="model"]').first().click();
        await item("Devin").waitFor();
        await page.keyboard.press("Shift"); // the last input is a key, so focus is shown
        await item("Devin").focus();
        await page.waitForTimeout(150);
        // WebKit doesn't draw focus on a button focused so; where it does, the name shows
        if (await item("Devin").evaluate((b) => b.matches(":focus-visible"))) {
          w = await where(page, "Devin");
          assert.equal(w?.text, "Devin", "a rail icon focused from the keyboard is named");
          assert.deepEqual(w.over, []);
        } else assert.equal(engine, "webkit");
        assert.deepEqual(errors, [], "page runtime errors");
      } finally {
        await browser.close();
      }
    });
  }
}
