// Run with Node's test runner and Playwright on the module path; see README.md.
// #726 (EZN7L2C3): with Claude Code connected, picking Opus 5.5 in its
// model list moved it under Not set up and turned 「接入」 off, picking
// Sonnet 5.5 then turned it back on, and neither row said why: the Opus
// was Claude Code's own model, asked of Anthropic directly (picking it
// unroutes Claude Code), the Sonnet magpie's, its "· via magpie" cut off
// the end of a long note. Now each model says which way it goes, and a
// pick that takes a connected agent off magpie (one of its own models, or
// Default) is asked first, as the switch's off is; Default's ask offers
// Disconnect and restore beside it, the two not being the same. Switched
// on, the switch says what model it started the agent on and why. No click
// moves the page; in English and Chinese, the API faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [
  { value: "claude-opus-5-5", note: "Claude Opus 5.5", group: "Claude Code", direct: "Anthropic", icon: "claude-color" },
  { value: "claude-sonnet-5-5", note: "Claude Sonnet 5.5", group: "Claude Code", direct: "Anthropic", icon: "claude-color" },
  { value: "claude/claude-sonnet-5-5[1m]", label: "Claude Sonnet 5.5", ref: "claude/claude-sonnet-5-5", group: "Claude", icon: "claude-color",
    note: "someone.with.a.long.address@example.com · via magpie" },
  { value: "claude/claude-opus-5-5[1m]", label: "Claude Opus 5.5", ref: "claude/claude-opus-5-5", group: "Claude", icon: "claude-color",
    note: "someone.with.a.long.address@example.com · via magpie" },
];
const fresh = () => ({
  agents: [
    { id: "claude", name: "Claude Code", icon: "generic", path: "/fixture/claude", wired: true, fields: [{ key: "model", label: "model", value: "claude/claude-sonnet-5-5[1m]", options }] },
    { id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", wired: true, fields: [{ key: "model", label: "model", value: "", options: [{ value: "relay/m1", label: "m1", ref: "relay/m1", note: "Relay · via magpie" }] }] },
  ],
  profiles: [],
});

function server(lang, posts) {
  let cur = fresh();
  const reply = (route, extra = {}) => route.fulfill({ json: { ...cur, ...extra, settings: { lang, theme: "light" } } });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return reply(route);
    if (url.pathname === "/api/set") {
      const body = JSON.parse(req.postData());
      posts.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      const a = cur.agents.find((x) => x.id === body.agent);
      a.fields[0].value = body.value;
      a.wired = body.value.includes("/");
      return reply(route);
    }
    if (url.pathname === "/api/agents/connect/claude") {
      posts.push({ connect: "claude" });
      cur = JSON.parse(JSON.stringify(cur));
      const a = cur.agents.find((x) => x.id === "claude");
      a.wired = true;
      a.fields[0].value = "claude/claude-sonnet-5-5[1m]";
      return reply(route, { connected: { how: "default", field: "model", value: "claude/claude-sonnet-5-5[1m]" } });
    }
    if (url.pathname.startsWith("/api/agents/preview/")) return json({ changes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { via: "via magpie", direct: "direct, not via magpie", off: "off magpie", ask: "Take Claude Code off magpie?", use: "Use claude-opus-5-5", restore: "Disconnect and restore", cancel: "Cancel", connected: "it had no model set, so the first of its own" },
  zh: { via: "经 magpie", direct: "直连，不经 magpie", off: "不经 magpie", ask: "让 Claude Code 不再经过 magpie？", use: "使用 claude-opus-5-5", restore: "断开并还原", cancel: "取消", connected: "它原来没有设模型" },
};
const row = '.row.agent[data-id="claude"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a pick that takes Claude Code off magpie says so and is asked first`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 980, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [], dialogs = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { dialogs.push(d.message()); d.dismiss(); });
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/?view=agents");
      await page.locator(`${row} .ag-conn`).waitFor();
      const missing = await page.evaluate(() => [
        "via magpie", "{agent} asks magpie for it", "direct, not via magpie", "off magpie",
        "{agent} as installed: magpie's endpoint and models come out", "Take {agent} off magpie?",
        "Default is {agent} as installed: magpie's endpoint and models come out, and {agent} starts on its own default model. What it had before magpie isn't put back; Disconnect and restore does that.",
        "{model} is {agent}'s own model: {agent} asks {vendor} for it itself, with its own sign-in, not through magpie. Picking it takes {agent} off magpie, and it starts on {model}.",
        "「接入」 goes off, and {agent} is listed under Not set up with the agents not connected. Switch it on again to go back through magpie.",
        "「接入」 goes off. Switch it on again to go back through magpie.",
        "Use default", "Use {model}", "{agent} no longer goes through magpie · on its own default",
        "{agent} is connected to magpie · back on {model}, what it was on when it was switched off",
        "{agent} is connected to magpie · on {model} through magpie, the model it was on",
        "{agent} is connected to magpie · on {model} through magpie: it had no model set, so the first of its own",
        "{agent} is connected to magpie · on {model} through magpie: magpie doesn't serve the model it was on",
        "{agent} is connected to magpie · it stays on its own last pick; magpie's models join its {cmd}",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      const tops = () => page.evaluate(() => [document.scrollingElement.scrollTop, document.querySelector("#view-agents")?.scrollTop]);
      const top = await tops();
      const start = page.locator(`${row} > .field.ag-start`);
      const item = (text) => page.locator("#pop #list li", { hasText: text }).first();
      const open = async () => { await start.click(); await page.locator("#pop:not([hidden]) #list li").first().waitFor(); };

      // each model says which way it goes, the tag whole however long the note
      await open();
      const tag = (li) => li.locator(".badge.path").textContent();
      assert.equal(await tag(item("claude-opus-5-5")), w.direct);
      const via = item("someone.with");
      assert.equal(await tag(via), w.via);
      assert.ok(await via.locator(".badge.path").evaluate((b) => b.scrollWidth <= b.clientWidth + 1 && b.getBoundingClientRect().right <= b.closest("li").getBoundingClientRect().right), "the tag isn't cut off");
      assert.ok(!(await via.locator(".n").textContent()).includes("via magpie"), "said once, as the tag");
      assert.equal(await tag(page.locator("#pop #list li.reset", { hasText: lang === "en" ? "Default" : "默认" }).first()), w.off);

      // Claude Code's own Opus is asked first; Cancel leaves it connected
      await item("claude-opus-5-5").click();
      const ask = page.locator("#modal .leave-ask");
      await ask.waitFor();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.ask);
      assert.deepEqual(posts, [], "nothing written before it is confirmed");
      await ask.locator("button", { hasText: w.cancel }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(posts, []);
      assert.equal(await page.locator(`${row} .ag-conn`).getAttribute("aria-checked"), "true");

      // Default is asked too, with Disconnect and restore beside it
      await open();
      await page.locator("#pop #list li.reset", { hasText: lang === "en" ? "Default" : "默认" }).first().click();
      await ask.waitFor();
      assert.equal(await ask.locator("button", { hasText: w.restore }).count(), 1);
      await ask.locator("button", { hasText: w.cancel }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(posts, []);

      // confirmed, it is written: off magpie, 「接入」 off
      await open();
      await item("claude-opus-5-5").click();
      await ask.waitFor();
      await ask.locator("button", { hasText: w.use }).click();
      await page.waitForFunction(() => document.querySelector("#modal").hidden);
      await page.waitForFunction((r) => document.querySelector(r + " .ag-conn")?.getAttribute("aria-checked") === "false", row);
      assert.deepEqual(posts, [{ agent: "claude", field: "model", value: "claude-opus-5-5" }]);

      // a model of magpie's picked while not connected connects it, no ask
      // (the owner's one click), and is said to go through magpie; off
      // magpie, the row is folded under Not set up with Codex still on it
      assert.equal(await page.locator(".agent-more").getAttribute("aria-expanded"), "false");
      await page.locator(".agent-more").click();
      await open();
      assert.equal(await tag(item("someone.with")), w.via);
      await item("someone.with").click();
      await page.waitForFunction((r) => document.querySelector(r + " .ag-conn")?.getAttribute("aria-checked") === "true", row);
      assert.equal(await page.locator("#modal:not([hidden]) .leave-ask").count(), 0);
      assert.equal(posts.length, 2);

      // a pick within magpie's models isn't asked
      await open();
      await page.locator("#pop #list li", { hasText: "someone.with" }).filter({ hasText: "Opus" }).click();
      await page.waitForFunction(() => document.querySelector('.row.agent[data-id="claude"] > .field.ag-start')?.textContent.includes("Opus"));
      assert.equal(await page.locator("#modal:not([hidden]) .leave-ask").count(), 0);
      assert.deepEqual(posts.at(-1), { agent: "claude", field: "model", value: "claude/claude-opus-5-5[1m]" });

      // the switch says what it started the agent on, and why
      const s = page.locator(`${row} .ag-conn`);
      await page.evaluate(() => { const a = state.agents.find((x) => x.id === "claude"); a.wired = false; renderAgents(); });
      await s.click();
      await page.waitForFunction((r) => document.querySelector(r + " .ag-conn")?.getAttribute("aria-checked") === "true", row);
      assert.ok((await page.locator("#status").textContent()).includes(w.connected), await page.locator("#status").textContent());
      assert.ok((await page.locator("#status").textContent()).includes("Claude Sonnet 5.5"));

      assert.deepEqual(await tops(), top, "no click moved the page");
      assert.deepEqual(dialogs, [], "no native dialog");
      assert.deepEqual(errors, []);
    });
  }
}
