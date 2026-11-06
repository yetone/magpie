// Run with Node's test runner and Playwright on the module path; see README.md.
// Plugins › Built-in (Lxxyx on X: too much to look through): magpie's
// built-in plugins (internal/pluginrt, plugins/), each with a switch kept in
// plugins.yml. Off, a plugin's parts leave the window: its page leaves the
// nav — the sidebar's and the header's — and an address kept for it opens
// Agents, its Settings tab goes. One in use can't be switched off: its
// switch is on and disabled and its row says what uses it, in the page's
// language. Switched on, a page's tab is back at once. A plugins.yml that
// doesn't parse says so and no switch moves. The tab is first on the
// Plugins page, with no search field. Every language, both layouts and a
// narrow window; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

// as plugins/*'s manifests say them, in pluginrt.Registered's order (by id)
const PLUGINS = [
  { id: "budget", name: "Key limits", sub: "Hold each gateway key to the tokens or estimated cost you set for it", one: "1 key with a limit", many: "{n} keys with a limit" },
  { id: "gateway", name: "Gateway page", sub: "The gateway's state and keys, how to connect a client, its models and recent calls", one: "1 gateway key", many: "{n} gateway keys" },
  { id: "groups", name: "Routing groups", sub: "Several models agents pick as one: each request goes to the right one, and to the next when one fails", one: "1 group in use", many: "{n} groups in use" },
  { id: "library", name: "Library", sub: "Instructions, MCP servers, skills and hooks, kept once and given to every agent", one: "1 item given to agents", many: "{n} items given to agents" },
  { id: "middleware", name: "JS gateway plugins", sub: "Run the JavaScript gateway plugins: middleware on agents' requests and replies, routings and usage observers", one: "1 gateway plugin on", many: "{n} gateway plugins on" },
  { id: "otel", name: "Observability", sub: "Send each request's traces and metrics to an OpenTelemetry collector, such as Langfuse", one: "Sending to a collector", many: "Sending to a collector" },
  { id: "redact", name: "Privacy masking", sub: "Secrets, personal data and your own words go to vendors as placeholders, and come back as they were", one: "Masking is on", many: "Masking is on" },
  { id: "rotation", name: "Routing strategies", sub: "The routings besides Smart: In order, Until used up, In turn, Least used first, Use before expiry, By weight, Free first, Speed first and Cheapest first", one: "1 provider or group routed so", many: "{n} providers and groups routed so" },
  { id: "routing", name: "Routing", sub: "Each request as the gateway routes it, the accounts it weighed and what each answered, and your routing groups", one: "1 group in use", many: "{n} groups in use" },
  { id: "search", name: "Web search", sub: "Models that can't search the web search with a provider that can, or a search API", one: "1 search source set up", many: "{n} search sources set up" },
  { id: "sessions", name: "Sessions", sub: "Your agents' past conversations: read, search, resume and export them", one: "Recording gateway conversations", many: "Recording gateway conversations" },
  { id: "tools", name: "Plugin tools", sub: "Offer the tools your plugins give to every agent, as an MCP server", one: "1 plugin gives tools", many: "{n} plugins give tools" },
  { id: "usage", name: "Usage", sub: "Your allowances, what was spent and on what, each request, and what filled the context window" },
  { id: "warmups", name: "Warm-up and check-in", sub: "Start a subscription's usage window at a time you pick, and claim vendors' daily check-in credits", one: "1 warm-up or check-in on", many: "{n} warm-ups and check-ins on" },
];
// the Essentials preset, picked by the user: all off but the Gateway, Routing
// and Usage pages, the routing strategies, the gateway middleware, privacy
// masking, web search, key limits and plugin tools, which are essential,
// and two routing groups the user made
const START = { budget: [true, 0], gateway: [true, 0], groups: [false, 2], library: [false, 0], middleware: [true, 0], otel: [false, 0], redact: [true, 0], rotation: [true, 0], routing: [true, 0], search: [true, 0], sessions: [false, 0], tools: [true, 0], usage: [true, 0], warmups: [false, 0] };

