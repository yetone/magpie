// Run with Node's test runner and Playwright on the module path; see README.md.
// #3950 (墨雨藏锋 / mycf_711): prepaid wallets of one provider, in one
// unit, have a balance and no share. On the Routing page each one's row
// says what it has left, "750 积分 left", with no bar (a share it was
// never told), in the live rows and in the accounts listed below; the
// story says the first went first as its wallet has the most left,
// naming both figures, under Smart, Least used and Use before expiry alike.
// Where it said "0% used" and "in the order given". Every language, at a
// window's width and at 420px, in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (s) => new Date(now.getTime() - s * 1000).toISOString();
// as the gateway sends them (Weighed): known, used 0, the balance and unit
const seat = (who, balance, routing) => ({ id: `magicrew#${who}`, provider: "magicrew", name: "SuperMagic", who, kind: "account", model: "magic-1", plan: "Points",
  ...(routing ? { routing } : {}), known: true, used: 0, balance, unit: "积分", renews: ["0001-01-01T00:00:00Z"], pace: 100 / 168 });

function serve(lang, routing) {
  const order = [seat("big@example.com", 6291, routing), seat("small@example.com", 750, routing)];
  const route = {
    id: 1, seq: 1, time: ago(1), agent: "codex", model: "magic-1", provider: "magicrew", done: true, status: 200, ms: 900, tokens: 1200, order,
    tries: [{ id: order[0].id, model: "magic-1", start: ago(1), done: true, status: 200, ms: 900 }],
  };
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const WHY = "{who} goes first: of those alike, its wallet has the most left — {n}, where {other} has {m}.";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a wallet's row says its balance, and the story why the fuller went first`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const [routing, width] of [["", 1100], ["usage", 1100], ["pace", 420]]) {
        await t.test(`${lang} ${routing || "smart"} at ${width}px`, async () => {
          const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          try {
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (error) => errors.push(error.message));
            await page.route("**/*", serve(lang, routing));
            await page.goto("http://magpie.test/?view=routing");
            const req = page.locator(".rt-req").first();
            await req.waitFor();
            if (await req.getAttribute("aria-pressed") !== "true") await req.click();
            await page.locator(".rt-steps li.why").first().waitFor();
            await page.waitForFunction(() => [...document.querySelectorAll(".rt-accts > li")].every((li) => li.querySelector("em")?.textContent.trim()));
            const tr = (key, vars) => page.evaluate(([lang, key, vars]) => {
              const s = (lang !== "en" && I18N[lang]?.[key]) || key;
              return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
            }, [lang, key, vars]);
            const big = await tr("{n} left", { n: "6,291 积分" }), small = await tr("{n} left", { n: "750 积分" });
            const look = (id) => page.locator(`.rt-accts > li[title^="${id}"]`).first().evaluate((li) => ({
              st: li.querySelector("em").textContent, bar: getComputedStyle(li.querySelector(".bar")).display !== "none" && li.querySelector(".bar i").style.width !== "0px" && li.querySelector(".bar i").style.width !== "0" }));
            const b = await look("magicrew#big@example.com"), s = await look("magicrew#small@example.com");
            // the first says it answered; the one behind it, its figure
            assert.equal(s.st, small);
            assert.equal(b.bar, false, "no bar from a share never told");
            assert.equal(s.bar, false);
            // the story: the first went first by what its wallet has left
            const why = (await page.locator(".rt-steps li.why").first().textContent()).trim();
            const tpl = await tr(WHY, { n: "6,291 积分", m: "750 积分" });
            const [head, mid, tail] = tpl.split(/\{who\}|\{other\}/);
            assert.ok(why.includes(mid.trim()) && why.endsWith(tail.trim()) && (!head || why.startsWith(head)), `the story: ${why}`);
            // the accounts below: their figures, no bar
            const acts = page.locator(".rt-act");
            await acts.filter({ hasText: "6,291" }).first().waitFor();
            const list = await acts.evaluateAll((as) => as.map((a) => ({ st: a.querySelector(".st")?.textContent || "", bar: !!a.querySelector(".bar") })));
            assert.ok(list.some((a) => a.st.startsWith(big)), JSON.stringify(list));
            assert.ok(list.some((a) => a.st.startsWith(small)), JSON.stringify(list));
            assert.ok(list.every((a) => !a.bar), "no bar in the list either");
            const over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
            assert.ok(over <= 1, `${over}px past`);
            assert.deepEqual(errors, []);
          } finally {
            await page.close();
          }
        });
      }
    }
  });
}
