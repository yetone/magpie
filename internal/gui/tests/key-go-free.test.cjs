// Run with Node's test runner and Playwright on the module path; see README.md.
// xzhih on X, #7773: an OpenCode Go key's free models are served with its
// windows used up or the key at its usage cap (v0.1.4818), and its row
// still said it was out: dimmed, "At its cap". A key at its cap that
// still serves its free models says its paid models are at the cap,
// names the free ones in its title, and isn't dimmed; a key whose models
// rest alone has a line for each, with Try now on a rest and none on a
// notice, and isn't dimmed either. A key held whole is shown as before.
// At a phone's width the rows stay inside the window. In every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const later = (h) => new Date(Date.now() + h * 36e5).toISOString();
const fresh = () => ({
  id: "go", name: "OpenCode Go", icon: "generic", chat: "https://opencode.ai/zen/go/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "glm-5.3", name: "GLM-5.3", on: true }, { id: "hy4-flash-free", name: "Hy4 Flash Free", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" },
  accountCaps: { k1: 50, k3: 50 },
  keyList: [
    { id: "k1", name: "Main", masked: "sk-…one", on: true, usageCap: true, capHeld: { used: 80, cap: 50, back: later(3), free: ["hy4-flash-free"] } },
    { id: "k2", name: "Team", masked: "sk-…two", on: true, usageCap: true, rests: [
      { why: "quota", status: 429, until: later(2), key: "go#k2/glm-5.3", model: "glm-5.3", by: "window" },
      { why: "quota", status: 429, until: later(5), key: "go#k2/kimi-k3", model: "kimi-k3", notice: true, error: "usage limit reached", at: later(-1) },
    ] },
    { id: "k3", name: "Spare", masked: "sk-…three", on: true, usageCap: true, capHeld: { used: 80, cap: 50, back: later(3) } },
  ],
  balanceToken: { takes: false, set: false }, proxy: "",
});

function serve(lang, posts) {
  const go = fresh();
  const providers = { providers: [go], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/lanes") return json({});
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/unrest") {
      posts.push(route.request().postDataJSON());
      return json({});
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { paid: "Paid models at its cap · back ", held: "At its cap · back ", free: "free models still use this key", other: "this key's other models are used as before", tryNow: "Try now" },
  zh: { paid: "付费模型已达上限 · ", held: "已达上限 · ", free: "免费模型仍用这个密钥", other: "这个密钥的其他模型照常使用", tryNow: "立即重试" },
  "zh-TW": { paid: "付費模型已達上限 · ", held: "已達上限 · ", free: "免費模型仍用這個金鑰", other: "這個金鑰的其他模型照常使用", tryNow: "立即重試" },
  ja: { paid: "有料モデルは上限に到達 · ", held: "上限に到達 · ", free: "無料モデルは引き続きこのキーを使います", other: "このキーの他のモデルはそのまま使います", tryNow: "今すぐ試す" },
  de: { paid: "Bezahlte Modelle am Limit · wieder ", held: "Am Limit · wieder ", free: "kostenlosen Modelle nutzen diesen Schlüssel weiter", other: "die übrigen Modelle dieses Schlüssels werden wie gewohnt genutzt", tryNow: "Jetzt versuchen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 400]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: an OpenCode Go key serving its free models says its paid ones are out, undimmed`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-key-go-free.png`) });
          }
          await browser.close();
        });
        page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "OpenCode Go" }).first().click();
        await page.locator(".editor .accts .acc .acap").first().waitFor({ state: "attached" });
        await page.waitForTimeout(300);
        const row = (id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
        const dimmed = (id) => row(id).evaluate((e) => e.classList.contains("resting"));

        // k1: at its cap, its free model still served
        const cap = row("k1").locator(".key-cap-line");
        assert.equal(await cap.count(), 1);
        assert((await cap.textContent()).startsWith(w.paid), await cap.textContent());
        const title = await cap.locator("span").first().getAttribute("title");
        assert(title.includes("hy4-flash-free") && title.includes(w.free), title);
        assert.equal(await dimmed("k1"), false, "a key serving its free models is dimmed");

        // k2: its paid models rest alone
        const lines = row("k2").locator(".key-model-rest");
        assert.equal(await lines.count(), 2);
        assert((await lines.nth(0).textContent()).includes("glm-5.3"), await lines.nth(0).textContent());
        assert((await lines.nth(0).locator(".key-rest").getAttribute("title")).includes(w.other));
        assert((await lines.nth(1).textContent()).includes("kimi-k3"), await lines.nth(1).textContent());
        assert((await lines.nth(1).textContent()).includes("usage limit reached"));
        assert.equal(await lines.nth(1).locator(".key-unrest").count(), 0, "a notice has Try now");
        assert.equal(await dimmed("k2"), false, "a key whose models rest alone is dimmed");

        // k3: held whole, as before
        const whole = row("k3").locator(".key-cap-line");
        assert((await whole.textContent()).startsWith(w.held), await whole.textContent());
        assert.equal(await dimmed("k3"), true, "a key held whole isn't dimmed");

        const over = await page.evaluate(() => [...document.querySelectorAll(".editor .accts .acc, .editor .key-rest-line")].filter((e) => e.getBoundingClientRect().right > innerWidth + 1).length);
        assert.equal(over, 0, "a key's row past the window's edge");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1), true, "the page scrolls sideways");

        // Try now lifts glm-5.3's rest alone
        const go = lines.nth(0).locator(".key-unrest");
        assert.equal(await go.textContent(), w.tryNow);
        await row("k2").hover();
        await go.click();
        for (let i = 0; i < 60 && !posts.length; i++) await new Promise((r) => setTimeout(r, 50));
        assert.deepEqual(posts, [{ key: "go#k2/glm-5.3" }]);
        await page.waitForTimeout(300);
        assert.equal(await row("k2").locator(".key-model-rest").count(), 1, "the lifted rest is still shown");
        assert((await row("k2").locator(".key-model-rest").textContent()).includes("kimi-k3"));

        assert.deepEqual(errors, []);
      });
    }
  }
}
