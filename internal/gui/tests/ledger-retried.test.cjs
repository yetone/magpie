// Run with Node's test runner and Playwright on the module path; see README.md.
// The Requests card counts requests, each once by how it ended (小一 on
// Discord: 「5,482 · 1,094 次失败」 while the list's 「2 次尝试 / 3 次尝试」
// rows had been answered): a request answered after a try that failed is
// counted beside the failures as answered on a retry. Each count is a
// button for its requests: "N failed" turns on the list's Failed filter,
// which lists every request a try failed on, those a later try answered
// too, as Routing shows them failed (maohy915 on X: Routing had failed
// calls the Failed filter didn't), "N answered on a retry" its Retried one, and the button again turns it
// off. A try handed on to the next says so in its row, and a request that
// took more than one says how many. No click moves the page, nothing runs
// past the window at 420 or 1100 wide, in every language; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const row = (i, extra) => ({
  t: at(i), agent: "codex", agentName: "Codex", icon: "codex", provider: "relay", providerName: "Relay",
  req: "group/g", model: "gpt-5", served: "gpt-5", in: 300, out: 40, ms: 2000, status: 200, rid: "r-" + i, route_id: 1000 + i, ...extra,
});
// answered on its third try, after a refusal kept for what it billed; one
// that failed outright; one answered at once
const RETRIED = row(0, { tries: 3, route_id: 900 });
const HANDED = row(1, { status: 400, err: "refused", handed_on: true, route_id: 900, out: 2 });
const FAILED = row(2, { status: 502, err: "bad gateway", tries: 2, out: 0 });
const OK = row(3);
const sum = (rows, calls, errors, retried) => ({ rows, offset: 0, total: rows.length, calls, errors, retried, input: 300 * rows.length, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: rows.length });

