// Run with Node's test runner and Playwright on the module path; see README.md.
// #844 (evilgaoshu): the header's refresh reads again what the page shown
// draws. On Agents it looks for the agents on this computer again (one just
// installed shows, and leaves Install another agent); on Usage it reads the
// usage and the allowances now; on Providers it refreshes the model lists.
// It never looks for a newer magpie (that is Settings › About's Check, and
// magpie's own timer), its tooltip says what it reads on the page shown, and
// a click doesn't move the page. The panel's does the same for its tabs.
// English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: {
    agents: "Look for agents on this computer again: one just installed or removed, or its settings changed",
    usage: "Read the usage again now",
    allowances: "Read the allowances again now",
    models: "Refresh model lists (models.dev and every vendor)",
    nav: { usage: "Usage", providers: "Providers" },
  },
  zh: {
    agents: "重新检测本机的 Agent：刚安装或卸载的，或在别处改过配置的",
    usage: "立即重新读取用量",
    allowances: "立即重新读取额度",
    models: "刷新模型列表（models.dev 与各供应商）",
    nav: { usage: "用量", providers: "供应商" },
  },
};

const models = [{ value: "model-a", label: "model-a" }];
const agent = (id, name) => ({ id, name, path: "/test/" + id, fields: [{ key: "model", label: "model", value: "model-a", options: models }] });
const INSTALLS = [
  { id: "claude", name: "Claude Code", icon: "claudecode-color", commands: [{ via: "npm", command: "npm install -g @anthropic-ai/claude-code" }] },
  { id: "opencode", name: "OpenCode", icon: "opencode", commands: [{ via: "npm", command: "npm install -g opencode-ai" }] },
];

function server(lang, ctl) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    const state = () => ({ agents: ctl.agents, profiles: [], settings: { lang, theme: "light" } });
    ctl.calls.push(req.method() + " " + url.pathname + url.search);
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state" || url.pathname === "/api/sync") return json(state());
    if (url.pathname === "/api/agents/rescan") {
      // Claude Code was installed meanwhile
      ctl.agents = [...ctl.agents, agent("claude", "Claude Code")];
      ctl.installs = ctl.installs.filter((x) => x.id !== "claude");
      return json(state());
    }
    if (url.pathname === "/api/agents/install") return json(ctl.installs);
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/update") return json({ state: "latest", current: "0.1.400" });
    if (url.pathname === "/api/usage/quotas") return json(ctl.quotas || []);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/usage") return json({ totals: {}, days: [], providers: [], models: [], agents: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const count = (ctl, re) => ctl.calls.filter((c) => re.test(c)).length;
const where = (page) => page.evaluate(() => [scrollX, scrollY, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)].join(","));

async function click(page, sync) {
  const before = await where(page);
  await sync.click();
  await page.waitForFunction(() => !document.querySelector("#sync").classList.contains("spin"));
  assert.equal(await where(page), before, "the click moved the page");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the refresh reads again what the page shows", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": the window", async () => {
        const ctl = { calls: [], agents: [agent("codex", "Codex")], installs: structuredClone(INSTALLS) };
        const page = await (await browser.newContext({ viewport: { width: 1000, height: 600 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, ctl));
        await page.goto("http://magpie.test/");
        const sync = page.locator("#sync");
        await page.locator('.row.agent[data-id="codex"]').waitFor();
        await page.waitForTimeout(300);

        // Agents: looks for the agents again
        assert.equal(await sync.getAttribute("title"), words[lang].agents);
        assert.equal(await page.locator('.row.agent[data-id="claude"]').count(), 0);
        await click(page, sync);
        await page.locator('.row.agent[data-id="claude"]').waitFor();
        assert.equal(count(ctl, /^POST \/api\/agents\/rescan$/), 1);
        assert.equal(count(ctl, /\/api\/sync$/), 0, "the model lists aren't refreshed on Agents");
        assert.equal(count(ctl, /\/api\/update\/check/), 0, "no update check");
        // the one just installed has left the install list
        assert.equal(await page.locator("#agentsInstall").evaluate((b) => /Claude Code/.test(b.textContent)), false);

        // Usage: reads the allowances afresh
        await page.locator(`#nav [data-view="usage"]`).click();
        assert.equal(await sync.getAttribute("title"), words[lang].usage);
        await page.waitForTimeout(300);
        const asked = count(ctl, /\/api\/usage\/quotas\?asked=1&all=1$/);
        await click(page, sync);
        // every account's, a saved Claude account's too (#5380)
        assert.equal(count(ctl, /\/api\/usage\/quotas\?asked=1&all=1$/), asked + 1, "the allowances read afresh, every account");
        assert.equal(count(ctl, /\/api\/sync$/), 0);

        // Providers: refreshes the model lists
        await page.locator(`#nav [data-view="providers"]`).click();
        assert.equal(await sync.getAttribute("title"), words[lang].models);
        await click(page, sync);
        assert.equal(count(ctl, /^POST \/api\/sync$/), 1);
        assert.equal(count(ctl, /\/api\/agents\/rescan/), 1);
        assert.equal(count(ctl, /\/api\/update\/check/), 0, "no update check");
        assert.deepEqual(errors, []);
      });

      await t.test(lang + ": the panel", async () => {
        const ctl = { calls: [], agents: [agent("codex", "Codex")], installs: structuredClone(INSTALLS) };
        const page = await (await browser.newContext({ viewport: { width: 380, height: 600 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "agents"); } catch {} });
        await page.route("**/*", server(lang, ctl));
        await page.goto("http://magpie.test/?mode=panel");
        const sync = page.locator("#sync");
        await page.locator('.row.agent[data-id="codex"]').waitFor();
        assert.equal(await sync.getAttribute("title"), words[lang].agents);
        await click(page, sync);
        await page.locator('.row.agent[data-id="claude"]').waitFor();
        assert.equal(count(ctl, /^POST \/api\/agents\/rescan$/), 1);
        assert.equal(count(ctl, /\/api\/sync$|\/api\/update\/check/), 0);
        // Allowances: every account read again, a saved Claude account's
        // too, as its card's refresh reads it (#5380); opening the tab
        // asks without
        await page.locator("#sync:not(.spin)").waitFor();
        ctl.quotas = [{ provider: "claude", name: "Claude", user: "a@x.com", windows: [{ label: "5h", used: 9, limit: 100 }] }];
        await page.evaluate(() => loadQuotas());
        await page.locator('#ptabs [data-ptab="usage"]').click();
        await page.waitForFunction(() => document.body.dataset.ptab === "usage");
        await page.waitForTimeout(300);
        assert.equal(count(ctl, /\/api\/usage\/quotas\?.*all=1/), 0, "opening the tab reads every account: " + ctl.calls.join(" "));
        assert.equal(await sync.getAttribute("title"), words[lang].allowances);
        await click(page, sync);
        await page.locator("#sync:not(.spin)").waitFor();
        assert.equal(count(ctl, /\/api\/usage\/quotas\?asked=1&all=1$/), 1, "the top refresh reads every account");
        assert.deepEqual(errors, []);
      });
    }
  });
}
