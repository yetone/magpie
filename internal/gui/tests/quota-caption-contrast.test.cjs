// Run with Node's test runner and Playwright on the module path; see README.md.
// A usage card's captions read on the card (Jiabubu on Discord, #feedback):
// "重置于 …", "本周期" and a curve's dates were --faint, 1.95:1 on a light
// card and 2.16:1 on a dark one. Now every caption on a Usage card, and on
// the tray panel's allowance card, is at WCAG AA (4.5:1) or better against
// the card, and still below the card's figures. In both engines, light and
// dark, every language, at a wide and a narrow window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();

function data(now) {
  const r5 = now + 2 * H, rw = now + 3 * 24 * H;
  const quotas = [
    { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: "a@x.com", readAt: iso(now), windows: [
      { name: "5 hours", used: 40, resetsAt: iso(r5) }, { name: "Weekly", used: 30, resetsAt: iso(rw) }] },
  ];
  const five = [], week = [];
  for (let m = 0; m <= 180; m += 30) five.push({ at: iso(r5 - 5 * H + m * 60e3), left: 100 - m / 3, start: iso(r5 - 5 * H), resetsAt: iso(r5) });
  for (let h = 90; h >= 0; h -= 6) week.push({ at: iso(now - h * H), left: 70 + h / 9, start: iso(rw - 168 * H), resetsAt: iso(rw) });
  return { quotas, history: [{ provider: "codex", user: "a@x.com", lines: [{ name: "5 hours", points: five }, { name: "Weekly", points: week }] }] };
}

function serve(lang, theme, web) {
  const settings = { theme, lang, tray: "panel", quotaLeft: false, currency: "usd" };
  const d = data(Date.now());
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (b) => r.fulfill({ json: b });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json(d.quotas);
    if (url.pathname === "/api/usage/quotas/history/holds") return json([]);
    if (url.pathname === "/api/usage/quotas/history") return json(d.history);
    if (url.pathname === "/api/usage") return json({ calls: 0, input: 0, output: 0, cost: 0, agents: [], accounts: [], models: [], series: [], days: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

// each element's contrast against the first opaque background behind it
const ratios = ([card, sels]) => {
  const rgb = (s) => { const m = s.match(/[\d.]+/g).map(Number); return { r: m[0], g: m[1], b: m[2], a: m[3] ?? 1 }; };
  const lum = ({ r, g, b }) => { const f = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
  const bgOf = (e) => { for (let x = e; x; x = x.parentElement) { const c = rgb(getComputedStyle(x).backgroundColor); if (c.a > 0.5) return c; } return { r: 255, g: 255, b: 255 }; };
  const out = [];
  for (const sel of sels) {
    for (const e of document.querySelectorAll(card + " " + sel)) {
      if (!e.getClientRects().length || !e.textContent.trim()) continue;
      let op = 1;
      for (let x = e; x; x = x.parentElement) op *= parseFloat(getComputedStyle(x).opacity);
      const fg = rgb(getComputedStyle(e).color), bg = bgOf(e), a = fg.a * op;
      const mix = { r: fg.r * a + bg.r * (1 - a), g: fg.g * a + bg.g * (1 - a), b: fg.b * a + bg.b * (1 - a) };
      const L1 = lum(mix), L2 = lum(bg);
      out.push({ sel, text: e.textContent.trim().slice(0, 24), ratio: (Math.max(L1, L2) + 0.05) / (Math.min(L1, L2) + 0.05) });
    }
  }
  return out;
};

const captions = [".quota-reset", ".qc-range", ".qc-axis", ".qc-pace", ".quota-read", ".quota-labels"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a usage card's captions read at 4.5:1 on the card, below its figures`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const theme of ["light", "dark"]) {
      for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
        for (const width of [900, 420]) {
          const where = `${theme} ${lang} ${width}`;
          const ctx = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce", colorScheme: theme });
          const errors = [];
          const page = await ctx.newPage();
          page.setDefaultTimeout(8000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, theme, true));
          await page.goto("http://magpie.test/?view=usage");
          await page.locator(".subscription-card .quota-curve").first().waitFor();
          const caps = await page.evaluate(ratios, [".subscription-card", captions]);
          for (const sel of [".quota-reset", ".qc-range", ".qc-axis"]) assert.ok(caps.some((c) => c.sel === sel), `${where}: ${sel} shows`);
          for (const c of caps) assert.ok(c.ratio >= 4.5, `${where}: ${c.sel} "${c.text}" reads at ${c.ratio.toFixed(2)}:1`);
          const [fig] = await page.evaluate(ratios, [".subscription-card", [".quota-n"]]);
          assert.ok(fig, `${where}: a figure shows`);
          for (const c of caps) assert.ok(fig.ratio > c.ratio + 1.5, `${where}: the figure (${fig.ratio.toFixed(2)}:1) stands above ${c.sel} (${c.ratio.toFixed(2)}:1)`);
          assert.deepEqual(errors, [], where);
          await ctx.close();
        }
        // the tray panel's Allowances tab
        const ctx = await browser.newContext({ viewport: { width: 360, height: 640 }, reducedMotion: "reduce", colorScheme: theme });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "usage"); } catch {} });
        const panel = await ctx.newPage();
        panel.setDefaultTimeout(8000);
        await panel.route("**/*", serve(lang, theme, false));
        await panel.goto("http://magpie.test/?mode=panel");
        await panel.locator(".pq-card .pq-sub").first().waitFor();
        const pcaps = await panel.evaluate(ratios, [".pq-card", [".pq-sub", ".pq-rn"]]);
        assert.ok(pcaps.length, `${theme} ${lang} panel: a caption shows`);
        for (const c of pcaps) assert.ok(c.ratio >= 4.5, `${theme} ${lang} panel: ${c.sel} "${c.text}" reads at ${c.ratio.toFixed(2)}:1`);
        await ctx.close();
      }
    }
  });
}
