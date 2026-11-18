// Run with Node's test runner and Playwright on the module path; see README.md.
// The provider editor's Advanced holds some thirty rows. They sit under
// four small headings, Routing, Models, Limits and cost, and Connection
// (advgroups.js; the 11-17 scan, item 19), each row in its own order
// inside its group. For an account and a key, in every language, both
// engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = { icon: "generic", catalog: "", models: [{ id: "glm-5", name: "GLM-5", on: true }], agents: [], fallback: [], headers: {}, routing: "", affinity: "", ready: true };
const sub = {
  ...base, id: "workbuddy-ai", name: "WorkBuddy AI", chat: "", responses: "", anthropic: "", keyList: [],
  account: {
    agent: "workbuddy-ai", agentName: "WorkBuddy AI", user: "one@example.test", plan: "Pro",
    logins: ["one", "two"].map((n, i) => ({ user: `${n}@example.test`, plan: "Pro", active: i === 0, on: true })),
  },
};
// Codex's own Codex client row (07d19510f) goes under Connection, beside User-Agent
const codex = {
  ...base, id: "codex", name: "ChatGPT", chat: "", responses: "", anthropic: "", keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: "one@example.test", plan: "Plus",
    logins: ["one", "two"].map((n, i) => ({ user: `${n}@example.test`, plan: "Plus", active: i === 0, on: true })),
  },
};
const relay = {
  ...base, id: "relay", name: "Relay", chat: "https://relay.example.invalid/v1", responses: "", anthropic: "", key: { set: true, masked: "sk-…1234" },
  keyList: [1, 2].map((i) => ({ id: `key-${i}`, name: `Key ${i}`, masked: `sk-…000${i}`, active: i === 1, on: true })),
};

function serve(lang, p) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const providers = { providers: [p], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}


// the heading each label sits under, in order, as the reader sees it
const layout = () => {
  const inner = document.querySelector(".editor details.advanced > .inner");
  const out = [];
  let head = "";
  for (const n of inner.children) {
    if (n.classList.contains("adv-head")) { head = n.textContent; out.push({ head }); }
    else if (n.nodeName === "LABEL" && !n.hidden && n.textContent) out.push({ label: n.textContent, under: head });
  }
  return out;
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the provider editor's Advanced rows sit under Routing, Models, Limits and cost, Connection`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const p of [sub, codex, relay]) {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, p));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: p.name }).first().click();
        await page.locator(".editor .accts").first().waitFor();
        await page.locator(".editor details.advanced > summary").click();
        await page.locator(".editor details.advanced .adv-head").first().waitFor();
        const said = await page.evaluate(() => Object.fromEntries(["Routing", "Models", "Limits and cost", "Connection", "Stays", "Context window", "Requests at once", "Headers", "Proxy", "Codex client"].map((k) => [k, t(k)])));
        const rows = await page.evaluate(layout);
        const at = `${p.name}: ${JSON.stringify(rows)}`;
        const heads = rows.filter((r) => r.head).map((r) => r.head);
        assert.deepEqual(heads, [said.Routing, said.Models, said["Limits and cost"], said.Connection].filter((h) => heads.includes(h)), `${at}: headings in order`);
        assert.ok(heads.length >= 3, `${at}: three headings or more`);
        assert.ok(rows[0].head, `${at}: a heading comes first`);
        const under = (label) => rows.find((r) => r.label === label)?.under;
        assert.equal(under(said.Routing), said.Routing, at);
        assert.equal(under(said.Stays), said.Routing, at);
        assert.equal(under(said["Context window"]), said.Models, at);
        assert.equal(under(said["Requests at once"]), said["Limits and cost"], at);
        assert.equal(under(said.Proxy), said.Connection, at);
        if (p === codex) assert.equal(under(said["Codex client"]), said.Connection, at);
        // the heading takes the whole row: the labels keep their column
        const box = await page.evaluate(() => {
          const h = document.querySelector(".editor .adv-head"), inner = h.parentElement;
          return { h: h.getBoundingClientRect().width, inner: inner.getBoundingClientRect().width - parseFloat(getComputedStyle(inner).paddingLeft) - parseFloat(getComputedStyle(inner).paddingRight) };
        });
        assert.ok(Math.abs(box.h - box.inner) < 2, `${at}: heading spans the row ${JSON.stringify(box)}`);
        assert.deepEqual(errors, [], at);
        await page.context().close();
      }
    });
  }
}
