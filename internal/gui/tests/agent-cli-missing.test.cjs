// Run with Node's test runner and Playwright on the module path; see README.md.
// #843 (evilgaoshu): DeepSeek Harness uninstalled, its ~/.dsh left behind,
// was listed on the Agents page as if it were installed, and Install
// another agent didn't offer it. Its row says its CLI isn't found, and
// Install another agent has it with its install command, marked the same.
// #2177 (mengyianx), #2173 (SkyTodInfi): such a row still offered every
// setting, and Hermes, left as its config.yaml alone, looked installed. A
// row whose CLI is gone offers none of its settings now, nor Apply again;
// one still connected keeps its switch and Disconnect, and one that isn't
// folds under Not set up. In the window, wide and narrow, and the tray
// panel, in every language. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["model-a", "model-b"].map((m) => ({ value: m, label: m }));
const refs = ["magpie/relay/glm-4.6"].map((m) => ({ value: m, label: m, ref: m }));
const agent = (id, name, more = {}) => ({
  id, name, path: "~/." + id + "/config.yaml", fields: [{ key: "model", label: "model", value: "model-a", options: models }], ...more,
});
const AGENTS = [
  agent("crush", "Crush"),
  agent("dsh", "DeepSeek Harness", { cliMissing: true }),
  // left connected to magpie before it was uninstalled
  agent("hermes", "Hermes Agent", {
    cliMissing: true, wired: true,
    fields: [{ key: "model", label: "model", value: "magpie/relay/glm-4.6", options: refs }, { key: "effort", label: "effort", value: "high", options: ["low", "high"].map((value) => ({ value })) }],
  }),
];
const CLIS = { crush: { version: "0.3.0", latest: "0.3.0", via: "brew", command: "brew upgrade crush" } };
const INSTALLS = [
  { id: "dsh", name: "DeepSeek Harness", icon: "deepseek-color", missing: true, commands: [{ via: "npm", command: "npm install -g @deepseek-ai/dsh" }] },
  { id: "opencode", name: "OpenCode", icon: "opencode", commands: [{ via: "npm", command: "npm install -g opencode-ai" }] },
];
const OFFERED = "{agent}'s settings are still here ({path}), but its command-line program isn't found: it may have been uninstalled. Install another agent, below the list, has its install command.";
const NOT_OFFERED = "{agent}'s settings are still here ({path}), but its command-line program isn't found: it may have been uninstalled. Its settings are offered here again once it is installed.";

function server(lang, agents = AGENTS, posts = []) {
  let current = structuredClone(agents);
  const state = () => ({ agents: current, profiles: [], settings: { lang, theme: "light" } });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state() });
    if (req.method() === "POST" && url.pathname.startsWith("/api/agents/")) {
      posts.push({ path: url.pathname, body: req.postDataJSON() });
      const id = url.pathname.match(/^\/api\/agents\/disconnect\/(.+)$/)?.[1];
      assert(id, "unexpected mutation: " + url.pathname);
      current = current.map((a) => a.id === id ? { ...a, wired: false } : a);
      return route.fulfill({ json: state() });
    }
    if (url.pathname === "/api/agents/cli" && req.method() === "GET") return route.fulfill({ json: { agents: CLIS, pending: false } });
    if (url.pathname === "/api/agents/install") return route.fulfill({ json: INSTALLS });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = (id) => `.row.agent[data-id="${id}"]`;