// the Gateway page's settings, as pluginrt.SchemaOf makes them
const GATEWAY_SCHEMA = {
  type: "object",
  properties: { callsPage: { type: "integer", title: "Recent calls at a time", description: "How many of the gateway's recent calls the page shows, and how many more each Show more adds", enum: [10, 20, 50, 100], default: 20 } },
  "x-order": ["callsPage"],
};

function builtins(st, broken, config) {
  return PLUGINS.map((p) => {
    const [chosen, inUse] = st[p.id];
    const on = chosen || inUse > 0;
    const f = { id: p.id, name: p.name, sub: p.sub, on, chosen, source: inUse > 0 && !chosen ? "in use" : "preset", state: on ? "active" : "off", inUse, use: inUse === 1 ? p.one : inUse > 1 ? p.many : undefined, broken };
    if (p.id === "gateway") Object.assign(f, { schema: GATEWAY_SCHEMA, config });
    return f;
  });
}

function server(lang, posts, broken) {
  const st = structuredClone(START);
  const config = { callsPage: 20 };
  const list = () => builtins(st, broken, config);
  const cur = () => ({
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "",
    version: "0.1.400", dir: "/config/home/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    builtins: list(),
  });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") {
      const off = list().filter((f) => !f.on).map((f) => f.id);
      return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",featuresOff:${JSON.stringify(off)}};` });
    }
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: cur(), fx: cur().fx });
    if (url.pathname === "/api/plugins/builtin") {
      if (req.method() !== "POST") return json(list());
      const { id, on, config: change } = req.postDataJSON();
      if (change) {
        posts.push([id, change]);
        if (broken || ![10, 20, 50, 100].includes(change.callsPage)) return route.fulfill({ status: 400, json: { error: "callsPage: not one of [10 20 50 100]" } });
        Object.assign(config, change);
        return json(list());
      }
      posts.push([id, on]);
      if (broken || (!on && st[id][1] > 0)) return route.fulfill({ status: 400, json: { error: "in use" } });
      st[id][0] = on;
      return json(list());
    }
    if (url.pathname === "/api/settings") return json(cur());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: [], picker: false, movable: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/github") return json({ repos: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the pages' tabs shown, the page shown
const tabs = (page) => page.evaluate(() => [...document.querySelectorAll("#nav button[data-view]")].filter((b) => !b.hidden && b.offsetParent && getComputedStyle(b).display !== "none").map((b) => b.dataset.view));
const viewShown = (page) => page.evaluate(() => [...document.querySelectorAll("main.view")].filter((v) => !v.hidden).map((v) => v.id));
// the tabs with Sessions and Library off, the preset's masking on
// (Routing groups has a tab of its own in the sidebar only, from 1024
// points; under it Routing's page opens them)
const KEPT = ["agents", "providers", "routing", "gateway", "usage", "masking", "plugins"];
const keptAt = (width) => (width >= 1024 ? KEPT.flatMap((k) => (k === "routing" ? [k, "groups"] : [k])) : KEPT);
const rowsOf = (page) => page.evaluate(() => [...document.querySelectorAll("#builtinList .feat-row")].map((r) => {
  const b = r.querySelector('[role="switch"]');
  return { id: r.dataset.id, name: r.querySelector(".name").textContent, on: b.getAttribute("aria-checked"), disabled: b.disabled, why: r.querySelector(".feat-why")?.textContent || "" };
}));

