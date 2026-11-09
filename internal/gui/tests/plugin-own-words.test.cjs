// Run with Node's test runner and Playwright on the module path; see README.md.
// #3608 (MYCF711): a repository tagged magpie-plugin that says what it is
// in its own package.json (magpie.name, magpie.summary) shows that on its
// card under Discover → Unofficial, said as the package's own words: a
// chip whose tip says nobody reviewed them, and no community badge. A
// package that says nothing keeps the repository's name and GitHub's
// description; one whose summary has no English keeps GitHub's
// description in English (zh-TW reads zh, as every listing's summary
// does). Every language, both engines, wide and narrow;
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = { owner: "someone", stars: 3, license: "MIT", pushed: "2026-10-01T00:00:00Z" };
const repos = [
  { ...base, spec: "github:someone/magpie-officeace", repo: "someone/magpie-officeace", url: "https://github.com/someone/magpie-officeace",
    description: "the repository's description", package: "magpie-officeace", version: "0.3.0",
    name: "OfficeAce", summary: { en: "Unofficial: OfficeAce", zh: "非官方：OfficeAce" } },
  { ...base, spec: "github:someone/magpie-plain", repo: "someone/magpie-plain", url: "https://github.com/someone/magpie-plain",
    description: "a repository description", package: "magpie-plain", version: "1.0.0" },
  { ...base, spec: "github:someone/magpie-zhonly", repo: "someone/magpie-zhonly", url: "https://github.com/someone/magpie-zhonly",
    description: "zh-only's description", package: "magpie-zhonly", version: "1.0.0", summary: { zh: "只有中文" } },
];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins/updates") return json({ checked: "2026-10-01T00:00:00Z", waiting: [], updated: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/github") return json({ topic: "magpie-plugin", repos });
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins") {
      return json({ listings: [], state: { bun: true, bunVersion: "1.3.0", plugins: [] } });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const OWN = "Its own words";
const TIP = "Its name and summary are what the package says of itself in its package.json: nobody reviewed them";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a tagged package's own words are on its card, said as its own", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [980, 420]) {
      await t.test(lang + " " + width, async () => {
        const page = await (await browser.newContext({ viewport: { width, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=plugins");
        const [own, tip] = await page.evaluate(([l, a, b]) => l === "en" ? [a, b] : [I18N[l][a], I18N[l][b]], [lang, OWN, TIP]);
        assert.ok(own && tip, "both words are translated");
        const card = (repo) => page.locator(`#view-plugins .pm-card[data-pkg="github:someone/${repo}"]`).first();

        const office = card("magpie-officeace");
        await office.waitFor();
        assert.equal((await office.locator(".pm-name b").textContent()).trim(), "OfficeAce");
        assert.equal((await office.locator(".pm-sum").textContent()).trim(), lang.startsWith("zh") ? "非官方：OfficeAce" : "Unofficial: OfficeAce");
        const chip = office.locator(".pm-chip", { hasText: own });
        assert.equal(await chip.count(), 1);
        assert.equal(await chip.getAttribute("title"), tip);
        // the package's own words are never the community's: no badge
        assert.equal(await office.locator(".pm-verified").count(), 0);
        const box = await office.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width, `the card fits: ${JSON.stringify(box)}`);

        // a package that says nothing of itself keeps the card it had
        const plain = card("magpie-plain");
        assert.equal((await plain.locator(".pm-name b").textContent()).trim(), "magpie-plain");
        assert.equal((await plain.locator(".pm-sum").textContent()).trim(), "a repository description");
        assert.equal(await plain.locator(".pm-chip", { hasText: own }).count(), 0);

        // a summary with no English keeps GitHub's description in English
        const zh = card("magpie-zhonly");
        assert.equal((await zh.locator(".pm-sum").textContent()).trim(), lang.startsWith("zh") ? "只有中文" : "zh-only's description");
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
