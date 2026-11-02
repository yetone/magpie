// Run with Node's test runner and Playwright on the module path; see README.md.
// wangye1644294 on X: magpie isn't friendly to a first-time user; a guide,
// routing set up in a click and the agents found here, please. A new user's
// Agents page (the window's first page, and the panel's first tab) has a Get
// started card: add a model source, connect an agent (those found here, each
// connected in a click), and, optionally, a routing group made of their own
// models in a click. Each step is ticked from the state the server gives,
// never from having been seen; the card goes once the first two are done,
// and put away it stays away, kept in the settings, not the page's storage.
// Renzexi01 on X (一进来就这么复杂): while the card shows, the agents'
// list is folded under it, behind "Show all {n} agents", since its second
// step names the ones found here; one connected, or the card put away,
// and the list is in view as before.
// Both engines, a wide and a narrow window and the panel, every language.
// No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const shots = process.env.ARTIFACT_DIR;

// world is what the fake server knows, changed by the page's writes
function newWorld() {
  return { served: 0, wired: false, hidden: false, groups: [] };
}

function agents(w) {
  // with no model served, the agents' pickers have none of magpie's
  const mine = w.served ? [{ value: "relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie" }] : [];
  const field = (value, own) => [{ key: "model", label: "model", value, options: [...own, ...mine] }];
  return [
    { id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", wired: w.wired, fields: field(w.wired ? "relay/m1" : "", [{ value: "gpt-5.4", label: "GPT-5.4" }]) },
    { id: "claude", name: "Claude Code", icon: "generic", path: "/fixture/claude", fields: field("", [{ value: "sonnet", label: "Sonnet" }]) },
    { id: "opencode", name: "OpenCode", icon: "generic", path: "/fixture/opencode", fields: field("", []) },
    // never connectable: not offered on the card
    { id: "cursor", name: "Cursor", icon: "generic", path: "/fixture/cursor", fields: [] },
    // its folder left behind with the CLI uninstalled (#2173): not offered
    { id: "hermes", name: "Hermes Agent", icon: "generic", path: "/fixture/hermes", cliMissing: true, fields: field("", [{ value: "hermes-own", label: "Own" }]) },
  ];
}

const templates = [
  { kind: "smart", group: { id: "smart-split", name: "Hard to strong, easy to cheap", members: ["relay/m1", "other/m2"] } },
  { kind: "thrifty", need: "cheap", of: "m1", group: { id: "cheap-first", name: "Cheap first", members: [] } },
  { kind: "steady", group: { id: "never-stuck", name: "Never stuck", members: ["relay/m1", "other/m2"] } },
];

function server(lang, w, posts) {
  const settings = () => ({ lang, theme: "light", guideHidden: w.hidden || undefined });
  const state = () => ({ agents: agents(w), profiles: [], served: w.served, settings: settings() });
  const groups = () => ({ groups: w.groups, models: [], pools: [], deciders: [], templates });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (req.method() === "POST") posts.push(url.pathname + url.search);
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/settings/guide") {
      w.hidden = !!req.postDataJSON().hidden;
      return json(settings());
    }
    if (url.pathname === "/api/agents/connect/codex") {
      w.wired = true;
      return json(state());
    }
    if (url.pathname === "/api/groups/template") {
      const { kind, name } = req.postDataJSON();
      const tp = templates.find((x) => x.kind === kind);
      w.groups.push({ ...tp.group, name });
      return json(groups());
    }
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const LAYOUTS = [
  { name: "window", url: "/?view=agents", viewport: { width: 1100, height: 760 } },
  { name: "narrow", url: "/?view=agents", viewport: { width: 420, height: 760 } },
  { name: "panel", url: "/?mode=panel", viewport: { width: 400, height: 640 } },
];
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const STRINGS = ["Get started", "Add a model source", "Connect an agent", "Set up routing (optional)", "Hide Get started",
  "A subscription you already pay for, a provider, or an API key", "magpie found these on this computer. Click one to connect it:",
  "A routing group sends each request to the right model, and to the next one when a model fails", "+{n} more below", "{n} of {m} done", "{n} models ready to use", "Add “{name}”"];

async function open(browser, layout, lang, w, posts) {
  const context = await browser.newContext({ viewport: layout.viewport, reducedMotion: "reduce" });
  await context.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "agents"); } catch {} });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  page.errors = [];
  page.on("pageerror", (e) => page.errors.push(e.message));
  await page.route("**/*", server(lang, w, posts));
  await page.goto("http://magpie.test" + layout.url);
  // folded under Get started, the rows are there but out of view
  await page.locator(".row.agent").first().waitFor({ state: "attached" });
  return { context, page };
}