function serve(lang, asked) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") {
      const q = url.searchParams;
      asked.push(q);
      const base = { period: q.get("period"), agents: [{ id: "codex", name: "Codex", icon: "codex" }] };
      if (q.get("failed") === "1") return json({ ...base, ...sum([RETRIED, HANDED, FAILED], 2, 1, 1) });
      if (q.get("retried") === "1") return json({ ...base, ...sum([RETRIED], 1, 0, 1) });
      return json({ ...base, ...sum([RETRIED, HANDED, FAILED, OK], 3, 1, 1) });
    }
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 3, errors: 1, retried: 1, input: 1200, output: 160, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 4, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const STRINGS = ["{n} failed", "{n} answered on a retry", "Retried", "Failed", "Show the requests a try failed on, with those a later try answered", "Show only the requests answered on a retry",
  "handed on", "{n} tries", "This try failed and the request was handed on to the next: the request is counted by how it ended, not by this try"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [420, 1100]) {
      test(`${engine} ${lang} ${width}: the Requests card's counts list their requests`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, asked));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-retried.png`), fullPage: true });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/?view=usage");
        const tr = (k, vars) => page.evaluate(([k, vars]) => t(k, vars), [k, vars || {}]);
        await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        const rows = page.locator(".led tbody tr.led-row");
        await rows.nth(3).waitFor({ timeout: 15000 });

        // every string, in every language
        for (const l of ["zh", "zh-TW", "ja", "de"]) {
          const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [l, STRINGS]);
          assert.deepEqual(missing, [], `every string has its ${l}`);
        }

        // the card: 3 requests, 1 failed, 1 answered on a retry, each a button
        const card = page.locator("#ledKpi .blk").nth(1);
        assert.equal((await card.locator(".v").textContent()).trim(), "3");
        const failed = card.locator("button.led-jump.bad"), retried = card.locator("button.led-jump:not(.bad)");
        assert.equal(await failed.textContent(), await tr("{n} failed", { n: "1" }));
        assert.equal(await retried.textContent(), await tr("{n} answered on a retry", { n: "1" }));
        assert.equal(await failed.getAttribute("title"), await tr("Show the requests a try failed on, with those a later try answered"));
        assert.equal(await page.locator("#ledStatus .opt").nth(1).getAttribute("title"), await tr("Show the requests a try failed on, with those a later try answered"));
        // whole, inside the card, and nothing past the window
        const fits = await card.evaluate((b) => {
          const r = b.getBoundingClientRect();
          return [...b.querySelectorAll("button.led-jump")].map((x) => {
            const s = x.getBoundingClientRect(), line = x.parentElement;
            return s.width > 0 && s.left >= r.left - 0.5 && s.right <= r.right + 0.5 && line.scrollWidth <= line.clientWidth + 1;
          });
        });
        assert.deepEqual(fits, [true, true], "both counts shown whole in the card");
        const noHScroll = () => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1);
        assert(await noHScroll(), "the page runs past the window");
        const seg = await page.locator("#ledStatus").evaluate((s) => {
          const r = s.getBoundingClientRect();
          return { opts: [...s.querySelectorAll(".opt")].map((o) => o.textContent), inside: r.right <= innerWidth + 0.5 && r.left >= -0.5 };
        });
        assert.deepEqual(seg.opts, [await tr("All"), await tr("Failed"), await tr("Retried")]);
        assert(seg.inside, "the status filter is cut off");

        // the rows: the try handed on, and the request that took three
        const status = (i) => rows.nth(i).locator("td .st").textContent();
        assert.equal(await status(0), "200 · " + (await tr("{n} tries", { n: 3 })));
        assert.equal(await status(1), "400 · " + (await tr("handed on")));
        const why = await rows.nth(1).locator("td:has(.st)").getAttribute("title");
        assert(why.includes(await tr("This try failed and the request was handed on to the next: the request is counted by how it ended, not by this try")) && why.includes("refused"), why);
        assert.equal(await status(3), "200");

        // a click on a count: the list's filter, the page where it was
        const top = () => page.locator("#view-usage").evaluate((v) => v.scrollTop);
        const press = async (b) => {
          await reader.inView(page, b);
          const was = await top(), xy = await page.evaluate(() => [scrollX, scrollY]);
          const n = asked.length;
          await b.click();
          await page.waitForTimeout(300);
          assert(asked.length > n, "the click asked for the list again");
          assert.equal(await top(), was, "a click moved the page");
          assert.deepEqual(await page.evaluate(() => [scrollX, scrollY]), xy);
          return asked[asked.length - 1];
        };
        let q = await press(failed);
        assert.equal(q.get("failed"), "1");
        assert.equal(q.get("retried"), null);
        // the failed request, the try handed on and the request it was
        // handed to, each counted by how it ended
        await page.waitForFunction(() => document.querySelectorAll(".led tbody tr.led-row").length === 3);
        assert.equal(await page.locator("#ledStatus .opt.on").textContent(), await tr("Failed"));
        assert.equal(await card.locator("button.led-jump.bad").getAttribute("aria-pressed"), "true");
        assert.equal(await status(0), "200 · " + (await tr("{n} tries", { n: 3 })));
        assert((await page.locator("#ledSum").textContent()).includes((await tr("{n} failed", { n: "1" })) + " · " + (await tr("{n} answered on a retry", { n: "1" }))));
        // pressed again: every request
        q = await press(card.locator("button.led-jump.bad"));
        assert.equal(q.get("failed"), null);
        await rows.nth(3).waitFor();
        assert.equal(await page.locator("#ledStatus .opt.on").textContent(), await tr("All"));

        q = await press(card.locator("button.led-jump:not(.bad)"));
        assert.equal(q.get("retried"), "1");
        assert.equal(q.get("failed"), null);
        await page.waitForFunction(() => document.querySelectorAll(".led tbody tr.led-row").length === 1);
        assert.equal(await page.locator("#ledStatus .opt.on").textContent(), await tr("Retried"));
        // and from the filter itself, straight to Failed
        q = await press(page.locator("#ledStatus .opt").nth(1));
        assert.equal(q.get("failed"), "1");
        assert.equal(q.get("retried"), null);
        await page.locator("#ledStatus .opt.on", { hasText: await tr("Failed") }).waitFor();
        q = await press(page.locator("#ledStatus .opt").nth(0));
        assert.equal(q.get("failed"), null);
        await rows.nth(3).waitFor();
        assert(await noHScroll(), "the page runs past the window");

        // the Usage page's overview counts the same way
        await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(0).click();
        const both = (await tr("{n} failed", { n: 1 })) + " · " + (await tr("{n} answered on a retry", { n: 1 }));
        await page.waitForFunction((s) => document.querySelector("#view-usage").textContent.includes(s), both);
        assert.deepEqual(errors, []);
      });
    }
  }
}
