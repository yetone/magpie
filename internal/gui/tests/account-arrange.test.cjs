// Run with Node's test runner and the repository's existing Playwright setup.
// All API responses are fulfilled in memory; no server, live sign-ins or keys.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");

function fixture() {
  const base = { icon: "generic", chat: "https://example.invalid/v1", models: [], agents: [], fallback: [], headers: {}, key: { set: true, masked: "fixture" }, routing: "" };
  const logins = Array.from({ length: 18 }, (_, i) => ({ agent: "antigravity", user: `account-${String(i + 1).padStart(2, "0")}@example.test`, plan: "Pro", active: i === 0, on: i !== 2 }));
  const providers = { providers: [
    { ...base, id: "antigravity", name: "Antigravity", keyList: [], account: { agent: "antigravity", agentName: "Antigravity", user: logins[0].user, logins } },
    { ...base, id: "relay", name: "Relay", keyList: Array.from({ length: 5 }, (_, i) => ({ id: `key-${i + 1}`, name: `Key ${i + 1}`, masked: `test-key-${i + 1}`, active: i === 0, on: i !== 2 })) },
  ], presets: [], excluded: [], gateway: { running: false, window: true } };
  const state = { agents: ["alpha", "beta", "gamma"].map(id => ({ id, name: id, icon: "generic", path: "/fixture", fields: [] })), profiles: [], settings: { lang: "en", theme: "light" } };
  const f = { providers, state, posted: [], failNext: false };
  const items = p => p.account ? p.account.logins : p.keyList;
  const id = (p, item) => p.account ? item.user : item.id;
  const arrange = (p, order) => {
    const old = items(p), byID = new Map(old.map(item => [id(p, item), item]));
    assert.equal(new Set(order).size, old.length);
    assert(order.every(ref => byID.has(ref)));
    const next = order.map(ref => byID.get(ref));
    assert(next[0].on, "a disabled account must not be silently enabled");
    next.forEach((item, i) => { item.active = i === 0; });
    if (p.account) { p.account.logins = next; p.account.user = next[0].user; }
    else p.keyList = next;
  };
  f.route = async route => {
    const request = route.request(), url = new URL(request.url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs={lang:"en",theme:"light",web:true};' });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/login/usage") return json(Object.fromEntries(logins.map((l, i) => [l.user, { windows: Array.from({ length: i % 3 + 1 }, () => ({ label: "Weekly", used: 10, limit: 100 })) }])));
    if (request.method() === "POST" && url.pathname.startsWith("/api/")) {
      const body = request.postDataJSON();
      f.posted.push({ action: url.pathname, body });
      if (url.pathname === "/api/agents/arrange") { state.settings.agentOrder = body.order; return json(state.settings); }
      const p = providers.providers.find(p => p.id === (body.id || body.agent));
      if (url.pathname === "/api/provider/arrange") {
        if (f.failNext) { f.failNext = false; return json({ error: "Fixture save failure" }, 500); }
        const first = items(p).find(item => id(p, item) === body.accountOrder[0]);
        if (!first?.on) return json({ error: "turn this account on before moving it first" }, 400);
        arrange(p, body.accountOrder);
      }
      if (url.pathname === "/api/provider/route") p.routing = body.routing;
      if (["/api/keys/use", "/api/login/switch"].includes(url.pathname)) {
        const first = body.ref || body.user;
        arrange(p, [first, ...items(p).map(item => id(p, item)).filter(ref => ref !== first)]);
      }
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
  return f;
}

const rows = page => page.locator(".accts [data-account-id]");
const ids = page => rows(page).evaluateAll(rs => rs.map(r => r.dataset.accountId));
const settled = page => page.waitForFunction(() => !accountArranging && !accountSaving);
async function open(page, id) {
  await page.goto(`http://magpie.test/?view=providers&edit=${id}`);
  await rows(page).first().waitFor();
}
async function point(row) {
  await row.scrollIntoViewIfNeeded();
  return row.evaluate(r => {
    const b = r.getBoundingClientRect(), gap = r.querySelector(".grow").getBoundingClientRect();
    return { x: gap.width > 8 ? gap.left + gap.width / 2 : b.right - 5, y: b.top + b.height / 2, bottom: b.bottom };
  });
}
async function drag(page, cancel = false) {
  const a = await point(rows(page).nth(0)), b = await point(rows(page).nth(1));
  await page.mouse.move(a.x, a.y);
  await page.mouse.down();
  await page.mouse.move(a.x, b.bottom - 2, { steps: 12 });
  await page.locator(".acc.dragging").waitFor();
  if (cancel) await page.keyboard.press("Escape");
  await page.mouse.up();
  await settled(page);
}
async function checkFirst(page, f, id) {
  const p = f.providers.providers.find(p => p.id === id), items = p.account ? p.account.logins : p.keyList;
  const first = p.account ? items[0].user : items[0].id;
  assert.equal((await ids(page))[0], first, "displayed first equals the fixture's routing first; native routing is tested in Go");
  assert(items[0].active && items[0].on);
  assert.equal(await rows(page).first().locator(".using").textContent(), "First");
  assert.equal(await rows(page).locator(".using").count(), 1);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: account dragging uses the routing order in every mode`, { timeout: 90000 }, async t => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const context = await browser.newContext({ viewport: { width: 1000, height: 850 }, reducedMotion: "reduce", hasTouch: true });
    const page = await context.newPage(), f = fixture(), errors = [];
    page.setDefaultTimeout(5000);
    page.on("pageerror", e => errors.push(e.message));
    await page.route("**/*", f.route);
    for (const id of ["antigravity", "relay"]) {
      await open(page, id);
      for (const [mode, label] of [["", "Smart"], ["order", "In order"], ["rotate", "In turn"], ["usage", "Least used first"]]) {
        // Smart may already be selected. The old routing value alone says
        // nothing about whether its POST has finished redrawing the editor.
        const oldFirst = await rows(page).first().elementHandle();
        const routeResponse = page.waitForResponse(response =>
          new URL(response.url()).pathname === "/api/provider/route" && response.request().method() === "POST");
        await page.locator(".editor .segs button", { hasText: new RegExp(`^${label}$`) }).click();
        assert.equal((await routeResponse).status(), 200);
        await page.waitForFunction(row => !row.isConnected, oldFirst);
        assert.equal(f.providers.providers.find(p => p.id === id).routing, mode);
        const before = await ids(page), count = f.posted.filter(p => p.action === "/api/provider/arrange").length;
        await drag(page);
        assert.deepEqual(await ids(page), [before[1], before[0], ...before.slice(2)]);
        assert.equal(f.posted.filter(p => p.action === "/api/provider/arrange").length, count + 1);
        await checkFirst(page, f, id);
        await page.reload(); await rows(page).first().waitFor();
        await checkFirst(page, f, id);
        const wanted = (await ids(page))[1];
        await rows(page).nth(1).getByRole("button", { name: "Make first", exact: true }).click();
        await page.waitForFunction(ref => document.querySelector(".accts [data-account-id]").dataset.accountId === ref, wanted);
        await checkFirst(page, f, id);
      }
      const before = await ids(page), count = f.posted.length;
      await drag(page, true);
      assert.deepEqual(await ids(page), before);
      assert.equal(f.posted.length, count);
      await rows(page).first().focus(); await page.keyboard.press("Alt+ArrowDown"); await settled(page);
      assert.deepEqual(await ids(page), [before[1], before[0], ...before.slice(2)]);
      assert.equal(await page.evaluate(() => document.activeElement.dataset.accountId), before[0]);
      await checkFirst(page, f, id);
      const saved = await ids(page);
      f.failNext = true; await drag(page);
      assert.deepEqual(await ids(page), saved, "failed save rolls back");
      await checkFirst(page, f, id);
      // Disabled rows may move among the others, but not become First merely
      // by dragging: neither routing nor enabled state may silently change.
      const off = saved[2];
      await rows(page).nth(2).focus(); await page.keyboard.press("Alt+ArrowUp"); await settled(page);
      const movedOff = [saved[0], off, saved[1], ...saved.slice(3)];
      assert.deepEqual(await ids(page), movedOff);
      await page.keyboard.press("Alt+ArrowUp"); await settled(page);
      assert.deepEqual(await ids(page), movedOff);
      await checkFirst(page, f, id);
      await page.keyboard.press("Alt+ArrowDown"); await settled(page);
      assert.deepEqual(await ids(page), saved);
      assert.equal(await page.locator(".accts .grip, .accts .ag-handle").count(), 0);
      const style = await rows(page).first().evaluate(r => ({ select: getComputedStyle(r.querySelector(".n")).userSelect, touch: getComputedStyle(r).touchAction }));
      assert.notEqual(style.select, "none"); assert.equal(style.touch, "pan-y");
    }
    // A name remains editable, and a single account needs no sort affordance.
    await page.locator(".accts .rename").first().click();
    await page.locator(".rename-in").waitFor(); await page.keyboard.press("Escape");
    await page.evaluate(() => { providers.providers.find(p => p.id === "relay").keyList.length = 1; renderProviders(); });
    assert.equal(await page.locator(".accts").evaluate(r => r.classList.contains("reorderable")), false);
    // Account text selection must not turn into a drag.
    await open(page, "antigravity");
    const name = await rows(page).first().locator(".n").boundingBox();
    const prior = await ids(page), count = f.posted.length;
    await page.mouse.move(name.x + 2, name.y + name.height / 2); await page.mouse.down();
    await page.mouse.move(name.x + name.width - 2, name.y + name.height / 2, { steps: 10 }); await page.mouse.up();
    assert(await page.evaluate(() => getSelection().toString().length > 0));
    assert.deepEqual(await ids(page), prior); assert.equal(f.posted.length, count);
    // Background quota refresh cannot replace a captured row; edge scroll
    // reaches rows below the fold without rebuilding during the drag.
    const a = await point(rows(page).first());
    const bounds = await page.locator(".ebody").boundingBox();
    await page.mouse.move(a.x, a.y); await page.mouse.down();
    await page.mouse.move(a.x, bounds.y + bounds.height - 5, { steps: 20 });
    await page.locator(".acc.dragging").waitFor();
    await page.waitForFunction(() => document.querySelector(".ebody").scrollTop > 100);
    assert(await page.evaluate(() => { const row = document.querySelector(".acc.dragging"); renderProviders(); return row === document.querySelector(".acc.dragging") && accountRenderPending; }));
    await page.mouse.up(); await settled(page); await checkFirst(page, f, "antigravity");
    // Existing Agent sorting still works, with no unused landing styles.
    await page.goto("http://magpie.test/?view=agents");
    await page.locator("#agents > .agent").first().waitFor();
    const agents = await page.locator("#agents > .agent").evaluateAll(rs => rs.map(r => ({ id: r.dataset.id, box: r.querySelector(".ag-handle").getBoundingClientRect().toJSON() })));
    const h = agents[0].box, target = agents[1].box;
    await page.mouse.move(h.left + h.width / 2, h.top + h.height / 2); await page.mouse.down();
    await page.mouse.move(target.left + target.width / 2, target.bottom + 4, { steps: 12 });
    await page.locator("#agents > .dragging").waitFor();
    assert(await page.evaluate(() => { const row = document.querySelector("#agents > .dragging"); renderAgents(); return row === document.querySelector("#agents > .dragging") && agentRenderPending; }));
    await page.mouse.up();
    await page.waitForFunction(id => document.querySelector("#agents > .agent").dataset.id === id, agents[1].id);
    assert.deepEqual(f.posted.filter(p => p.action === "/api/agents/arrange").at(-1).body.order, [agents[1].id, agents[0].id, ...agents.slice(2).map(a => a.id)]);
    assert.deepEqual(errors, []);
  });
}
