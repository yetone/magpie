// Run with Node's test runner and Playwright on the module path; see README.md.
// An Antigravity card keeps its order from one refresh to the next (#860,
// huoranxuanyuan: 刷新额度有时会让5h和七天的顺序颠倒). retrieveUserQuotaSummary
// sometimes leaves out a pool's 5-hour bucket (Gemini's, here, its week used
// up); magpie then keeps the Gemini models' own 5-hour windows as a family
// row (#745). That row used to go last, after Claude & GPT, so the card read
// Gemini · 7 days, Claude & GPT · 5 hours, Claude & GPT · 7 days, Gemini, and
// with the 5 hours back, Gemini · 5 hours, Gemini · 7 days, … — in the card's
// two columns the 5 hours and the weeks swapped sides. Now the family row
// takes the pool's 5-hour place, on the Usage page and in the panel's rings.
// Chromium and WebKit, English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (h) => new Date(Date.now() + h * 36e5).toISOString();
const model = (name, family, pool, used, h) => ({ name, family, ...(pool ? { pool } : {}), used, resetsAt: at(h) });
const pool = (pool, name, used, h) => ({ name, pool, used, resetsAt: at(h) });
// the reporter's card: Gemini's week used up and no Gemini 5 hours in the
// summary, so the Gemini models carry no pool; Claude & GPT read whole
const partial = {
  provider: "antigravity", name: "Antigravity", icon: "antigravity-color", user: "ada@example.com", plan: "Google AI Pro",
  windows: [
    model("Claude Opus 4.6 (Thinking)", "Claude", "Claude & GPT", 0, 5), model("Claude Sonnet 4.6", "Claude", "Claude & GPT", 0, 5),
    model("Gemini 3 Flash", "Gemini", "", 100, 100), model("Gemini 3.1 Pro (High)", "Gemini", "", 100, 100),
    model("GPT-OSS 120B (Medium)", "GPT-OSS", "Claude & GPT", 0, 5),
    pool("Gemini", "7 days", 100, 100),
    pool("Claude & GPT", "7 days", 0, 168), pool("Claude & GPT", "5 hours", 0, 5),
  ],
};
const antigravity = {
  id: "antigravity", name: "Antigravity", icon: "antigravity-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gemini-3.1-pro", name: "Gemini 3.1 Pro", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "antigravity", agentName: "Antigravity", user: partial.user, plan: partial.plan, logins: [{ user: partial.user, plan: partial.plan, active: true, on: true }] },
};

function serve(lang, panel) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${!panel}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [antigravity], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json([partial]);
    if (url.pathname === "/api/login/usage") return json({ [partial.user]: partial });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = { en: { five: "5 hours", week: "7 days" }, zh: { five: "5 小时", week: "7 天" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a pool without its 5 hours keeps its place`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const open = async (url, viewport) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, url.includes("mode=panel")));
        await page.goto(url);
        return page;
      };
      const page = await open("http://magpie.test/?view=usage", { width: 900, height: 700 });
      const box = page.locator(".subscription-card", { hasText: "Antigravity" }).locator(".quota-windows").first();
      await box.waitFor();
      assert.deepEqual(await box.locator(".quota-labels > span:first-child").allTextContents(),
        ["Gemini", `Gemini · ${w.week}`, `Claude & GPT · ${w.five}`, `Claude & GPT · ${w.week}`],
        "Gemini's 5-hour reading first, then its week, then Claude & GPT's");

      const panel = await open("http://magpie.test/?mode=panel", { width: 440, height: 640 });
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      const card = panel.locator(".pq-group", { hasText: "Antigravity" }).locator(".pq-card").first();
      await card.locator(".pq-ring").first().waitFor();
      const rings = await card.locator(".pq-rn").evaluateAll((es) => es.map((e) => e.textContent));
      assert.equal(rings.length, 4);
      assert(/^Gemini/.test(rings[0]) && /^Gemini/.test(rings[1]) && /^Claude/.test(rings[2]) && /^Claude/.test(rings[3]), "rings Gemini, Gemini, Claude, Claude: " + rings);
      assert.deepEqual(errors, []);
    });
  }
}
