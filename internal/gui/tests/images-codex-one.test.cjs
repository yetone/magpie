// Run with Node's test runner and Playwright on the module path; see README.md.
// #1865: chatgpt.com's Codex images backend draws the same image for
// gpt-image-2, gpt-image-2.5 or a made-up name, so Settings › Models ›
// Image generation lists a ChatGPT account's one GPT Image, its note saying
// ChatGPT picks the model, and the row says so while it draws (in its
// hover card: the line explaining it rests out of sight). Before, it
// listed GPT Image 2 and 2.5 as if they differed. A gpt-image-2.5 saved
// when it was listed (imageGenShown) shows as that entry, is ticked in the
// picker, and picking that entry again saves nothing over it. An API key's
// GPT Image 2.5 is listed as before, with no such note. Every language;
// wide and at a phone's width; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(lang, over) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], workbuddyCheckins: [], lanURLs: [],
    imageGenAuto: "codex/gpt-image-2",
    imageGenModels: [
      { id: "codex/gpt-image-2", name: "GPT Image", provider: "codex", providerName: "Codex", icon: "openai", vendorPicks: true },
      { id: "openai/gpt-image-2", name: "gpt-image-2", provider: "openai", providerName: "OpenAI", icon: "openai" },
      { id: "openai/gpt-image-2.5", name: "gpt-image-2.5", provider: "openai", providerName: "OpenAI", icon: "openai" },
    ],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, cur, posted) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: 100 }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = JSON.parse(req.postData() || "{}");
        posted.push(body);
        Object.assign(cur, body);
      }
      return json(cur);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const want = {
  en: { vendor: /^ChatGPT's image backend picks the model itself, whichever is named, so one GPT Image is listed$/, note: "ChatGPT picks the model" },
  zh: { vendor: /^ChatGPT 的生图后端自己决定用哪个模型/, note: "ChatGPT 自己决定模型" },
  "zh-TW": { vendor: /^ChatGPT 的生圖後端自己決定用哪個模型/, note: "ChatGPT 自己決定模型" },
  ja: { vendor: /^ChatGPT の画像生成バックエンドは/, note: "モデルは ChatGPT が選択" },
  de: { vendor: /^Das Bild-Backend von ChatGPT wählt das Modell selbst/, note: "ChatGPT wählt das Modell" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a ChatGPT account lists one GPT Image, said to be ChatGPT's pick", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-images-codex-one-${name}.png`), buf);
      }
      await browser.close();
    });
    const open = async (lang, cur, width, posted) => {
      const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.errors = [];
      page.on("pageerror", (e) => page.errors.push(e.message));
      await page.route("**/*", server(lang, cur, posted));
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await page.locator("#imagesList .row").nth(1).waitFor();
      return page;
    };
    const click = async (page, loc) => {
      const b = await loc.boundingBox();
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
    };
    for (const lang of Object.keys(want)) {
      for (const width of lang === "en" || lang === "zh" ? [1000, 360] : [1000]) {
        await t.test(`${lang} at ${width}px: a saved gpt-image-2.5 shows as the one GPT Image`, async () => {
          const posted = [];
          const page = await open(lang, settingsPayload(lang, { imageGen: "codex/gpt-image-2.5", imageGenShown: "codex/gpt-image-2" }), width, posted);
          const draw = page.locator("#imagesList .row").nth(1);
          const button = draw.locator("button.rt-cond");
          assert.equal((await button.textContent()).trim(), "GPT Image · Codex");
          const line = draw.locator(".image-gen-vendor");
          // the line explains the list: it rests out of sight, shown whole in the
          // row's hover card (settings-hints.test.cjs)
          assert.equal(await line.evaluate((e) => e.isConnected && getComputedStyle(e).getPropertyValue("--hint").trim()), "1", "the explanation shows at rest");
          assert.match((await line.textContent()).trim(), want[lang].vendor);
          assert.equal(await draw.locator(".image-gen-missing").count(), 0);
          const fit = await page.evaluate(() => {
            return { scroll: document.documentElement.scrollWidth - document.documentElement.clientWidth };
          });
          assert.deepEqual(fit, { scroll: 0 });
          shots.push([`${lang}-${width}`, await page.screenshot({ fullPage: true })]);

          await click(page, button);
          await page.locator("#pop").waitFor({ state: "visible" });
          const items = await page.locator("#list li:not(.group)").allInnerTexts();
          // the model rows (Automatic's note names the entry too)
          const codex = items.filter((x) => x.includes("Codex") && x.split("\n")[0] === "GPT Image");
          assert.equal(codex.length, 1, items.join(" | "));
          assert.equal(items.filter((x) => /GPT Image 2|gpt-image-2\.5[^]*Codex/.test(x)).length, 0, items.join(" | "));
          assert(codex[0].includes("GPT Image") && codex[0].includes(want[lang].note), codex[0]);
          assert.equal(items.filter((x) => x.includes("gpt-image-2.5")).length, 1, "an API key's 2.5 is still listed: " + items.join(" | "));
          assert.equal(items.filter((x) => x.includes("OpenAI") && x.includes(want[lang].note)).length, 0, "no note on an API key's");
          const cur = await page.locator("#list li.cur").allInnerTexts();
          assert.equal(cur.length, 1, cur.join(" | "));
          assert(cur[0].includes("GPT Image") && cur[0].includes("Codex"), cur[0]);
          // the same entry again: the saved value is kept
          await click(page, page.locator("#list li.cur"));
          await page.locator("#pop").waitFor({ state: "hidden" });
          assert.deepEqual(posted, []);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
      }
    }
    await t.test("Automatic on the ChatGPT account says so; an API key's pick doesn't", async () => {
      const posted = [];
      let page = await open("en", settingsPayload("en", {}), 1000, posted);
      let draw = page.locator("#imagesList .row").nth(1);
      assert.equal((await draw.locator("button.rt-cond").textContent()).trim(), "Automatic · GPT Image · Codex");
      assert.equal(await draw.locator(".image-gen-vendor").isVisible(), true);
      await page.context().close();
      page = await open("en", settingsPayload("en", { imageGen: "openai/gpt-image-2.5" }), 1000, posted);
      draw = page.locator("#imagesList .row").nth(1);
      assert.equal((await draw.locator("button.rt-cond").textContent()).trim(), "gpt-image-2.5 · OpenAI");
      assert.equal(await draw.locator(".image-gen-vendor").count(), 0);
      // picking the ChatGPT entry from there saves it
      await click(page, draw.locator("button.rt-cond"));
      await page.locator("#pop").waitFor({ state: "visible" });
      await click(page, page.locator("#list li:not(.group)", { hasText: "ChatGPT picks the model" }));
      await page.waitForFunction(() => document.querySelector("#imagesList .image-gen-vendor"));
      assert.equal(posted.at(-1).imageGen, "codex/gpt-image-2");
      assert.deepEqual(page.errors, []);
      await page.context().close();
    });
  });
}
