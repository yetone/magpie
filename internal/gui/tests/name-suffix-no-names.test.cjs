// Run with Node's test runner and Playwright on the module path; see README.md.
// love1sbug on X (v0.1.1220): Names in agents' lists (Agent 列表里的名称)
// changed nothing in the agent's model list. Claude Code's and Qwen Code's
// lists follow it now; an agent whose list magpie hands the models' ids
// alone, or that reads the gateway's names itself (agent.NoNames), can't,
// so each place the setting is offered names the connected ones it changes
// nothing in: under Settings › Provider in model names, under Routing's
// Names in agents' lists, and in a provider's Names, prices & levels. One not
// connected, or one whose list follows it, isn't named; with none, nothing
// is said. Every language, at a narrow width too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const said = {
  en: "Changes nothing in Goose, Hermes Agent: magpie’s names don’t reach their model lists",
  zh: "对 Goose, Hermes Agent 不起作用：它们的模型列表里没有 magpie 给的名称",
  "zh-TW": "對 Goose, Hermes Agent 不起作用：它們的模型列表裡沒有 magpie 給的名稱",
  ja: "Goose, Hermes Agent には効きません：そのモデル一覧には magpie の名前が出ません",
  de: "Wirkt nicht in Goose, Hermes Agent: deren Modelllisten zeigen magpies Namen nicht",
};
const names = { en: "Names, prices & levels", zh: "名称、价格与推理档位" };

function serve(lang, agents, late = 0) {
  const cur = { lang, theme: "light", plainNames: false, plainOwnNames: false };
  const state = { agents, profiles: [], settings: cur };
  const models = [{ id: "glm-4.6", name: "GLM-4.6", default: "glm-4.6", on: true, efforts: [], images: false }];
  const provider = { id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true };
  const groups = { models: [{ id: "relay/glm-4.6", name: "GLM-4.6", providerName: "Relay", icon: "generic" }], pools: [],
    groups: [{ id: "fast", name: "Fast", members: ["relay/glm-4.6"], routing: "order", ready: true, memberInfo: [{ id: "relay/glm-4.6", ready: true }] }] };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    // late: the agents read after the page is drawn, as at a start (#1519)
    if (url.pathname === "/api/state") return setTimeout(() => json(state).catch(() => {}), late);
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function launch(t, engine, lang, agents, width = 900, late = 0) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-no-names.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, agents, late));
  return { page, errors };
}

// Goose and Hermes are connected and can't follow it; Claude Code is
// connected and does; Jcode can't but isn't connected
const agents = [
  { id: "claude", name: "Claude Code", path: "/test/claude", fields: [], wired: true },
  { id: "goose", name: "Goose", path: "/test/goose", fields: [], wired: true, noNames: true },
  { id: "hermes", name: "Hermes Agent", path: "/test/hermes", fields: [], wired: true, noNames: true },
  { id: "jcode", name: "jcode", path: "/test/jcode", fields: [], noNames: true },
];
const overflows = (page) => page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(said)) {
    for (const width of [900, 390]) {
      test(`${engine} ${lang} ${width}px: Settings and Routing name the connected agents the setting changes nothing in`, async (t) => {
        const { page, errors } = await launch(t, engine, lang, agents, width);
        await page.goto("http://magpie.test/?view=settings");
        const sub = page.locator(".row.pref", { has: page.locator("#plainNamesSegs") }).locator(".sub.no-names");
        await sub.waitFor();
        assert.equal(await sub.textContent(), said[lang]);
        assert.equal(await overflows(page), false, "the page scrolls sideways");
        await page.goto("http://magpie.test/?view=routing");
        const note = page.locator(".rt-gnames .no-names");
        await note.waitFor();
        assert.equal(await note.textContent(), said[lang]);
        assert.equal(await overflows(page), false, "the page scrolls sideways");
        assert.equal(await note.evaluate((e) => getComputedStyle(e).borderLeftWidth), "0px");
        assert.deepEqual(errors, []);
      });
    }
  }
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a provider's Names, prices & levels says it too`, async (t) => {
      const { page, errors } = await launch(t, engine, lang, agents);
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider").click();
      if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: names[lang], exact: true }).click();
      const note = page.locator(".mnames:not([hidden]) .msuffix .no-names");
      await note.waitFor();
      assert.equal(await note.textContent(), said[lang]);
      assert.deepEqual(errors, []);
    });
  }
  // drawn before the agents are read, each place says it once they are
  test(`${engine}: read after the page is drawn, the agents are named all the same`, async (t) => {
    const { page, errors } = await launch(t, engine, "en", agents, 900, 1500);
    await page.goto("http://magpie.test/?view=providers");
    await page.locator(".row.provider").click();
    if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: names.en, exact: true }).click();
    const note = page.locator(".mnames:not([hidden]) .msuffix .no-names");
    await note.waitFor();
    assert.equal(await note.textContent(), said.en);
    for (const [view, sel] of [["routing", ".rt-gnames .no-names"], ["settings", ".row.pref .sub.no-names"]]) {
      await page.goto(`http://magpie.test/?view=${view}`);
      await page.locator(sel).waitFor();
      assert.equal(await page.locator(sel).textContent(), said.en);
    }
    assert.deepEqual(errors, []);
  });
  test(`${engine}: with every connected agent following it, nothing is said`, async (t) => {
    const { page, errors } = await launch(t, engine, "en", agents.filter((a) => a.id === "claude" || a.id === "jcode"));
    await page.goto("http://magpie.test/?view=settings");
    await page.locator("#plainNamesSegs .opt").first().waitFor();
    assert.equal(await page.locator(".row.pref", { has: page.locator("#plainNamesSegs") }).locator(".sub").count(), 0);
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-gnames .segs").waitFor();
    assert.equal(await page.locator(".rt-gnames .no-names:visible").count(), 0);
    assert.deepEqual(errors, []);
  });
}
