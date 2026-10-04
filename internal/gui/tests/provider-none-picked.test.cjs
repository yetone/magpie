// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider with none of its models picked serves agents its vendor's
// list (#614: the one model unticked and saved was still "1 model" on the
// card, and the editor showed it unticked as if left out). With no pick the
// models agents are served are drawn as served (dashed, .auto), their title
// says why, and the hint says how to show agents none, from the moment the
// editor opens. A served one clicked is picked alone (#681: it used to be
// left out, the rest picked in its place). In English and Chinese, Chromium
// and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const relay = {
  id: "router", name: "Router", icon: "generic", host: "router.example.com", chat: "https://router.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "glm-5.3-free", name: "glm-5.3-free", on: true }, { id: "glm-5.2", name: "glm-5.2", on: true }], chosen: [], fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};

function serve(lang) {
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { why: "Agents see it: none are picked", hint: "To show them none, tick Only through routing groups" },
  zh: { why: "Agent 能看到它：没有勾选任何模型时", hint: "不想让 Agent 看到任何模型，就勾选「只通过路由分组使用」" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: with none picked, the models agents are served are drawn as served`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-provider-none-picked.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Router" }).click();
      const editor = page.locator("#modal:not([hidden]) .editor");
      const chip = (id) => editor.locator(".mchips .mchip", { hasText: id });
      await chip("glm-5.2").waitFor();
      const cls = (id) => chip(id).evaluate((c) => [c.classList.contains("on"), c.classList.contains("auto")]);

      // opened with none picked, both are drawn as served, not as picked
      // (#614: they were drawn ticked, and the next Save made them picks)
      for (const id of ["glm-5.3-free", "glm-5.2"]) {
        assert.deepEqual(await cls(id), [false, true], id + " drawn as served");
        assert.ok((await chip(id).getAttribute("title")).includes(w.why), id + " title");
      }
      assert.equal(await chip("glm-5.2").evaluate((c) => getComputedStyle(c).borderTopStyle), "dashed");
      await editor.getByText(w.hint).waitFor();

      // a served one clicked is picked, alone (#681)
      await chip("glm-5.2").click();
      assert.deepEqual(await cls("glm-5.2"), [true, false]);
      assert.deepEqual(await cls("glm-5.3-free"), [false, false]);
      assert.equal(await editor.getByText(w.hint).count(), 0);
      // and another clicked joins it
      await chip("glm-5.3-free").click();
      assert.deepEqual(await cls("glm-5.2"), [true, false]);
      assert.deepEqual(await cls("glm-5.3-free"), [true, false]);
      assert.deepEqual(errors, []);
    });
  }
}
