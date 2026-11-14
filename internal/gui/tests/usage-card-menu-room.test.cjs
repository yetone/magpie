// Run with Node's test runner and Playwright on the module path; see README.md.
// normalcoder_ on X ("文字因布局被挡住"): a Usage card's ⋯, shown as the
// card is hovered, was drawn over the end of its head, over "Show the other
// accounts in brief" there. Room is kept for it now: hovered, the ⋯ covers
// none of the head's text, in any language, wide or narrow, in both
// engines. The API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (h) => new Date(Date.now() + h * 36e5).toISOString();
const w = (name, used, h) => ({ name, used, resetsAt: at(h) });
const model = (name, family, pool, used, h) => ({ name, family, ...(pool ? { pool } : {}), used, resetsAt: at(h) });
const pool = (p, name, used, h) => ({ name, pool: p, used, resetsAt: at(h) });
const AG = { provider: "antigravity", name: "Antigravity", icon: "antigravity-color", plan: "Google AI Pro" };
// as magpie reports them: one account whose quota summary was read (its
// models draw on pools, each pool's own windows beside them), two whose
// summary wasn't (their models' own windows, by family)
const pooled = { ...AG, user: "pooled@example.com", windows: [
  model("Claude Sonnet 4.6", "Claude", "Claude & GPT", 30, 3), model("GPT-OSS 120B (Medium)", "GPT-OSS", "Claude & GPT", 30, 3),
  model("Gemini 3 Flash", "Gemini", "Gemini", 11, 2), model("Gemini 3.1 Pro (High)", "Gemini", "Gemini", 11, 2),
  pool("Gemini", "7 days", 23, 100), pool("Gemini", "5 hours", 11, 2),
  pool("Claude & GPT", "7 days", 64, 90), pool("Claude & GPT", "5 hours", 30, 3),
] };
const bare = (user, g) => ({ ...AG, user, windows: [
  model("Gemini 3 Flash", "Gemini", "", g, 4), model("Gemini 3.1 Pro (High)", "Gemini", "", g, 4),
  model("Claude Sonnet 4.6", "Claude", "", 5, 4), model("GPT-OSS 120B (Medium)", "GPT-OSS", "", 5, 4),
] });
const quotas = [
  pooled, bare("bare1@example.com", 40), bare("bare2@example.com", 0),
  { provider: "kimi", name: "Kimi", icon: "kimi", user: "k1@example.com", windows: [w("Weekly", 42, 70)] },
  { provider: "zai", name: "Z.ai", icon: "zai", user: "z1@example.com", windows: [w("5 hours", 64, 1), w("Weekly", 12, 70)] },
  { provider: "codex", name: "Codex", kind: "subscription", icon: "codex-color", plan: "Plus", user: "c1@example.com", windows: [w("5 hours", 20, 2), w("7 days", 40, 90)] },
  { provider: "codex", name: "Codex", kind: "subscription", icon: "codex-color", plan: "Plus", user: "c2@example.com", windows: [w("5 hours", 90, 2), w("7 days", 10, 90)] },
  // a provider's keys: a balance a line
  { provider: "openrouter", name: "OpenRouter", icon: "openrouter", user: "key-a", windows: [], balance: "$4.00" },
  { provider: "openrouter", name: "OpenRouter", icon: "openrouter", user: "key-b", windows: [], balance: "$9.50" },
];

function serve(lang) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a card's ⋯ covers none of its head`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForSelector("#subscriptionUsage .subscription-card .subscription-head .quota-more");
        // every card with more than one account, its fold's label both ways
        for (const provider of ["antigravity", "codex", "openrouter"]) {
          const card = page.locator(`#subscriptionUsage .subscription-card[data-provider="${provider}"]`);
          for (let round = 0; round < 2; round++) {
            // in the middle of the view, clear of a phone's tab bar
            await card.evaluate((c) => c.scrollIntoView({ block: "center" }));
            await card.locator(".subscription-head b").hover();
            await page.waitForFunction((p) => getComputedStyle(document.querySelector(`.subscription-card[data-provider="${p}"] .us-more`)).opacity === "1", provider);
            const hit = await card.evaluate((c) => {
              const more = c.querySelector(".subscription-head .us-more").getBoundingClientRect();
              const out = [];
              for (const e of c.querySelectorAll(".subscription-head > :not(.us-more)")) {
                // the text as it is seen: inside its element's content box
                // (an ellipsized label's text runs on past it, unseen)
                const box = e.getBoundingClientRect(), cs = getComputedStyle(e);
                const right = box.right - parseFloat(cs.paddingRight) - parseFloat(cs.borderRightWidth);
                const walk = document.createTreeWalker(e, NodeFilter.SHOW_TEXT);
                for (let n = walk.nextNode(); n; n = walk.nextNode()) {
                  if (!n.textContent.trim()) continue;
                  const r = document.createRange();
                  r.selectNodeContents(n);
                  for (const x of r.getClientRects()) if (Math.min(x.right, right) > more.left + 0.5 && x.left < more.right && x.bottom > more.top && x.top < more.bottom) out.push(n.textContent.trim());
                }
              }
              return out;
            });
            assert.deepEqual(hit, [], `${provider}: the ⋯ covers ${hit}`);
            // and the other way: its fold's other label
            await card.locator(".subscription-head .quota-more").click();
            await page.mouse.move(0, 0);
          }
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
