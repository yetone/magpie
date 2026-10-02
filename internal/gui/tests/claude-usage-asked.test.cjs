// Run with Node's test runner and Playwright on the module path; see README.md.
// A Claude account's usage is read only when the reader asks (the user:
// 不要伪造任何的 Claude 请求，能否通过 claude cli 获取): magpie runs Claude
// Code's own /usage, and only when the Usage page is opened or Refresh is
// pressed. Opening the page and Refresh (the header's, which on the Overview
// is the allowances' too, #486) load usage/quotas?asked=1; the
// window coming back to the front and the timed reload load it without; an
// asked load isn't swallowed by an unasked one already on its way, it goes
// after it. Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(asks, hold, quotas = [{ provider: "claude", name: "Claude", user: "a@example.com", windows: [{ name: "5 hours", used: 13 }] }], lang = "en", web = false) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [{
      id: "claude", name: "Claude", icon: "claudecode-color", models: [], agents: [], fallback: [], headers: {}, keyList: [],
      account: { agent: "claude", agentName: "Claude Code", user: "a@example.com", plan: "max",
        logins: [{ user: "a@example.com", plan: "max", active: true, on: true }] },
    }], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/login/usage") return json(Object.fromEntries(quotas.map((q) => [q.user, q])));
    if (url.pathname === "/api/usage/quotas") {
      asks.push(url.search);
      if (hold.on) await new Promise((r) => (hold.release = r));
      return json(quotas);
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: an expired Claude snapshot is dated, then replaced on recovery`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const quota = { provider: "claude", name: "Claude", user: "a@example.com",
        asOf: new Date(Date.now() - 72e5).toISOString(),
        windows: [{ name: "5 hours", used: 100, resetsAt: new Date(Date.now() - 36e5).toISOString() }] };
      await page.route("**/*", serve([], { on: false }, [quota], lang));
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card");
      const reading = card.locator(".quota-read");
      await reading.waitFor();
      assert.match(await reading.innerText(), lang === "en" ? /As of .*expired.*unknown/ : /截至 .*已过期.*未知/);
      assert.match(await card.locator(".quota-n").innerText(), /100%/);
      assert.match(await card.locator(".quota-reset").innerText(), lang === "en" ? /Reset time passed/ : /重置时间已过/);
      assert.doesNotMatch(await card.locator(".quota-reset").innerText(), /in 1m|1 分钟后|1分钟后/);
      delete quota.asOf;
      quota.windows[0].used = 25;
      quota.windows[0].resetsAt = new Date(Date.now() + 36e5).toISOString();
      await page.locator("#usageReload").click();
      await page.waitForFunction(() => document.querySelector(".subscription-card .quota-n")?.textContent.includes("25%"));
      assert.equal(await reading.count(), 0, "a new reading clears the snapshot warning");
      assert.deepEqual(errors, []);
    });
    for (const surface of ["panel", "editor"]) {
      test(`${engine} ${lang}: ${surface} shows a visible dated snapshot and clears it on recovery`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width: surface === "panel" ? 440 : 900, height: 760 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const quota = { provider: "claude", name: "Claude", user: "a@example.com", asOf: new Date(Date.now() - 72e5).toISOString(),
          windows: [{ name: "5 hours", used: 100, resetsAt: new Date(Date.now() - 36e5).toISOString() }] };
        await page.route("**/*", serve([], { on: false }, [quota], lang, true));
        const open = async () => {
          await page.goto(surface === "panel" ? "http://magpie.test/?mode=panel" : "http://magpie.test/?view=providers");
          if (surface === "panel") await page.locator('button[data-ptab="usage"]').click();
          else await page.locator(".row.provider", { hasText: "Claude" }).first().click();
        };
        await open();
        const card = page.locator(surface === "panel" ? ".pq-card" : ".editor .acc.with-aq").first();
        const reading = card.locator(surface === "panel" ? ".pq-asof" : ".aq-asof");
        await reading.waitFor();
        assert.match(await reading.innerText(), lang === "en" ? /As of .*expired.*unknown/ : /截至 .*已过期.*未知/);
        if (surface === "panel") assert.doesNotMatch(await reading.innerText(), /couldn't be read just now|暂时无法获取最新额度/);
        assert.match(await card.innerText(), /100%/);
        assert.match(await card.innerText(), lang === "en" ? /Reset time passed/ : /重置时间已过/);
        assert.doesNotMatch(await card.innerText(), /in 1m|1 分钟后|1分钟后/);
        assert(await reading.evaluate((e) => e.scrollWidth <= e.clientWidth + 1 && getComputedStyle(e).textOverflow !== "ellipsis"), "the visible warning is not clipped");
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the warning fits the view");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.evaluate(() => Promise.all(document.getAnimations().filter((a) => a.effect.getTiming().iterations !== Infinity).map((a) => a.finished.catch(() => {}))));
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-claude-stale-${surface}.png`) });
        }
        delete quota.asOf;
        quota.windows[0].used = 25;
        quota.windows[0].resetsAt = new Date(Date.now() + 36e5).toISOString();
        await open();
        await page.waitForFunction((selector) => document.querySelector(selector)?.textContent.includes("25%"), surface === "panel" ? ".pq-card" : ".editor .acc.with-aq");
        assert.equal(await reading.count(), 0, "recovery clears the visible old-reading marker");
        assert.doesNotMatch(await card.innerText(), /Reset time passed|重置时间已过/);
        assert.deepEqual(errors, []);
      });
    }
    test(`${engine} ${lang}: the tray's cached subscription date is as short as a balance card's`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 440, height: 760 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const asOf = new Date(Date.now() - 18e5).toISOString();
      const quota = { provider: "claude", name: "Claude", user: "a@example.com", asOf,
        windows: [{ name: "5 hours", used: 40, resetsAt: new Date(Date.now() + 36e5).toISOString() }] };
      const balance = { provider: "deepseek", name: "DeepSeek", kind: "key", asOf, balance: "$10", windows: [] };
      await page.route("**/*", serve([], { on: false }, [quota, balance], lang, true));
      await page.goto("http://magpie.test/?mode=panel");
      await page.locator('button[data-ptab="usage"]').click();
      const card = page.locator(".pq-card:not(.bal)");
      const reading = card.locator(".pq-asof");
      await reading.waitFor();
      assert.equal(await reading.innerText(), await page.locator(".pq-card.bal .pq-asof").innerText());
      assert.match(await reading.innerText(), lang === "en" ? /^As of / : /^截至 /);
      assert.doesNotMatch(await reading.innerText(), /couldn't be read just now|expired|unknown|暂时无法获取最新额度|已过期|未知/);
      assert.match(await card.getAttribute("title"), lang === "en" ? /couldn't be read just now/ : /暂时无法获取最新额度/);
      assert(await reading.evaluate((e) => e.scrollWidth <= e.clientWidth + 1), "the short date fits the card");
      assert.deepEqual(errors, []);
    });
  }
  test(`${engine}: Claude's usage is asked for only on opening Usage or Refresh`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const asks = [], hold = { on: false };
    await page.route("**/*", serve(asks, hold));
    const asked = () => asks.filter((s) => s === "?asked=1").length;
    const settle = async (n) => { for (let i = 0; i < 100 && asks.length < n; i++) await page.waitForTimeout(20); await page.waitForTimeout(150); };

    // opening the page asks
    await page.goto("http://magpie.test/?view=usage");
    await page.locator("#usageReload").waitFor();
    await settle(1);
    assert.equal(asked(), 1, `opening Usage asks once: ${JSON.stringify(asks)}`);

    // the window coming back to the front reads what is kept, asking nothing
    let n = asks.length;
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    await settle(n + 1);
    assert.equal(asked(), 1, `focus must not ask: ${JSON.stringify(asks)}`);
    // the timed reload is loadQuotas() with nothing
    n = asks.length;
    await page.evaluate(() => loadQuotas());
    await settle(n + 1);
    assert.equal(asked(), 1, `a timed reload must not ask: ${JSON.stringify(asks)}`);

    // Refresh asks
    await page.locator("#usageReload").click();
    await settle(asks.length + 1);
    assert.equal(asked(), 2, `Refresh asks: ${JSON.stringify(asks)}`);

    // an unasked load on its way doesn't swallow Refresh: it goes after it
    hold.on = true;
    n = asks.length;
    await page.evaluate(() => { loadQuotas(); });
    for (let i = 0; i < 100 && !hold.release; i++) await page.waitForTimeout(20);
    await page.locator("#usageReload").click();
    hold.on = false;
    hold.release();
    await settle(n + 2);
    assert.deepEqual(asks.slice(n), ["", "?asked=1"], "Refresh went after the load on its way");
    assert.deepEqual(errors, []);
  });
}
