// Run with Node's test runner and Playwright on the module path; see README.md.
// arculongus on Discord: on Settings many rows' lines under their names
// ended in "…" and couldn't be read: Image generation's ChatGPT note, the
// Image recognition MCP server, Web search's Searches for other models. A
// row with a control on its right held its line to one line, cut with an
// ellipsis (`.row .sub`), while rows that had opted out (`.wraps`) wrapped.
// Now every setting row's line wraps beside its control. In the window (not
// browser mode, which wrapped at a phone's width already), in every
// language, at 1000px and 440px, in both engines, on every part of Settings
// and on Plugins › Built-in (the same rows):
// - no row's line is cut: each is as wide as its words, with no ellipsis;
// - the row's control stays inside the row, at its own width, and the page
//   doesn't scroll sideways.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, openPart } = require("./reader.cjs");

const assets = path.resolve(process.env.MAGPIE_WRAP_ASSETS || path.join(__dirname, "../assets"));

function settings(lang) {
  return {
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100, proxy: "", proxyNow: "none", proxySource: "none",
    redact: true, redactPersonal: true, redactWords: [], version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    lanURLs: [], searchVendors: [], searchAPIs: [], vision: "", imageGen: "",
    visionModels: [{ id: "codex/gpt-5", name: "GPT-5", provider: "codex", providerName: "Codex" }],
    // a ChatGPT account draws, so Image generation has its second note
    imageGenAuto: "codex/gpt-image-2",
    imageGenModels: [{ id: "codex/gpt-image-2", name: "GPT Image", provider: "codex", providerName: "Codex", icon: "openai", vendorPicks: true }],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
}

// Plugins › Built-in's rows: a name, what it is, a switch (builtins.test.cjs)
const builtins = [
  { id: "groups", name: "Routing groups", sub: "Several models agents pick as one: each request goes to the right one, and to the next when one fails", on: true, chosen: true, source: "preset", state: "active", inUse: 0 },
  { id: "warmups", name: "Warm-up and check-in", sub: "Start a subscription's usage window at a time you pick, and claim vendors' daily check-in credits", on: true, chosen: false, source: "in use", state: "active", inUse: 2, use: "{n} warm-ups and check-ins on" },
];

function serve(lang) {
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings(lang) });
    if (url.pathname === "/api/settings") return json(settings(lang));
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins/builtin") return json(builtins);
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: [], picker: false, movable: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? r.fulfill({ body, contentType }) : r.fulfill({ status: 404, body: "" }));
  };
}

// every shown setting row's lines and controls, in the part shown
const measure = (scope) => {
  const shown = (e) => e.getClientRects().length && !e.closest("[hidden]");
  const rows = [...document.querySelectorAll(`${scope} .row.pref`)].filter(shown);
  const cut = [], out = [];
  for (const r of rows) {
    const rb = r.getBoundingClientRect();
    for (const s of r.querySelectorAll(".who .sub")) {
      if (!shown(s) || !s.textContent.trim()) continue;
      // an explanation kept out of sight shows whole in its card on hover
      // (settings-hints.test.cjs)
      if (getComputedStyle(s).getPropertyValue("--hint").trim() === "1") continue;
      const cs = getComputedStyle(s);
      if (s.scrollWidth > s.clientWidth + 1 || cs.whiteSpace === "nowrap") cut.push({ text: s.textContent.slice(0, 80), scroll: s.scrollWidth, width: s.clientWidth, whiteSpace: cs.whiteSpace });
    }
    const v = r.querySelector(":scope > .val");
    if (v && shown(v)) {
      const b = v.getBoundingClientRect();
      if (b.right > rb.right + 0.5 || b.left < rb.left - 0.5) out.push({ row: r.querySelector(".name")?.textContent, val: [b.left, b.right], rowBox: [rb.left, rb.right] });
    }
  }
  return { rows: rows.length, cut, out, sideways: document.documentElement.scrollWidth > innerWidth };
};

const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
for (const engine of engines) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1000, 440]) {
    test(`${engine} ${lang} ${width}px: every Settings row's line wraps whole beside its control`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.addInitScript(() => Object.defineProperty(navigator, "platform", { get: () => "MacIntel" }));
      await page.route("**/*", serve(lang));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=general");
      await settingsDrawn(page);
      const parts = await page.$$eval("[id^=setTab-]", (bs) => bs.filter((b) => !b.hidden).map((b) => b.dataset.set));
      assert.ok(parts.length >= 7, `parts: ${parts}`);
      let seen = 0;
      for (const part of parts) {
        await openPart(page, part);
        await page.locator(`#setPage-${part}`).waitFor();
        await settingsDrawn(page);
        const m = await page.evaluate(measure, `#setPage-${part}`);
        seen += m.rows;
        assert.deepEqual(m.cut, [], `${part}: a row's line is cut short`);
        assert.deepEqual(m.out, [], `${part}: a row's control runs out of its row`);
        assert.equal(m.sideways, false, `${part}: the page scrolls sideways`);
      }
      assert.ok(seen > 30, `only ${seen} setting rows were looked at`);
      // the reported rows were among them, and read whole
      await openPart(page, "models");
      for (const sel of ["#imagesList .image-gen-vendor", "#visionMcpRow .sub", ".searcher-row .who > .sub"]) {
        const s = page.locator(sel).first();
        await s.waitFor({ state: "attached" });
        if (await s.evaluate((e) => getComputedStyle(e).getPropertyValue("--hint").trim() === "1")) {
          // out of sight at rest: the row's card shows it whole
          await s.evaluate((e) => e.closest(".row.pref").scrollIntoView({ block: "center" }));
          await s.locator("xpath=ancestor::div[contains(concat(' ', @class, ' '), ' pref ')][1]").locator(":scope > .who > .name").hover();
          const card = page.locator(".set-hint.on");
          await card.waitFor();
          assert.ok((await card.textContent()).includes((await s.textContent()).trim()), `${sel} isn't in its card`);
          await page.mouse.move(0, 0);
          continue;
        }
        const whole = await s.evaluate((e) => e.scrollWidth <= e.clientWidth + 1 && getComputedStyle(e).whiteSpace !== "nowrap");
        assert.equal(whole, true, `${sel} is cut`);
      }

      // Plugins › Built-in: the same rows
      // (its tab is the page's own strip's first below 1024 points)
      await page.goto("http://magpie.test/?view=plugins");
      await page.locator("#view-plugins .lib-tabs .opt").first().press("Enter");
      await page.locator("#builtinList .feat-row").first().waitFor();
      const m = await page.evaluate(measure, "#builtinList");
      assert.ok(m.rows >= builtins.length, `built-in rows: ${m.rows}`);
      assert.deepEqual(m.cut, [], "a built-in plugin's line is cut short");
      assert.deepEqual(m.out, [], "a built-in plugin's switch runs out of its row");
      assert.equal(m.sideways, false, "Plugins scrolls sideways");
      assert.deepEqual(errors, []);
    });
  }
}
