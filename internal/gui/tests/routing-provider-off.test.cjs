// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's member whose provider is switched off (groupsState's
// providerOff) is said as that, by its provider's name and model, not as a
// bare id nothing serves: on the group's card, on a manual group's pick
// and in the editor, where its row is greyed and says why — the group skips
// it until the provider is on again (Discord: a found group's members of
// a provider switched off or removed stayed, to be taken out by hand).
// One whose provider is gone still says no provider serves it. Every
// language, 1100 and 420 wide; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ id: "a/sol", name: "sol", providerName: "A Cloud", icon: "generic" }];
const info = [
  { id: "a/sol", ready: true, provider: "a", name: "A Cloud", model: "sol", on: 1 },
  { id: "b/sol", ready: false, providerOff: true, provider: "b", name: "B Cloud", icon: "generic", model: "sol", on: 0 },
  { id: "c/sol", ready: false, on: 0 },
];
const base = { members: ["a/sol", "b/sol", "c/sol"], ready: true, memberInfo: info, offers: [], shared: [] };
const groups = { models, pools: [], groups: [
  { ...base, id: "auto-sol", name: "Sol", routing: "order" },
  { ...base, id: "hand", name: "Hand", routing: "manual", pick: "a/sol", picked: "a/sol" },
] };

function serve(lang) {
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: a member whose provider is switched off is said as that`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");
        const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
          const s = (lang !== "en" && I18N[lang]?.[key]) || key;
          return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
        }, [lang, key, vars]);
        const off = await say("switched off"), why = await say("{name} is switched off: the group skips it until it is on again", { name: "B Cloud" });
        assert.notEqual(why, "", "the string");
        if (lang !== "en") assert.ok(!why.startsWith("{name} is switched off"), `${lang} has it in its own words`);

        const card = page.locator(".rt-group", { hasText: "Sol" }).first();
        await card.waitFor();
        assert.equal(await card.locator(".mem").first().textContent(), `A Cloud · sol → B Cloud · sol (${off}) → c/sol`);
        // a manual group's pick says why it is skipped
        const hand = page.locator(".rt-group", { hasText: "Hand" });
        const pick = hand.locator('.rt-pick[data-member="b/sol"]');
        assert.equal(await pick.locator(".n").textContent(), "sol");
        assert.equal(await pick.locator("small").textContent(), `B Cloud · ${off}`);
        assert.equal(await pick.getAttribute("title"), why);
        assert.equal(await pick.evaluate((b) => b.classList.contains("off")), true);
        assert.equal(await hand.locator('.rt-pick[data-member="c/sol"]').getAttribute("title"), await say("No provider serves {id} now; it is skipped", { id: "c/sol" }));

        // the editor: its row muted, saying why
        // the page puts back a scroll the reader didn't make, so at 420 the
        // button is clicked where it is
        await card.locator("button", { hasText: await say("Edit") }).dispatchEvent("click");
        const ed = page.locator(".rt-gedit");
        const row = ed.locator(".fbrow", { has: page.locator(".n small", { hasText: `B Cloud · ${off}` }) });
        assert.equal(await row.count(), 1);
        assert.equal(await row.getAttribute("title"), why);
        assert.equal(await row.evaluate((r) => r.classList.contains("off")), true);
        assert.equal(await row.locator(".n > span").first().textContent(), "sol");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, "nothing spills");
        assert.deepEqual(errors, []);
      });
    }
  }
}
