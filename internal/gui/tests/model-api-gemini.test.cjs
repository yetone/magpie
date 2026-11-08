// Run with Node's test runner and Playwright on the module path; see README.md.
// A custom provider with a Gemini URL beside its chat and Anthropic ones
// (Kayphoon on Discord: 供应商为自定义的时候已经兼容 gemini 格式了，但是在下方
// 模型的详细配置里还是只能限定 chat 和 anthropic): a model can be asked on
// Gemini alone, from its chip's right-click and from Model settings' API
// row alike, and the Save sends it as modelPrefs. Responses, which it has
// no URL for, isn't offered. In every language, wide and at a phone's
// width, where the row's API choice stays inside the editor and grows to
// hold its options when they wrap.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = { icon: "generic", catalog: "", agents: [], fallback: [], headers: {}, keyList: [], balanceToken: { takes: false, set: false }, proxy: "" };
const relay = {
  ...base, id: "relay", name: "Relay", chat: "https://relay.example.com/v1", responses: "", anthropic: "https://relay.example.com", gemini: "https://relay.example.com/v1beta",
  models: [{ id: "gemini-2.5-pro", name: "gemini-2.5-pro", on: true }, { id: "glm-5", name: "glm-5", on: true }],
  key: { set: true, masked: "sk-…one" },
};

function serve(lang, posts) {
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/save") { posts.push(route.request().postDataJSON()); return json(providers); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { auto: "Asked on: Auto…", staged: "gemini-2.5-pro is asked on Gemini once saved" },
  zh: { auto: "协议：自动…", staged: "保存后 gemini-2.5-pro 将使用 Gemini 协议" },
  "zh-TW": { auto: "協議：自動…", staged: "儲存後 gemini-2.5-pro 將使用 Gemini 協議" },
  ja: { auto: "API：自動…", staged: "保存後、gemini-2.5-pro は Gemini で要求されます" },
  de: { auto: "Angefragt über: Automatisch…", staged: "gemini-2.5-pro wird nach dem Speichern über Gemini angefragt" },
};

async function rclick(loc) {
  await loc.click({ button: "right", trial: true });
  await loc.page().evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  await loc.click({ button: "right" });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 360]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a custom provider's model asked on Gemini`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-model-api-gemini.png`), fullPage: true });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        await page.locator(".editor .mchips .mchip").first().waitFor();
        const chip = (id) => page.locator(".editor .mchips .mchip", { hasText: id });

        // the chip's menu offers the provider's three APIs, Gemini among them
        await rclick(chip("gemini-2.5-pro"));
        await page.locator(".pop.row-menu").getByRole("menuitem", { name: w.auto }).click();
        const apiMenu = page.locator(".pop.proto-menu.model-api-menu");
        await apiMenu.waitFor();
        const names = (await apiMenu.locator(".pm-name").allTextContents()).slice(1);
        assert.deepEqual(names, ["OpenAI", "Anthropic", "Gemini"], "the APIs it has a URL for, Responses not");
        await apiMenu.locator(".pm-item", { hasText: "Gemini" }).click();
        await apiMenu.waitFor({ state: "detached" });
        assert.equal(await chip("gemini-2.5-pro").locator(".mapi-tag").textContent(), "Gemini");
        assert.equal(await page.locator("#status").textContent(), w.staged);
        assert.equal(posts.length, 0, "staged, not saved");

        // Model settings' API row shows it, and has Gemini for the other model
        await page.evaluate(() => [...document.querySelectorAll(".editor button.text.action")].find((b) => b.closest(".editor") && /Model settings|模型设置|模型設定|モデル設定|Modelleinstellungen/.test(b.textContent)).click());
        const row = (id) => page.locator(".editor .mnames .mname", { has: page.locator(`input[placeholder="${id}"]`) });
        await row("glm-5").locator(".mapi").waitFor();
        assert.deepEqual(await row("glm-5").locator(".mapi .opt").evaluateAll((os) => os.map((o) => o.dataset.api)), ["", "chat", "anthropic", "gemini"]);
        assert.equal(await row("gemini-2.5-pro").locator(".mapi .opt.on").getAttribute("data-api"), "gemini");
        await row("glm-5").locator('.mapi .opt[data-api="gemini"]').click();
        assert.equal(await row("glm-5").locator(".mapi .opt.on").getAttribute("data-api"), "gemini");
        // inside the editor at a phone's width: no page scrolling sideways
        const over = await page.evaluate(() => {
          const ed = document.querySelector(".editor").getBoundingClientRect();
          return [...document.querySelectorAll(".editor .mnames .mapi")].map((m) => m.getBoundingClientRect()).filter((r) => r.right > ed.right + 1 || r.left < ed.left - 1).length
            + (document.documentElement.scrollWidth > innerWidth + 1 ? 100 : 0);
        });
        assert.equal(over, 0, "the API choice stays inside the editor");
        // its options inside its own box, a second row of them too: a
        // phone's 44px options hung out of a 24px box over the levels
        const out = await row("glm-5").locator(".mapi").evaluate((m) => {
          const b = m.getBoundingClientRect();
          return [...m.querySelectorAll(".opt")].filter((o) => { const r = o.getBoundingClientRect(); return r.bottom > b.bottom + 0.5 || r.right > b.right + 0.5; }).map((o) => o.dataset.api);
        });
        assert.deepEqual(out, [], "every option inside the API choice");

        await page.locator(".editor button.text.primary").last().click();
        for (let i = 0; i < 40 && !posts.length; i++) await page.waitForTimeout(50);
        assert.deepEqual(posts[0]?.modelPrefs, { "gemini-2.5-pro": { api: "gemini" }, "glm-5": { api: "gemini" } });
        assert.deepEqual(errors, []);
      });
    }
  }
}