// the Plugins page's tabs: the sidebar's, from 1024 points, or the page's
// own strip
const pluginTabs = (page, side) => page.locator(side ? ".nav-sub:not(.leaving) .nav-s" : "#view-plugins .lib-tabs .opt");
async function builtinTab(page, side) {
  await pluginTabs(page, side).first().click();
  await page.waitForSelector("#builtinList .feat-row");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Plugins › Built-in`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      // the sidebar from 1024 points, the header's tabs under it, and narrow
      for (const width of [1100, 800, 420]) {
        const where = `${engine} ${lang} ${width}`;
        const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/?view=sessions");
        await page.waitForSelector("#nav button.on");
        const side = await page.evaluate(() => document.body.classList.contains("side-nav"));
        assert.equal(side, width >= 1024, `${where}: the layout`);
        if (width >= 800) assert.deepEqual(await tabs(page), keptAt(width), `${where}: Sessions' and Library's tabs gone`);
        // an address kept for a page switched off opens Agents
        assert.deepEqual(await viewShown(page), ["view-agents"], `${where}: ?view=sessions`);
        // Settings has no Features tab of its own, and Observability's is off
        assert.equal(await page.locator("#setTab-features").count(), 0, `${where}: no Settings › Features`);
        assert.ok(await page.locator("#setTab-otel").isHidden(), `${where}: Observability's tab, off`);

        await page.goto("http://magpie.test/?view=plugins");
        await pluginTabs(page, side).first().waitFor();
        // Built-in first, in the page's language
        const first = (await pluginTabs(page, side).first().textContent()).trim();
        assert.equal(first, await page.evaluate(() => t("Built-in")), where);
        if (lang !== "en") assert.notEqual(first, "Built-in", `${where}: Built-in translated`);
        await builtinTab(page, side);
        assert.ok(await page.locator("#view-plugins .pm-find").isHidden(), `${where}: no search on Built-in`);
        const rows = await rowsOf(page);
        const want = await page.evaluate((ps) => ps.map((p) => ({ id: p.id, name: t(p.name) })), PLUGINS);
        assert.deepEqual(rows.map((r) => ({ id: r.id, name: r.name })), want, `${where}: a row each, by the table`);
        // the groups in use: on, can't be switched off, saying why
        const g = rows.find((r) => r.id === "groups");
        assert.deepEqual([g.on, g.disabled], ["true", true], `${where}: in use, locked`);
        assert.equal(g.why, await page.evaluate(() => t("{n} groups in use", { n: 2 })), where);
        if (lang !== "en") assert.notEqual(g.why, "2 groups in use", `${where}: why translated`);
        const gw = rows.find((r) => r.id === "gateway");
        assert.deepEqual([gw.on, gw.disabled], ["true", false], `${where}: the Gateway page, on`);
        const rt = rows.find((r) => r.id === "routing");
        assert.deepEqual([rt.on, rt.disabled], ["true", false], `${where}: the Routing page, on`);
        const ro = rows.find((r) => r.id === "rotation");
        assert.deepEqual([ro.on, ro.disabled], ["true", false], `${where}: the routing strategies, on`);
        for (const id of ["budget", "middleware", "redact", "search", "tools"]) {
          const r = rows.find((r) => r.id === id);
          assert.deepEqual([r.on, r.disabled], ["true", false], `${where}: ${id}, on`);
        }
        const us = rows.find((r) => r.id === "usage");
        assert.deepEqual([us.on, us.disabled], ["true", false], `${where}: the Usage page, on`);
        for (const r of rows.filter((r) => !["budget", "groups", "gateway", "middleware", "redact", "rotation", "routing", "search", "tools", "usage"].includes(r.id))) assert.deepEqual([r.on, r.disabled, r.why], ["false", false, ""], `${where}: ${r.id}`);
        await page.locator('#builtinList [data-id="groups"] [role="switch"]').click({ force: true });
        await page.waitForTimeout(100);
        assert.deepEqual(posts, [], `${where}: a locked switch posts nothing`);
        // nothing wider than the window
        assert.ok(await page.evaluate(() => { const v = document.querySelector("#view-plugins"); return v.scrollWidth <= v.clientWidth + 1; }), `${where}: fits`);

        // the Gateway page's setting, under its row, from its schema: in
        // the page's language, its value in the app's menu, saved as picked
        const set = page.locator('#builtinList .feat-set[data-of="gateway"][data-setting="callsPage"]');
        assert.equal(await set.locator(".name").textContent(), await page.evaluate(() => t("Recent calls at a time")), where);
        if (lang !== "en") assert.notEqual(await set.locator(".name").textContent(), "Recent calls at a time", `${where}: translated`);
        assert.equal(await page.evaluate(() => document.querySelector('#builtinList .feat-set').previousElementSibling.dataset.id), "gateway", `${where}: under its row`);
        assert.equal((await set.locator(".proto.pick").textContent()).trim(), "20", where);
        await set.locator(".proto.pick").click();
        await page.locator(".proto-menu .pm-item", { hasText: "50" }).click();
        await page.waitForFunction(() => document.querySelector('#builtinList .feat-set .proto.pick')?.textContent.trim() === "50");
        assert.deepEqual(posts, [["gateway", { callsPage: 50 }]], `${where}: posted`);
        assert.equal(await page.evaluate(() => document.querySelectorAll(".proto-menu").length), 0, `${where}: the menu closed`);
        posts.length = 0;
        assert.ok(await page.evaluate(() => { const v = document.querySelector("#view-plugins"); return v.scrollWidth <= v.clientWidth + 1; }), `${where}: the setting fits`);

        // Library switched on: its tab is back at once, the page stays
        await page.locator('#builtinList [data-id="library"] [role="switch"]').click();
        await page.waitForSelector('#builtinList [data-id="library"] [aria-checked="true"]');
        assert.deepEqual(posts, [["library", true]], where);
        if (width >= 800) assert.deepEqual(await tabs(page), ["agents", "library", ...keptAt(width).slice(1)], `${where}: Library back`);
        assert.deepEqual(await viewShown(page), ["view-plugins"], `${where}: Plugins stays`);
        // Observability's, Routing's and Usage's rows can be below the fold:
        // the reader scrolls to them with the wheel, since the page puts back
        // a scroll that isn't the reader's (scrollOnPurpose), a click's own
        // too. The wheel turns over what shows of the list (its top may have
        // scrolled away, and a wheel there scrolls nothing), as often as the
        // row needs: at 420 the rows wrap and Usage's is more than one turn
        // down
        const below = (id) => page.evaluate((id) => { const r = document.querySelector(`#builtinList [data-id="${id}"] [role="switch"]`).getBoundingClientRect(), foot = document.querySelector("footer.foot"); return r.bottom > Math.min(document.getElementById("view-plugins").getBoundingClientRect().bottom, foot && foot.offsetParent ? foot.getBoundingClientRect().top : Infinity); }, id);
        const wheelTo = async (id) => {
          for (let i = 0; i < 5 && await below(id); i++) {
            const at = await page.evaluate(() => { const l = document.getElementById("builtinList").getBoundingClientRect(), v = document.getElementById("view-plugins").getBoundingClientRect(); return { x: l.x + 20, y: (Math.max(l.top, v.top) + Math.min(l.bottom, v.bottom)) / 2 }; });
            await page.mouse.move(at.x, at.y);
            await page.mouse.wheel(0, 400);
            await page.waitForFunction(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(true)))));
          }
          assert.equal(await below(id), false, `${where}: ${id}'s row reached by the wheel`);
        };
        // the wheel's own check only sees the list's foot: a last wheel that
        // clamps at the list's end can carry a row's switch under the sticky
        // Plugins head, or past the view's top, where the head takes the
        // click and it waits out its timeout (#3498, red 1 of 3 in WebKit).
        // Wheel back over what shows of the list, as the wheel above does,
        // till the switch's centre is on the switch, and say so
        const clearOfHead = (id) => page.evaluate((id) => { const s = document.querySelector(`#builtinList [data-id="${id}"] [role="switch"]`), b = s.getBoundingClientRect(), hit = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2); return !!hit && (hit === s || s.contains(hit)); }, id);
        const outFromUnder = async (id) => {
          for (let i = 0; i < 20 && !(await clearOfHead(id)); i++) {
            const at = await page.evaluate(() => { const l = document.getElementById("builtinList").getBoundingClientRect(), v = document.getElementById("view-plugins").getBoundingClientRect(); return { x: l.x + 20, y: (Math.max(l.top, v.top) + Math.min(l.bottom, v.bottom)) / 2 }; });
            await page.mouse.move(at.x, at.y);
            await page.mouse.wheel(0, -50);
            await page.waitForFunction(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(true)))));
          }
          assert.equal(await clearOfHead(id), true, `${where}: ${id}'s switch clear of the sticky head`);
        };
        // and Observability: its Settings tab is back
        await wheelTo("otel");
        await outFromUnder("otel");
        await page.locator('#builtinList [data-id="otel"] [role="switch"]').click();
        await page.waitForSelector('#builtinList [data-id="otel"] [aria-checked="true"]');
        assert.ok(await page.locator("#setTab-otel").evaluate((b) => getComputedStyle(b).display !== "none"), `${where}: Observability back`);
        // Routing switched off: its tab and the panel's go, the page stays.
        await wheelTo("routing");
        await outFromUnder("routing");
        await page.locator('#builtinList [data-id="routing"] [role="switch"]').click();
        await page.waitForSelector('#builtinList [data-id="routing"] [aria-checked="false"]');
        assert.deepEqual(posts.at(-1), ["routing", false], where);
        if (width >= 800) assert.deepEqual(await tabs(page), ["agents", "library", ...keptAt(width).slice(1).filter((k) => k !== "routing" && k !== "groups")], `${where}: Routing gone`);
        assert.deepEqual(await viewShown(page), ["view-plugins"], `${where}: Plugins stays`);
        // and Usage: its tab goes
        const usageSwitch = page.locator('#builtinList [data-id="usage"] [role="switch"]');
        await wheelTo("usage");
        await outFromUnder("usage");
        await usageSwitch.click();
        await page.waitForSelector('#builtinList [data-id="usage"] [aria-checked="false"]');
        assert.deepEqual(posts.at(-1), ["usage", false], where);
        if (width >= 800) assert.deepEqual(await tabs(page), ["agents", "library", ...keptAt(width).slice(1).filter((k) => k !== "routing" && k !== "groups" && k !== "usage")], `${where}: Usage gone`);
        assert.deepEqual(await viewShown(page), ["view-plugins"], `${where}: Plugins stays`);
        // Discover brings the search back
        await pluginTabs(page, side).nth(1).click();
        assert.ok(await page.locator("#view-plugins .pm-find").isVisible(), `${where}: search on Discover`);
        // a way to Usage left somewhere (the tray's Open Usage) gives way to Agents
        await page.evaluate(() => show("usage"));
        assert.deepEqual(await viewShown(page), ["view-agents"], `${where}: no Usage page with it off`);
        assert.deepEqual(errors, [], where);
        await context.close();
      }
    }
  });

  test(`${engine}: Plugins › Built-in, plugins.yml broken`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const where = `${engine} ${lang}`;
      const context = await browser.newContext({ viewport: { width: 420, height: 700 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts, "/config/plugins.yml doesn't parse: yaml: line 2"));
      await page.goto("http://magpie.test/?view=plugins");
      await builtinTab(page, false);
      const say = await page.locator("#builtinList .feat-say.warn").textContent();
      assert.ok(say.includes("yaml: line 2"), `${where}: says the error`);
      if (lang !== "en") assert.ok(!say.startsWith("plugins.yml can't be read"), `${where}: translated`);
      for (const r of await rowsOf(page)) assert.equal(r.disabled, true, `${where}: ${r.id} can't be switched`);
      await page.locator('#builtinList [data-id="library"] [role="switch"]').click({ force: true });
      await page.waitForTimeout(100);
      assert.deepEqual(posts, [], `${where}: no switch posts`);
      assert.deepEqual(errors, [], where);
      await context.close();
    }
  });
}
