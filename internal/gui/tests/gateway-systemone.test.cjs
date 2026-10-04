// Run with Node's test runner and Playwright on the module path; see README.md.
// The Gateway page's Connect speaks magpie's own System One API too (ARNO on
// Discord: gateway添加system one api 有想法吗): a System One tab whose
// snippets POST …/v1/systemone with a decision model, picked from the
// decision models alone; a click on one in the list switches to it, a click
// on any other model back to OpenAI's. No SDK speaks it, so it has no
// shell variables.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const providers = {
  providers: [
    { id: "acme", name: "Acme", icon: "generic", agents: [], chat: "https://acme.test/v1", models: [
      { id: "gpt-5.5", name: "GPT-5.5", on: true, context: 400000 },
    ] },
    { id: "cf", name: "Workers AI", icon: "generic", agents: [], decide: "https://api.cloudflare.com/client/v4/accounts/a/ai/run", models: [
      { id: "@cf/typesafe/jev", name: "Jev", on: true },
      { id: "@cf/cloudflare/clef", name: "Clef", on: true },
    ] },
  ],
  gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", models: 3, calls: [], groups: [] },
};

function serve(lang, theme) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { five: "five APIs, one URL", note: "TypeSafe's decision API", hint: "A decision model (Jev, Clef)" },
  zh: { five: "五种 API，一个地址", note: "TypeSafe 的决策 API", hint: "决策模型（Jev、Clef）" },
};
const rowOf = (page, id) => page.locator("#gwModels .row.model").filter({ has: page.locator(".name", { hasText: new RegExp("^" + id.replace(/[./@]/g, "\\$&") + "$") }) });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const theme of ["light", "dark"]) {
      test(`${engine} ${lang} ${theme}: Connect speaks System One with a decision model`, async (t) => {
        assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
        const w = words[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width: 900, height: 1000 }, reducedMotion: "reduce", colorScheme: theme });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${theme}-gateway-systemone.png`), fullPage: true });
          }
          await browser.close();
        });

        await page.goto("http://magpie.test/?view=gateway&lang=curl");
        await page.locator("#gwModels .row.model").first().waitFor();
        assert(await page.getByText(w.five).first().isVisible(), "the head counts five APIs");
        const connect = page.locator("#connect");
        const modelCode = () => connect.locator(".val").nth(2).locator("code").textContent();
        const snippet = () => connect.locator("pre.snip").textContent();
        const tabs = connect.locator(".segs").first().locator(".opt");
        assert.deepEqual(await tabs.allTextContents(), ["OpenAI", "Responses", "Anthropic", "Gemini", "System One"]);
        // OpenAI's snippets never take a decision model
        assert.equal(await modelCode(), "acme/gpt-5.5");

        await tabs.filter({ hasText: "System One" }).click();
        assert.equal(await modelCode(), "cf/@cf/typesafe/jev", "a decision model");
        assert(await connect.getByText(w.note).isVisible());
        assert(await connect.getByText(w.hint).isVisible());
        const langs = await connect.locator(".segs").nth(1).locator(".opt").allTextContents();
        assert.deepEqual(langs, ["curl", "Python", "Node"], "no shell variables");
        let s = await snippet();
        assert.match(s, /curl http:\/\/127\.0\.0\.1:3999\/v1\/systemone/);
        assert.match(s, /"model": "cf\/@cf\/typesafe\/jev"/);
        assert.match(s, /"questions"/);
        assert.match(s, /Authorization: Bearer magpie/);
        await connect.locator(".segs").nth(1).locator(".opt", { hasText: "Python" }).click();
        assert.match(await snippet(), /requests\.post\("http:\/\/127\.0\.0\.1:3999\/v1\/systemone"/);
        await connect.locator(".segs").nth(1).locator(".opt", { hasText: "Node" }).click();
        assert.match(await snippet(), /fetch\("http:\/\/127\.0\.0\.1:3999\/v1\/systemone"/);

        // a click on a decision model puts it in, a click on another model
        // goes back to OpenAI's
        await rowOf(page, "cf/@cf/cloudflare/clef").click();
        assert.equal(await modelCode(), "cf/@cf/cloudflare/clef");
        assert(await rowOf(page, "cf/@cf/cloudflare/clef").evaluate((r) => r.classList.contains("selected")));
        await rowOf(page, "acme/gpt-5.5").click();
        assert.equal(await connect.locator(".segs").first().locator(".opt.on").textContent(), "OpenAI");
        assert.equal(await modelCode(), "acme/gpt-5.5");
        await rowOf(page, "cf/@cf/cloudflare/clef").click();
        assert.equal(await connect.locator(".segs").first().locator(".opt.on").textContent(), "System One");
        assert.equal(await modelCode(), "cf/@cf/cloudflare/clef", "the decision model picked is kept");
        assert.deepEqual(errors, []);
      });
    }
  }
}
