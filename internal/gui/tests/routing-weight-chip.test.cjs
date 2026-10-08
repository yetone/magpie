// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider routed By weight (#841) had the Routing page's stage say Smart:
// MODES had no weight entry, so the header's chip and mode paragraph fell
// back to Smart's for a trace whose keys route by weight. The chip now
// reads By weight — the provider editor's own words, already spoken in
// every language — and the story says whose share went first, while keys
// under Smart still say Smart. In English, Chinese, Japanese and German.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (minutes) => new Date(now.getTime() - minutes * 60e3).toISOString();
const key = (who, routing) => ({ id: "relay#" + who, provider: "relay", name: "Relay", who, kind: "key", model: "gpt-6-astra", routing });

// two keys of one relay, routed by weight or left to Smart
const scenes = {
  weight: { order: [key("relay-b", "weight"), key("relay-a", "weight")], why: "By weight: it's {who}'s share — each key takes requests as its weight says." },
  smart: { order: [key("relay-a", ""), key("relay-b", "")], why: "{who} goes first: keys go in their order, those that suit the request first." },
};

function serve(lang, scene) {
  const s = scenes[scene];
  const route = {
    id: 1, seq: 1, time: ago(1), agent: "codex", model: "gpt-6-astra", provider: "relay", done: true, status: 200, ms: 900, tokens: 1200,
    order: s.order, tries: [{ id: s.order[0].id, model: "gpt-6-astra", start: ago(1), done: true, status: 200, ms: 900 }],
  };
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const chip = { en: "By weight", zh: "按权重", ja: "重み付け", de: "Nach Gewicht" };
const smart = { en: "Smart", zh: "智能", ja: "スマート", de: "Intelligent" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: a request over keys routed by weight says so, not Smart`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.route("**/*", serve(lang, "weight"));
      await page.goto("http://magpie.test/?view=routing");
      // the one request is opened already
      const req = page.locator(".rt-req").first();
      await req.waitFor();
      if (await req.getAttribute("aria-pressed") !== "true") await req.click();

      // the header's chip and mode paragraph tell the provider's routing,
      // the mode in the provider editor's words
      await page.locator(".rt-hub i").filter({ hasText: chip[lang] }).waitFor();
      assert.equal((await page.locator(".rt-hub i").textContent()).trim(), chip[lang]);
      // the Accounts and keys panel's heading names the provider's routing the same way
      await page.locator(".rt-prov span").filter({ hasText: chip[lang] }).first().waitFor();
      const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => ((lang !== "en" && I18N[lang]?.[key]) || key).replace(/\{(\w+)\}/g, (_, k) => vars[k]), [lang, key, vars]);
      assert.equal((await page.locator(".rt-mode").textContent()).trim(),
        await say("Requests spread over the keys by the weight set beside each: a key weighing 3 takes three requests for every one a key weighing 1 takes, evenly over a few requests. One that fails is passed over while it rests, and the others share its requests; a conversation stays with its key as Stays says."));
      // the story says whose share went first
      await page.locator(".rt-steps li.why").first().waitFor();
      assert.equal((await page.locator(".rt-steps li.why").first().textContent()).trim(),
        await say(scenes.weight.why, { who: "relay-b" }));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: keys under Smart still say Smart`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.route("**/*", serve(lang, "smart"));
      await page.goto("http://magpie.test/?view=routing");
      const req = page.locator(".rt-req").first();
      await req.waitFor();
      if (await req.getAttribute("aria-pressed") !== "true") await req.click();
      await page.locator(".rt-hub i").filter({ hasText: smart[lang] }).waitFor();
      assert.equal((await page.locator(".rt-hub i").textContent()).trim(), smart[lang]);
      assert.equal((await page.locator(".rt-mode").textContent()).trim(),
        await page.evaluate(([lang, key]) => (lang !== "en" && I18N[lang]?.[key]) || key, [lang,
          "Smart: keys that suit the request go first — one made for the model's own API — then in their order. One resting after a failure goes last."]));
      assert.deepEqual(errors, []);
    });
  }
}
