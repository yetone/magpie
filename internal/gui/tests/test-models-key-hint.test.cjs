// Run with Node's test runner and Playwright on the module path; see README.md.
// Which key Test models goes out with (water5433 on Discord): with more
// than one key ticked, its tooltip says each model is tested with the
// first ticked key that can serve it (keySeeing in internal/provider), and
// it still says so after a test has run. With one key ticked, or none
// listed, it doesn't. In every language, Chromium and WebKit, wide and
// narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["gpt-5.1", "model-b"].map((id) => ({ id, name: id, on: true }));
const base = {
  icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models, agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…one" },
  balanceToken: { takes: false, set: false }, proxy: "",
};
const keys = (second) => [
  { id: "aaaaaaaaa1", name: "Personal", masked: "sk-…a1", on: true, active: true },
  { id: "aaaaaaaaa2", name: "Team", masked: "sk-…a2", on: second },
];
const providers = [
  { ...base, id: "two", name: "Two keys", keyList: keys(true) },
  { ...base, id: "oneon", name: "One on", keyList: keys(false) },
  { ...base, id: "single", name: "Single", keyList: [] },
];

function serve(lang) {
  const list = { providers, presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(list);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/provider/test") {
      const body = route.request().postDataJSON();
      return json({ results: (body.test || []).map((model) => ({ ok: true, ms: 120, model })) });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the hint and the button, as each language words them
const words = {
  en: ["Test models", "Each model is tested with the first ticked key that can serve it"],
  zh: ["测试模型", "每个模型用第一个已勾选、能提供它的密钥测试"],
  "zh-TW": ["測試模型", "每個模型用第一個已勾選、能提供它的金鑰測試"],
  ja: [null, "各モデルは、それを提供できる最初のチェック済みキーでテストされます"],
  de: [null, "Jedes Modell wird mit dem ersten angehakten Schlüssel getestet, der es bereitstellen kann"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, [label, hint]] of Object.entries(words)) {
    for (const width of [900, 390]) {
      test(`${engine} ${lang} ${width}: Test models says which key a test goes out with`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-test-models-key-hint.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        let name = label;
        const tip = async (row) => {
          // each editor opened from a fresh list
          await page.goto("http://magpie.test/?view=providers");
          name ||= await page.evaluate(() => t("Test models"));
          await page.locator(".row.provider", { hasText: row }).first().click();
          await page.locator(".editor .mchips .mchip").first().waitFor();
          const b = page.locator(".editor .mfoot").getByRole("button", { name, exact: true });
          return { b, title: await b.getAttribute("title") };
        };

        const two = await tip("Two keys");
        assert(two.title.includes(hint), `two keys ticked: the tooltip names the key (${two.title})`);
        // a test's end puts the tooltip back whole
        await two.b.click();
        await page.waitForFunction((n) => [...document.querySelectorAll(".editor .mfoot button")].some((b) => b.textContent === n && b.title), name);
        assert((await two.b.getAttribute("title")).includes(hint), "still said after a test");

        for (const row of ["One on", "Single"]) {
          const { title } = await tip(row);
          assert(title && !title.includes(hint), `${row}: no key to choose, no hint (${title})`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
