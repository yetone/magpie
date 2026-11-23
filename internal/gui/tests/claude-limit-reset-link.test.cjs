// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude's limit resets (ahz2ygnhjmIJ0E0 on X: "cc有reset的功能，magpie能接入吗"):
// they are free, given now and then to some plans, and used only in
// claude.ai's Settings › Usage, never from Claude Code
// (support.claude.com/en/articles/17007452), so magpie can't spend one. A
// Claude account whose limit is used up has a row on its card, on the
// Usage page and in the menu bar panel, that opens that page; one under
// its limit, and another vendor's used-up account, have none. Each
// language, Chromium and WebKit, at 1100 and 420; no backend, the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const later = new Date(Date.now() + 3 * 864e5).toISOString();
const now = new Date().toISOString();
const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claudecode-color", user: "full@example.com", plan: "Max", lastServedAt: now,
    windows: [{ name: "Current session", used: 100, resetsAt: later }, { name: "Current week", used: 64, resetsAt: later }] },
  { provider: "claude", name: "Claude Code", icon: "claudecode-color", user: "fine@example.com", plan: "Pro",
    windows: [{ name: "Current session", used: 30, resetsAt: later }, { name: "Current week", used: 40, resetsAt: later }] },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "c@example.com", plan: "Plus", lastServedAt: now,
    windows: [{ name: "5 hours", used: 100, resetsAt: later }, { name: "7 days", used: 100, resetsAt: later }] },
];

function serve(lang, opened) {
  const settings = () => ({ theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", trayUsages: ["claude", "codex"], trayUsageEvery: 3 });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/open") { opened.push(JSON.parse(req.postData() || "{}").url); return json({}); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: "Limit resets are used on claude.ai",
  zh: "额度重置卡在 claude.ai 使用",
  "zh-TW": "額度重置卡在 claude.ai 使用",
  ja: "リミットのリセットは claude.ai で使います",
  de: "Limit-Resets werden auf claude.ai eingelöst",
};
const page = "https://claude.ai/settings/usage";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, say] of Object.entries(words)) {
    test(`${engine} ${lang}: a Claude account at its limit opens claude.ai's page for limit resets`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-claude-limit-reset-${i}.png`) });
        }
        await browser.close();
      });
      const errors = [], opened = [];
      const open = async (url, viewport) => {
        const p = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push(p);
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", serve(lang, opened));
        await p.goto(url);
        return p;
      };
      const text = (loc) => loc.evaluate((e) => e.textContent.replace(/\s+/g, " ").trim());
      const fits = async (p, sel, card) => {
        for (let i = 0; i < 40; i++) {
          const ok = await p.locator(sel).evaluate((e, card) => {
            const c = e.closest(card).getBoundingClientRect(), b = e.getBoundingClientRect();
            return b.width > 0 && b.left >= c.left - 0.5 && b.right <= c.right + 0.5 && e.scrollWidth <= e.clientWidth + 1;
          }, card);
          if (ok) return true;
          await p.waitForTimeout(50);
        }
        return false;
      };

      for (const width of [1100, 420]) {
        const p = await open("http://magpie.test/?view=usage", { width, height: 900 });
        const full = '.subscription-account[data-card="claude|full@example.com"]';
        await p.locator(full).waitFor();
        // the account's own box, its name and its rows (several accounts
        // of one vendor are each a card in the vendor's)
        const box = (who) => `.subscription-acct:has(.subscription-account[data-card="${who}"])`;
        const row = p.locator(box("claude|full@example.com") + " .claude-resets");
        assert.equal(await row.count(), 1, `${width}: the used-up account has the row`);
        assert.match(await text(row), new RegExp("^" + say.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
        assert(await fits(p, box("claude|full@example.com") + " .claude-resets", ".subscription-card"), `${width}: the row fits its card`);
        assert.equal(await p.locator(box("claude|fine@example.com") + " .claude-resets").count(), 0, "under its limit: none");
        assert.equal(await p.locator(".subscription-card .claude-resets").count(), 1, "Codex, at its limit too: none");
        opened.length = 0;
        await row.locator("button").click();
        for (let i = 0; i < 40 && !opened.length; i++) await p.waitForTimeout(50);
        assert.deepEqual(opened, [page]);
      }

      // the menu bar panel's card
      const panel = await open("http://magpie.test/?mode=panel", { width: 440, height: 700 });
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      await panel.waitForFunction(() => !quotasLoading);
      const prow = panel.locator(".pq-card .claude-resets");
      await prow.first().waitFor();
      assert.equal(await prow.count(), 1, "one panel card has it");
      assert.equal(await text(prow.locator("span")), say);
      assert(await fits(panel, ".pq-card .claude-resets", ".pq-card"), "the row fits the panel's card");
      opened.length = 0;
      await prow.locator("button").click();
      for (let i = 0; i < 40 && !opened.length; i++) await panel.waitForTimeout(50);
      assert.deepEqual(opened, [page]);
      assert.deepEqual(errors, []);
    });
  }
}
