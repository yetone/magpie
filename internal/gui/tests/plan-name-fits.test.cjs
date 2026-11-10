// Run with Node's test runner and Playwright on the module path; see README.md.
// visamzh on X: a ChatGPT Business seat's plan, as /wham/usage names it
// (self_serve_business_prolite), was shown as it came, and on the Usage
// page's Codex card it pushed each account's name out of its row. A plan
// id is named as the vendor's own app names it (ChatGPT's Business), and
// any plan too long for its row gives its room up first: cut with "…",
// its whole text in its tooltip, the account's name still read. Checked
// on the Usage page's card (the account rows, the card's head and the
// "All accounts" strip), the tray panel's Allowances tab and the provider
// editor's account rows, five languages, wide and narrow, Chromium and
// WebKit. No backend; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const later = new Date(Date.now() + 3 * 864e5).toISOString();
const LONG = "Coding Plan Pro Max Ultra Enterprise Seat for the whole team";
const UNNAMED = "self_serve_business_flex_usage_based_credits";
const quotas = [
  { provider: "codex", name: "Codex", icon: "codex-color", user: "siaorhm.long.name@example.com", plan: "self_serve_business_prolite",
    windows: [{ name: "7 days", used: 20, resetsAt: later }], resets: { count: 2, until: later } },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "second.seat@example.com", plan: "self_serve_business_prolite",
    windows: [{ name: "7 days", used: 7, resetsAt: later }], resets: { count: 1, until: later } },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "plus.account@example.com", plan: "plus",
    windows: [{ name: "5 hours", used: 7, resetsAt: later }, { name: "7 days", used: 64, resetsAt: later }], resets: { count: 1, until: later } },
  // a plan id no app has named yet: shown as it comes, and cut
  { provider: "codex", name: "Codex", icon: "codex-color", user: "next.seat@example.com", plan: UNNAMED,
    windows: [{ name: "7 days", used: 3, resetsAt: later }] },
  // one account of a vendor whose plan is long in its own words: named in
  // the card's head, and alone in its group on the panel
  { provider: "volc", name: "Volcengine", icon: "volcengine-color", plan: LONG, until: later, renew: "auto",
    windows: [{ name: "5 hours", used: 30, resetsAt: later }] },
];
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "codex", agentName: "Codex", agentIcon: "codex-color", user: "siaorhm.long.name@example.com", plan: "self_serve_business_prolite",
    logins: [
      { user: "siaorhm.long.name@example.com", plan: "self_serve_business_prolite", active: true, on: true },
      { user: "plus.account@example.com", plan: "plus", on: true },
    ],
  },
};
const serve = (lang) => {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", trayUsages: ["codex", "volc"], codexAutoReset: ["siaorhm.long.name@example.com", "second.seat@example.com", "plus.account@example.com"] };
  return async (route) => {
    const url = new URL(route.request().url()), json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [codex], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, cost: 0, days: [], agents: [], models: [], keys: [] });
    if (url.pathname === "/api/login/usage") return json({});
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
};

