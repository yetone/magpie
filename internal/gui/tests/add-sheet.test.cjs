// Run with Node's test runner and Playwright on the module path; see README.md.
// The Providers page's add sheet as quiet rows (Image #24: 这个页面看起来有点
// 乱糟糟的): under Subscriptions, Vendors, Relays and On this machine, each
// named with a word on what it is, the choices sit three to a line as an
// icon and a name, with no frame and no second line (the host or plans are
// in the row's title). One already added is not faded: it says so in green
// on its right, a subscription how many accounts it has. A custom provider
// is one line under them all, not a tile, and is gone while searching.
// Clicking a row still opens what it did. English and Chinese; no backend,
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const preset = (id, name, kind, added, extra = {}) => ({ id, name, icon: "openai", kind, chat: `https://api.${id}.example.com/v1`, added, ...extra });
const presets = [
  preset("anthropic", "Anthropic", "vendor", true),
  preset("openai", "OpenAI", "vendor", false),
  preset("deepseek", "DeepSeek", "vendor", false),
  preset("openrouter", "OpenRouter", "relay", true),
  preset("siliconflow", "SiliconFlow", "relay", false),
  preset("ollama", "Ollama", "local", false, { noKey: true }),
];
const prov = (id, name, extra = {}) => ({ id, name, icon: "openai", preset: id, models: [], agents: [], key: { set: true, masked: "sk-…ab12" }, ...extra });
const providers = [
  prov("anthropic", "Anthropic"), prov("openrouter", "OpenRouter"),
  prov("claude", "Claude", { preset: "", account: { agent: "claude", logins: [{ user: "a" }, { user: "b" }] } }),
];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers, presets, excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: {
    kinds: [["Subscriptions", "sign in, no key"], ["Vendors", "the makers' own APIs"], ["Relays", "one key, many vendors"], ["On this machine", ""]],
    added: "Added", accounts: "2 accounts", custom: "Custom provider", customHint: "any OpenAI or Anthropic compatible URL",
  },
  zh: {
    kinds: [["订阅", "登录即可，无需密钥"], ["供应商", "模型厂商的 API"], ["中转", "一个密钥，多家模型"], ["本机", ""]],
    added: "已添加", accounts: "2 个账号", custom: "自定义供应商", customHint: "任意 OpenAI / Anthropic 兼容的 URL",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the add sheet is quiet rows", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        const sheet = page.locator("#addSheet");
        await sheet.locator(".tile").first().waitFor();

        // the sections, each a name and a word on what it is
        const kinds = await sheet.locator(".kind").evaluateAll((ks) => ks.map((k) => [k.querySelector("b").textContent, k.querySelector("span")?.textContent || ""]));
        assert.deepEqual(kinds, w.kinds);

        // rows: no frame, no second line, three to a line
        const rows = sheet.locator(".tile");
        const look = await rows.evaluateAll((rs) => rs.map((r) => {
          const s = getComputedStyle(r);
          return { border: s.borderTopWidth, opacity: s.opacity, lines: r.querySelectorAll(".s, .tt").length, over: r.scrollWidth > r.clientWidth + 1, h: r.getBoundingClientRect().height };
        }));
        for (const l of look) {
          assert.equal(l.border, "0px");
          assert.equal(l.opacity, "1");
          assert.equal(l.lines, 0);
          assert.equal(l.over, false);
          assert(l.h <= 40, "row " + l.h + "px tall");
        }
        const tops = await sheet.locator(".kind").nth(1).evaluate((k) => [...k.nextElementSibling.children].map((r) => Math.round(r.getBoundingClientRect().top)));
        assert.equal(tops.filter((y) => y === tops[0]).length, 3, "three vendors on the first line");

        // added in green, a subscription with its accounts; the rest bare
        const row = (name) => rows.filter({ has: page.locator(".n", { hasText: new RegExp("^" + name + "$") }) });
        assert.equal(await row("Anthropic").locator(".st.added").textContent(), w.added);
        assert.equal(await row("OpenRouter").locator(".st.added").textContent(), w.added);
        assert.equal(await row("Claude").locator(".st.added").textContent(), w.accounts);
        assert.equal(await row("OpenAI").locator(".st").count(), 0);
        const green = await row("Anthropic").locator(".st.added").evaluate((e) => getComputedStyle(e).color);
        assert.notEqual(green, await row("OpenAI").locator(".n").evaluate((e) => getComputedStyle(e).color));
        assert.match(await row("OpenAI").getAttribute("title"), /api\.openai\.example\.com/);

        // the custom provider: one line at the foot, gone while searching
        const foot = sheet.locator(".custom-foot");
        assert.equal(await foot.locator(".custom").textContent(), w.custom);
        assert.equal(await foot.locator(".hint").textContent(), w.customHint);
        await sheet.locator(".find").fill("deep");
        assert.deepEqual(await rows.locator(".n").allTextContents(), ["DeepSeek"]);
        assert.equal(await foot.count(), 0);
        await sheet.locator(".find").fill("");

        // a row opens what it did: a new preset's editor
        await row("DeepSeek").click();
        await page.locator(".editor.new .ehead b", { hasText: "DeepSeek" }).waitFor();
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
