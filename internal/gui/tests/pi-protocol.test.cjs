// Run with Node's test runner and Playwright on the module path; see README.md.
// xiaomanweiman on X: Pi's opened row has a Protocol row, the API magpie's
// models are asked on, picked in the app's menu (never a native select):
// each model's own (as before), OpenAI Chat Completions, OpenAI Responses
// or Anthropic Messages. The pick posts the protocol field; an api the user
// typed in models.json comes back as the value and shows as picked. The
// field is not a control on the row itself. In Chromium and WebKit, in
// every language. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const protocols = [
  { value: "", label: "Each model's own", note: "each model on the API its provider serves" },
  { value: "openai-completions", label: "OpenAI Chat Completions", note: "openai-completions" },
  { value: "openai-responses", label: "OpenAI Responses", note: "openai-responses" },
  { value: "anthropic-messages", label: "Anthropic Messages", note: "anthropic-messages" },
];
const models = [{ value: "magpie/relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie" }];
const pi = (proto) => ({
  id: "pi", name: "Pi", icon: "generic", path: "/fixture/pi", wired: true,
  fields: [
    { key: "model", label: "model", value: "magpie/relay/m1", options: models },
    { key: "protocol", label: "protocol", value: proto, options: protocols },
  ],
});

function server(lang, proto, sets) {
  let cur = { agents: [pi(proto)], profiles: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...cur, settings: { lang, theme: "light" } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents[0].fields.find((f) => f.key === body.field).value = body.value;
      return json({ ...cur, settings: { lang, theme: "light" } });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = '.row.agent[data-id="pi"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const proto of ["", "openai-responses"]) {
      test(`${engine} ${lang}: Pi's Protocol row ${proto ? "shows " + proto : "picks a protocol"}`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width: 1000, height: 800 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], sets = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, proto, sets));
        await page.goto("http://magpie.test/?view=agents");
        await page.locator(row).waitFor();
        // the words as i18n.js has them in this language
        const tr = (s) => page.evaluate(([l, s]) => (l === "en" ? s : I18N[l][s]), [lang, s]);
        for (const s of ["Protocol", "Each model's own", "each model on the API its provider serves"]) {
          assert.ok(await tr(s), `${s} has no ${lang}`);
        }

        // not a control on the row
        assert.equal(await page.locator(`${row} > * .field[data-key="protocol"]:not(.ag-protocol)`).count(), 0);
        await page.locator(`${row} .ag-link`).click();
        await page.locator(`${row} .ag-exp`).waitFor();
        const kv = page.locator(`${row} .ag-exp .ag-kv`, { has: page.locator(".ag-protocol") });
        assert.equal(await kv.count(), 1, "no Protocol row");
        assert.equal(await kv.locator(".ag-k").textContent(), await tr("Protocol"));
        const b = kv.locator(".ag-protocol");
        assert.equal(await b.locator(".v").textContent(), proto ? "OpenAI Responses" : await tr("Each model's own"));

        const y = await page.evaluate(() => scrollY);
        await b.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");
        const items = page.locator("#pop:not([hidden]) #list li");
        assert.equal(await items.count(), 4, (await items.allTextContents()).join(" | "));
        const texts = await items.allTextContents();
        const own = await tr("Each model's own"), ownNote = await tr("each model on the API its provider serves");
        assert.ok(texts.some((s) => s.includes(own) && s.includes(ownNote)), texts.join(" | "));
        assert.equal(await page.locator("select").count(), 0, "a native select");
        // the one shown as picked is the value, a hand-typed one too
        assert.deepEqual((await page.evaluate(() => pick.options.map((o) => o.value))).sort(), ["", "anthropic-messages", "openai-completions", "openai-responses"]);
        const on = page.locator("#pop:not([hidden]) #list li.cur");
        assert.equal(await on.count(), 1, "one shown as picked");
        assert.ok((await on.textContent()).includes(proto ? "OpenAI Responses" : await tr("Each model's own")), await on.textContent());

        await items.filter({ hasText: "Anthropic Messages" }).click();
        await page.waitForFunction(() => document.querySelector("#status")?.textContent);
        assert.deepEqual(sets, [{ agent: "pi", field: "protocol", value: "anthropic-messages" }]);
        await page.locator(`${row} .ag-protocol .v`, { hasText: "Anthropic Messages" }).waitFor();
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the Agents view overflows");
        assert.deepEqual(errors, []);
      });
    }
  }
}
