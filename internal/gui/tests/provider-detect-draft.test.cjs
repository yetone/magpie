// Run with Node's test runner and Playwright on the module path; see README.md.
// #1545 (thedavidweng): Detect APIs and Each picked model ask with the
// form as it stands, before any Save. A custom provider being added sends
// the URL and key typed and the models typed in its Models field — Detect
// APIs as the models to try (picked), Each picked model as the models to
// ask — with no id, and nothing posts a save. What is missing (the URL, the
// models) and what went wrong are said in place under the buttons, not in
// a toast. No click moves the page, there is no native <select> and no
// left-border accent, and at 360px nothing runs off the side. In every
// language magpie speaks.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const fsp = require("node:fs/promises");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const URL_ = "https://relay.example.com/v1";

// the words as i18n.js has them
const ctx = {};
vm.runInNewContext(fs.readFileSync(path.join(assets, "i18n.js"), "utf8") + "\nthis.I18N = I18N;", ctx);
const tr = (lang, s) => (lang === "en" ? s : ctx.I18N[lang][s] || assert.fail(`${lang} has no "${s}"`));

function serve(lang, posts, answer) {
  const providers = { providers: [{ id: "anthropic", name: "Anthropic", icon: "anthropic", preset: "anthropic", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      const body = route.request().postDataJSON();
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/provider/detect") return answer(body, json);
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try { await route.fulfill({ body: await fsp.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// a relay that chats with m-a only
const one = (m, proto) => proto === "chat" && m === "m-a"
  ? { protocol: proto, ok: true, ms: 140, model: m, base: URL_ }
  : { protocol: proto, ok: false, status: 404, error: "model not found", model: m, base: proto === "anthropic" ? "https://relay.example.com" : URL_ };
const PROTOS = ["chat", "responses", "anthropic", "gemini"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [900, 360]) {
      test(`${engine} ${lang} ${width}px: Detect APIs and Each picked model ask the form not saved`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fsp.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.locator(".editor.new").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-provider-detect-draft.png`) }).catch(() => {});
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        let refuse = false, keyless = false;
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts, (body, json) => {
          if (refuse) return json({ error: "the base URL must start with http:// or https://" }, 400);
          if (body.detectModels) {
            const models = body.detectModels.map((m) => ({ model: m, results: PROTOS.map((p) => one(m, p)) }));
            return json({ results: PROTOS.map((_, i) => models.find((x) => x.results[i].ok)?.results[i] || models[0].results[i]), models });
          }
          const m = body.model || (body.picked || [])[0] || "";
          if (keyless) return json({ results: PROTOS.map((p) => ({ ...one(m, p), ok: false, status: 401, error: "invalid api key" })) });
          return json({ results: PROTOS.map((p) => one(m, p)) });
        }));
        await page.goto("http://magpie.test/?view=providers");
        const T = (s) => tr(lang, s);
        await page.locator("#addProvider").click();
        await page.locator("#addSheet .row-head .custom").click();
        const ed = page.locator(".editor.new");
        await ed.locator(".detect").waitFor();
        const detect = ed.getByRole("button", { name: T("Detect APIs"), exact: true });
        const each = ed.getByRole("button", { name: T("Each picked model"), exact: true });
        const out = ed.locator(".detect-out");
        const say = out.locator(".detect-say");
        const detects = () => posts.filter((p) => p.path === "/api/provider/detect");

        // a click that leaves the page, and the button, where they were
        const still = async (loc, what) => {
          await loc.scrollIntoViewIfNeeded();
          await page.waitForTimeout(120);
          const before = await loc.evaluate((e) => e.getBoundingClientRect().top);
          const y = await page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join());
          await loc.click();
          await page.waitForTimeout(250);
          // to half a pixel: a dialog that starts to scroll under a hint
          // lays its rows out again, WebKit's to a fraction of a pixel
          const after = await loc.evaluate((e) => e.getBoundingClientRect().top);
          assert(Math.abs(after - before) < 0.5, `${what} moved ${before} to ${after}`);
          assert.equal(await page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join()), y, what + " scrolled the page");
        };
        const inPlace = async (kind, text, what) => {
          await say.waitFor();
          assert.equal(await say.getAttribute("class"), `hint detect-say ${kind}`, what);
          assert.equal(await say.textContent(), text, what);
          // in the form, under the buttons, not a toast at the top
          const [row, hint] = await Promise.all([ed.locator(".detect-row").first().boundingBox(), say.boundingBox()]);
          assert(hint.y >= row.y + row.height - 1, what + " is under the buttons");
          assert(hint.x >= 0 && hint.x + hint.width <= width + 0.5, what + " runs off the side");
        };

        // nothing typed: the URL is asked for, in place, and nothing posted
        await still(detect, "Detect APIs with no URL");
        await inPlace("warn", T("Type the base URL first"), "no URL");
        // no model picked yet: said in place
        await still(each, "Each picked model with no models");
        await inPlace("warn", T("Pick models first, or type one and press Detect APIs"), "no models");
        // models, and still no URL
        await ed.locator(".add-models .detect-row input").fill("m-a, m-b");
        await still(each, "Each picked model with no URL");
        await inPlace("warn", T("Type the base URL first"), "no URL, each");
        assert.equal(detects().length, 0, "nothing asked without a URL or models");

        // the URL, key and models typed in the add form, never saved
        await ed.locator('input[type="url"][placeholder="https://…/v1"]').first().fill(URL_);
        await ed.locator('input[type="password"]').first().fill("sk-typed");
        await still(detect, "Detect APIs");
        await out.locator('.ep[data-api="chat"] .res.ok').waitFor();
        let sent = detects().at(-1).body;
        assert.equal(sent.key, "sk-typed", "the key typed");
        assert.equal(sent.chat, "", "the URL typed is sent as base");
        assert.equal(sent.base, URL_);
        assert.equal(sent.model, "");
        assert.deepEqual(sent.picked, ["m-a", "m-b"], "the models typed are the ones tried");
        assert(!sent.id && !sent.copyOf, "no saved provider is asked");
        assert.equal(await say.count(), 0, "the hint gives way to the answer");

        await still(each, "Each picked model");
        await out.locator(".detect-grid").waitFor();
        sent = detects().at(-1).body;
        assert.deepEqual(sent.detectModels, ["m-a", "m-b"], "each model typed is asked");
        assert.equal(sent.key, "sk-typed");
        assert.equal(await out.locator('.dg-cell.ok[data-model="m-a"][data-api="chat"]').count(), 1);
        assert.equal(await out.locator('.dg-cell.bad[data-model="m-b"][data-api="chat"]').count(), 1);

        // what went wrong, in place
        refuse = true;
        await still(detect, "Detect APIs refused");
        await inPlace("err", "the base URL must start with http:// or https://", "a refusal");
        assert.equal(await page.locator("#status").evaluate((e) => e.textContent.includes("http://")), false, "not in the toast");

        // no key typed, and nothing answers: said, as the likely cause
        refuse = false; keyless = true;
        await ed.locator('input[type="password"]').first().fill("");
        await still(detect, "Detect APIs with no key");
        await out.locator(".detect-acts .hint").waitFor();
        assert.equal(await out.locator(".detect-acts .hint").textContent(), T("None answered, and no API key is typed: paste the key under API key and detect again"));
        assert.equal(detects().at(-1).body.key, "");

        assert.equal(posts.filter((p) => p.path !== "/api/provider/detect").length, 0, `nothing but detect posted: ${posts.map((p) => p.path)}`);
        assert.equal(await page.locator("select").count(), 0, "no native select");
        const border = await page.evaluate(() => [...document.querySelectorAll(".editor.new .detect, .editor.new .detect *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
        assert.deepEqual(border, [], "no left-border accent");
        assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "no sideways scroll");
        assert.deepEqual(errors, []);
      });
    }
  }
}
