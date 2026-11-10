// Run with Node's test runner and Playwright on the module path; see README.md.
// RealZeroP27 on X: switch which Codex account is used from the tray
// panel's Usage tab, not only from the Providers page. A vendor with
// several accounts has its count in the group's heading open the app's
// menu (never a <select>) of the accounts the provider editor can put
// first, the first ticked; the first account's card is ticked too. A pick
// does what the editor's Make first or Use does: signs Codex in to it
// (/api/login/switch), or, kept signed in to one, puts it first in the
// order (/api/provider/arrange). A setup-token's, an access token's and a
// lapsed account are not offered; a vendor with nothing to choose keeps a
// plain count. In every language, at two panel widths, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const iso = (n) => new Date(n).toISOString();
const USERS = ["a@example.com", "b@example.com", "c@example.com", "d@example.com"];

function quotas() {
  const now = Date.now(), readAt = iso(now - 60000), resetsAt = iso(now + 2 * 3600000);
  return [
    ...USERS.map((user, i) => ({ provider: "codex", name: "Codex", icon: "openai", user, plan: "Pro", readAt, windows: [{ name: "5 hours", used: 10 + i * 10, resetsAt }] })),
    // Claude: two accounts, the other a setup-token's, so nothing to choose
    ...["x@example.com", "y@example.com"].map((user) => ({ provider: "claude", name: "Claude", icon: "claude-color", user, plan: "Max", readAt, windows: [{ name: "5 hours", used: 5, resetsAt }] })),
  ];
}

// w: { active, order, kept (keepLoginAs or "") }
function providersOf(w) {
  const logins = w.order.map((user) => ({
    user, plan: "pro", active: user === w.active, on: user !== "c@example.com",
    ...(user === "d@example.com" ? { accessOnly: true } : {}),
  }));
  logins.push({ user: "e@example.com", plan: "plus", active: false, on: false, lapsed: "sign in again" });
  const codex = {
    id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "order",
    models: [], agents: [], fallback: [], headers: {}, keyList: [], keepLogin: !!w.kept, keepLoginAs: w.kept || "",
    account: { agent: "codex", agentName: "Codex", user: w.active, plan: "pro", logins },
  };
  const claude = {
    id: "claude", name: "Claude Code", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "order",
    models: [], agents: [], fallback: [], headers: {}, keyList: [],
    account: { agent: "claude", agentName: "Claude Code", user: "x@example.com", plan: "max", logins: [{ user: "x@example.com", plan: "max", active: true, on: true }, { user: "y@example.com", plan: "max", token: true, on: true }] },
  };
  return { providers: [codex, claude], presets: [], excluded: [], gateway: { running: true, window: true } };
}

function server(lang, w, log) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", guideHidden: true };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    // Codex is connected, so the Agents tab has no account line to load the list for
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", source: "chatgpt", wired: true, fields: [] }], profiles: [], settings });
    if (url.pathname === "/api/agents/cli" && req.method() === "GET") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/agents/install") return json([]);
    if (url.pathname === "/api/usage/quotas") return json(quotas());
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers" && req.method() === "GET") {
      log.push(url.searchParams.get("last") === "1" ? "kept" : "fresh");
      return json(url.searchParams.get("last") === "1" ? { ...providersOf(w), last: true } : providersOf(w));
    }
    if (req.method() === "POST" && ["/api/login/switch", "/api/provider/arrange", "/api/window/main"].includes(url.pathname)) {
      log.push(url.pathname + url.search + " " + (req.postData() || ""));
      if (url.pathname === "/api/login/switch") {
        const user = req.postDataJSON().user;
        w.active = user;
        w.order = [user, ...w.order.filter((u) => u !== user)];
        return json(providersOf(w));
      }
      if (url.pathname === "/api/provider/arrange") {
        w.order = req.postDataJSON().accountOrder.filter((u) => USERS.includes(u));
        return json(providersOf(w));
      }
      return route.fulfill({ status: 204 });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); }
    catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const K = {
  head: "Use first",
  manage: "Manage accounts…",
  count: "{n} accounts",
  first: "First",
  signed: "{agent} is now signed in as {user}",
  gateway: "The gateway now uses {user} first",
};
const group = (id) => `#panelQuota .pq-group[data-provider="${id}"]`;

async function open(browser, lang, width, w) {
  const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], log = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, w, log));
  await page.goto("http://magpie.test/?mode=panel");
  await page.locator('#ptabs [data-ptab="usage"]').click();
  await page.locator(`${group("codex")} .pq-pick`).waitFor();
  const tr = async (k, vars = {}) => {
    const s = await page.evaluate(([k, lang]) => (lang === "en" ? k : I18N[lang]?.[k]), [k, lang]);
    assert.ok(s, `${lang} has ${k.slice(0, 40)}`);
    return Object.entries(vars).reduce((s, [k, v]) => s.replaceAll(`{${k}}`, v), s);
  };
  return { page, errors, log, tr };
}

async function said(page, want) {
  await page.waitForFunction((w) => document.querySelector("#status")?.textContent === w, want).catch(() => {});
  assert.equal(await page.locator("#status").textContent(), want);
}

