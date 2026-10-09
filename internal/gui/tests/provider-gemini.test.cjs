// Run with Node's test runner and Playwright on the module path; see README.md.
// A custom provider's Base URL can be a Gemini API (#1346, NagaseMinato):
// "Gemini compatible" beside the other APIs. A provider saved with a Gemini
// URL opens on it, and Detect asks a Gemini API too (NagaseMinato: 「检测」
// 不会探测 Gemini 端点，为什么呢？): the URL typed is sent as the base, its
// Gemini row shown with the others, and Use these keeps Gemini's URL;
// picked for a provider that has a chat URL, the Gemini URL typed is saved
// as its own, the chat URL kept, and the Base URL said to be Gemini's. The
// pick and Detect's rows fit the editor at a narrow width. In English,
// Chinese, Japanese and German, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const U = "https://relay.example.com/v1";
const G = "https://relay.example.com/proxy/v1beta";

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = [{ id: "gemini-2.5-pro", name: "Gemini 2.5 Pro", on: true }];
  const base = { icon: "generic", anthropic: "", responses: "", models, agents: [], fallback: [], headers: {}, key: { set: true, masked: "AIza…1234" }, keyList: [], ready: true };
  const providers = { providers: [
    { ...base, id: "gem", name: "Gem", chat: "", gemini: G, baseAPI: "gemini" },
    { ...base, id: "plain", name: "Plain", chat: U },
  ], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/detect") {
      const body = route.request().postDataJSON();
      posts.push({ path: url.pathname, body });
      return json({ results: [
        { protocol: "chat", ok: false, status: 404, error: "Not Found", model: "gemini-2.5-pro", base: G },
        { protocol: "responses", ok: false, status: 404, error: "Not Found", model: "gemini-2.5-pro", base: G },
        { protocol: "anthropic", ok: false, status: 404, error: "Not Found", model: "gemini-2.5-pro", base: G },
        { protocol: "gemini", ok: true, ms: 412, model: "gemini-2.5-pro", base: G },
      ] });
    }
    if (url.pathname.startsWith("/api/provider/")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

const L = {
  en: { gemini: "Gemini compatible", openai: "OpenAI compatible", save: "Save", more: "Gemini URL", detect: "Detect APIs", use: "Use these" },
  zh: { gemini: "Gemini 兼容", openai: "OpenAI 兼容", save: "保存", more: "Gemini 地址", detect: "检测协议", use: "使用检测结果" },
  ja: { gemini: "Gemini 互換", openai: "OpenAI 互換", save: "保存", more: "Gemini URL", detect: "API を検出", use: "これを使う" },
  de: { gemini: "Gemini-kompatibel", openai: "OpenAI-kompatibel", save: "Speichern", more: "Gemini-URL", detect: "APIs erkennen", use: "Diese verwenden" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(L)) {
    test(`${engine} ${lang}: a custom provider's Base URL can be a Gemini API`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const S = L[lang];
      const ed = page.locator(".editor");
      const open = async (id) => {
        if (await ed.count()) { await page.keyboard.press("Escape"); await ed.waitFor({ state: "detached" }); }
        await page.locator(`#providers .row[data-id="${id}"]`).click();
        await ed.locator(".base-url").waitFor();
      };
      const on = () => ed.locator(".segs:has(.opt[data-api]) .opt.on").textContent();
      const detect = ed.locator(".detect");

      // saved with a Gemini URL: opens on it, and Detect asks it
      await open("gem");
      assert.equal(await on(), S.gemini);
      assert.equal(await ed.locator(".base-url").inputValue(), G);
      assert.equal(await ed.locator(".base-url").getAttribute("placeholder"), "https://…/v1beta");
      assert.equal(await detect.isVisible(), true, "Detect is there for a Gemini URL");
      await detect.getByRole("button", { name: S.detect, exact: true }).click();
      const gemRow = detect.locator('.detect-out .ep[data-api="gemini"]');
      await gemRow.locator(".res.ok").waitFor();
      const asked = posts.find((p) => p.path === "/api/provider/detect");
      assert.equal(asked.body.base, G, "the URL typed is the base each API is asked at");
      assert.equal(asked.body.gemini, "", "the Gemini field is the URL typed, so it is asked as Gemini takes it");
      assert.equal(await detect.locator(".detect-out .ep").count(), 4);
      assert.equal(await gemRow.locator(".pl").textContent(), "Gemini");
      for (const width of [900, 440]) {
        await page.setViewportSize({ width, height: 760 });
        await page.waitForTimeout(100);
        const over = await detect.evaluate((d) => {
          const er = d.closest(".editor").getBoundingClientRect();
          return [...d.querySelectorAll(".detect-out .ep, .detect-row > *")].map((e) => e.getBoundingClientRect().right - er.right).filter((x) => x > 1);
        });
        assert.deepEqual(over, [], `${width}px: Detect's rows overflow the editor`);
      }
      await page.setViewportSize({ width: 900, height: 760 });
      await detect.getByRole("button", { name: S.use, exact: true }).click();
      assert.equal(await on(), S.gemini, "Gemini answered: the Base URL stays Gemini's");
      assert.equal(await ed.locator(".base-url").inputValue(), G);

      // picked for a provider with a chat URL: the Gemini URL is its own
      await open("plain");
      assert.equal(await on(), S.openai);
      assert.equal(await detect.isVisible(), true);
      await ed.locator('.segs .opt[data-api="gemini"]').click();
      assert.equal(await on(), S.gemini);
      assert.equal(await ed.locator(".base-url").inputValue(), "", "the saved chat URL stays chat's");
      assert.equal(await detect.isVisible(), true);
      await ed.locator("details.more > summary").click();
      const labels = await ed.locator("details.more label").allTextContents();
      assert(!labels.includes(S.more), "the Base URL's API is not under More endpoints: " + labels);
      await ed.locator(".base-url").fill(G);

      // back on OpenAI compatible, the Gemini URL is one of More endpoints
      await ed.locator('.segs .opt[data-api="openai"]').click();
      assert((await ed.locator("details.more label").allTextContents()).includes(S.more));
      await ed.locator('.segs .opt[data-api="gemini"]').click();
      assert.equal(await ed.locator(".base-url").inputValue(), G);

      // wide and narrow: the five APIs fit the editor, wrapping where they
      // must, and the thumb is under the one picked, on its row
      for (const width of [900, 440]) {
        await page.setViewportSize({ width, height: 760 });
        await page.waitForTimeout(100);
        await ed.locator('.segs .opt[data-api="openai"]').click();
        await ed.locator('.segs .opt[data-api="gemini"]').click();
        await page.waitForTimeout(350);
        const fit = await ed.locator(".segs:has(.opt[data-api])").evaluate((s) => {
          const r = (e) => e.getBoundingClientRect();
          const on = r(s.querySelector(".opt.on")), th = r(s.querySelector(".thumb")), ed = r(s.closest(".editor"));
          return { over: r(s).right - ed.right, cut: [...s.querySelectorAll(".opt")].filter((b) => b.scrollWidth > b.clientWidth + 1 || r(b).right > ed.right).map((b) => b.textContent),
            off: Math.max(Math.abs(on.left - th.left), Math.abs(on.top - th.top), Math.abs(on.width - th.width), Math.abs(on.height - th.height)) };
        });
        assert(fit.over <= 1, `${width}px: the APIs overflow the editor by ${fit.over}px`);
        assert.deepEqual(fit.cut, [], `${width}px: cut`);
        assert(fit.off <= 1, `${width}px: the thumb is ${fit.off}px off the API picked`);
      }

      // saved: the Gemini URL its own, chat's kept, the Base URL Gemini's
      await ed.locator(":scope > .bar").getByRole("button", { name: S.save, exact: true }).click();
      for (let i = 0; i < 60 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(50);
      const save = posts.find((p) => p.path === "/api/provider/save");
      assert(save, "saved");
      assert.equal(save.body.baseAPI, "gemini");
      assert.equal(save.body.gemini, G);
      assert.equal(save.body.chat, U, "chat's URL kept");
      assert.deepEqual(errors, []);
    });
  }
}
