// Run with Node's test runner and Playwright on the module path; see README.md.
// An Image recognition or Image generation model picked in Settings › Models
// that magpie can't find any more (its provider removed or off, the model
// gone) is said where the setting shows: the picker names the model picked
// as missing, and a line under the row says what describes or draws in its
// place — the automatic choice, or nothing — and to pick another. Before,
// the picker showed the gone model's id as if it were in use, and magpie
// quietly described with its own pick. A model that is found shows as
// before. English and Chinese; wide and at a phone's width, nothing running
// off to the side; no backend, the API is faked here.
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
    visionModels: [{ id: "codex/gpt-6.1-sol", name: "GPT-6.1 Sol", provider: "codex", providerName: "Codex", icon: "openai" }],
    visionAuto: "codex/gpt-6.1-sol",
    imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, cur) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: 100 }, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
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
  en: {
    pick: "gone/qwen-vl-max · missing",
    vision: [/gone\/qwen-vl-max, picked here, isn't set up any more/, /GPT-6\.1 Sol · Codex, the automatic choice, describes images in its place/, /Pick another model here/],
    draw: [/gone\/gpt-image-2, picked here, isn't set up any more/, /No other model draws, so a request that names no model is turned away/],
    found: "GPT-6.1 Sol · Codex",
  },
  zh: {
    pick: "gone/qwen-vl-max · 已失效",
    vision: [/这里选的 gone\/qwen-vl-max 已不可用/, /现在由自动选择的 GPT-6\.1 Sol · Codex 代为描述图片/, /请在这里另选一个模型/],
    draw: [/这里选的 gone\/gpt-image-2 已不可用/, /也没有其他能生图的模型/],
    found: "GPT-6.1 Sol · Codex",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a picked image model that is gone is said in Settings", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-images-missing-${name}.png`), buf);
      }
      await browser.close();
    });
    const open = async (lang, cur, width) => {
      const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.errors = [];
      page.on("pageerror", (e) => page.errors.push(e.message));
      await page.route("**/*", server(lang, cur));
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await page.locator("#imagesList .row").nth(1).waitFor();
      return page;
    };
    for (const lang of ["en", "zh"]) {
      for (const width of [1000, 360]) {
        await t.test(`${lang} at ${width}px: the missing picks are named, with what stands in`, async () => {
          const page = await open(lang, settingsPayload(lang, {
            vision: "gone/qwen-vl-max", visionMissing: "gone/qwen-vl-max",
            imageGen: "gone/gpt-image-2", imageGenMissing: "gone/gpt-image-2",
          }), width);
          const [vision, draw] = [page.locator("#imagesList .row").nth(0), page.locator("#imagesList .row").nth(1)];
          assert.equal((await vision.locator("button.rt-cond").textContent()).trim(), want[lang].pick);
          const line = vision.locator(".vision-missing");
          assert.equal(await line.isVisible(), true);
          for (const re of want[lang].vision) assert.match(await line.textContent(), re);
          const drawLine = draw.locator(".image-gen-missing");
          assert.equal(await drawLine.isVisible(), true);
          for (const re of want[lang].draw) assert.match(await drawLine.textContent(), re);
          // the line wraps inside the row, and the page doesn't run off
          // to the side at a phone's width
          const fit = await page.evaluate(() => {
            const l = document.querySelector("#imagesList .vision-missing"), row = l.closest(".row").getBoundingClientRect(), r = l.getBoundingClientRect();
            return { inRow: r.left >= row.left - 1 && r.right <= row.right + 1, scroll: document.documentElement.scrollWidth - document.documentElement.clientWidth };
          });
          assert.deepEqual(fit, { inRow: true, scroll: 0 });
          shots.push([`${lang}-${width}`, await page.screenshot({ fullPage: true })]);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
      }
      await t.test(`${lang}: a pick that is found shows as before`, async () => {
        const page = await open(lang, settingsPayload(lang, { vision: "codex/gpt-6.1-sol" }), 1000);
        const vision = page.locator("#imagesList .row").nth(0);
        assert.equal((await vision.locator("button.rt-cond").textContent()).trim(), want[lang].found);
        assert.equal(await page.locator("#imagesList .vision-missing, #imagesList .image-gen-missing").count(), 0);
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });
    }
  });
}
