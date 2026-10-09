const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
// a time as the gateway writes it: local, with its offset, so its date is
// the local day its history keeps it under (a UTC time is a day off for
// the hours between the two midnights)
const stamp = (d) => {
  const o = -d.getTimezoneOffset(), p = (n) => String(Math.floor(Math.abs(n))).padStart(2, "0");
  return new Date(d.getTime() + o * 60e3).toISOString().slice(0, 19) + (o < 0 ? "-" : "+") + p(o / 60) + ":" + p(o % 60);
};
function req(id, agent, session, cost, priced = true) {
  const time = stamp(new Date(now.getTime() - (10 - id) * 60e3));
  const seat = { id: "relay", provider: "relay", name: "Relay", who: "Test account", kind: "account", model: id === 1 ? "model-a" : "model-b" };
  return { id, seq: id, time, agent, session, model: `relay/${seat.model}`, provider: "relay", cost, priced,
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: time, done: true, status: 200, ms: 4500, effort: "medium" }],
    done: true, status: 200, ms: 4500, tokens: 2000, ttft: 1200 };
}
const initial = [req(1, "codex", "conversation-a", 0.01), req(2, "claude", "conversation-a", 0),
  req(3, "codex", "conversation-a", 0, false), req(4, "codex", "conversation-b", 0.02),
  req(5, "codex", "conversation-a", 0.02), req(6, "codex", "", 0, false)];