// waits out a redraw: the list read draws the group again
async function inside(page, sel, width) {
  await page.waitForFunction(([s, w]) => { const r = document.querySelector(s)?.getBoundingClientRect(); return r?.width && r.x >= 0 && r.right <= w + 0.5; }, [sel, width], { timeout: 5000 }).catch(() => {});
  const b = await page.locator(sel).boundingBox();
  assert.ok(b && b.x >= 0 && b.x + b.width <= width + 0.5, `${sel} off the panel: ${JSON.stringify(b)}`);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the panel's Usage tab puts a Codex account first", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [440, 360]) {
        await t.test(`${lang} ${width}px`, async () => {
          const w = { active: "a@example.com", order: [...USERS], kept: "" };
          const { page, errors, log, tr } = await open(browser, lang, width, w);
          for (const k of Object.values(K)) await tr(k);
          const pick = page.locator(`${group("codex")} .pq-pick`);
          assert.equal((await pick.textContent()).trim(), "4");
          assert.equal(await pick.getAttribute("aria-haspopup"), "menu");
          assert.equal(await pick.getAttribute("title"), await tr(K.count, { n: 4 }));
          await inside(page, `${group("codex")} .pq-pick`, width);
          // the first account's card is ticked
          await page.locator(`${group("codex")} .pq-card.first .pq-first`).waitFor();
          assert.equal(await page.locator(`${group("codex")} .pq-card.first .pq-user`).textContent(), "a@example.com");
          assert.equal(await page.locator(`${group("codex")} .pq-first`).getAttribute("title"), await tr(K.first));
          // Claude: its other account is a setup-token's, so a plain count
          assert.equal(await page.locator(`${group("claude")} .pq-pick`).count(), 0);
          assert.equal(await page.locator(`${group("claude")} span.pq-count`).textContent(), "2");
          assert.equal(await page.locator("#panelQuota select").count(), 0);

          await pick.click();
          const menu = page.locator(".proto-menu");
          await menu.waitFor();
          // the kept list drew it; the menu is a fresh read's
          assert.ok(log.includes("fresh"), `no fresh read before the menu: ${log}`);
          assert.equal(await menu.locator(".pm-head").textContent(), await tr(K.head));
          // never an access token's (d) or one to sign in again (e)
          assert.deepEqual(await menu.locator(".pm-item .pm-name").allTextContents(), ["a@example.com", "b@example.com", "c@example.com", await tr(K.manage)]);
          assert.equal(await menu.locator(".pm-item.on .pm-name").allTextContents().then((x) => x.join()), "a@example.com");
          await inside(page, ".proto-menu", width);

          // an account that is off is signed in to, as the editor's Use does
          await menu.locator(".pm-item", { hasText: "c@example.com" }).click();
          for (let i = 0; i < 100 && !log.some((x) => x.startsWith("/api/login/switch")); i++) await page.waitForTimeout(20);
          const sw = log.filter((x) => x.startsWith("/api/login/switch"));
          assert.equal(sw.length, 1, `switches: ${log}`);
          assert.deepEqual(JSON.parse(sw[0].slice(sw[0].indexOf(" ") + 1)), { agent: "codex", user: "c@example.com" });
          await said(page, await tr(K.signed, { agent: "Codex", user: "c@example.com" }));
          // the menu ticks the new first
          await pick.click();
          await menu.waitFor();
          assert.equal(await menu.locator(".pm-item.on .pm-name").allTextContents().then((x) => x.join()), "c@example.com");
          // Manage accounts… opens the window's Providers
          await menu.locator(".pm-item", { hasText: await tr(K.manage) }).click();
          for (let i = 0; i < 100 && !log.some((x) => x.startsWith("/api/window/main")); i++) await page.waitForTimeout(20);
          assert.ok(log.some((x) => x.startsWith("/api/window/main?view=providers")), `Manage accounts… didn't open the window: ${log}`);
          assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1), "the panel scrolls sideways");
          if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `panel-first-${engine}-${lang}-${width}.png`) });
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }
    }
  });

  test(engine + ": kept signed in to one, the pick puts it first in the order", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    // Codex kept on b; a is first in the order; c is off, so not offered
    const w = { active: "b@example.com", order: [...USERS], kept: "b@example.com" };
    const { page, errors, log, tr } = await open(browser, "en", 360, w);
    await page.locator(`${group("codex")} .pq-pick`).click();
    const menu = page.locator(".proto-menu");
    await menu.waitFor();
    assert.deepEqual(await menu.locator(".pm-item .pm-name").allTextContents(), ["a@example.com", "b@example.com", await tr(K.manage)]);
    assert.equal(await menu.locator(".pm-item.on .pm-name").allTextContents().then((x) => x.join()), "a@example.com");
    await menu.locator(".pm-item", { hasText: "b@example.com" }).click();
    for (let i = 0; i < 100 && !log.some((x) => x.startsWith("/api/provider/arrange")); i++) await page.waitForTimeout(20);
    const ar = log.filter((x) => x.startsWith("/api/provider/arrange"));
    assert.equal(ar.length, 1, `arranges: ${log}`);
    const body = JSON.parse(ar[0].slice(ar[0].indexOf(" ") + 1));
    assert.equal(body.id, "codex");
    assert.equal(body.accountOrder[0], "b@example.com");
    assert.ok(!log.some((x) => x.startsWith("/api/login/switch")), "kept signed in: Codex isn't signed in to another");
    await said(page, await tr(K.gateway, { user: "b@example.com" }));
    await page.locator(`${group("codex")} .pq-pick`).click();
    await menu.waitFor();
    assert.equal(await menu.locator(".pm-item.on .pm-name").allTextContents().then((x) => x.join()), "b@example.com");
    assert.deepEqual(errors, []);
    await page.context().close();
  });
}
