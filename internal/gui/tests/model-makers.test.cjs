// Run with Node's test runner and Playwright on the module path; see README.md.
// The provider editor's models stand by maker, the newest first
// (WJaunita45604 on X: 供应商的模型里面应该支持默认按厂商分类，并且按模型里的数字
// 从大到小排序). A relay serving several makers: each maker's name over its
// models, makers in a fixed order (Anthropic, OpenAI, Google, DeepSeek…),
// one magpie doesn't know under Other, last, in the vendor's order; within a maker the version
// in its name decides, number by number (gpt-6.1 over gpt-5.10 over
// gpt-5.9), a release before its preview, the alias before its snapshot.
// A list of one maker has no labels. Labels are plain small text, no
// stripe; clicking a chip picks it and scrolls nothing. Every language,
// Chromium and WebKit, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const fetched = new Date(Date.now() - 3600e3).toISOString();
const base = {
  icon: "generic", responses: "", anthropic: "", catalog: "", chosen: [], fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const m = (id, maker, name) => ({ id, ...(name ? { name } : {}), on: false, ...(maker ? { maker } : {}) });
// the vendor's order, mixed on purpose
const relay = {
  ...base, id: "relay", name: "Relay", host: "relay.example.com", chat: "https://relay.example.com/v1",
  models: [
    m("llama-4-maverick"),
    m("gpt-5.9", "openai"),
    m("deepseek-v3.2", "deepseek-color"),
    m("claude-opus-4-6", "claude-color"),
    m("gpt-5.10", "openai"),
    m("gemini-3-pro-preview", "gemini-color"),
    m("gpt-5.5", "openai"),
    m("claude-opus-5-5", "claude-color"),
    m("gemini-3-pro", "gemini-color"),
    m("deepseek-v4.1", "deepseek-color"),
    m("gpt-6.1", "openai"),
    m("claude-sonnet-4-5-20250929", "claude-color"),
    m("claude-sonnet-4-5", "claude-color"),
    m("glm-plan", "zhipu-color", "GLM-5.3"),
    m("glm-4.7", "zhipu-color"),
    m("mystery-model"),
    // a maker magpie can't place keeps the vendor's order: its numbers are
    // no version of the others'
    m("zeta-9"),
  ],
};
const one = {
  ...base, id: "solo", name: "Solo", host: "solo.example.com", chat: "https://solo.example.com/v1",
  models: [m("gpt-5.9", "openai"), m("gpt-6.1", "openai"), m("gpt-5.10", "openai")],
};

function serve(lang) {
  const providers = { providers: [relay, one], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const other = { en: "Other", zh: "其他", "zh-TW": "其他", ja: "その他", de: "Sonstige" };
const zhipu = { en: "Zhipu", zh: "智谱", "zh-TW": "智譜", ja: "Zhipu", de: "Zhipu" };
const want = (lang) => [
  ["Anthropic"], "claude-opus-5-5", "claude-opus-4-6", "claude-sonnet-4-5", "claude-sonnet-4-5-20250929",
  ["OpenAI"], "gpt-6.1", "gpt-5.10", "gpt-5.9", "gpt-5.5",
  ["Google"], "gemini-3-pro", "gemini-3-pro-preview",
  ["DeepSeek"], "deepseek-v4.1", "deepseek-v3.2",
  [zhipu[lang]], "GLM-5.3", "glm-4.7",
  [other[lang]], "llama-4-maverick", "mystery-model", "zeta-9",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [900, 420]) {
      test(`${engine} ${lang} ${width}px: the editor's models stand by maker, the newest first`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-model-makers.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));

        await page.goto("http://magpie.test/?view=providers");
        const editor = page.locator("#modal:not([hidden]) .editor");
        // what the chips first draw: by maker from the first, never moved
        // after (modelorder.js comes with advgroups.js, which the page loads)
        await page.locator(".row.provider", { hasText: "Relay" }).waitFor();
        await page.evaluate(() => {
          new MutationObserver((_, o) => {
            const box = document.querySelector("#modal .editor .mchips");
            if (!box?.firstElementChild) return;
            window.firstDrawn = box.firstElementChild.className;
            o.disconnect();
          }).observe(document.body, { childList: true, subtree: true });
        });
        await page.locator(".row.provider", { hasText: "Relay" }).click();
        await editor.locator(".mchips .mchip").first().waitFor();
        const drawn = () => editor.locator(".mchips").first().evaluate((box) =>
          [...box.querySelectorAll(":scope > .mmaker, :scope > .mchip")].map((e) => (e.classList.contains("mmaker") ? [e.textContent] : e.querySelector("span").textContent)));
        // the order settles once modelorder.js is in
        await page.waitForFunction(() => document.querySelector("#modal:not([hidden]) .editor .mchips .mmaker"));
        assert.deepEqual(await drawn(), want(lang));
        assert.equal(await page.evaluate(() => window.firstDrawn), "mmaker", "the chips were drawn in the vendor's order first, then moved");

        // a label is a line of its own, plain and small, no stripe
        const look = await editor.locator(".mchips .mmaker").nth(1).evaluate((e) => {
          const s = getComputedStyle(e), chip = e.parentNode.querySelector(".mchip").getBoundingClientRect(), r = e.getBoundingClientRect();
          return { size: parseFloat(s.fontSize), weight: s.fontWeight, left: parseFloat(s.borderLeftWidth), bg: s.backgroundColor, width: r.width, box: e.parentNode.clientWidth, above: r.bottom <= chip.top || r.top >= chip.bottom };
        });
        assert.ok(look.size <= 12, `small (${look.size}px)`);
        assert.ok(Number(look.weight) < 600, "not bold");
        assert.equal(look.left, 0, "no stripe");
        assert.equal(look.bg, "rgba(0, 0, 0, 0)");
        assert.ok(look.width >= look.box - 8, "a line of its own");
        // nothing runs past the editor at a narrow width
        const over = await editor.locator(".mchips").first().evaluate((b) => b.scrollWidth - b.clientWidth);
        assert.ok(over <= 0, `the chips run ${over}px past their box`);

        // clicking a chip picks it, alone, and moves nothing
        const scrolled = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className.replace(/\bholds\b/, "").trim()}:${e.scrollTop}`)].join(" "));
        const chip = editor.locator(".mchips .mchip", { hasText: "gpt-5.10" });
        await chip.scrollIntoViewIfNeeded();
        const before = await scrolled();
        await chip.click();
        assert.equal(await editor.locator(".mchips .mchip.on").count(), 1);
        assert.match(await editor.locator(".mchips .mchip.on").textContent(), /gpt-5\.10/);
        assert.equal(await scrolled(), before, "the page moved");
        // the order stays the same once picked
        assert.deepEqual(await drawn(), want(lang));
        await page.keyboard.press("Escape");
        await page.locator("dialog.action-confirm[open] button").last().click();
        await page.locator("#modal").waitFor({ state: "hidden" });

        // one maker: the newest first, no label
        await page.locator(".row.provider", { hasText: "Solo" }).click();
        await page.waitForFunction(() => [...document.querySelectorAll("#modal:not([hidden]) .editor .mchips .mchip span")].map((s) => s.textContent).join() === "gpt-6.1,gpt-5.10,gpt-5.9");
        assert.equal(await editor.locator(".mchips .mmaker").count(), 0);

        const missing = await page.evaluate(() => ["Other", "Zhipu", "Moonshot AI", "Xiaomi", "StepFun"].filter((k) => !I18N.zh[k]));
        assert.deepEqual(missing, [], "every string has its Chinese");
        assert.deepEqual(errors, []);
      });
    }
  }
}
