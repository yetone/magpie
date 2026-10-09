// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's Save says it is under way (zola_xynb on X: Save asks the
// provider for its model list, which can take seconds, and the button only
// faded, as if the click had done nothing). While the save is out the
// button shows a spinner and "Saving…"; a save that fails puts "Save" back,
// with the error, for another try; one that goes through closes the editor.
// English, Chinese, Japanese and German, Chromium and WebKit, narrow and
// wide; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, saves) {
  const models = ["deepseek-v4-flash", "deepseek-v4-pro"].map((id) => ({ id, name: id, on: true, context: 1000000 }));
  const provider = { id: "deepseek", name: "DeepSeek", icon: "generic", chat: "https://api.deepseek.example/v1", responses: "", anthropic: "", models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true };
  const providers = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/provider/save") {
      // held until the test answers it: ok, or the error to give
      const err = await new Promise((answer) => saves.push(answer));
      return err ? json({ error: err }, 502) : json(providers);
    }
    if (url.pathname === "/api/settings") return json({ theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.1080", lanURLs: [], redactWords: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try {
      await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  };
}

const words = {
  en: { save: "Save", saving: "Saving…" },
  zh: { save: "保存", saving: "保存中…" },
  ja: { save: "保存", saving: "保存中…" },
  de: { save: "Speichern", saving: "Wird gespeichert…" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [440, 900]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a provider's Save shows it is under way`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 720 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], saves = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, saves));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator('.row.provider[data-id="deepseek"]').click();
        const bar = page.locator(".editor .bar");
        const save = bar.getByRole("button", { name: w.save, exact: true });
        const top = await page.evaluate(() => document.scrollingElement.scrollTop);
        await save.click();

        // out: a spinner, what it is doing, not to be clicked again
        const busy = bar.locator("button.saving");
        await busy.waitFor();
        await page.waitForFunction(() => document.querySelector(".editor .bar button.saving svg") && true);
        assert.equal((await busy.textContent()).trim(), w.saving);
        assert.equal(await busy.getAttribute("aria-busy"), "true");
        const look = await busy.evaluate((b) => {
          const s = getComputedStyle(b), svg = b.querySelector("svg"), r = b.getBoundingClientRect(), label = b.querySelector("span").getBoundingClientRect();
          return { opacity: s.opacity, events: s.pointerEvents, spin: getComputedStyle(svg.firstElementChild).animationName, svgW: svg.getBoundingClientRect().width, inside: label.right <= r.right + 0.5 && label.width > 0, clipped: b.scrollWidth > b.clientWidth + 1 };
        });
        assert.equal(look.opacity, "1", "readable, not faded as if off");
        assert.equal(look.events, "none", "not clicked twice");
        assert.equal(look.spin, "spin");
        assert(look.svgW > 0, "the spinner is shown");
        assert(look.inside && !look.clipped, `the label fits: ${JSON.stringify(look)}`);
        assert.equal(saves.length, 1);

        // failed: Save again, with why
        saves.shift()("upstream: model list timed out");
        await page.getByText("upstream: model list timed out").waitFor();
        await save.waitFor();
        assert.equal(await bar.locator("button.saving, button.busy").count(), 0);
        assert.equal(await save.getAttribute("aria-busy"), null);
        assert.equal((await save.textContent()).trim(), w.save);

        // again, and through: the editor closes
        await save.click();
        await busy.waitFor();
        while (!saves.length) await new Promise((r) => setTimeout(r, 20));
        saves.shift()(null);
        await page.waitForFunction(() => !document.querySelector(".editor .bar"));
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top, "saving scrolled the page");
        assert.deepEqual(errors, []);
      });
    }
  }
}
