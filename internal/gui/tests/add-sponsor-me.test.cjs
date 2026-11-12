// Run with Node's test runner and Playwright on the module path; see README.md.
// The Add sheet's Partners heading ends in "Become a sponsor" (yetone: 在右边
// 加上一个优雅精致的我要赞助的按钮): on the heading's own line, at the sheet's
// right, clear of the heading's light, and a click asks magpie to open the
// mail usemagpie.ai's "Become a sponsor" writes, that one link.
// English, Chinese, Japanese and German, at 900px and 440px; no backend.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const MAIL = "mailto:yetoneful@gmail.com?subject=Sponsoring%20magpie";
const partners = [
  { id: "acme", name: "Acme AI", icon: "generic", kind: "partner", sponsored: true, chat: "https://api.acme.example.com/v1" },
  { id: "beta", name: "Beta Relay", icon: "generic", kind: "partner", sponsored: true, chat: "https://api.beta.example.com/v1" },
];
const presets = [{ id: "openai", name: "OpenAI", icon: "openai", kind: "vendor", chat: "https://api.openai.example.com/v1" }];
const providers = [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }];

function server(lang, opened, withPartners = true) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/api/open") { opened.push(JSON.parse(req.postData() || "{}").url); return route.fulfill({ status: 204, body: "" }); }
    if (url.pathname === "/api/partner") return route.fulfill({ status: 204, body: "" });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers, presets: [...(withPartners ? partners : []), ...presets], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = { en: "Become a sponsor", zh: "我要赞助", ja: "スポンサーになる", de: "Sponsor werden" };

// where the button sits against the heading's words, its light and the sheet
const where = (page) => page.locator("#addSheet .sponsor-me").evaluate((b) => {
  const r = (e) => e.getBoundingClientRect();
  const row = b.closest(".kind-row"), k = row.querySelector(".kind"), sheet = r(b.closest("#addSheet"));
  const me = r(b), words = r(k.querySelector("b")), ring = k.querySelector("i.ring");
  const o = ring ? r(ring) : r(k);
  return {
    text: b.textContent, icon: !!b.querySelector("svg"),
    oneLine: me.top < words.bottom && me.bottom > words.top,
    right: Math.round(sheet.right - me.right), inSheet: me.left >= sheet.left && me.right <= sheet.right,
    clear: me.left >= o.right,
    gridNext: row.nextElementSibling?.classList.contains("grid"),
  };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Partners' heading ends in Become a sponsor", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [900, 440]) {
      await t.test(lang + " " + width, async () => {
        const page = await (await browser.newContext({ viewport: { width, height: 800 } })).newPage();
        const errors = [], opened = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, opened));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        await page.locator("#addSheet .kind.lit").first().waitFor();

        assert.equal(await page.locator("#addSheet .sponsor-me").count(), 1);
        const w = await where(page);
        assert.equal(w.text, L[lang]);
        assert(w.icon);
        assert(w.oneLine, "on the heading's line");
        assert(w.inSheet, "in the sheet");
        assert(w.right <= 32, "at the sheet's right: " + w.right + "px off");
        assert(w.clear, "clear of the heading's light");
        assert(w.gridNext, "the partners' tiles follow the heading's row");

        const asked = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/open");
        await page.locator("#addSheet .sponsor-me").click();
        await asked;
        assert.deepEqual(opened, [MAIL]);
        // the sheet stays open
        assert(await page.locator("#addSheet").isVisible());
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
    await t.test("no partners, no button", async () => {
      const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
      await page.route("**/*", server("en", [], false));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#addProvider").click();
      await page.locator("#addSheet .tile").first().waitFor();
      assert.equal(await page.locator("#addSheet .sponsor-me").count(), 0);
      await page.context().close();
    });
  });
}
