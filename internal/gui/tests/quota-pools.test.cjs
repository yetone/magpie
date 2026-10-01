// Run with Node's test runner and Playwright on the module path; see README.md.
// Antigravity's allowance a row a pool of models, each with its 5-hour and
// its weekly window (a user on Discord: 这里显示5h 和一周剩余比较好吧，现在
// 是只显示5h，这3个模型都是一样的，没必要分开…多加个7day 条就好). When an
// account's windows name a pool — the models that share one allowance,
// Gemini, Claude & GPT — the Usage page's card shows each pool's 5 hours
// then its 7 days, the pool's models in the tooltip; the menu bar panel
// shows the four as rings (Gemini 5h, Gemini 7d, Claude 5h, Claude 7d) and
// the provider's account rows each pool with both meters. "Every model"
// lists the models' own windows, not the pools' again, and back, the page
// not moving. Claude Code's windows are as they were. No left-border
// accent; English and Chinese, Chromium and WebKit; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (h) => new Date(Date.now() + h * 36e5).toISOString();
const model = (name, family, pool, used, h) => ({ name, family, pool, used, resetsAt: at(h) });
const pool = (pool, name, used, h) => ({ name, pool, used, resetsAt: at(h) });
// as magpie reports an Antigravity account with retrieveUserQuotaSummary
// read: a window a model, then each pool's weekly and 5-hour windows
const ag = (user, [g5, g7, c5, c7]) => ({
  provider: "antigravity", name: "Antigravity", icon: "antigravity-color", user, plan: "Google AI Pro",
  windows: [
    model("Claude Opus 4.6 (Thinking)", "Claude", "Claude & GPT", c5, 3), model("Claude Sonnet 4.6", "Claude", "Claude & GPT", c5, 3),
    model("Gemini 3 Flash", "Gemini", "Gemini", g5, 2), model("Gemini 3.1 Pro (High)", "Gemini", "Gemini", g5, 2),
    model("GPT-OSS 120B (Medium)", "GPT-OSS", "Claude & GPT", c5, 3),
    pool("Gemini", "7 days", g7, 100), pool("Gemini", "5 hours", g5, 2),
    pool("Claude & GPT", "7 days", c7, 90), pool("Claude & GPT", "5 hours", c5, 3),
  ],
});
const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", plan: "Max 5x",
    windows: [{ name: "5 hours", used: 30, resetsAt: at(2) }, { name: "7 days", used: 12, resetsAt: at(90) }] },
  ag("ada@example.com", [5, 25, 30, 60]),
  ag("bob@example.com", [0, 10, 100, 100]),
];
// each account's meters: Gemini 5h, 7d, then Claude & GPT 5h, 7d
const figures = { "ada@example.com": [5, 25, 30, 60], "bob@example.com": [0, 10, 100, 100] };
const logins = quotas.slice(1).map((q, i) => ({ user: q.user, plan: q.plan, active: i === 0, on: true }));
const antigravity = {
  id: "antigravity", name: "Antigravity", icon: "antigravity-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gemini-3.1-pro", name: "Gemini 3.1 Pro", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "antigravity", agentName: "Antigravity", user: logins[0].user, plan: logins[0].plan, logins },
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
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/login/usage") return json(Object.fromEntries(quotas.slice(1).map((q) => [q.user, q])));
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { five: "5 hours", week: "7 days", every: "Every model (5)", back: "By group", shared: "Gemini: one allowance for these models" },
  zh: { five: "5 小时", week: "7 天", every: "全部模型（5）", back: "按分组", shared: "Gemini：以下模型共用这份额度" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Antigravity's allowance a row a pool, 5 hours and 7 days`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [name, p] of pages) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-quota-pools-${name}.png`) });
        }
        await browser.close();
      });
      const errors = [];
      const open = async (name, url, viewport) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push([name, page]);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, url.includes("mode=panel")));
        await page.goto(url);
        return page;
      };
      const fill = (loc) => loc.evaluateAll((es) => es.map((e) => Math.round(parseFloat(e.style.width))));
      const names = [`Gemini · ${w.five}`, `Gemini · ${w.week}`, `Claude & GPT · ${w.five}`, `Claude & GPT · ${w.week}`];

      // the Usage page: each pool's 5 hours then its 7 days, Gemini first
      const page = await open("usage", "http://magpie.test/?view=usage", { width: 900, height: 700 });
      const card = page.locator(".subscription-card", { hasText: "Antigravity" });
      await card.locator(".quota-windows").first().waitFor();
      const boxes = card.locator(".quota-windows");
      assert.equal(await boxes.count(), 2, "one meter box an account");
      for (const [i, user] of ["ada@example.com", "bob@example.com"].entries()) {
        const box = boxes.nth(i);
        assert.deepEqual(await box.locator(".quota-labels > span:first-child").allTextContents(), names, `${user}: a pool's two windows`);
        assert.deepEqual(await fill(box.locator(".quota-track i")), figures[user], `${user}: each window's own figure`);
      }
      const tip = await boxes.first().locator(".quota").nth(1).getAttribute("title");
      assert(tip.includes(w.shared), "the pool says its models share it: " + tip);
      for (const m of ["Gemini 3 Flash", "Gemini 3.1 Pro (High)"]) assert(tip.includes(m), `the tooltip names ${m}`);
      assert(!tip.includes("Claude"), "only its own pool's models");
      const other = await boxes.first().locator(".quota").nth(2).getAttribute("title");
      for (const m of ["Claude Opus 4.6 (Thinking)", "Claude Sonnet 4.6", "GPT-OSS 120B (Medium)"]) assert(other.includes(m), `Claude & GPT names ${m}`);
      // Claude Code's windows name no pool: as they were, no toggle
      const cc = page.locator(".subscription-card", { hasText: "Claude Code" });
      assert.deepEqual(await cc.locator(".quota-labels > span:first-child").allTextContents(), [w.five, w.week]);
      assert.equal(await cc.locator(".quota-every").count(), 0);

      // Every model: the five models' windows, not the pools' again; and back
      const toggle = card.locator(".subscription-account", { hasText: "ada@example.com" }).locator("button.quota-every");
      assert.equal((await toggle.textContent()).trim(), w.every);
      const where = () => toggle.evaluate((e) => [e.getBoundingClientRect().top, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)]);
      await toggle.scrollIntoViewIfNeeded();
      await page.waitForTimeout(150);
      const before = await where();
      await toggle.click();
      await page.waitForTimeout(200);
      assert.deepEqual(await boxes.first().locator(".quota-labels > span:first-child").allTextContents(),
        ["Claude Opus 4.6 (Thinking)", "Claude Sonnet 4.6", "Gemini 3 Flash", "Gemini 3.1 Pro (High)", "GPT-OSS 120B (Medium)"]);
      assert.equal((await toggle.textContent()).trim(), w.back);
      assert.deepEqual(await where(), before, "the click moved the page");
      await toggle.click();
      await page.waitForTimeout(200);
      assert.equal(await boxes.first().locator(".quota").count(), 4, "by pool again");
      assert.deepEqual(await where(), before, "the click moved the page");
      const border = await page.evaluate(() => [...document.querySelectorAll(".subscription-card, .subscription-card *, .acc, .acc *, .pq-card, .pq-card *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");

      // the menu bar panel: four rings an account, 5h and 7d a pool
      const panel = await open("panel", "http://magpie.test/?mode=panel", { width: 440, height: 640 });
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      const pcards = panel.locator(".pq-group", { hasText: "Antigravity" }).locator(".pq-card");
      await pcards.first().locator(".pq-ring").first().waitFor();
      assert.equal(await pcards.count(), 2);
      for (let i = 0; i < 2; i++)
        assert.deepEqual(await pcards.nth(i).locator(".pq-rn").evaluateAll((es) => es.map((e) => [...e.children].map((c) => c.textContent).join(" "))), ["Gemini 5h", "Gemini 7d", "Claude 5h", "Claude 7d"]);
      // each label whole, not cut short
      assert(await pcards.first().locator(".pq-rn > span").evaluateAll((es) => es.every((e) => e.scrollWidth <= e.clientWidth)), "the ring labels fit");
      assert.deepEqual(await pcards.first().locator(".pq-dial b").allTextContents(), ["5%", "25%", "30%", "60%"]);
      const ring = await pcards.first().locator(".pq-ring").nth(3).getAttribute("title");
      assert(ring.startsWith(`Claude & GPT · ${w.week}`) && ring.includes("GPT-OSS 120B (Medium)"), "the ring names its pool and models: " + ring);
      // the rings fit the card, none cut off
      const fits = await pcards.first().evaluate((c) => { const r = c.getBoundingClientRect(); return [...c.querySelectorAll(".pq-ring")].every((x) => x.getBoundingClientRect().right <= r.right + 0.5); });
      assert(fits, "the four rings fit the card");

      // the provider's accounts: each pool with its 5h and 7d meters
      const prov = await open("accounts", "http://magpie.test/?view=providers", { width: 900, height: 700 });
      await prov.locator(".row.provider", { hasText: "Antigravity" }).click();
      await prov.locator(".accts .acc .aq-w").first().waitFor();
      for (const user of ["ada@example.com", "bob@example.com"]) {
        const row = prov.locator(".accts .acc", { hasText: user });
        assert.deepEqual(await row.locator(".aq-n").allTextContents(), ["Gemini", "Claude & GPT"], user);
        assert.deepEqual(await row.locator(".aq-k").allTextContents(), ["5h", "7d", "5h", "7d"], user);
        assert.deepEqual(await fill(row.locator(".aq-track i")), figures[user], user);
      }
      const aq = await prov.locator(".accts .acc", { hasText: "bob@example.com" }).locator(".aq-w").nth(1).getAttribute("title");
      assert(aq.includes("Claude Opus 4.6 (Thinking)") && aq.includes(`Claude & GPT · ${w.week}`), "the meter's tooltip: " + aq);
      assert(await prov.locator(".accts .acc", { hasText: "bob@example.com" }).locator(".aq-w").nth(1).evaluate((e) => e.classList.contains("full")), "a used-up pool reads full");

      const missing = await page.evaluate(() => [
        "By group", "Each group of models' 5-hour and weekly allowance, shared by its models", "{pool}: one allowance for these models",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