function serve(lang, feed, fixture = initial) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", fields: [] }, { id: "claude", name: "Claude Code", fields: [] }], profiles: [], settings: { lang, theme: "dark" } });
    if (url.pathname === "/api/gateway/trace") {
      const routes = url.searchParams.has("wait") ? await new Promise((resolve) => { feed.next = (value) => { feed.next = null; resolve(value); }; }) : feed.routes || fixture;
      return json({ mine: true, now: now.toISOString(), seq: routes.at(-1)?.seq || 1, totals: { requests: 6, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day, requests: fixture.length }], routes: url.searchParams.get("day") ? feed.routes || fixture : [] });
    if (url.pathname === "/api/gateway/route" && feed.route) return json(feed.route);
    if (url.pathname === "/api/gateway/session-titles") {
      const input = route.request().postDataJSON();
      feed.titleRequests?.push(input);
      if (input.ids.length > 2000 || input.routeIds.length > 2000) return route.fulfill({ status: 400, body: "too many IDs" });
      return json(typeof feed.names === "function" ? feed.names(input) : feed.names || {});
    }
    if (url.pathname === "/api/clis") return json({ agents: [], providers: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: routing groups by agent and session, totals known costs and keeps folds on live updates`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const errors = [], feed = {};
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, feed));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-group-by button").nth(1).click();
      await page.locator(".rt-session").nth(3).waitFor();
      const group = page.locator("button.rt-session").filter({ hasText: "Codex · conversation-a" });
      assert.equal(await page.locator(".rt-session").count(), 4, "identical IDs from different agents must not merge");
      assert.match(await group.locator(".summary").textContent(), lang === "zh" ? /3 个请求.*6.0k/ : /3 requests.*6.0k/);
      assert.equal(await group.locator(".cost").textContent(), "≈$0.030+", "partial totals must be marked");
      assert.equal(await page.locator("button.rt-session").filter({ hasText: "Claude Code" }).locator(".cost").textContent(), "≈$0.000", "zero price is known");
      assert.equal(await page.locator("div.rt-session .cost").isVisible(), false, "unknown cost is omitted for legacy requests");
      assert.equal(await page.locator(".rt-req").count(), 6);

      await group.click();
      assert.equal(await group.getAttribute("aria-expanded"), "false");
      assert.equal(await page.locator(".rt-req").count(), 3);
      const handle = await group.elementHandle();
      await page.waitForFunction(() => document.querySelectorAll(".rt-req").length === 3);
      // Add one more call to the folded conversation through the real poll path.
      for (let i = 0; i < 50 && !feed.next; i++) await page.waitForTimeout(20);
      assert(feed.next, "long poll started");
      feed.next([req(7, "codex", "conversation-a", 0.01)]);
      await page.waitForFunction(() => [...document.querySelectorAll("button.rt-session")].some((e) => e.textContent.includes("≈$0.040+")));
      assert.equal(await group.getAttribute("aria-expanded"), "false");
      assert(await group.evaluate((e, previous) => e === previous, handle), "live update replaced the heading");

      // Currency changes redraw routing costs using the existing formatter.
      await page.evaluate(() => { currency = "cny"; fx = { rate: 7, at: null, stale: false }; renderCosts(); });
      assert.equal(await group.locator(".cost").textContent(), "≈¥0.280+");
      await group.click();
      assert.equal(await page.locator(".rt-req").count(), 7);
      await page.locator(".rt-group-by button").first().click();
      assert.equal(await page.locator(".rt-session").count(), 0);
      assert.equal(await page.locator(".rt-req").count(), 7);
      await page.locator(".rt-day").filter({ hasText: lang === "zh" ? "今天" : "today" }).click();
      await page.waitForFunction(() => document.querySelectorAll(".rt-req").length === 6);
      await page.locator(".rt-group-by button").nth(1).click();
      assert.equal(await page.locator(".rt-session").count(), 4);
      // Original request selection and its routing story still work.
      await page.locator(".rt-req").filter({ hasText: "model-a" }).click();
      await page.waitForFunction(() => document.querySelector(".rt-steps").textContent.includes("model-a"));
      assert.match(await page.locator(".rt-steps").textContent(), /model-a/);
      for (const width of [1440, 560]) {
        await page.setViewportSize({ width, height: 800 });
        await page.waitForTimeout(150);
        const fit = await page.locator(".rt-reqs").evaluate((e) => ({ client: e.clientWidth, scroll: e.scrollWidth }));
        assert(fit.scroll <= fit.client + 1, `request list overflows at ${width}: ${JSON.stringify(fit)}`);
      }
      await page.setViewportSize({ width: 1100, height: 1200 });
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.locator(".rt-reqs").hover();
        await page.mouse.wheel(0, -3000);
        await page.waitForTimeout(200);
        await page.locator(".rt-cols").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-routing-sessions.png`) });
      }
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: background memory groups explain their purpose and keep their own identity`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const errors = [], feed = {};
      const fixture = [
        { ...req(1, "codex", "chat", 0.01), sessionTitle: "Chat title" },
        { ...req(2, "codex", "chat", 0.02), kind: "memory_consolidation", sessionTitle: "Chat title" },
        { ...req(3, "codex", "memory-a", 0.03), kind: "memory_consolidation" },
        { ...req(4, "codex", "memory-a", 0.04), kind: "memgen" },
        { ...req(5, "codex", "memory-b", 0.05), kind: "memory" },
        { ...req(6, "codex", "named-memory", 0.06), kind: "memory_consolidation", sessionTitle: "Named memory" },
        { ...req(7, "codex", "mixed", 0.07), kind: "memory_consolidation" },
        req(8, "codex", "mixed", 0.08),
        req(9, "codex", "unknown", 0.09),
        { ...req(10, "codex", "", 0.1), kind: "memory_consolidation" },
      ];
      await page.route("**/*", serve(lang, feed, fixture));
      page.on("pageerror", (e) => errors.push(e.message));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-group-by button").nth(1).click();
      await page.locator(".rt-session").nth(6).waitFor();
      const group = (id) => page.locator(`button.rt-session:has(.nm[title$="${id}"])`);
      const memoryName = lang === "zh" ? "后台记忆整理" : "Background memory task";
      assert.equal(await group("memory-a").locator(".nm").textContent(), `Codex · ${memoryName}`);
      assert.equal(await group("memory-b").locator(".nm").textContent(), `Codex · ${memoryName}`);
      assert.equal(await group("memory-a").locator(".cost").textContent(), "≈$0.070");
      assert.match(await group("memory-a").locator(".summary").textContent(), lang === "zh" ? /2 个请求/ : /2 requests/);
      assert.match(await group("memory-a").locator(".nm").getAttribute("title"), lang === "zh" ? /回答结束后.*继续/ : /continue after a chat finishes/);
      assert.equal(await group("chat").locator(".nm").textContent(), "Codex · Chat title");
      assert.equal(await group("named-memory").locator(".nm").textContent(), "Codex · Named memory");
      assert.equal(await group("mixed").locator(".nm").textContent(), "Codex · mixed");
      assert.equal(await group("unknown").locator(".nm").textContent(), "Codex · unknown");
      assert.equal(await page.locator("div.rt-session .nm").textContent(), lang === "zh" ? "未提供会话标识" : "No session ID");
      assert.equal(await page.locator(".rt-session").count(), 7, "separate memory IDs must not merge with each other or the chat");

      const memory = group("memory-a"), handle = await memory.elementHandle();
      await memory.click();
      for (let i = 0; i < 50 && !feed.next; i++) await page.waitForTimeout(20);
      assert(feed.next, "long poll started");
      feed.next([{ ...req(11, "codex", "memory-a", 0.01), kind: "memory_consolidation" }]);
      await page.waitForFunction(() => [...document.querySelectorAll("button.rt-session .cost")].some((e) => e.textContent === "≈$0.080"));
      assert.equal(await memory.getAttribute("aria-expanded"), "false");
      assert(await memory.evaluate((e, previous) => e === previous, handle), "labeling must preserve the heading through live updates");
      assert.equal(await group("memory-b").getAttribute("aria-expanded"), "true");
      await memory.click();
      for (const width of [1100, 560]) {
        await page.setViewportSize({ width, height: 800 });
        await page.waitForTimeout(100);
        const fit = await page.locator(".rt-reqs").evaluate((e) => ({ client: e.clientWidth, scroll: e.scrollWidth }));
        assert(fit.scroll <= fit.client + 1, `request list overflows at ${width}: ${JSON.stringify(fit)}`);
      }
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.setViewportSize({ width: 1100, height: 1000 });
        await page.locator(".rt-reqs").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-background-memory.png`) });
      }
      await page.locator(".rt-group-by button").first().click();
      assert.equal(await page.locator(".rt-req").count(), 11, "by-request view retains all requests");
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: background suggestion groups explain their purpose without renaming mixed chats`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const errors = [], feed = {};
      const fixture = [
        { ...req(1, "codex", "suggestions-a", 0.01), kind: "ambient_suggestion_safety" },
        { ...req(2, "codex", "suggestions-a", 0.02), kind: "ambient_suggestions" },
        { ...req(3, "codex", "safety-only", 0.03), kind: "ambient_suggestion_safety" },
        { ...req(4, "codex", "generation-only", 0.04), kind: "ambient_suggestions" },
        { ...req(5, "codex", "named-suggestions", 0.05), kind: "ambient_suggestions", sessionTitle: "Named suggestions" },
        req(6, "codex", "mixed", 0.06),
        { ...req(7, "codex", "mixed", 0.07), kind: "ambient_suggestions" },
        { ...req(8, "codex", "named-chat", 0.08), sessionTitle: "Chat title" },
        { ...req(9, "codex", "named-chat", 0.09), kind: "ambient_suggestion_safety" },
        { ...req(10, "claude", "other-agent", 0.1), kind: "ambient_suggestions" },
        { ...req(11, "codex", "", 0.11), kind: "ambient_suggestions" },
      ];
      // the gateway names Codex sessions from the same threads its trace's
      // titles come from, so its answer agrees with them. The Claude Code row
      // has the page ask for names 300ms after it draws, and an empty answer
      // there took the titles away mid-test on a loaded machine
      feed.names = { "named-suggestions": "Named suggestions", "named-chat": "Chat title" };
      await page.route("**/*", serve(lang, feed, fixture));
      page.on("pageerror", (e) => errors.push(e.message));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-group-by button").nth(1).click();
      await page.locator(".rt-session").nth(7).waitFor();
      const group = (id) => page.locator(`button.rt-session:has(.nm[title$="${id}"])`);
      const name = lang === "zh" ? "后台提示词建议" : "Background prompt suggestions";
      for (const id of ["suggestions-a", "safety-only", "generation-only"]) {
        assert.equal(await group(id).locator(".nm").textContent(), `Codex · ${name}`);
        const tooltip = await group(id).locator(".nm").getAttribute("title");
        assert.match(tooltip, lang === "zh" ? /后台.*提示词建议.*安全检查/ : /suggested prompts.*in the background.*checked them for safety/);
        assert(tooltip.endsWith((lang === "zh" ? "会话 ID" : "Session id") + ": " + id), "tooltip retains the group's own session ID");
      }
      assert.equal(await group("suggestions-a").locator(".cost").textContent(), "≈$0.030");
      assert.equal(await group("named-suggestions").locator(".nm").textContent(), "Codex · Named suggestions");
      assert.equal(await group("mixed").locator(".nm").textContent(), "Codex · mixed");
      assert.equal(await group("named-chat").locator(".nm").textContent(), "Codex · Chat title");
      assert.equal(await group("other-agent").locator(".nm").textContent(), "Claude Code · other-agent");
      assert.equal(await page.locator("div.rt-session .nm").textContent(), lang === "zh" ? "未提供会话标识" : "No session ID");
      assert.equal(await page.locator(".rt-session").count(), 8, "suggestion workers retain separate session groups");
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: title helpers belong to the named parent, including totals and title-first delivery`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const page = await browser.newPage();
    page.setDefaultTimeout(5000);
    const feed = {};
    const title = { ...req(1, "codex", "hidden-title", 0.013), kind: "thread_title", parentSession: "main-a" };
    const fixture = [title, req(2, "codex", "main-b", 0.2), req(3, "codex", "main-a", 0.029),
      { ...req(4, "codex", "unrelated-title", 0.1), kind: "thread_title" }];
    await page.route("**/*", serve("zh", feed, fixture));
    t.after(async () => { feed.next?.([]); await browser.close(); });
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-group-by button").nth(1).click();
    const group = page.locator("button.rt-session").filter({ hasText: "Codex · main-a" });
    await group.waitFor();
    assert.equal(await page.locator(".rt-session").count(), 3);
    assert.match(await group.locator(".summary").textContent(), /2 个请求/);
    assert.equal(await group.locator(".cost").textContent(), "≈$0.042");
    assert.equal(await page.locator(".rt-req .kind").filter({ hasText: "标题" }).count(), 2);
    await group.click();
    assert.equal(await page.locator(".rt-req").count(), 2, "only this parent and its title fold together");
    const handle = await group.elementHandle();
    for (let i = 0; i < 50 && !feed.next; i++) await page.waitForTimeout(20);
    assert(feed.next);
    feed.next(fixture.map((r) => r.session === "main-a" || r.parentSession === "main-a" ? { ...r, sessionTitle: "查询长沙天气" } : r));
    const named = page.locator("button.rt-session").filter({ hasText: "查询长沙天气" });
    await named.waitFor();
    assert.equal(await named.getAttribute("aria-expanded"), "false", "late title must not reset folding");
    assert(await named.evaluate((e, previous) => e === previous, handle));
    assert.match(await named.locator(".nm").getAttribute("title"), /main-a/);
    assert.equal(await named.locator(".cost").textContent(), "≈$0.042");
    assert.equal(await page.locator("button.rt-session").filter({ hasText: "main-b" }).count(), 1, "unknown names retain ID");
    await page.waitForTimeout(50);
    for (let i = 0; i < 50 && !feed.next; i++) await page.waitForTimeout(20);
    feed.next(fixture.map((r) => r.session === "main-a" || r.parentSession === "main-a" ? { ...r, sessionTitle: "长沙天气更新" } : r));
    await page.locator("button.rt-session").filter({ hasText: "长沙天气更新" }).waitFor();
    feed.names = { "main-a": "独立刷新名称" };
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    const refreshed = page.locator("button.rt-session").filter({ hasText: "独立刷新名称" });
    await refreshed.waitFor();
    assert.equal(await refreshed.getAttribute("aria-expanded"), "false", "names refresh without new routing requests");
    assert.equal(await page.locator("button.rt-session").filter({ hasText: "Claude" }).count(), 0);
    await page.locator(".rt-group-by button").first().click();
    assert.equal(await page.locator(".rt-req").count(), 4, "by-request view preserves all original requests");
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: request grouping is the default and either choice survives a reload`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage();
      page.setDefaultTimeout(5000);
      const feed = {};
      await page.route("**/*", serve(lang, feed));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      const buttons = page.locator(".rt-group-by button");
      await page.locator(".rt-req").nth(5).waitFor();
      assert.equal(await buttons.first().getAttribute("aria-pressed"), "true");
      assert.equal(await page.locator(".rt-session").count(), 0);
      await buttons.nth(1).click();
      await page.locator(".rt-session").nth(3).waitFor();
      // the live trace's held answer is left held: the reload cancels it. An
      // answer let go now has the page ask again while it is going away, and
      // WebKit turns that ask away "due to access control checks", an error
      // on the page (#1307)
      await page.reload();
      await page.locator(".rt-session").nth(3).waitFor();
      assert.equal(await buttons.nth(1).getAttribute("aria-pressed"), "true");
      await buttons.first().click();
      await page.reload();
      await page.locator(".rt-req").nth(5).waitFor();
      assert.equal(await buttons.first().getAttribute("aria-pressed"), "true");
      assert.equal(await page.locator(".rt-session").count(), 0);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: zero partial estimates keep the amount and one request uses the singular label`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage();
      page.setDefaultTimeout(5000);
      const feed = {};
      const fixture = [{ ...req(1, "codex", "partial", 0), unpriced: 1 },
        req(2, "codex", "mixed", 0), req(3, "codex", "mixed", 0, false),
        req(4, "claude", "unknown", 0, false)];
      await page.route("**/*", serve(lang, feed, fixture));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-req").nth(3).waitFor();
      assert.equal(await page.locator(".rt-req").filter({ hasText: "model-a" }).locator(".cost").textContent(), "≈$0.000+", "a zero-priced attempt plus an unknown attempt retains its amount");
      await page.locator(".rt-group-by button").nth(1).click();
      const group = (name) => page.locator("button.rt-session").filter({ hasText: name });
      assert.equal(await group("Codex · partial").locator(".cost").textContent(), "≈$0.000+");
      assert.match(await group("Codex · partial").locator(".summary").textContent(), lang === "zh" ? /^1 个请求/ : /^1 request ·/);
      assert.equal(await group("Codex · mixed").locator(".cost").textContent(), "≈$0.000+", "a free request plus an unknown request is a partial zero estimate");
      assert.match(await group("Codex · mixed").locator(".summary").textContent(), lang === "zh" ? /^2 个请求/ : /^2 requests ·/);
      assert.equal(await group("Claude Code · unknown").locator(".cost").isVisible(), false, "all unknown costs are omitted");
      await page.evaluate(() => { currency = "cny"; fx = { rate: 7, at: null, stale: false }; renderCosts(); });
      assert.equal(await group("Codex · mixed").locator(".cost").textContent(), "≈¥0.000+");
      assert.equal(await page.locator(".rt-req").filter({ hasText: "model-a" }).locator(".cost").textContent(), "≈¥0.000+");
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: applied titles regroup automatically and conflicts revoke the match without new traffic`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 1000 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const feed = {}, errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      let data = {
        before: [req(1, "codex", "main-test", 0.02), { ...req(2, "codex", "hidden-title-test", 0.01), kind: "thread_title" }, req(3, "codex", "unrelated-chat", 0.04)],
        afterRefresh: { names: { "main-test": "完成标题关联测试" }, parents: { 1: "", 2: "main-test" }, matched: { 1: false, 2: true } },
        conflictRefresh: { names: { "main-test": "完成标题关联测试" }, parents: { 1: "", 2: "" }, matched: { 1: false, 2: false } },
      };
      // Optional evidence comes from actual gateway HTTP requests and the Go
      // GUI APIs, rather than fixture-supplied parentSession guesses.
      if (process.env.TITLE_ASSOCIATION_FIXTURE) data = JSON.parse(await fs.readFile(process.env.TITLE_ASSOCIATION_FIXTURE, "utf8"));
      const fixture = data.before, [main, title] = fixture;
      await page.route("**/*", serve(lang, feed, fixture));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-req").nth(2).waitFor();
      await page.locator(".rt-group-by button").nth(1).click();
      const group = (id) => page.locator(`button.rt-session:has(.nm[title$="${id}"])`);
      await group(title.session).waitFor();
      assert.equal(await page.locator(".rt-session").count(), 3);
      assert.equal(title.parentSession, undefined, "fixture must start without an explicit parent");
      const shot = async (state) => {
        if (!process.env.ARTIFACT_DIR) return;
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.locator(".rt-reqs").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-title-${state}.png`) });
      };
      await shot("before");
      // No trace update is sent. Focus uses the same poll as the 15s refresh.
      feed.names = data.afterRefresh;
      await page.evaluate(() => window.dispatchEvent(new Event("focus")));
      await page.waitForFunction(() => document.querySelectorAll(".rt-session").length === 2);
      const parent = group(main.session);
      assert.equal(await group(title.session).count(), 0);
      assert.equal(await parent.locator(".nm").textContent(), "Codex · 完成标题关联测试");
      if (main.priced && title.priced) {
        const cost = await page.evaluate((amount) => "≈" + fmtCost({ cost: amount, unpriced: 0 }), main.cost + title.cost);
        assert.equal(await parent.locator(".cost").textContent(), cost, "helper cost joins the original chat total");
      }
      assert.match(await parent.locator(".summary").textContent(), lang === "zh" ? /2 个请求/ : /2 requests/);
      assert.equal(await page.locator(".rt-req .kind").filter({ hasText: lang === "zh" ? "标题" : "title" }).count(), 1);
      await shot("after");
      feed.routes = data.after || fixture.map((r) => ({ ...r,
        parentSession: data.afterRefresh.parents[r.id] || "", parentMatched: !!data.afterRefresh.matched[r.id],
        sessionTitle: data.afterRefresh.names[data.afterRefresh.parents[r.id] || r.session] || "" }));
      // Reload the day from history, then reload the page. Both fetch native
      // records again; the persisted evidence must restore the same grouping.
      await page.locator(".rt-day").filter({ hasText: lang === "zh" ? "今天" : "today" }).click();
      await page.waitForFunction(() => document.querySelectorAll(".rt-session").length === 2);
      assert.equal(await group(title.session).count(), 0, "history navigation lost title association");
      // the held live trace is left for the reload to cancel (see "either
      // choice survives a reload")
      await page.reload();
      await page.waitForFunction(() => document.querySelectorAll(".rt-session").length === 2);
      assert.equal(await group(title.session).count(), 0, "page reload lost title association");
      assert.match(await parent.locator(".summary").textContent(), lang === "zh" ? /2 个请求/ : /2 requests/);
      await shot("after-navigation");
      await parent.click();
      assert.equal(await page.locator(".rt-req").count(), 1, "original request and title helper fold together");
      const handle = await parent.elementHandle();
      await page.evaluate(() => window.dispatchEvent(new Event("focus")));
      await page.waitForTimeout(150);
      assert.equal(await parent.getAttribute("aria-expanded"), "false");
      assert(await parent.evaluate((e, previous) => e === previous, handle));
      feed.names = data.conflictRefresh;
      await page.evaluate(() => window.dispatchEvent(new Event("focus")));
      await group(title.session).waitFor();
      assert.equal(await page.locator(".rt-session").count(), 3, "ambiguous helper becomes independent automatically");
      assert.equal(await parent.getAttribute("aria-expanded"), "false", "unrelated heading state survives regrouping");
      await parent.click();
      assert.equal(await page.locator(".rt-req").count(), 3);
      await page.locator(".rt-group-by button").first().click();
      assert.equal(await page.locator(".rt-req").count(), 3, "by-request view keeps native request identities");
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const mixed of [true, false]) {
      test(`${engine} ${lang}: large ${mixed ? "mixed-agent" : "Codex"} history refresh includes an individually opened request`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(10000);
        const errors = [], titleRequests = [];
        const fixture = Array.from({ length: 2000 }, (_, i) => ({ ...req(i + 1, mixed && i < 800 ? "claude" : "codex", `chat-${i + 1}`, 0.01), time: stamp(now) }));
        const extra = { ...req(2001, "codex", "opened-chat", 0.01), time: stamp(now) };
        const feed = { titleRequests, route: extra, names: ({ ids }) => ({ names: Object.fromEntries(ids.map((id) => [id, `Applied ${id}`])) }) };
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, feed, fixture));
        t.after(async () => { feed.next?.([]); await browser.close(); });
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-req").nth(59).waitFor(); // live trace retains only 60 rows
        await page.locator(".rt-group-by button").nth(1).click();
        // openRoute loads the full day, then appends a row outside its 2,000-row page.
        await page.evaluate(({ id, time }) => window.openRoute(id, time), extra);
        await page.locator(".rt-req").nth(2000).waitFor();
        await page.evaluate(() => window.dispatchEvent(new Event("focus")));
        await page.waitForFunction(() => [...document.querySelectorAll(".rt-session .nm")].some((e) => e.textContent === "Codex · Applied opened-chat"));
        const group = (id) => page.locator(`button.rt-session:has(.nm[title$="${id}"])`);
        assert.equal(await group("chat-2000").locator(".nm").textContent(), "Codex · Applied chat-2000");
        assert.equal(await group(mixed ? "chat-801" : "chat-1").locator(".nm").textContent(), `Codex · Applied chat-${mixed ? 801 : 1}`);
        assert.equal(await group("opened-chat").locator(".cost").textContent(), "≈$0.010");
        const batches = titleRequests.filter((input) => input.day === day);
        assert(batches.length > 0, "history refresh never ran");
        assert(batches.every((input) => input.ids.length <= 2000 && input.routeIds.length <= 2000), "API batch exceeded the ID limit");
        assert(batches.some((input) => input.routeIds.includes(extra.id)), "individually opened row was omitted");
        if (mixed) assert(batches.every((input) => input.routeIds.every((id) => id > 800)), "other agents were sent to the Codex title API");
        else assert(batches.some((input) => input.routeIds.length === 2000) && batches.some((input) => input.routeIds.includes(2001)), "full Codex day was not split into batches");
        assert.deepEqual(errors, []);
      });
    }
  }
}

