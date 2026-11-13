// Run with Node's test runner and Playwright on the module path; see README.md.
// An open provider editor takes its accounts' readings without the page
// and the editor being drawn again (#5310). The readings load after the
// editor opens. When they came in, the whole Providers page was drawn
// again, every row and the editor, and with 600 accounts that was a second
// full draw of the editor in WebKit. Only the accounts list is drawn again
// now. This holds that the page's row and the editor's head are the same
// nodes after the readings come in, that every account shows its readings,
// that the focus stays on the same control of the same account, and that
// the list's inputs are labelled and described as a full draw labels them;
// in both engines and every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (ms) => new Date(Date.now() + ms).toISOString();
const N = 12;

function serve(lang, readings) {
  const settings = { lang, theme: "light", currency: "usd" };
  const users = Array.from({ length: N }, (_, i) => `user${i}@example.com`);
  const quotas = users.map((user, i) => ({ provider: "claude", name: "Claude", user, windows: [
    { name: "5 hours", used: (i * 7) % 100, resetsAt: at(36e5 * ((i % 5) + 1)) },
    { name: "7 days", used: (i * 13) % 100, resetsAt: at(864e5 * ((i % 7) + 1)) },
  ] }));
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [{
      id: "claude", name: "Claude", icon: "claudecode-color", models: [], agents: [], fallback: [], headers: {}, keyList: [],
      account: { agent: "claude", agentName: "Claude Code", user: users[0], plan: "max",
        logins: users.map((user, i) => ({ user, plan: "max", active: i === 0, on: i < 4 })) },
    }], presets: [], excluded: [], gateway: { running: true, window: true } });
    // held until the test lets the readings in
    if (url.pathname === "/api/login/usage") { await readings; return json(Object.fromEntries(quotas.map((q) => [q.user, q]))); }
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/usage") return json({ days: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

// how the accounts list's inputs are named and described, ids aside
const labels = () => {
  const list = document.querySelector(".editor .accts"), l = list.parentElement.previousElementSibling;
  const inputs = [...list.querySelectorAll("input, select, textarea")];
  const hint = list.parentElement.querySelector(":scope > .hint:last-child");
  return {
    inputs: inputs.map((i) => [i.type, !!i.id, i.getAttribute("aria-label"), !!hint && (i.getAttribute("aria-describedby") || "").split(" ").includes(hint.id)]),
    for: inputs.length ? l.htmlFor === inputs[0].id : l.htmlFor === "",
  };
};

for (const engine of ["chromium", "webkit"]) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the readings fill an open editor's accounts in, not the page drawn again`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(30000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      let let_in;
      const readings = new Promise((r) => (let_in = r));
      await page.route("**/*", serve(lang, readings));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Claude" }).first().click();
      await page.locator(".editor .accts .acc").nth(N - 1).waitFor();
      assert.equal(await page.locator(".editor .accts .aq-w").count(), 0, "no readings before they are let in");
      // the third account's tick holds the focus
      await page.evaluate(() => {
        window.__before = { row: document.querySelector(".row.provider"), head: document.querySelector(".editor .ehead") };
        document.querySelectorAll(".editor .accts .acc")[2].querySelector("button").focus();
      });
      let_in();
      await page.waitForFunction((n) => document.querySelectorAll(".editor .accts .acc .aq-w").length >= n * 2, N);
      const seen = await page.evaluate((labels) => {
        const b = window.__before, f = document.activeElement, acc = f.closest(".editor .accts .acc");
        return {
          row: b.row === document.querySelector(".row.provider") && b.row.isConnected,
          head: b.head === document.querySelector(".editor .ehead") && b.head.isConnected,
          focus: acc && [acc.dataset.accountId, acc.querySelector("button") === f],
          labels: eval(`(${labels})`)(),
        };
      }, labels.toString());
      assert.equal(seen.row, true, "the provider's row is the one drawn before");
      assert.equal(seen.head, true, "the editor's head is the one drawn before");
      assert.deepEqual(seen.focus, ["user2@example.com", true]);
      // a full draw names the list's inputs the same way
      const full = await page.evaluate((labels) => { renderProviders(true); return eval(`(${labels})`)(); }, labels.toString());
      assert.deepEqual(seen.labels, full);
      assert.equal(full.for, true);
      await context.close();
      assert.deepEqual(errors, []);
    });
  }
}
