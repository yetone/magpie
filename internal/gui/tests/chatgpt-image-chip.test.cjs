// Run with Node's test runner and Playwright on the module path; see README.md.
// StarBits on X: a ChatGPT (Codex) account's editor showed its one image
// model as "gpt-image-2", read as an old model, while its backend draws
// with the model ChatGPT picks whichever is named (#1865). The chip shows
// the model's name, GPT Image, with the id as its tooltip, and unticking
// it still saves the id. A key's image model with no name of its own
// shows its id. Every language, wide and at a phone's width; the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();

function codex(hidden) {
  return {
    id: "codex", name: "Codex", icon: "codex-color", host: "", chat: "", responses: "https://chatgpt.com/backend-api/codex", anthropic: "", catalog: "",
    models: [{ id: "gpt-6-astra", name: "GPT-6-Astra", on: true }], chosen: [], fetched, agents: [], fallback: [], headers: {},
    key: { set: false, masked: "", optional: true }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
    draws: 1, drawIds: ["gpt-image-2"], drawNames: { "gpt-image-2": "GPT Image" }, drawPicks: true, hiddenDrawers: hidden,
  };
}

function openai() {
  return {
    id: "openai", name: "OpenAI", icon: "openai", host: "", chat: "https://api.openai.com/v1", responses: "", anthropic: "", catalog: "",
    models: [{ id: "gpt-6", name: "GPT-6", on: true }], chosen: [], fetched, agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…one", optional: false }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
    draws: 2, drawIds: ["gpt-image-2.6-sunburst", "gpt-image-2"], drawNames: { "gpt-image-2.6-sunburst": "GPT Image 2.6 Sunburst" }, hiddenDrawers: [],
  };
}

function serve(lang, saves) {
  let hidden = [];
  const providers = () => ({ providers: [codex(hidden), openai()], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: 100 } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/save") {
      const body = req.postDataJSON();
      saves.push(body.hiddenDrawers);
      if (body.hiddenDrawers) hidden = body.hiddenDrawers;
      return json(providers());
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const words = {
  en: { save: "Save", hint: /ChatGPT's image backend picks the GPT Image version itself/ },
  zh: { save: "保存", hint: /ChatGPT 的生图后端自己决定用哪个 GPT Image 版本/ },
  "zh-TW": { save: "儲存", hint: /ChatGPT 的生圖後端自己決定用哪個 GPT Image 版本/ },
  ja: { save: "保存", hint: /ChatGPT の画像生成バックエンドは/ },
  de: { save: "Speichern", hint: /Das Bild-Backend von ChatGPT wählt die GPT-Image-Version selbst/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a ChatGPT account's image model shows as GPT Image", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      for (const width of [1000, 360]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const saves = [];
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, saves));
          await page.goto("http://magpie.test/?view=providers");
          const editor = page.locator("#modal:not([hidden]) .editor");
          const chips = editor.locator(".mchips.drawers .mchip");
          const texts = () => chips.evaluateAll((cs) => cs.map((c) => [c.textContent, c.title]));
          const open = async (name) => {
            await page.locator(".row.provider", { hasText: name }).first().click();
            await chips.first().waitFor();
          };

          await open("Codex");
          await editor.getByText(w.hint).waitFor();
          assert.deepEqual(await texts(), [["GPT Image", "gpt-image-2"]]);
          assert.equal(await editor.getByText("gpt-image-2", { exact: true }).count(), 0, "the id isn't shown");
          await chips.first().click();
          assert.equal(await chips.first().getAttribute("aria-pressed"), "false");
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0);
          await editor.getByRole("button", { name: w.save, exact: true }).click();
          await editor.waitFor({ state: "detached" });
          assert.deepEqual(saves.at(-1), ["gpt-image-2"], "unticking saves the id");

          await open("OpenAI");
          assert.deepEqual(await texts(), [["GPT Image 2.6 Sunburst", "gpt-image-2.6-sunburst"], ["gpt-image-2", ""]]);
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }
    }
  });
}
