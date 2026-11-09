// Run with Node's test runner and Playwright on the module path; see README.md.
// Discord #feedback: "自定义的供应商还是不能选视频模型". A custom
// provider's model marked a video model (right-click in its Model settings,
// Makes › Video model) made videos only when magpie happened to pick it:
// Magpie Image's video generation had no setting, Automatic took the first
// provider's. Settings › Models › Images now has a Video generation row,
// beside Image generation: Automatic names its pick, the picker lists every
// model that makes videos by provider, picking one saves videoGen, a pick
// that no longer makes videos says so, and with none at all the row says
// how to mark one (in sight: it is the way there). Image generation off
// takes the row away. Every language; wide and at a phone's width; no
// backend, the API is faked here.
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
    imageGenAuto: "openai/gpt-image-2",
    imageGenModels: [{ id: "openai/gpt-image-2", name: "gpt-image-2", provider: "openai", providerName: "OpenAI", icon: "openai" }],
    videoGen: "",
    videoGenAuto: "grok/grok-imagine-video",
    videoGenModels: [
      { id: "grok/grok-imagine-video", name: "Grok Imagine Video", provider: "grok", providerName: "Grok", icon: "grok" },
      { id: "studio/kling-v3", name: "kling-v3", provider: "studio", providerName: "Studio", icon: "" },
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
  en: { providers: "Providers", name: "Video generation", auto: "Automatic", how: /^To make videos with a provider's model, right-click it/, none: "no model that makes videos", missing: "missing", gone: /^studio\/kling-v3, picked here, no longer makes videos/ },
  zh: { providers: "供应商", name: "视频生成", auto: "自动", how: /类型：视频模型/, none: "没有能生成视频的模型", missing: "已失效", gone: /studio\/kling-v3[^]*Grok Imagine Video · Grok/ },
  "zh-TW": { providers: "供應商", name: "影片生成", auto: "自動", how: /類型：影片模型/, none: "沒有能生成影片的模型", missing: "已失效", gone: /studio\/kling-v3[^]*Grok Imagine Video · Grok/ },
  ja: { providers: "プロバイダ", name: "動画生成", auto: "自動", how: /種類：動画モデル/, none: "動画を生成できるモデルがありません", missing: "見つかりません", gone: /studio\/kling-v3[^]*Grok Imagine Video · Grok/ },
  de: { providers: "Anbieter", name: "Videoerzeugung", auto: "Automatisch", how: /Videomodell/, none: "kein Modell zur Videoerzeugung", missing: "fehlt", gone: /studio\/kling-v3[^]*Grok Imagine Video · Grok/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Settings picks the model Magpie Image makes videos with", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-video-gen-pick-${name}.png`), buf);
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
    const fits = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    for (const lang of Object.keys(want)) {
      const w = want[lang];
      for (const width of lang === "en" || lang === "zh" ? [1100, 400] : [1100]) {
        await t.test(`${lang} at ${width}px: Automatic names its pick, and a custom provider's video model is picked`, async () => {
          const posted = [];
          const page = await open(lang, settingsPayload(lang, {}), width, posted);
          const row = page.locator("#imagesList #videoGenRow");
          await row.waitFor();
          assert.equal((await row.locator(".name").textContent()).trim(), w.name);
          const button = row.locator("button.rt-cond");
          assert.equal((await button.textContent()).trim(), `${w.auto} · Grok Imagine Video · Grok`);
          assert.equal(await row.locator(".video-gen-how").count(), 0, "no how-to while a model makes videos");
          assert.equal(await row.locator(".video-gen-missing").count(), 0);
          assert.equal(await fits(page), 0, "no sideways scroll");
          shots.push([`${lang}-${width}`, await page.screenshot({ fullPage: true })]);

          await click(page, button);
          await page.locator("#pop").waitFor({ state: "visible" });
          const items = await page.locator("#list li:not(.group)").allInnerTexts();
          assert.equal(items.length, 3, items.join(" | "));
          assert(items[0].startsWith(w.auto), items[0]);
          assert(items.some((x) => x.includes("kling-v3") && x.includes("Studio")), items.join(" | "));
          assert(items.some((x) => x.includes("Grok Imagine Video") && x.includes("Grok")), items.join(" | "));
          await click(page, page.locator("#list li:not(.group)", { hasText: "kling-v3" }));
          await page.waitForFunction(() => document.querySelector("#videoGenRow button.rt-cond")?.textContent.includes("kling-v3"));
          assert.equal(posted.length, 1, JSON.stringify(posted));
          assert.equal(posted[0].videoGen, "studio/kling-v3");
          assert.equal((await page.locator("#videoGenRow button.rt-cond").textContent()).trim(), "kling-v3 · Studio");
          // picking Automatic again clears it
          await click(page, page.locator("#videoGenRow button.rt-cond"));
          await page.locator("#pop").waitFor({ state: "visible" });
          assert((await page.locator("#list li.cur").innerText()).includes("kling-v3"));
          await click(page, page.locator("#list li:not(.group):not(.cur)", { hasText: w.auto }));
          await page.waitForFunction(() => !document.querySelector("#videoGenRow button.rt-cond")?.textContent.includes("kling-v3"));
          assert.equal(posted.at(-1).videoGen, "");
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
        await t.test(`${lang} at ${width}px: a pick that no longer makes videos says so; none at all says how to mark one`, async () => {
          const posted = [];
          let page = await open(lang, settingsPayload(lang, { videoGen: "studio/kling-v3", videoGenMissing: "studio/kling-v3",
            videoGenModels: [{ id: "grok/grok-imagine-video", name: "Grok Imagine Video", provider: "grok", providerName: "Grok", icon: "grok" }] }), width, posted);
          let row = page.locator("#videoGenRow");
          await row.waitFor();
          assert.equal((await row.locator("button.rt-cond").textContent()).trim(), `studio/kling-v3 · ${w.missing}`);
          const gone = row.locator(".video-gen-missing");
          assert.match((await gone.textContent()).trim(), w.gone);
          assert.equal(await gone.isVisible(), true, "the error is in sight");
          assert.equal(await fits(page), 0);
          await page.context().close();

          page = await open(lang, settingsPayload(lang, { videoGenAuto: "", videoGenModels: [] }), width, posted);
          row = page.locator("#videoGenRow");
          await row.waitFor();
          assert.equal((await row.locator("button.rt-cond").textContent()).trim(), `${w.auto} · ${w.none}`);
          const how = row.locator(".video-gen-how");
          assert.match((await how.textContent()).trim(), w.how);
          assert.equal(await how.evaluate((e) => getComputedStyle(e).getPropertyValue("--hint").trim()), "", "the way to a video model shows at rest");
          assert.equal(await how.isVisible(), true);
          assert.equal(await fits(page), 0);
          shots.push([`${lang}-${width}-none`, await page.screenshot({ fullPage: true })]);
          assert.deepEqual(posted, []);
          // and the way there is a click away
          const go = how.locator("button.video-gen-go");
          assert.equal((await go.textContent()).trim(), `${w.providers} ›`);
          await go.click();
          await page.locator("#view-providers").waitFor({ state: "visible" });
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
      }
    }
    await t.test("Image generation off takes the row away", async () => {
      const page = await open("en", settingsPayload("en", { imageGen: "off" }), 1100, []);
      await page.locator("#imagesList .row").nth(1).waitFor();
      assert.equal(await page.locator("#videoGenRow").count(), 0);
      assert.deepEqual(page.errors, []);
      await page.context().close();
    });
  });
}