// the agents' rows in view, not folded under the list's Show more
const inView = (page) => page.locator(".row.agent").evaluateAll((rs) => rs.filter((r) => !r.closest(".agent-fold")).map((r) => r.dataset.id || r.id));
const folded = (page) => page.locator(".agent-fold .row.agent").count();
const stepDone = (page, step) => page.locator(`.guide-step[data-step="${step}"]`).evaluate((e) => e.classList.contains("done"));
const tops = (page) => page.evaluate(() => [document.scrollingElement.scrollTop, document.querySelector("#view-agents")?.scrollTop]);
const sideways = (page) => page.evaluate(() => {
  const v = document.querySelector("#view-agents"), g = document.querySelector(".guide");
  return v.scrollWidth > v.clientWidth + 1 || document.scrollingElement.scrollWidth > innerWidth
    || (g && [...g.querySelectorAll("*")].some((e) => e.getBoundingClientRect().right > g.getBoundingClientRect().right + 1));
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const layout of LAYOUTS) {
      test(`${engine} ${lang} ${layout.name}: Get started ticks from state, connects in a click and goes once done`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const w = newWorld(), posts = [];
        const { page } = await open(browser, layout, lang, w, posts);
        const tr = (s, v) => page.evaluate(([s, v]) => t(s, v), [s, v]);
        if (lang !== "en") {
          for (const s of STRINGS) assert.notEqual(await tr(s), s, `${lang} has "${s}"`);
        }

        // a new user: nothing served, nothing wired
        const card = page.locator("#agentsLead .guide");
        await card.waitFor();
        assert.equal((await page.locator(".guide-h").textContent()), await tr("Get started"));
        assert.equal(await stepDone(page, "source"), false);
        assert.equal(await stepDone(page, "agent"), false);
        assert.equal(await page.locator(".guide-n").textContent(), await tr("{n} of {m} done", { n: 0, m: 2 }));
        // the agents found here are named, Cursor (never connectable) not;
        // with no models yet they can't be connected
        assert.deepEqual(await page.locator(".guide-agent").evaluateAll((bs) => bs.map((b) => b.dataset.id)), ["codex", "claude", "opencode"]);
        assert.ok(await page.locator(".guide-agent").evaluateAll((bs) => bs.every((b) => b.disabled)));
        // routing waits for a model source: no button yet
        assert.equal(await page.locator(".guide-tpl").count(), 0);
        // the card alone, the list folded under it
        assert.deepEqual(await inView(page), [], "no agent row in view under Get started");
        assert.equal(await folded(page), 5);
        if (layout.name !== "panel") {
          const more = page.locator(".agent-more");
          assert.equal((await more.textContent()).trim(), await tr("Show all {n} agents", { n: 5 }));
          if (lang !== "en") assert.notEqual(await tr("Show all {n} agents"), "Show all {n} agents", `${lang} has "Show all {n} agents"`);
          assert.equal(await more.getAttribute("aria-expanded"), "false");
        }
        assert.equal(await sideways(page), false, "nothing runs off sideways");
        if (shots && (lang === "en" || lang === "zh")) await page.screenshot({ path: path.join(shots, `getting-started-${engine}-${lang}-${layout.name}-new.png`) });

        // the first step's button: the window opens the add sheet, the
        // panel the window on Providers
        const top = await tops(page);
        await page.locator('.guide-step[data-step="source"] .guide-go').click();
        if (layout.name === "panel") {
          for (let i = 0; i < 50 && !posts.includes("/api/window/main?view=providers"); i++) await page.waitForTimeout(20);
          assert.ok(posts.includes("/api/window/main?view=providers"), posts.join(" "));
        } else {
          await page.locator("#view-providers").waitFor({ state: "visible" });
          await page.locator("#addSheet").waitFor({ state: "visible" });
        }

        // a provider added: the step is ticked from the state, the agents can
        // be connected, and a routing group is a click away
        w.served = 12;
        await page.goto("http://magpie.test" + layout.url);
        await card.waitFor();
        assert.equal(await stepDone(page, "source"), true);
        assert.match(await page.locator('.guide-step[data-step="source"] .guide-say').textContent(), /12/);
        assert.equal(await page.locator(".guide-n").textContent(), await tr("{n} of {m} done", { n: 1, m: 2 }));
        const codex = page.locator('.guide-agent[data-id="codex"]');
        assert.equal(await codex.isDisabled(), false);
        const tpl = page.locator(".guide-tpl");
        await tpl.waitFor();
        assert.equal(await tpl.getAttribute("data-kind"), "steady", "the harmless template first; the one that can't be made isn't offered");
        const name = await tr("Never stuck");
        assert.equal(await tpl.textContent(), await tr("Add “{name}”", { name }));
        if (shots && (lang === "en" || lang === "zh")) await page.screenshot({ path: path.join(shots, `getting-started-${engine}-${lang}-${layout.name}-source.png`) });

        const before = await tops(page);
        await tpl.click();
        await page.locator('.guide-step[data-step="routing"].done').waitFor();
        assert.ok(posts.includes("/api/groups/template"));
        assert.equal(w.groups[0].name, name, "saved under the name shown");
        assert.match(await page.locator('.guide-step[data-step="routing"] .guide-say').textContent(), new RegExp(name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));

        // one click connects Codex, and with both steps done the card goes
        await codex.click();
        await card.waitFor({ state: "detached" });
        assert.ok(posts.includes("/api/agents/connect/codex"));
        assert.deepEqual(await tops(page), before, "no click moved the page");
        assert.equal(top[0], before[0]);

        // done stays done: a reload brings no card back, and the agent
        // connected is in view, the rest folded as for anyone
        await page.goto("http://magpie.test" + layout.url);
        await page.locator(".row.agent").first().waitFor();
        assert.equal(await page.locator(".guide").count(), 0);
        assert.equal(await folded(page), 4);
        assert.deepEqual(page.errors, []);
      });

      test(`${engine} ${lang} ${layout.name}: Get started put away stays away, in the settings`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const w = newWorld(), posts = [];
        const first = await open(browser, layout, lang, w, posts);
        const page = first.page;
        await page.locator("#agentsLead .guide").waitFor();
        const top = await tops(page);
        await page.locator(".guide-x").click();
        await page.locator(".guide").waitFor({ state: "detached" });
        assert.ok(posts.includes("/api/settings/guide"));
        assert.equal(w.hidden, true);
        // put away, the list is in view as it was before there was a card
        assert.equal(await folded(page), 0);
        assert.deepEqual(await tops(page), top, "no click moved the page");
        assert.deepEqual(page.errors, []);
        await first.context.close();

        // a fresh browser storage (the other window, a reinstall's webview):
        // still away, though no step is done
        const again = await open(browser, layout, lang, w, posts);
        await again.page.waitForTimeout(150);
        assert.equal(await again.page.locator(".guide").count(), 0);
        assert.equal(await again.page.evaluate(() => { try { return Object.keys(localStorage).filter((k) => /guide/i.test(k)).length; } catch { return 0; } }), 0, "nothing kept in localStorage");
        assert.deepEqual(again.page.errors, []);
      });
    }
  }
}
