// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's editor sets how many images one request to it may carry
// (#6849, Yuu_jiang_ on X: 100milionseek and Coarena take 8 and refuse the
// ninth), under Advanced: folded on a provider with nothing advanced set,
// open on one with a cap. One for all its models and model=n for one,
// shown as set, sent with the Save as a map, {} when emptied, and a value
// that isn't a whole number up to 1000 refused before anything is sent.
// A subscription's editor has it too. In every language, at 900px and
// 420px.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = {
  icon: "generic", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-1", name: "Model 1", on: true }, { id: "gemini-3-flash", name: "Gemini 3 Flash", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null,
};
const plain = { ...base, id: "plain", name: "Plain", chat: "https://plain.example.com/v1" };
const capped = { ...base, id: "capped", name: "Capped", chat: "https://capped.example.com/v1", maxImages: { "gemini-3-flash": 10, "*": 8 } };
const codex = {
  ...base, id: "codex", name: "Codex", icon: "codex-color", chat: "", key: undefined,
  models: [{ id: "gpt-6", name: "GPT-6", on: true }],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
};

function serve(lang, posts) {
  const providers = { providers: [plain, capped, codex], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [900, 420]) {
      const open = async (t, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: name }).first().click();
        await page.locator(".editor details.advanced").waitFor({ state: "attached" });
        const words = await page.evaluate((l) => Object.fromEntries(["Images per request", "Save", "Images per request: {v} is not a whole number up to 1000",
          "The most images one request carries: the oldest are left out first, the latest message's kept; empty leaves it to the vendor"].map((k) => [k, l === "en" ? k : I18N[l][k]])), lang);
        for (const [k, v] of Object.entries(words)) assert(v && (lang === "en" || v !== k), `${lang} has ${k}`);
        return { page, errors, posts, words };
      };
      const fold = (page) => page.locator(".editor details.advanced");
      // an editor opened and left as it was closes on Escape with nothing
      // to discard, then opens again
      const closesClean = async (page, name) => {
        await page.keyboard.press("Escape");
        await page.locator(".editor input.max-images").waitFor({ state: "detached" });
        await page.locator(".row.provider", { hasText: name }).first().click();
        await page.locator(".editor input.max-images").waitFor({ state: "attached" });
      };
      const save = (page, words) => page.locator(".editor .bar").getByRole("button", { name: words.Save, exact: true }).click();

      test(`${engine} ${lang} ${width}px: a provider's images per request are set under Advanced`, async (t) => {
        const { page, errors, posts, words } = await open(t, "Capped");
        await fold(page).locator("input.max-images").waitFor();
        await closesClean(page, "Capped");
        assert.equal(await fold(page).evaluate((d) => d.open), true, "a cap set opens Advanced");
        const box = fold(page).locator("input.max-images");
        assert(await box.isVisible());
        assert.equal(await page.locator(".editor").getByText(words["Images per request"], { exact: true }).count(), 1, "labelled");
        assert.equal(await box.inputValue(), "8, gemini-3-flash=10", "what is set is shown, the one for all first");
        const hint = await box.evaluate((b) => document.getElementById(b.getAttribute("aria-describedby").split(" ").pop()).textContent);
        assert.equal(hint, words["The most images one request carries: the oldest are left out first, the latest message's kept; empty leaves it to the vendor"]);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "nothing runs off the side");
        for (const bad of ["lots", "1001", "8k", "model-1=2.5"]) {
          await box.fill(bad);
          await save(page, words);
          await page.locator(".editor .editor-error", { hasText: words["Images per request: {v} is not a whole number up to 1000"].replace("{v}", bad) }).waitFor();
          assert.equal(await page.evaluate(() => document.activeElement?.matches("input.max-images")), true, `${bad}: the field has the focus`);
          assert.deepEqual(posts, [], `${bad} is refused before anything is sent`);
        }
        await box.fill("6， model-1 = 12, gemini-3-flash=0");
        await save(page, words);
        await page.waitForFunction(() => !document.querySelector("input.max-images"));
        assert.deepEqual(posts.map((p) => p.action), ["save"]);
        assert.deepEqual(posts[0].body.maxImages, { "*": 6, "model-1": 12 });
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: images per request folded away until set, and emptied clears it`, async (t) => {
        const { page, errors, posts, words } = await open(t, "Plain");
        assert.equal(await fold(page).evaluate((d) => d.open), false, "nothing set: Advanced stays folded");
        assert.equal(await fold(page).locator("input.max-images").count(), 1, "the field is under Advanced");
        assert.equal(await page.locator(".editor input.max-images").isVisible(), false);
        await closesClean(page, "Plain");
        await save(page, words);
        await page.waitForFunction(() => !document.querySelector("input.max-images"));
        assert.deepEqual(posts[0].body.maxImages, {}, "an empty field saves no cap");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a subscription's editor sets images per request too`, async (t) => {
        const { page, errors, posts, words } = await open(t, "Codex");
        await fold(page).locator(":scope > summary").click();
        const box = fold(page).locator("input.max-images");
        await box.fill("4");
        await save(page, words);
        await page.waitForFunction(() => !document.querySelector("input.max-images"));
        const saved = posts.find((p) => p.action === "save");
        assert.deepEqual(saved?.body.maxImages, { "*": 4 });
        assert.deepEqual(errors, []);
      });
    }
  }
}