// the language's own words for an English key, failing for one it hasn't
const said = (page, lang, key, vars = {}) => page.evaluate(([lang, key, vars]) => {
  const s = lang === "en" ? key : I18N[lang]?.[key];
  if (!s) return null;
  return s.replace(/\{(\w+)\}/g, (_, k) => vars[k]);
}, [lang, key, vars]);
const noSideScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an agent whose CLI is gone says so, offers nothing to set, and is offered again", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} ${width}`, async () => {
          const page = await (await browser.newContext({ viewport: { width, height: 760 } })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang));
          await page.goto("http://magpie.test/");
          await page.locator(`${row("crush")} .ag-ver`).waitFor();
          await page.locator("#agentsInstall .ag-install-head").waitFor();
          const gone = await said(page, lang, "CLI not found");
          assert(gone, "CLI not found has no " + lang);
          // each gone row says so, with nothing of its settings to pick
          for (const id of ["dsh", "hermes"]) {
            const tag = page.locator(`${row(id)} .ag-missing`);
            assert.equal(await tag.textContent(), gone, id);
            assert.equal(await page.locator(`${row(id)} .field`).count(), 0, id + " offers a setting");
            assert.equal(await page.locator(`${row(id)} .ag-ver`).count(), 0);
            assert(await page.locator(row(id)).evaluate((r) => r.classList.contains("gone")));
          }
          assert.equal(await page.locator(`${row("crush")} .ag-missing`).count(), 0, "an installed one says nothing of it");
          assert(await page.locator(`${row("crush")} .field`).count() > 0, "an installed one lost its settings");
          // where it is installed again: Install another agent for dsh,
          // nowhere magpie knows for Hermes
          await page.locator(`${row("hermes")} .ag-missing`).hover();
          const tip = (id) => page.locator(`${row(id)} .ag-missing`).getAttribute("title");
          const dshTip = await said(page, lang, OFFERED, { agent: "DeepSeek Harness", path: "~/.dsh/config.yaml" });
          const hermesTip = await said(page, lang, NOT_OFFERED, { agent: "Hermes Agent", path: "~/.hermes/config.yaml" });
          assert(dshTip && hermesTip, "a tip has no " + lang);
          assert.equal(await tip("hermes"), hermesTip);
          await page.locator(`${row("dsh")} .ag-missing`).evaluate((m) => m.dispatchEvent(new MouseEvent("mouseenter")));
          assert.equal(await tip("dsh"), dshTip);
          // still connected: its switch stays, on, to put back what it had
          const sw = page.locator(`${row("hermes")} .ag-conn`);
          assert.equal(await sw.count(), 1);
          assert.equal(await sw.getAttribute("aria-checked"), "true");
          assert.equal(await page.locator(`${row("dsh")} .ag-conn`).count(), 0, "a gone one not connected has a switch");
          // the switch on the row's own line, inside it
          const box = (sel) => page.locator(sel).evaluate((e) => { const r = e.getBoundingClientRect(); return { l: r.left, r: r.right, t: r.top, b: r.bottom }; });
          const r = await box(row("hermes")), s = await box(`${row("hermes")} .ag-conn`), n = await box(`${row("hermes")} .name`);
          assert(s.r <= r.r + 0.5 && s.l >= n.r, "the switch is off its row");
          assert(s.t < n.b && s.b > n.t, "the switch is not on the name's line");
          // its menu: Disconnect, no Apply again
          await page.locator(row("hermes")).click({ button: "right" });
          const items = await page.locator(".row-menu .rm-item .rm-name").allTextContents();
          assert(items.includes(await said(page, lang, "Disconnect from magpie")), items.join(", "));
          assert(!items.includes(await said(page, lang, "Apply again")), items.join(", "));
          await page.keyboard.press("Escape");
          // the one not connected folds under Not set up, the connected one stays up
          assert.equal(await page.locator(`#agents > ${row("hermes")}`).count(), 1, "the connected one folded");
          assert.equal(await page.locator(`.agent-fold ${row("dsh")}`).count(), 1, "the one not connected stayed up");
          assert(await noSideScroll(page), "the page scrolls sideways");
          // Install another agent offers dsh, though it is listed above
          const inst = page.locator("#agentsInstall");
          assert.equal(await inst.locator(".ag-install-head").textContent(), await said(page, lang, "Install another agent ({n})", { n: 2 }));
          await inst.locator(".ag-install-head").click();
          assert.deepEqual(await inst.locator(".ag-install-row").evaluateAll((rs) => rs.map((r) => r.dataset.id)), ["dsh", "opencode"]);
          const dsh = inst.locator('.ag-install-row[data-id="dsh"]');
          assert.equal(await dsh.locator(".ag-install-missing").textContent(), gone);
          assert.equal(await dsh.locator("code").textContent(), "npm install -g @deepseek-ai/dsh");
          assert(await dsh.locator("b").evaluate((b) => b.scrollWidth <= b.clientWidth + 1), "name cut");
          // the tag isn't cut, nor the name beside it
          for (const id of ["dsh", "hermes"]) {
            assert(await page.locator(`${row(id)} .name`).evaluate((b) => b.scrollWidth <= b.clientWidth + 1), id + "'s name cut");
          }
          assert(await page.locator(`${row("hermes")} .ag-missing`).evaluate((b) => b.scrollWidth <= b.clientWidth + 1), "the tag cut");
          await page.screenshot({ path: path.join(process.env.SHOTS || "/tmp", `cli-missing-${engine}-${lang}-${width}.png`), fullPage: true }).catch(() => {});
          await page.context().close();
        });
      }
      await t.test(`${lang} panel`, async () => {
        const page = await (await browser.newContext({ viewport: { width: 400, height: 640 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?mode=panel");
        await page.locator(`${row("crush")} .ag-sum`).waitFor();
        const gone = await said(page, lang, "CLI not found");
        const sum = page.locator(`${row("hermes")} .ag-sum`);
        assert.equal(await sum.locator(".v").textContent(), gone);
        // nothing to open: a click opens no settings
        await page.locator(`${row("hermes")} .name`).click();
        assert.equal(await page.locator(`${row("hermes")}.open`).count(), 0);
        assert.equal(await page.locator(`${row("hermes")} .field, ${row("hermes")} .ag-open`).count(), 0);
        await sum.hover();
        assert.equal(await sum.getAttribute("title"), await said(page, lang, NOT_OFFERED, { agent: "Hermes Agent", path: "~/.hermes/config.yaml" }));
        // in words in the same column as the others'
        const right = (id) => page.locator(`${row(id)} .ag-sum`).evaluate((e) => e.getBoundingClientRect().right);
        assert(Math.abs(await right("hermes") - await right("crush")) < 1, "not in the column");
        assert(await sum.locator(".v").evaluate((b) => b.scrollWidth <= b.clientWidth + 1), "the words cut");
        assert(await noSideScroll(page), "the panel scrolls sideways");
        await page.screenshot({ path: path.join(process.env.SHOTS || "/tmp", `cli-missing-${engine}-${lang}-panel.png`), fullPage: true }).catch(() => {});
        await page.context().close();
      });
    }
    assert.deepEqual(errors, []);
  });

  // #7291: disconnecting an existing setup needs no models, even after
  // the CLI is removed. Check the same case for another agent and a WSL twin.
  test(engine + ": a missing CLI with no models can still disconnect and restore", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        for (const id of ["opencode", "hermes", "opencode@wsl:Ubuntu"]) {
          await t.test(`${lang} ${width} ${id}`, async (t) => {
            const context = await browser.newContext({ viewport: { width, height: 760 } });
            t.after(() => context.close());
            const page = await context.newPage(), posts = [], errors = [];
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            page.on("pageerror", (e) => errors.push(e.message));
            const missing = agent(id, id === "hermes" ? "Hermes Agent" : "OpenCode", {
              cliMissing: true, wired: true,
              fields: [{ key: "model", label: "model", value: "", options: [] }],
              models: { listed: 0, shown: 0 },
            });
            await page.route("**/*", server(lang, [missing], posts));
            await page.goto("http://magpie.test/");
            await page.locator(`${row(id)} .ag-missing`).waitFor();
            const sw = page.locator(`${row(id)} .ag-conn`);
            assert.equal(await sw.count(), 1, "a wired agent with no models lost its disconnect switch");
            assert.equal(await sw.getAttribute("aria-checked"), "true");
            assert.equal(await page.locator(`${row(id)} .field`).count(), 0, "the missing CLI offers settings");
            await sw.click();
            const ask = page.locator(".editor.disconnect-ask");
            await ask.waitFor();
            assert.deepEqual(posts, [], "opening the confirmation changed the config");
            await ask.getByRole("button", { name: await said(page, lang, "Cancel"), exact: true }).click();
            await ask.waitFor({ state: "detached" });
            assert.deepEqual(posts, [], "Cancel disconnected the agent");
            assert.equal(await sw.getAttribute("aria-checked"), "true");
            // The menu is the other route to the same restore confirmation.
            await page.locator(row(id)).click({ button: "right" });
            await page.locator(".row-menu .rm-item", { hasText: await said(page, lang, "Disconnect from magpie") }).click();
            await ask.waitFor();
            assert.deepEqual(posts, [], "the menu disconnected without asking");
            await ask.getByRole("button", { name: await said(page, lang, "Disconnect and restore"), exact: true }).click();
            await ask.waitFor({ state: "detached" });
            await page.locator(`.agent-fold ${row(id)}`).waitFor();
            assert.deepEqual(posts, [{ path: "/api/agents/disconnect/" + id, body: {} }]);
            assert.equal(await sw.count(), 0, "a disconnected missing CLI offers a connect switch");
            assert.equal(await page.locator(`${row(id)} .field`).count(), 0);
            assert.deepEqual(errors, []);
          });
        }
      }
    }
  });
}
