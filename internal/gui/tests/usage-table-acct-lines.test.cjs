// Run with Node's test runner and Playwright on the module path; see README.md.
// normalcoder_ on X, on Usage › Overview's table: the account cell put a
// named account's name and its address side by side, so a long one was
// cut. The name is now on the first line and the address, the key plan it
// stands in for and its shared keys each on a line below, every one with
// the cell's whole width; the eye of a masked page stays on the first
// line. Both engines, every language, wide and narrow; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (h) => new Date(Date.now() + h * 36e5).toISOString();
const w = (name, used, h) => ({ name, used, resetsAt: at(h) });
const CX = { provider: "codex", name: "Codex", kind: "subscription", icon: "codex-color", plan: "Plus" };
const quotas = [
  { ...CX, alias: "Design team's seat", named: true, user: "design-team@example-company.com", windows: [w("5 hours", 20, 2), w("7 days", 40, 90)] },
  { ...CX, user: "plain@example.com", windows: [w("5 hours", 90, 2), w("7 days", 10, 90)] },
  { provider: "zai", name: "Z.ai", icon: "zai", alias: "Ops", named: true, user: "ops-lead@example.com", seat: "GLM Coding Max", windows: [w("5 hours", 64, 1), w("7 days", 12, 70)] },
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

const TABLE = { en: "Table", zh: "表格", "zh-TW": "表格", ja: "表", de: "Tabelle" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a table's account cell has its name above its address`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForSelector("#quotaView .opt");
        await page.locator("#quotaView .opt", { hasText: TABLE[lang] }).click();
        await page.waitForSelector("#subscriptionUsage .qt tbody tr");

        const cell = (user) => page.evaluate((user) => {
          const tr = [...document.querySelectorAll(".qt tbody tr")].find((r) => r.querySelector(".qt-acct")?.textContent.includes(user));
          const box = tr.querySelector(".qt-acct .subscription-account");
          const parts = [...box.children].filter((e) => e.offsetParent).map((e) => {
            const r = e.getBoundingClientRect();
            return { cls: e.className, text: e.textContent, top: r.top, bottom: r.bottom, left: r.left, cut: e.scrollWidth > e.clientWidth + 1 };
          });
          return { parts, height: box.getBoundingClientRect().height };
        }, user);
        const lines = (parts) => parts.map((p) => p.text);

        // a named account: the name, then the address, a line each, from the same left
        const named = await cell("design-team@example-company.com");
        assert.deepEqual(lines(named.parts), ["Design team's seat", "design-team@example-company.com"]);
        const [name, of] = named.parts;
        assert.ok(of.top >= name.bottom - 1, `the address is under the name: ${name.bottom} / ${of.top}`);
        assert.ok(Math.abs(of.left - name.left) <= 1, `both lines start at one left: ${name.left} / ${of.left}`);
        assert.deepEqual(named.parts.filter((p) => p.cut).map((p) => p.text), [], "nothing in it is cut");

        // a name, its address and the key plan it stands in for: three lines
        const seat = await cell("ops-lead@example.com");
        assert.equal(seat.parts.length, 3);
        for (let i = 1; i < 3; i++) assert.ok(seat.parts[i].top >= seat.parts[i - 1].bottom - 1, `line ${i + 1} under line ${i}`);
        assert.deepEqual(seat.parts.filter((p) => p.cut).map((p) => p.text), [], "nothing in it is cut");

        // an account with no name: its one line, as before
        const plain = await cell("plain@example.com");
        assert.deepEqual(lines(plain.parts), ["plain@example.com"]);
        assert.ok(plain.height < named.height - 4, `one line is shorter than two: ${plain.height} / ${named.height}`);

        // masked: the eye beside the name, not on a line of its own
        await page.evaluate(() => document.body.classList.add("masked"));
        const eye = await page.evaluate(() => {
          const tr = [...document.querySelectorAll(".qt tbody tr")].find((r) => r.textContent.includes("Design team's seat"));
          const a = tr.querySelector(".quota-alias").getBoundingClientRect(), e = tr.querySelector(".acct-eye")?.getBoundingClientRect();
          return e && { mid: (e.top + e.bottom) / 2, top: a.top, bottom: a.bottom, left: e.left, right: a.right };
        });
        assert.ok(eye, "the masked page shows the eye");
        assert.ok(eye.mid >= eye.top - 2 && eye.mid <= eye.bottom + 2, `the eye is on the name's line: ${JSON.stringify(eye)}`);

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 1, `the page doesn't scroll sideways: ${over}`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
