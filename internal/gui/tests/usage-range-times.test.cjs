// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage's days down to the minute (blrbrzkj on Discord): the range menu has a
// start and an end time under its month. Typed before the days are picked,
// they go with them: the backend is asked for "dayT09:30..dayT18:00", and the
// opt names the day with its minutes. A time that isn't one, or an end before
// the start on one day, is marked and asks for nothing. The menu opens again
// on the minutes picked. English and Chinese, at a phone's width; no backend,
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const dayKey = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const back = (n) => { const d = new Date(); d.setHours(12, 0, 0, 0); d.setDate(d.getDate() - n); return d; };

function serve(lang, asked) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage") {
      const p = url.searchParams.get("period");
      asked.push(p);
      return json({ period: p, calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Usage over minutes of the reader's own`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      let page;
      t.after(async () => {
        if (process.env.ARTIFACT_DIR && page) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-usage-range-times.png`) });
        }
        await browser.close();
      });
      page = await (await browser.newContext({ viewport: { width: 390, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const asked = [];
      await page.route("**/*", serve(lang, asked));
      await page.goto("http://magpie.test/?view=usage");
      const opt = page.locator("#period .range-opt");
      const head = page.locator("#usageRangeMenu");
      await head.waitFor();
      await page.waitForFunction(() => !document.querySelector("#period .opt:disabled"));
      const openDays = async () => {
        await head.click();
        const items = page.locator(".proto-menu .pm-item");
        await items.first().waitFor();
        await items.nth((await items.count()) - 1).click();
      };
      const menu = page.locator(".range-menu");
      const fromT = menu.locator(".rm-time").first(), toT = menu.locator(".rm-time").last();
      const day = async (d) => {
        for (let i = 0; i < 3 && !(await menu.locator(`.rm-day[data-day="${dayKey(d)}"]`).count()); i++) await menu.locator(".rm-step").first().click();
        await menu.locator(`.rm-day[data-day="${dayKey(d)}"]`).click();
      };
      const picked = back(3), k = dayKey(picked);

      await openDays();
      await menu.waitFor();
      assert.equal(await menu.locator(".rm-time").count(), 2, "a start and an end time");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
      const box = await menu.boundingBox();
      assert.ok(box.x >= 0 && box.x + box.width <= 390, `the menu runs off a phone's width: ${JSON.stringify(box)}`);

      // a time that isn't one is marked, and nothing is asked
      asked.length = 0;
      await fromT.fill("25:00");
      await day(picked);
      await day(picked);
      assert.equal(await fromT.getAttribute("aria-invalid"), "true");
      assert.equal(await menu.isVisible(), true, "the menu stays");
      assert.deepEqual(asked, []);

      // an end before the start on one day is marked too
      await fromT.fill("18:00");
      assert.equal(await fromT.getAttribute("aria-invalid"), null, "typing clears the mark");
      await toT.fill("9:30");
      await day(picked);
      await day(picked);
      assert.equal(await toT.getAttribute("aria-invalid"), "true");
      assert.deepEqual(asked, []);

      // 09:30 to 18:00 of one day
      await fromT.fill("0930");
      await toT.fill("18:00");
      await day(picked);
      await day(picked);
      await menu.waitFor({ state: "detached" });
      await page.waitForFunction(() => document.querySelector("#period .range-opt.on") && !document.querySelector("#period .opt:disabled"));
      const want = `${k}T09:30..${k}T18:00`;
      assert.ok(asked.includes(want), `asked for ${JSON.stringify(asked)}, want ${want}`);
      const label = (await opt.textContent()).trim();
      assert.match(label, /09:30 – 18:00$/, "the opt names the minutes: " + label);
      assert.equal((await head.textContent()).trim(), label, "the menu names them too");

      // the menu opens on them again; Enter applies new ones to the days picked
      await openDays();
      await menu.waitFor();
      assert.equal(await fromT.inputValue(), "09:30");
      assert.equal(await toT.inputValue(), "18:00");
      asked.length = 0;
      await toT.fill("");
      await fromT.fill("12:00");
      await fromT.press("Enter");
      await menu.waitFor({ state: "detached" });
      await page.waitForFunction(() => !document.querySelector("#period .opt:disabled"));
      assert.deepEqual(asked, [`${k}T12:00..${k}`]);
      assert.match((await opt.textContent()).trim(), /12:00 – 24:00$/);
      assert.deepEqual(errors, []);
    });
  }
}
