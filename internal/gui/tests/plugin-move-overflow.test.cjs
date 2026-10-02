// Run with Node's test runner and Playwright on the module path; see README.md.
// A Plugins card that offers a built-in's accounts keeps its button inside
// the card: "Move my Grok (SuperGrok) accounts (1)" ran past the card's right
// edge, and Kiro's shorter one squeezed its name to "K…" and cut "magpie
// community". The button now has a row of its own at the card's foot and
// says how many accounts it moves ("Move my 1 Grok (SuperGrok) account").
// Every card stays whole, its name and byline uncut, in a three-, two- and
// one-column window. In English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const listing = (id, name, summary) => ({
  package: `@magpie-community/opencode-${id}-auth`, name, icon: id, providers: [id], community: true, replaces: id,
  summary: { en: summary, zh: summary }, npm: { version: "0.1.4" },
});
const listings = [
  listing("factory", "Factory", "Factory Droid subscriptions: Anthropic, OpenAI, xAI and open models."),
  listing("grok", "Grok", "Use SuperGrok and X Premium+ through the Grok Build CLI's sign-in."),
  listing("cursor", "Cursor", "Cursor Pro, Pro+, Ultra and Teams: every model of the plan."),
  listing("kiro", "Kiro", "Kiro Free, Pro, Pro+ and Power: Kiro sign-in, kiro-cli's, or an API key."),
];
const movable = [
  { id: "grok", name: "Grok (SuperGrok)", package: listings[1].package, accounts: 1 },
  { id: "kiro", name: "Kiro", package: listings[3].package, accounts: 1 },
];

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [], onPlugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    // the page asks for the market in parts (#488)
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins" || url.pathname === "/api/plugins/listings") { const m = { listings, state: { bun: true, bunVersion: "1.3.0", plugins: [], movable } }; return json(url.pathname === "/api/plugins" ? m.state : url.pathname === "/api/plugins/listings" ? { listings: m.listings } : m); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const card = { en: "Move my 1 Grok (SuperGrok) account", zh: "迁移我的 1 个 Grok (SuperGrok) 账号" };
const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const [width, cols] of [[920, 3], [600, 2], [480, 1]]) {
      test(`${engine} ${lang} ${width}px: a card's move button stays inside it`, async (t) => {
        const browser = await launch(engine);
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=plugins");
        await page.locator("#view-plugins button", { hasText: card[lang] }).waitFor();
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-move-overflow-${engine}-${lang}-${width}.png`) });

        const cards = await page.locator("#view-plugins .pm-card").evaluateAll((cs) => cs.map((c) => {
          const box = c.getBoundingClientRect();
          const out = [...c.querySelectorAll(".pm-top *")].filter((e) => {
            const r = e.getBoundingClientRect();
            return r.width && (r.left < box.left || r.right > box.right);
          }).map((e) => e.className || e.tagName);
          const cut = [...c.querySelectorAll(".pm-name b, .pm-by, .pm-by > span")].filter((e) => e.scrollWidth > e.clientWidth).map((e) => e.textContent);
          return { name: c.querySelector(".pm-name b")?.textContent, out, cut };
        }));
        assert.ok(cards.length >= listings.length, "the market's cards aren't there");
        const lefts = await page.locator("#view-plugins .pm-card").evaluateAll((cs) => new Set(cs.map((c) => Math.round(c.getBoundingClientRect().left))).size);
        assert.equal(lefts, cols, `not ${cols} columns at ${width}px`);
        const wrong = cards.flatMap((c) => [...(c.out.length ? [`${c.name}: runs past its card`] : []), ...c.cut.map((x) => `${c.name}: "${x}" is cut`)]);
        assert.deepEqual(wrong, []);
        assert.deepEqual(errors, []);
      });
    }
  }
}
