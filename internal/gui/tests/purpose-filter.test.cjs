// Purpose filtering on the real page assets, with isolated API fixtures.
// Run as described in README.md; no local accounts or providers are used.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
// Wheel into view before clicking: the app undoes unsolicited auto-scroll.
// Cover the long ledger and the Routing filters below a narrow-window stage.
async function click(page, locator) {
  await locator.waitFor({ state: "visible" });
  const view = await locator.evaluate((e) => e.closest("#view-usage, #view-routing")?.id);
  if (view) {
    const v = await page.locator("#" + view).boundingBox();
    await page.mouse.move(v.x + 40, v.y + v.height / 2);
    for (let i = 0; i < 80; i++) {
      const b = await locator.boundingBox();
      if (b.y >= v.y && b.y + b.height <= v.y + v.height) break;
      await page.mouse.wheel(0, b.y < v.y ? -120 : 120);
      await page.waitForTimeout(40);
    }
    await page.waitForTimeout(300);
  }
  await locator.click();
}
const assets = process.env.ASSET_DIR || path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((v) => String(v).padStart(2, "0")).join("-");
const titleKinds = ["thread_title", "thread_title_reconsideration", "title_generation", "title"];
const unknown = "future<&purpose>";
const rows = Array.from({ length: 108 }, (_, i) => ({
  route_id: i + 1, t: new Date(now - i * 1000).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color",
  provider: "openai", providerName: "OpenAI", model: "example-model", req: "example-model", served: "example-model",
  kind: i < 104 ? titleKinds[i % 4] : [unknown, "", "review", "unmarked"][i - 104],
  in: 10, out: 2, cache_read: 3, cache_write: 1, ms: 200, status: i === 0 ? 429 : 200, cost: 0.01, priced: true,
}));
const purposes = ["kind:thread_title", "kind:" + unknown, "unmarked", "kind:review", "kind:unmarked"];
const purpose = (r) => titleKinds.includes(r.kind) ? "kind:thread_title" : r.kind ? "kind:" + r.kind : "unmarked";
const routeKinds = [...titleKinds, unknown, "", "review", "unmarked"];
const seat = { id: "openai", provider: "openai", name: "OpenAI", kind: "provider", model: "example-model" };
const routes = routeKinds.map((kind, i) => ({ id: 100 - i, seq: 100 - i, time: new Date(now - i * 1000).toISOString(),
  agent: "codex", model: "example-model", provider: "openai", kind, session: "test-chat", tokens: 12, cost: 0.01, priced: true,
  done: true, status: 200, ms: 200, order: [seat], tries: [{ id: "openai", model: "example-model", start: now.toISOString(), done: true, status: 200, ms: 200 }],
}));
function ledger(q) {
  let filtered = rows;
  if (q.get("purpose")) filtered = filtered.filter((r) => purpose(r) === q.get("purpose"));
  if (q.get("route")) filtered = filtered.filter((r) => r.route_id === +q.get("route"));
  if (q.get("failed") === "1") filtered = filtered.filter((r) => r.status >= 400);
  const offset = +q.get("offset") || 0, limit = +q.get("limit") || 100;
  return { rows: filtered.slice(offset, offset + limit), offset, total: filtered.length, calls: filtered.length,
    errors: filtered.filter((r) => r.status >= 400).length,
    input: filtered.length * 10, output: filtered.length * 2, cache_read: filtered.length * 3, cache_write: filtered.length,
    cost: filtered.length * 0.01, unpriced: 0, agents: [{ id: "codex", name: "Codex", icon: "codex-color" }], purposes };
}
function server(lang, asked) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/fixture/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) await new Promise((r) => setTimeout(r, 2000));
      return json({ mine: true, seq: 1, now: now.toISOString(), totals: { requests: 8, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day, requests: 8 }], routes: url.searchParams.get("day") ? routes : [] });
    if (url.pathname === "/api/usage/requests") { asked.push(url.searchParams); return json(ledger(url.searchParams)); }
    if (url.pathname === "/api/usage/requests/export") { asked.push(Object.assign(url.searchParams, { method: req.method() })); return json({ path: "~/Downloads/purpose.csv", rows: ledger(url.searchParams).total }); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: purpose filters preserve aliases, unknown names, totals and exports`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(7000);
      const errors = [], asked = [];
      // WebKit reports a ResizeObserver round left to the next frame as a page
      // error; Chromium doesn't. A purpose picked from a long list shrinks the
      // Usage page, the click's hold (heldSizes) gives it room again before the
      // paint, and the page's scrollbar coming back narrows the ledger by 8px
      // in that same frame, so its own observers are told once more. Under
      // load that lands past the loop's depth. Nothing is lost or painted
      // wrong: the round is delivered on the next frame.
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, asked));
      const choose = async (id, value) => {
        const routing = id === "#rtPurpose";
        await click(page, page.locator(id));
        const wanted = routing ? await page.evaluate((v) => v ? purposeOptions([v], v)[0].name : t("All purposes"), value) : value;
        const item = page.locator(".proto-menu .pm-item");
        const matches = await item.evaluateAll((els, { wanted, routing }) => els.map((e, i) =>
          (routing ? e.querySelector(".pm-name").textContent : e.title) === wanted ? i : -1).filter((i) => i >= 0), { wanted, routing });
        assert.equal(matches.length, 1, `one menu choice for ${value}`);
        const name = await item.nth(matches[0]).locator(".pm-name").textContent();
        const label = routing ? await page.evaluate(({ value, name }) => value ? t("Purpose: {name}", { name }) : t("Purpose filter"), { value, name }) : name;
        const target = item.nth(matches[0]);
        if (!routing || !value || await target.getAttribute("aria-checked") !== "true") await target.click();
        if (routing && value) {
          const chosen = await page.locator('.rt-purpose-menu [role="menuitemcheckbox"][aria-checked="true"] .pm-name').allTextContents();
          for (const name of chosen) {
            if (name !== wanted) await page.locator(".rt-purpose-menu").getByRole("menuitemcheckbox", { name, exact: true }).click();
          }
        }
        await page.waitForFunction(({ id, label }) => document.querySelector(id + " span").textContent === label, { id, label });
        if (routing && await page.locator(id).getAttribute("aria-expanded") === "true") await page.keyboard.press("Escape");
      };
      const waitRows = async (n) => page.waitForFunction((n) => document.querySelectorAll("#ledWrap tbody tr.led-row").length === n, n);
      await page.goto("http://magpie.test/?view=usage&tab=requests");
      await waitRows(100);
      await choose("#ledPurpose", "kind:thread_title");
      await page.waitForFunction(() => document.querySelector("#ledSum").textContent.startsWith("104"));
      assert.equal(asked.at(-1).get("purpose"), "kind:thread_title");
      assert.equal(asked.at(-1).get("offset"), "0");
      await click(page, page.locator("#ledPager button").last());
      await waitRows(4);
      assert.equal(asked.at(-1).get("offset"), "100");
      assert.match(await page.locator("#ledSum").textContent(), /^104/);
      await choose("#ledPurpose", "kind:" + unknown);
      await waitRows(1);
      assert.equal(asked.at(-1).get("offset"), "0");
      assert.equal(await page.locator("#ledPurpose span").textContent(), unknown);
      await choose("#ledPurpose", "unmarked");
      await waitRows(1);
      assert.equal(await page.locator("#ledPurpose span").textContent(), lang === "zh" ? "未标记" : "Unmarked");
      await choose("#ledPurpose", "kind:thread_title");
      await waitRows(100);
      await click(page, page.locator("#ledStatus .opt").last());
      await waitRows(1);
      await click(page, page.locator("#ledExport"));
      await page.waitForFunction(() => document.querySelector("#status")?.textContent.includes("purpose.csv"));
      const exported = asked.findLast((q) => q.method === "POST");
      assert.equal(exported.get("purpose"), "kind:thread_title");
      assert.equal(exported.get("failed"), "1");
      // A route opened from elsewhere clears the purpose temporarily and restores it.
      await page.evaluate(() => window.openUsageRoute({ id: 107, time: new Date().toISOString(), model: "example-model" }));
      await page.locator("#ledRoute").waitFor({ state: "visible" });
      await waitRows(1);
      assert.equal(asked.at(-1).has("purpose"), false);
      await click(page, page.locator("#ledRouteClear"));
      await page.waitForFunction(() => document.querySelector("#ledRoute").hidden);
      assert.equal(asked.at(-1).get("purpose"), "kind:thread_title");
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-usage-purpose.png`) });
      }
      // The same aliases and unknown names in both live and historical Routing.
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-req").nth(7).waitFor();
      assert.equal(await page.locator("#rtPurpose span").textContent(), lang === "zh" ? "用途" : "Purpose filter");
      await click(page, page.locator("#rtPurpose"));
      assert.equal(await page.locator(".rt-purpose-menu input").count(), 0);
      assert.equal(await page.locator("#rtPurpose").getAttribute("aria-expanded"), "true");
      await page.keyboard.press("ArrowDown");
      await page.keyboard.press("Escape");
      assert.equal(await page.locator("#rtPurpose").getAttribute("aria-expanded"), "false");
      assert.equal(await page.locator("#rtPurpose").evaluate((e) => e === document.activeElement), true);
      await choose("#rtPurpose", "kind:thread_title");
      assert.equal(await page.locator(".rt-req").count(), 4);
      // Cross-page navigation must reveal a target excluded by the current purpose.
      await page.evaluate(() => window.openRoute(94, new Date().toISOString()));
      await page.waitForFunction(() => document.querySelectorAll(".rt-req").length === 8);
      assert.equal(await page.locator(".rt-req").count(), 8);
      await choose("#rtPurpose", "kind:thread_title");
      await page.locator(".rt-group-by button").last().click();
      assert.match(await page.locator(".rt-session .summary").textContent(), /^4/);
      await page.locator(".rt-days .rt-day").nth(1).click();
      assert.equal(await page.locator(".rt-req").count(), 4);
      await choose("#rtPurpose", "kind:" + unknown);
      assert.equal(await page.locator(".rt-req").count(), 1);
      assert.equal(await page.locator(".rt-req .kind").textContent(), unknown);
      await choose("#rtPurpose", "unmarked");
      assert.equal(await page.locator(".rt-req").count(), 1);
      assert.equal(await page.locator(".rt-req .kind").count(), 0);
      assert.equal(await page.locator("#rtPurpose span").textContent(), lang === "zh" ? "用途：未标记" : "Purpose: Unmarked");
      await click(page, page.locator("#rtPurposeClear"));
      await page.waitForFunction(() => document.querySelector("#rtPurposeClear").hidden && document.querySelectorAll(".rt-req").length === 8);
      assert.equal(await page.locator(".rt-req").count(), 8);
      assert.equal(await page.locator(".rt-group-by button").last().getAttribute("aria-pressed"), "true");
      assert.equal(await page.locator(".rt-days .rt-day").nth(1).getAttribute("aria-pressed"), "true");
      await choose("#rtPurpose", "unmarked");
      await choose("#rtPurpose", "");
      assert.equal(await page.locator(".rt-req").count(), 8);
      await page.setViewportSize({ width: 560, height: 800 });
      // Let the resize handler dismiss any old popup before opening a new one.
      await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      await choose("#rtPurpose", "kind:thread_title");
      const bounds = await page.locator(".rt-req-head").evaluate((e) => {
        const r = e.getBoundingClientRect(), b = document.querySelector("#rtPurpose").getBoundingClientRect();
        return { left: b.left >= r.left - 1, right: b.right <= r.right + 1 };
      });
      assert(bounds.left && bounds.right, JSON.stringify(bounds));
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-routing-purpose.png`) });
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: Routing purpose checkboxes show the union and stay open`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(7000);
      const errors = [];
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, []));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-req").nth(7).waitFor();
      const names = await page.evaluate(() => Object.fromEntries(purposeOptions(["kind:thread_title", "unmarked", "kind:review"]).map((o) => [o.v, o.name])));
      const menu = page.locator(".rt-purpose-menu");
      const title = menu.getByRole("menuitemcheckbox", { name: names["kind:thread_title"], exact: true });
      const unmarked = menu.getByRole("menuitemcheckbox", { name: names.unmarked, exact: true });
      const review = menu.getByRole("menuitemcheckbox", { name: names["kind:review"], exact: true });
      const waitRows = async (n) => page.waitForFunction((n) => document.querySelectorAll(".rt-req").length === n, n);
      await click(page, page.locator("#rtPurpose"));
      assert.equal(await menu.getByRole("menuitemcheckbox").count(), purposes.length, "purposes are checkbox choices");
      const scrollTop = await page.locator("#view-routing").evaluate((e) => e.scrollTop);
      await title.click();
      await waitRows(4);
      assert.equal(await page.locator(".rt-req").count(), 4, "all four title aliases match one checkbox");
      await unmarked.click();
      await waitRows(5);
      assert.equal(await page.locator(".rt-req").count(), 5, "selected purposes are combined");
      assert.equal(await menu.isVisible(), true, "ticking keeps the menu open");
      assert.equal(await title.getAttribute("aria-checked"), "true");
      assert.equal(await unmarked.getAttribute("aria-checked"), "true");
      assert.equal(await unmarked.evaluate((e) => e === document.activeElement), true);
      assert.equal(await page.locator("#view-routing").evaluate((e) => e.scrollTop), scrollTop, "ticking does not scroll the page");
      const label = await page.locator("#rtPurpose span").textContent();
      assert(label.includes(names["kind:thread_title"]) && label.includes(names.unmarked), "the heading names every selected purpose");
      await review.focus();
      await page.keyboard.press("Space");
      await waitRows(6);
      assert.equal(await page.locator(".rt-req").count(), 6, "keyboard adds another purpose");
      assert.equal(await review.getAttribute("aria-checked"), "true");
      await page.keyboard.press("Enter");
      await waitRows(5);
      assert.equal(await page.locator(".rt-req").count(), 5, "keyboard removes a purpose");
      assert.equal(await menu.isVisible(), true);
      await page.keyboard.press("Escape");
      assert.equal(await page.locator("#rtPurpose").evaluate((e) => e === document.activeElement), true);
      await page.locator(".rt-group-by button").last().click();
      assert.match(await page.locator(".rt-session .summary").textContent(), /^5/);
      await page.locator(".rt-days .rt-day").nth(1).click();
      assert.equal(await page.locator(".rt-req").count(), 5, "the same union applies to history and sessions");
      // Cross-page navigation keeps the union if its target already matches.
      await page.evaluate(() => window.openRoute(95, new Date().toISOString()));
      assert.equal(await page.locator(".rt-req").count(), 5);
      assert.equal(await page.locator("#rtPurpose span").textContent(), label);
      for (const width of [1100, 560, 360]) {
        await page.setViewportSize({ width, height: 800 });
        await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        await click(page, page.locator("#rtPurpose"));
        const bounds = await menu.boundingBox();
        assert(bounds.x >= 0 && bounds.x + bounds.width <= width, `menu fits at ${width}px`);
        assert.equal(await title.getAttribute("aria-checked"), "true");
        assert.equal(await unmarked.getAttribute("aria-checked"), "true");
        await title.click();
        await waitRows(1);
        assert.equal(await page.locator(".rt-req").count(), 1, "unticking keeps the other purpose");
        await title.click();
        await waitRows(5);
        assert.equal(await page.locator(".rt-req").count(), 5);
        await page.keyboard.press("Escape");
      }
      await click(page, page.locator("#rtPurpose"));
      const view = await page.locator("#view-routing").boundingBox();
      await page.mouse.move(view.x + 10, view.y + 80);
      await page.mouse.wheel(0, -200);
      await menu.waitFor({ state: "detached" });
      await click(page, page.locator("#rtPurpose"));
      await title.click();
      await unmarked.click();
      await waitRows(8);
      assert.equal(await page.locator(".rt-req").count(), 8, "unticking the last purpose restores all");
      assert.equal(await page.locator("#rtPurposeClear").isHidden(), true);
      assert.equal(await menu.isVisible(), true, "unticking the last purpose keeps the menu open");
      assert.equal(await unmarked.evaluate((e) => e === document.activeElement), true, "an open menu keeps focus on the checkbox");
      await title.click();
      await unmarked.click();
      const all = await page.evaluate(() => t("All purposes"));
      const beforeAll = await page.locator("#view-routing").evaluate((e) => e.scrollTop);
      await menu.locator(".pm-item").filter({ has: page.locator(".pm-name", { hasText: all }) }).focus();
      await page.keyboard.press("Enter");
      await waitRows(8);
      assert.equal(await page.locator(".rt-req").count(), 8, "All purposes resets the whole union");
      assert.equal(await menu.count(), 0);
      assert.equal(await page.locator("#rtPurpose").getAttribute("aria-expanded"), "false");
      assert.equal(await page.locator("#rtPurpose").evaluate((e) => e === document.activeElement), true, "All purposes returns keyboard focus to the purpose button");
      assert.equal(await page.locator("#view-routing").evaluate((e) => e.scrollTop), beforeAll, "returning focus does not scroll the page");
      await page.keyboard.press("Enter");
      assert.equal(await menu.isVisible(), true, "the purpose button can reopen the menu with the keyboard");
      await title.click();
      await unmarked.click();
      await page.keyboard.press("Escape");
      await click(page, page.locator("#rtPurposeClear"));
      await waitRows(8);
      assert.equal(await page.locator(".rt-req").count(), 8, "Clear filter also resets the whole union");
      assert.deepEqual(errors, []);
    });
  }
}
