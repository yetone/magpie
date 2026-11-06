// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin that serves one model from several sources (sunset on Discord:
// the Our Free Model plugin's buddy/deepseek-v4.1-flash, cline/… and
// kilo/…, each named "DeepSeek-V4.1-Flash" at 0.1×) drew chips that read
// alike, the source only in the tooltip. Models of one list that would
// show the same name now say, quietly after it, the part of the id that
// tells them apart (modelTells); a name no other model has is drawn as it
// was. Checked on the provider editor's chips and in a model picker, and
// modelTells itself for ids with no prefix, one prefix shared, and ids
// that differ deeper in. Chromium and WebKit, wide and narrow, in every
// language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const models = [
  { id: "buddy/deepseek-v4.1-flash", name: "DeepSeek-V4.1-Flash", on: true, rate: 0.1 },
  { id: "cline/deepseek-v4.1-flash", name: "DeepSeek-V4.1-Flash", on: true, rate: 0.1 },
  { id: "kilo/deepseek-v4.1-flash", name: "DeepSeek-V4.1-Flash", on: true, rate: 0.1 },
  { id: "buddy/glm-5.2", name: "GLM-5.2", on: true, rate: 0.1 },
];
const provider = {
  id: "our-free-model", name: "Our Free Model", icon: "generic", host: "", chat: "", responses: "", anthropic: "", catalog: "",
  models, chosen: models.map((m) => m.id), fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};

// the Routing page's view of the same models: a group of two of the alike
const routed = models.map((m) => ({ id: "our-free-model/" + m.id, name: m.name, provider: "our-free-model", providerName: "Our Free Model", icon: "generic" }));
const group = {
  id: "flash", name: "Flash", routing: "order", ready: true,
  members: routed.slice(0, 2).map((m) => m.id),
  memberInfo: routed.slice(0, 2).map((m) => ({ id: m.id, ready: true, name: "Our Free Model", provider: "our-free-model", model: m.id.split("/").slice(1).join("/"), icon: "generic" })),
};

function serve(lang) {
  const providers = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ models: routed, pools: [], deciders: [], found: false, groups: [group] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const cases = [...LANGS.map((lang) => [lang, 1100]), ["en", 420], ["zh", 420]];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, width] of cases) {
    test(`${engine} ${lang} ${width}px: models named alike say what tells them apart`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-model-same-name-tell.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=providers");

      const editor = page.locator("#modal:not([hidden]) .editor");
      await page.locator(".row.provider", { hasText: "Our Free Model" }).click();
      await editor.locator(".mchips .mchip").first().waitFor();

      // the three alike say their sources; the one of its own name says none
      const chips = editor.locator(".mchips .mchip");
      const drawn = await chips.evaluateAll((cs) => cs.map((c) => ({ name: c.querySelector("span").textContent, tell: c.querySelector(".mtell")?.textContent ?? null, title: c.title })));
      assert.deepEqual(drawn.map(({ name, tell }) => [name, tell]), [
        ["DeepSeek-V4.1-Flash", "buddy"],
        ["DeepSeek-V4.1-Flash", "cline"],
        ["DeepSeek-V4.1-Flash", "kilo"],
        ["GLM-5.2", null],
      ]);
      // the whole id is still the tooltip's
      assert.equal(drawn[0].title.split("\n")[0], "buddy/deepseek-v4.1-flash");

      // each tell inside its chip, and each chip inside the editor, narrow too
      const ed = await editor.boundingBox();
      for (let i = 0; i < 4; i++) {
        const c = await chips.nth(i).boundingBox();
        assert.ok(c.x >= ed.x - 0.5 && c.x + c.width <= ed.x + ed.width + 0.5, `chip ${i} ${JSON.stringify(c)} inside editor ${JSON.stringify(ed)}`);
        const tell = chips.nth(i).locator(".mtell");
        if (!await tell.count()) continue;
        const b = await tell.boundingBox();
        assert.ok(b.x >= c.x && b.x + b.width <= c.x + c.width + 0.5, `tell ${JSON.stringify(b)} inside chip ${JSON.stringify(c)}`);
      }

      // a model picker, its options as an agent's or Settings' come: the
      // alike rows of one provider say their sources, another provider's
      // row of the same name, alone in its group, says nothing
      await page.evaluate(() => {
        const b = document.createElement("button");
        b.id = "tellAnchor";
        b.textContent = "model";
        document.body.append(b);
        const opt = (provider, id, label) => ({ value: provider + "/" + id, label, note: provider, icon: "generic", group: provider, ref: provider + "/" + id });
        openPicker({ id: "", name: "", fields: [] }, { key: "tellTest", label: "model", value: "", onPick: () => {}, options: [
          opt("Our Free Model", "buddy/deepseek-v4.1-flash", "DeepSeek-V4.1-Flash"),
          opt("Our Free Model", "cline/deepseek-v4.1-flash", "DeepSeek-V4.1-Flash"),
          opt("Our Free Model", "buddy/glm-5.2", "GLM-5.2"),
          opt("DeepSeek", "deepseek-v4.1-flash", "DeepSeek-V4.1-Flash"),
        ] }, b, { stopPropagation() {} });
      });
      const rows = await page.locator("#list li:not(.group)").evaluateAll((ls) => ls.map((l) => [l.querySelector(".v")?.textContent, l.querySelector(".mtell")?.textContent ?? null]));
      assert.deepEqual(rows.filter(([v]) => v), [
        ["DeepSeek-V4.1-Flash", "buddy"],
        ["DeepSeek-V4.1-Flash", "cline"],
        ["GLM-5.2", null],
        ["DeepSeek-V4.1-Flash", null],
      ]);
      await page.keyboard.press("Escape");

      // the groups page's card of a group of two alike says each one's source
      await page.evaluate(() => document.querySelector("#tellAnchor")?.remove());
      await page.goto("http://magpie.test/?view=groups");
      const mem = page.locator("#view-groups .mem", { hasText: "DeepSeek-V4.1-Flash" }).first();
      await mem.waitFor();
      const said = await mem.textContent();
      assert.ok(said.includes("DeepSeek-V4.1-Flash · buddy") && said.includes("DeepSeek-V4.1-Flash · cline"), said);
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Our Free Model" }).click();
      await editor.locator(".mchips .mchip").first().waitFor();

      // the helper on its own: no prefix, one prefix shared, a difference
      // deeper in, and labels that are already apart
      const tells = await page.evaluate(() => Object.fromEntries(modelTells([
        { id: "deepseek-v4.1-flash", name: "Flash" },
        { id: "buddy/deepseek-v4.1-flash", name: "Flash" },
        { id: "buddy/a/x", name: "X" },
        { id: "buddy/b/x", name: "X" },
        { id: "buddy/x-fast", name: "Y" },
        { id: "buddy/x-slow", name: "Y" },
        { id: "only/one", name: "Z" },
        { id: "no-name" },
        { id: "other/no-name", name: "no-name" },
      ])));
      assert.deepEqual(tells, {
        "deepseek-v4.1-flash": "deepseek-v4.1-flash",
        "buddy/deepseek-v4.1-flash": "buddy",
        "buddy/a/x": "a",
        "buddy/b/x": "b",
        "buddy/x-fast": "x-fast",
        "buddy/x-slow": "x-slow",
        "no-name": "no-name",
        "other/no-name": "other",
      });
      assert.deepEqual(errors, []);
    });
  }
}