// A Claude Code session heads its group with the title in its own file, not
// its UUID (#1293): asked for as soon as it is listed, kept across the live
// updates that bring its rows anew, and the UUID stays in the tooltip.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: other agents' sessions show their own names`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const uuid = "578bd3d1-834b-43fb-b40c-ed9d17ad7731", errors = [], titleRequests = [];
      const feed = { titleRequests, names: (input) => input.sessions ? { titles: { ["claude:" + uuid]: "路由会话名" } } : {} };
      const fixture = [req(1, "claude", uuid, 0.01), { ...req(2, "claude", "magpie-set", 0.01), native_session: "unnamed-session" }, req(3, "codex", "chat", 0.01)];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, feed, fixture));
      t.after(async () => { feed.next?.([]); await browser.close(); });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-group-by button").nth(1).click();
      const named = page.locator("button.rt-session").filter({ hasText: "Claude Code · 路由会话名" });
      await named.waitFor();
      assert.match(await named.locator(".nm").getAttribute("title"), new RegExp(uuid));
      const asked = titleRequests.find((x) => x.sessions);
      assert.deepEqual([...asked.sessions].sort(), ["claude:" + uuid, "claude:unnamed-session"], "asks by the agent's own id, never Codex's");
      assert.equal(await page.locator("button.rt-session").filter({ hasText: "Claude Code · magpie-set" }).count(), 1, "an unknown name keeps the id");
      for (let i = 0; i < 50 && !feed.next; i++) await page.waitForTimeout(20);
      assert(feed.next, "long poll started");
      feed.next([...fixture, req(4, "claude", uuid, 0.02)]);
      await page.waitForFunction(() => [...document.querySelectorAll("button.rt-session")].some((e) => e.textContent.includes("≈$0.030")));
      assert.equal(await named.count(), 1, "a live update keeps the name");
      assert.deepEqual(errors, []);
    });
  }
}
