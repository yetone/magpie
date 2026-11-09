// Run with Node's test runner and Playwright on the module path; see README.md.
// 321sssrt on X: "openai有几十个图片模型，我没办法像是llm一样屏蔽他们". A
// provider's editor listed its image models as plain chips nothing could
// change. Now each is ticked, a click unticks it (and ticks it back), and
// Save writes the unticked ones as hiddenDrawers, which Settings › Models › Images
// then leaves out. A pick in Settings › Models › Images that was unticked since
// still draws, and its row says so and how to keep it listed. Every
// language, wide and at a phone's width; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const drawIds = ["gpt-image-2", "gpt-image-1.5", "dall-e-3"];

function providerFor(hidden) {
  return {
    id: "openai", name: "OpenAI", icon: "openai", host: "", chat: "https://api.openai.com/v1", responses: "", anthropic: "", catalog: "",
    models: [{ id: "gpt-6", name: "GPT-6", on: true }], chosen: [], fetched, agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…one", optional: false }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
    draws: drawIds.length, drawIds, hiddenDrawers: hidden,
  };
}

function settingsPayload(lang, over) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [
      { id: "openai/gpt-image-2", name: "gpt-image-2", provider: "openai", providerName: "OpenAI", icon: "openai" },
      { id: "openai/dall-e-3", name: "dall-e-3", provider: "openai", providerName: "OpenAI", icon: "openai" },
    ],
    imageGenAuto: "openai/gpt-image-2", workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function serve(lang, saves, cur) {
  let hidden = ["dall-e-3"];
  const providers = () => ({ providers: [providerFor(hidden)], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: 100 } });
    if (url.pathname === "/api/settings") return json(cur);
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
  en: { save: "Save", field: "Image models", hint: /Settings → Models → Images lists the ticked ones/, note: [/dall-e-3 · OpenAI, picked here, is unticked/, /it still draws/] },
  zh: { save: "保存", field: "图像模型", hint: /设置 → 模型 → 图像 只列出已勾选的/, note: [/dall-e-3 · OpenAI 是此处选中的/, /它仍用于生图/] },
  "zh-TW": { save: "儲存", field: "影像模型", hint: /設定 → 模型 → 影像 只列出已勾選的/, note: [/dall-e-3 · OpenAI 是此處選取的/, /它仍用於生圖/] },
  ja: { save: "保存", field: "画像モデル", hint: /設定 → モデル → 画像 にはチェックしたものだけ/, note: [/ここで選んだ dall-e-3 · OpenAI は/, /描画には引き続き使われます/] },
  de: { save: "Speichern", field: "Bildmodelle", hint: /Einstellungen → Modelle → Bilder listet die angehakten/, note: [/dall-e-3 · OpenAI, hier gewählt, ist/, /Es zeichnet weiter/] },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a provider's image models can be unticked, and a hidden pick says so", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (lang, width, view, saves, cur) => {
      const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.errors = [];
      page.on("pageerror", (e) => page.errors.push(e.message));
      await page.route("**/*", serve(lang, saves, cur));
      await page.goto("http://magpie.test/?view=" + view);
      return page;
    };
    const noSideScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      for (const width of [1000, 360]) {
        await t.test(`${lang} at ${width}px: the editor unticks and ticks back an image model`, async () => {
          const saves = [];
          const page = await open(lang, width, "providers", saves, settingsPayload(lang, {}));
          const editor = page.locator("#modal:not([hidden]) .editor");
          const openEditor = async () => {
            await page.locator(".row.provider", { hasText: "OpenAI" }).first().click();
            await editor.locator(".mchips.drawers .mchip").first().waitFor();
          };
          const chips = editor.locator(".mchips.drawers .mchip");
          const ticked = () => editor.locator(".mchips.drawers .mchip.on").evaluateAll((cs) => cs.map((c) => c.textContent));
          const chip = (id) => editor.locator(".mchips.drawers .mchip", { has: page.locator(`span:text-is("${id}")`) });
          await openEditor();
          await editor.getByText(w.field, { exact: true }).first().waitFor();
          await editor.getByText(w.hint).waitFor();
          assert.equal(await chips.count(), 3);
          assert.deepEqual(await ticked(), ["gpt-image-2", "gpt-image-1.5"], "the saved hidden one opens unticked");
          assert.equal(await chip("dall-e-3").getAttribute("aria-pressed"), "false");
          await chip("gpt-image-1.5").click();
          assert.deepEqual(await ticked(), ["gpt-image-2"]);
          await chip("dall-e-3").click();
          assert.deepEqual(await ticked(), ["gpt-image-2", "dall-e-3"]);
          assert.equal(await noSideScroll(page), 0);
          await editor.getByRole("button", { name: w.save, exact: true }).click();
          await editor.waitFor({ state: "detached" });
          assert.deepEqual(saves.at(-1), ["gpt-image-1.5"]);
          await openEditor();
          assert.deepEqual(await ticked(), ["gpt-image-2", "dall-e-3"], "reopened as saved");
          assert.equal(await page.locator("select").count(), 0);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        await t.test(`${lang} at ${width}px: Settings › Models › Images says a hidden pick still draws`, async () => {
          const page = await open(lang, width, "settings&tab=models", [], settingsPayload(lang, { imageGen: "openai/dall-e-3", imageGenHidden: true }));
          await page.locator("#imageGenRow").waitFor();
          const note = page.locator("#imageGenRow .image-gen-hidden");
          assert.equal(await note.isVisible(), true);
          for (const re of w.note) assert.match(await note.textContent(), re);
          const fit = await page.evaluate(() => {
            const l = document.querySelector("#imageGenRow .image-gen-hidden"), row = l.closest(".row").getBoundingClientRect(), r = l.getBoundingClientRect();
            return r.left >= row.left - 1 && r.right <= row.right + 1;
          });
          assert.equal(fit, true, "the note runs out of its row");
          assert.equal(await noSideScroll(page), 0);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
      }
      await t.test(`${lang}: a pick that isn't hidden has no note`, async () => {
        const page = await open(lang, 1000, "settings&tab=models", [], settingsPayload(lang, { imageGen: "openai/gpt-image-2" }));
        await page.locator("#imageGenRow").waitFor();
        assert.equal(await page.locator("#imageGenRow .image-gen-hidden").count(), 0);
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });
    }
  });
}
