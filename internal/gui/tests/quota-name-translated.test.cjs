// Run with Node's test runner and Playwright on the module path; see README.md.
// A window's name is what magpie translates; its display is shown as it came
// (quotaCount returns w.display for a window with no limit). A plugin that
// puts its sentence in the display instead keeps the card English whatever
// the interface's language is — the ZCode plugin's claim line was one
// ("1 to claim · claim it in the ZCode app", yetone/magpie#1001), and the
// plugin now names the window "Gift plans to claim in the ZCode app" and
// leaves the count and the plan's own name to the display, which are data.
//
// The name is a sentence, so it has to fit the card at the widths the window
// is really used at: .quota-labels > span is nowrap with no ellipsis and no
// tooltip, so a name wider than its .quota is cut off mid-word with nothing
// saying so (#1001 review). Every language is checked at 440 and 560px, not
// only at 1000 where nothing can be too wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const CLAIM = "Gift plans to claim in the ZCode app";
const quotas = [
  {
    provider: "zcode", name: "ZCode", icon: "zcode", plan: "GLM Coding Pro", user: "ada@example.com",
    windows: [
      { name: "5 hours", used: 25, resetsAt: new Date(Date.now() + 36e5).toISOString() },
      // the plugin's line: a sentence magpie knows, and a display of its own
      { name: CLAIM, used: 0, aside: true, display: "1 · ZCode Trust Build" },
    ],
  },
];

function serve(lang) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage") return json({ calls: 1, input: 10, output: 5, cost: 0, agents: [], models: [], days: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// what each language's name reads as; every language magpie has, so a long
// one can't be added later without this failing
const words = {
  en: CLAIM,
  zh: "待在 ZCode 客户端领取的赠送额度",
  "zh-TW": "待在 ZCode 用戶端領取的贈送額度",
  ja: "ZCode アプリで受け取れるギフト枠",
  de: "In der ZCode-App abzuholen",
};

// 440 is the narrowest the card is really seen at, 560 the window's minimum;
// 1000 is there because that is the width the old test used and it passed
const widths = [440, 560, 1000];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    test(`${engine} ${lang}: a window's name reads in the language, its display as it came`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      for (const width of widths) {
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=usage");
        const card = page.locator(".subscription-card", { hasText: "ZCode" });
        await card.waitFor();
        const labels = await card.locator(".quota-labels > span").allTextContents();
        assert.ok(labels.includes(words[lang]), `the name reads as ${lang} at ${width}px: ${JSON.stringify(labels)}`);
        // the plugin's own display: the count and the plan's name, untranslated
        const count = (await card.locator(".quota-n").last().innerText()).replace(/\s+/g, " ").trim();
        assert.equal(count, "1 · ZCode Trust Build", `the display at ${width}px`);
        // and the name is whole: .quota-labels > span is nowrap with no
        // ellipsis, so anything wider than its .quota is cut off in silence
        const fit = await page.evaluate(() => {
          const card = [...document.querySelectorAll(".subscription-card")].find((c) => c.textContent.includes("ZCode"));
          const spans = [...card.querySelectorAll(".quota-labels > span")];
          const s = spans[spans.length - 1];
          const quota = s.closest(".quota").getBoundingClientRect();
          const name = s.getBoundingClientRect();
          // the name's own box against its .quota: an inline span's scrollWidth
          // never exceeds its clientWidth, so geometry is what catches this
          return { over: Math.round(name.right - quota.right), wider: name.width > quota.width };
        });
        assert.ok(fit.over <= 0 && !fit.wider, `the name is cut off at ${width}px: ${fit.over}px past its quota, wider=${fit.wider}`);
        await page.close();
      }
      assert.deepEqual(errors, []);
    });
  }
}
