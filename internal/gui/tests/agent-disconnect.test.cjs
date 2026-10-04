// Run with Node's test runner and Playwright on the module path; see README.md.
// Fate on Discord: taking out what magpie wrote into an agent's config
// wasn't obvious — picking Default looked like it might, and isn't it (it is
// the agent as installed). An agent magpie is in (wired) has "Disconnect from
// magpie" in its row's menu and in its model picker, beside Default, each
// saying what it does; an agent magpie isn't in has neither. It asks first
// in the app's own dialog (no native confirm), Cancel posts nothing, and
// Disconnect posts agents/disconnect and draws the row with the model the
// agent had before, no longer wired. No click moves the page. In English and
// Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = ["gpt-5.4", "magpie/relay/m1"].map((m) => ({ value: m, label: "Label " + m, ...(m.startsWith("magpie/") ? { ref: "relay/m1" } : {}) }));
const fresh = () => ({
  agents: [
    { id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", wired: true, fields: [{ key: "model", label: "model", value: "magpie/relay/m1", options }] },
    { id: "claude", name: "Claude Code", icon: "generic", path: "/fixture/claude", fields: [{ key: "model", label: "model", value: "gpt-5.4", options }] },
  ],
  profiles: [],
});

function server(lang, posts) {
  let cur = fresh();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...cur, settings: { lang, theme: "light" } });
    if (url.pathname.startsWith("/api/agents/disconnect/")) {
      posts.push(url.pathname);
      cur = JSON.parse(JSON.stringify(cur));
      const a = cur.agents.find((x) => x.id === url.pathname.split("/").pop());
      delete a.wired;
      a.fields[0].value = "gpt-5.4";
      return json({ ...cur, settings: { lang, theme: "light" } });
    }
    if (url.pathname === "/api/set") { posts.push(url.pathname); return json({ ...cur, settings: { lang, theme: "light" } }); }
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
  en: { item: "Disconnect from magpie", ask: "Disconnect Codex from magpie?", go: "Disconnect", cancel: "Cancel", done: /Codex no longer goes through magpie/ },
  zh: { item: "断开 magpie（还原配置）", ask: "断开 Codex 和 magpie？", go: "断开", cancel: "取消", done: /Codex 已不再经过 magpie/ },
};
const row = (id) => `.row.agent[data-id="${id}"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Disconnect from magpie puts back what the agent had`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 980, height: 520 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [], dialogs = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { dialogs.push(d.message()); d.dismiss(); });
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/?view=agents");
      await page.locator(row("claude")).waitFor();
      // Claude Code, not connected, is under Not set up (#726): unrolled
      await page.locator(".agent-more").click();
      await page.waitForFunction(() => !document.querySelector(".agent-fold-inner")?.inert);
      const missing = await page.evaluate(() => [
        "Drag to reorder · click to move, hide or disconnect from magpie",
        "Disconnect from magpie",
        "Take out everything magpie wrote into {agent}'s config and put back what it had before",
        "put back what {agent} had before magpie",
        "Disconnect {agent} from magpie?",
        "magpie takes out everything it wrote into {agent}'s config — its endpoint, key, models and effort — and puts back the settings {agent} had before. magpie's providers and accounts stay as they are.",
        "Disconnect",
        "{agent} no longer goes through magpie; its own settings are back",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      const tops = () => page.evaluate(() => [document.scrollingElement.scrollTop, document.querySelector("#view-agents")?.scrollTop]);
      const top = await tops();
      const menuItem = () => page.locator(".pop.row-menu .rm-item", { hasText: w.item });
      const openMenu = async (id) => {
        await page.locator(`${row(id)} .ag-handle`).click();
        await page.locator(".pop.row-menu").waitFor();
      };
      const closeMenu = async () => { await page.keyboard.press("Escape"); await page.locator(".pop.row-menu").waitFor({ state: "detached" }); };

      // only where magpie is in the config
      await openMenu("claude");
      assert.equal(await menuItem().count(), 0, "an agent magpie isn't in has nothing to disconnect");
      await closeMenu();
      assert.match(await page.locator(`${row("codex")} .ag-handle`).getAttribute("title"), lang === "en" ? /disconnect from magpie/ : /断开 magpie/);
      await openMenu("codex");
      assert.equal(await menuItem().count(), 1);
      assert.ok(await menuItem().getAttribute("title"), "it says what it does");

      // asked in the app's own dialog; Cancel leaves everything
      await menuItem().click();
      const ask = page.locator(".editor.disconnect-ask");
      await ask.waitFor();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.ask);
      await ask.locator(".bar button", { hasText: w.cancel }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(posts, []);

      // the model picker beside the switch has it too, beside Default;
      // Claude Code, not connected, has only magpie's models to pick there
      // and no row to open
      assert.equal(await page.locator(`${row("claude")} .field:not(.ag-start)`).count(), 0);
      assert.equal(await page.locator(`${row("claude")} .ag-link`).count(), 0);
      await page.locator(`${row("codex")} > .field.ag-start[data-key="model"]`).click();
      const pickItem = page.locator("#list li", { hasText: w.item });
      await pickItem.waitFor();
      assert.ok(await pickItem.getAttribute("title"), "it says what it does");
      assert.equal(await pickItem.locator(".favorite").count(), 0, "an act, not a model to star");
      await pickItem.click();
      await ask.waitFor();
      assert.deepEqual(posts, [], "the pick asks first, it sets nothing");

      await ask.locator(".bar button", { hasText: w.go }).click();
      await ask.waitFor({ state: "detached" });
      await page.waitForFunction((r) => document.querySelector(r + " .ag-conn")?.getAttribute("aria-checked") === "false", row("codex"));
      assert.deepEqual(posts, ["/api/agents/disconnect/codex"]);
      assert.match(await page.locator("#status").textContent(), w.done);
      await openMenu("codex");
      assert.equal(await menuItem().count(), 0, "no longer wired, nothing to disconnect");
      await closeMenu();
      assert.deepEqual(await tops(), top, "no click moved the page");
      assert.deepEqual(dialogs, [], "no native dialog");
      assert.deepEqual(errors, []);
    });
  }
}