// measure: each part's box under root, whether it is cut, and whether it
// stays inside root, read in one go from the page as it is now (a redraw
// between finding a node and reading it would leave a detached, empty box)
const measure = (page, root, parts) => page.evaluate(([root, parts]) => {
  const r = document.querySelector(root), c = r.getBoundingClientRect();
  const m = (e) => {
    const b = e.getBoundingClientRect();
    return { cls: e.className, w: b.width, cut: e.scrollWidth > e.clientWidth + 1, inside: b.left >= c.left - 1 && b.right <= c.right + 1, text: e.textContent, title: e.title };
  };
  const out = {};
  for (const [k, sel] of Object.entries(parts)) out[k] = sel === "*rows" ? [...r.querySelectorAll(".subscription-account")].map((row) => ({
    user: m(row.querySelector(".user")), plan: m(row.querySelector(".plan")),
    kids: [...row.children].filter((k) => k.getClientRects().length).map(m),
  })) : m(r.querySelector(sel));
  return out;
}, [root, parts]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a long plan name never pushes an account's name out`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const open = async (url, viewport, init) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        if (init) await page.addInitScript(init);
        await page.route("**/*", serve(lang));
        await page.goto(url);
        return page;
      };

      for (const width of [1100, 760, 420]) {
        const at = `${lang} ${width}`;
        const page = await open("http://magpie.test/?view=usage", { width, height: 900 });
        const codex = '#subscriptionUsage .subscription-card[data-provider="codex"]';
        await page.locator(codex + " .subscription-account").nth(3).waitFor();
        const { rows } = await measure(page, codex, { rows: "*rows" });
        assert.equal(rows.length, 4, `${at}: four accounts`);
        rows.forEach(({ user, plan, kids }, i) => {
          assert.ok(user.w >= 60, `${at} row ${i}: the account's name is read (${user.w}px of ${user.text})`);
          for (const k of kids) assert.ok(k.inside, `${at} row ${i}: ${k.cls} stays inside the card`);
          assert.equal(plan.text, ["Business", "Business", "plus", UNNAMED][i], `${at} row ${i}: the plan as ChatGPT names it`);
          assert.equal(plan.title, plan.text, `${at} row ${i}: the plan's tooltip`);
        });
        // "All accounts": its windows by plan, named as the rows name them
        const all = await page.locator(codex + " .quota-all-name").allTextContents();
        assert.ok(all.length && all.every((s) => !s.includes("_")), `${at}: the strip names no plan id (${all.join(" | ")})`);
        assert.ok(all.some((s) => s.includes("Business")), `${at}: the strip says Business`);

        // a card named in its head, its plan long: the plan is cut, the name read
        const { name, plan } = await measure(page, '#subscriptionUsage .subscription-card[data-provider="volc"]', { name: ".subscription-head > b", plan: ".subscription-head > .plan" });
        assert.ok(name.w >= 40 && !name.cut, `${at}: the card's name is read whole (${name.w}px)`);
        assert.ok(plan.inside, `${at}: the long plan stays inside the card`);
        assert.ok(plan.text.startsWith(LONG) && plan.title.startsWith(LONG), `${at}: the long plan's whole text, and in its tooltip`);
        const wide = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
        assert.ok(wide <= 0, `${at}: the page doesn't scroll sideways (${wide}px)`);
        await page.context().close();
      }

      // the tray panel: the long plan alone in its group's head, cut, the name read
      for (const width of [440, 360]) {
        const at = `${lang} panel ${width}`;
        const page = await open("http://magpie.test/?mode=panel", { width, height: 700 }, () => { try { localStorage.setItem("magpie.panelTab", "usage"); } catch {} });
        await page.locator("#panelQuota .pq-group[data-provider=\"volc\"] .pq-gnote").waitFor();
        const { name, note } = await measure(page, '#panelQuota .pq-group[data-provider="volc"] .pq-gh', { name: ".pq-gn", note: ".pq-gnote" });
        assert.ok(name.w >= 40 && !name.cut, `${at}: the group's name is read whole (${name.w}px)`);
        assert.ok(note.inside && note.cut, `${at}: the long plan is cut inside the head`);
        assert.ok(note.title.startsWith(LONG), `${at}: the whole plan in its tooltip`);
        const cards = await page.locator("#panelQuota .pq-card").evaluateAll((cs) => cs.map((c) => c.title));
        assert.ok(cards.length && cards.every((s) => !s.includes("self_serve")), `${at}: the cards' tooltips name no plan id`);
        await page.context().close();
      }

      // the provider editor: the account rows' chip and the sheet
      for (const width of [1000, 560]) {
        const at = `${lang} editor ${width}`;
        const page = await open("http://magpie.test/?view=providers", { width, height: 900 });
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        const row = '.editor .accts .acc[data-account-id="siaorhm.long.name@example.com"]';
        await page.locator(row).waitFor();
        const { chip } = await measure(page, row, { chip: ".acc-line > .plan" });
        assert.equal(chip.text, "ChatGPT Business", `${at}: the chip names ChatGPT's Business`);
        assert.equal(chip.title, chip.text, `${at}: the chip's tooltip`);
        assert.ok(chip.inside, `${at}: the chip stays inside its row`);
        await page.context().close();
      }
      assert.deepEqual(errors, []);
    });
  }
}
