// Run with Node's test runner and Playwright on the module path; see README.md.
// A custom provider's Balance URL is asked with GET unless the user picks
// POST beside it, for a balance endpoint that answers only a POST (#2368,
// yaofeng): Check balance and Save both carry the pick, a provider saved
// with POST opens on POST, and the control is named in every language and
// fits beside the URL at a narrow width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const { I18N } = strings();
const tr = (lang, s) => (lang === "en" ? s : I18N[lang]?.[s]);
const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [],
  balanceURL: "https://relay.example.com/api/check-balance", balancePath: "$usd_balance",
};

function server(posted, lang, provider) {
  const all = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(all);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      const body = route.request().postDataJSON();
      posted.push({ action: url.pathname.slice("/api/provider/".length), body });
      if (url.pathname === "/api/provider/balance") return json({ ok: true, amount: "$0.94" });
      return json(all);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Balance URL is asked with the method picked beside it", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 420]) {
      for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
        for (const saved of ["", "POST"]) {
          await t.test(`${width}px ${lang} saved ${saved || "GET"}`, async () => {
            const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
            const page = await context.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [], posted = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", server(posted, lang, saved ? { ...relay, balanceMethod: saved } : relay));
            await page.goto("http://magpie.test/?view=providers");
            await page.locator(".row.provider", { hasText: "Relay" }).click();
            // a saved Balance URL opens Advanced, where it is asked
            assert(await page.locator(".editor details.advanced").evaluate((d) => d.open), "Advanced is open");

            const method = page.locator(".editor .bal-method");
            await method.waitFor();
            const name = tr(lang, "Balance request method");
            const tip = tr(lang, "How the Balance URL is asked: GET, or POST with no body for a vendor that answers only a POST");
            assert(name && tip, `${lang} has both new strings`);
            assert.equal(await method.getAttribute("aria-label"), name);
            assert.equal(await method.getAttribute("title"), tip);
            const pressed = async () => method.locator(".opt[aria-pressed=true]").textContent();
            assert.equal(await pressed(), saved || "GET", "it opens on the saved method");

            const layout = await page.locator(".editor .bal-ask").evaluate((box) => {
              const b = box.getBoundingClientRect();
              const inside = (e) => { const r = e.getBoundingClientRect(); return r.left >= b.left - 1 && r.right <= b.right + 1 && r.width > 0; };
              return {
                segs: inside(box.querySelector(".segs")),
                url: inside(box.querySelector("input")) && box.querySelector("input").getBoundingClientRect().width >= 120,
                page: document.documentElement.scrollWidth <= innerWidth,
              };
            });
            assert.deepEqual(layout, { segs: true, url: true, page: true }, "the method and the URL fit side by side");

            // pick the other one, then ask and save
            const to = saved ? "GET" : "POST";
            await method.getByRole("button", { name: to, exact: true }).click();
            assert.equal(await pressed(), to);
            assert.equal(posted.length, 0, "picking a method changes only the draft");
            const check = page.locator(".editor").getByRole("button", { name: tr(lang, "Check balance"), exact: true });
            const asked = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/provider/balance");
            await check.click();
            await asked;
            const want = to === "POST" ? "POST" : "";
            assert.equal(posted.find((p) => p.action === "balance").body.balanceMethod, want, "Check balance asks as the form has it");
            const saveName = tr(lang, "Save");
            const response = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/provider/save");
            await page.locator(".editor .bar").getByRole("button", { name: saveName, exact: true }).click();
            await response;
            const body = posted.find((p) => p.action === "save").body;
            assert.equal(body.balanceMethod, want, "Save keeps the method");
            assert.equal(body.balanceURL, relay.balanceURL);
            assert.deepEqual(errors, []);
            await context.close();
          });
        }
      }
    }
  });
}
