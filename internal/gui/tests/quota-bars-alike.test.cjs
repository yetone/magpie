// Run with Node's test runner and Playwright on the module path; see README.md.
// normalcoder_ on X: the Routing page's quota bars were grey and black while
// the Usage overview's are blue, and in a Usage card with several accounts
// one account ran into the next. Routing's bars are Usage's .quota-track,
// the same track and fill in light and dark; and in Usage every account
// after the first has a hairline over it, whatever the one before ends
// with (here a check-in row, which no list of row kinds named). Every
// language, wide and narrow; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const nowMs = Date.now(), now = new Date(nowMs).toISOString(), iso = (ms) => new Date(ms).toISOString();
const acct = (provider, name, user, used, extra) => ({ provider, name, kind: "subscription", icon: "generic", plan: "Pro", user, readAt: now,
  windows: [{ name: "5h", used, resetsAt: iso(nowMs + 3600e3) }, { name: "Weekly", used: used / 2, resetsAt: iso(nowMs + 3 * 864e5) }], ...extra });
const quotas = [
  acct("codex", "Codex", "a@example.com", 40, { lastServedAt: now, resets: { count: 2 } }),
  acct("codex", "Codex", "b@example.com", 70),
  // brief accounts that check in: the row under one is a check-in row
  acct("trae", "Trae", "t1@example.com", 20, { checkins: true, lastServedAt: now }),
  acct("trae", "Trae", "t2@example.com", 60, { checkins: true }),
  acct("trae", "Trae", "t3@example.com", 30, { checkins: true }),
];
const accts = [
  { id: "acct-1", provider: "codex", name: "Codex", who: "work@example.com", kind: "account", model: "gpt-6", known: true, used: 30, plan: "Plus" },
  { id: "acct-2", provider: "codex", name: "Codex", who: "spare@example.com", kind: "account", model: "gpt-6", known: true, used: 95, plan: "Pro" },
];
const route = { id: 7, seq: 7, time: iso(nowMs - 2000), agent: "codex", model: "gpt-6", provider: "codex", done: true, status: 200, ms: 900, tokens: 1500,
  order: accts, tries: [{ id: "acct-1", model: "gpt-6", start: iso(nowMs - 2000), done: true, ms: 800, status: 200 }] };

function serve(lang, theme) {
  const settings = { theme, lang, tray: "panel", quotaLeft: true, currency: "usd" };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

// each bar's track and fill colours, beside the Usage overview's .quota-track
// drawn on the same page
const bars = (page, sel) => page.evaluate((sel) => {
  const probe = document.createElement("div");
  probe.className = "quota-track";
  probe.append(document.createElement("i"));
  document.body.append(probe);
  const usage = { track: getComputedStyle(probe).backgroundColor, fill: getComputedStyle(probe.firstChild).backgroundColor };
  probe.remove();
  const seen = [...document.querySelectorAll(sel)].filter((b) => b.offsetParent).map((b) => ({
    track: getComputedStyle(b).backgroundColor, fill: getComputedStyle(b.querySelector("i")).backgroundColor,
  }));
  return { usage, seen };
}, sel);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: Routing's quota bars are Usage's, and Usage's accounts are apart`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const theme of ["light", "dark"]) for (const width of [1100, 420]) {
        const at = `${theme} ${width}px`;
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce", colorScheme: theme });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme));

        await page.goto("http://magpie.test/?view=usage");
        await page.locator(".subscription-card[data-provider='trae'] .subscription-account").nth(2).waitFor();
        const lines = await page.evaluate(() => [...document.querySelectorAll("#subscriptionUsage > .subscription-card")].map((c) => {
          const line = getComputedStyle(c).getPropertyValue("--line").trim();
          const probe = document.createElement("i");
          probe.style.color = line;
          c.append(probe);
          const want = getComputedStyle(probe).color;
          probe.remove();
          return { card: c.dataset.provider, want, rows: [...c.querySelectorAll(":scope > .subscription-account")].map((a) => {
            const s = getComputedStyle(a);
            return { width: s.borderTopWidth, style: s.borderTopStyle, color: s.borderTopColor };
          }) };
        }));
        for (const c of lines) {
          assert.ok(c.rows.length >= 2, `${at}: ${c.card} shows its accounts`);
          assert.equal(c.rows[0].style, "none", `${at}: ${c.card}'s first account has no line over it`);
          c.rows.slice(1).forEach((r, i) => {
            assert.equal(r.style, "solid", `${at}: ${c.card}'s account ${i + 2} has a line over it`);
            assert.equal(r.width, "1px", `${at}: ${c.card}'s account ${i + 2}'s line is a hairline`);
            assert.equal(r.color, c.want, `${at}: ${c.card}'s account ${i + 2}'s line is the app's --line`);
          });
        }
        const u = await bars(page, "#subscriptionUsage .quota-track");
        assert.ok(u.seen.length > 0, `${at}: Usage draws bars`);
        for (const b of u.seen) assert.deepEqual(b, u.usage, `${at}: Usage's bars`);

        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-act .bar").first().waitFor();
        for (const sel of [".rt-accts li:not(.nobar) > .bar", ".rt-act > .bar"]) {
          const r = await bars(page, sel);
          assert.ok(r.seen.length > 0, `${at}: Routing draws ${sel}`);
          for (const b of r.seen) assert.deepEqual(b, r.usage, `${at}: Routing's ${sel} is coloured as Usage's bars`);
        }
        // the account that answered is told by its frame and dot, its bar
        // the same as the others'
        assert.equal(await page.locator(".rt-accts li.on").count() > 0, true, `${at}: an account answered`);
        assert.deepEqual(errors, [], at);
        await ctx.close();
      }
    });
  }
}
