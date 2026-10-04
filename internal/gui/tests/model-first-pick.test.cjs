// Run with Node's test runner and Playwright on the module path; see README.md.
// #681 (ChenstarMr): Trae CN's plugin provider, 54 models, none picked. A
// click on DeepSeek V4 Flash, to pick it, left it the one model out and
// picked every other served one in its place: "我想选其中一个模型，但是点击
// 之后它却选了其他的所有模型". With none picked the served models are drawn
// apart from picks (dashed, no tick, no accent fill), the hint and their
// title say a click picks just that one, and it does: that model alone is
// ticked, the page doesn't move, and Save writes it alone. Opened and saved
// untouched it still writes no picks. A built-in key provider does the same.
// #702 (Dazzle-sys) is the same click: Cline signed in, 22 models, all of
// them served. Each of the 4 free ones clicked till it showed ticked (the
// first click unticked it and ticked the other 21, the second ticked it
// back) left all 22 picked, and Save wrote 22; now it writes the 4.
// English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const names = ["Doubao Seed 2.5 Pro", "Kimi K3", "GLM 5.3", "Qwen 4 Max", "DeepSeek V4", "DeepSeek V4 Flash"];
const models = Array.from({ length: 54 }, (_, i) => ({ id: i < names.length ? names[i].toLowerCase().replaceAll(" ", "-") : `trae-model-${i}`, name: i < names.length ? names[i] : `Trae Model ${i}` }));

// Cline's list: 22 models, 4 of them free
const clineFree = ["cline-free/deepseek-v4.1-flash", "stealth/space-bunny-alpha", "cline-free/mimo-v2.6-flash", "cline-free/muse-spark-1.3-contributor"];
const cline = [...Array.from({ length: 18 }, (_, i) => ({ id: `cline-pass/model-${i}`, name: `Model ${i} (ClinePass)` })), ...clineFree.map((id) => ({ id, name: id.split("/")[1] + " (free)", free: true }))];

function providerFor(id, name, chosen, plugin, list = models) {
  const on = new Set(chosen.length ? chosen : list.slice(0, 24).map((m) => m.id));
  const p = {
    id, name, icon: plugin ? "trae" : "generic", host: "", chat: plugin ? "" : "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
    models: list.map((m) => ({ ...m, on: on.has(m.id) })), chosen, fetched, agents: [], fallback: [], headers: {},
    key: { set: !plugin, masked: plugin ? "" : "sk-…one", optional: plugin }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
  };
  if (plugin) p.account = { agent: id, agentName: name, agentIcon: "trae", builtin: "", user: "a@trae", logins: [{ user: "a@trae", active: true, on: true }] };
  return p;
}

function serve(lang, saves) {
  const chosen = { "trae-cn-plugin": [], relay: [], cline: [] };
  const providers = () => ({ providers: [providerFor("trae-cn-plugin", "Trae CN", chosen["trae-cn-plugin"], true), providerFor("relay", "Relay", chosen.relay, false), providerFor("cline", "Cline", chosen.cline, true, cline)], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/save") {
      const body = req.postDataJSON();
      saves.push([body.id, body.models]);
      chosen[body.id] = body.models;
      return json(providers());
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { hint: "Click a model to pick just it", tip: "Click to pick just this model", save: "Save", picked: "Agents see the models picked." },
  zh: { hint: "点一个模型就只选它", tip: "点击只选这个模型", save: "保存", picked: "Agent 看到的是已勾选的模型。" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": with none picked, a click on a model picks just it (#681)", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      for (const [id, name] of [["trae-cn-plugin", "Trae CN"], ["relay", "Relay"]]) {
        await t.test(`${lang}: ${name}`, async () => {
          const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          const errors = [], saves = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, saves));
          await page.goto("http://magpie.test/?view=providers");
          const editor = page.locator("#modal:not([hidden]) .editor");
          const open = async () => {
            await page.locator(".row.provider", { hasText: name }).first().click();
            await editor.locator(".mchips .mchip").first().waitFor();
          };
          const chip = (n) => editor.locator(".mchips .mchip", { has: page.locator(`span:text-is("${n}")`) });
          const picked = () => editor.locator(".mchips .mchip.on").evaluateAll((cs) => cs.map((c) => c.firstChild.textContent));
          const ticked = () => editor.locator(".mchips .mchip").evaluateAll((cs) => cs.filter((c) => getComputedStyle(c, "::before").content.includes("✓")).length);

          // untouched and saved: no picks written, as before
          await open();
          assert.deepEqual(await picked(), []);
          assert.equal(await ticked(), 0, "none is drawn ticked with none picked");
          assert.equal(await editor.locator(".mchips .mchip.auto").count(), 24);
          await editor.getByText(w.hint).waitFor();
          assert.ok((await chip("DeepSeek V4 Flash").getAttribute("title")).includes(w.tip));
          await editor.getByRole("button", { name: w.save, exact: true }).click();
          await editor.waitFor({ state: "detached" });
          assert.deepEqual(saves, [[id, []]]);

          // a click on one served model picks it alone, the page left where it was
          await open();
          const target = chip("DeepSeek V4 Flash");
          await target.scrollIntoViewIfNeeded();
          const y = () => page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join(","));
          const before = await y();
          await target.click();
          assert.equal(await y(), before, "the click moved the page");
          assert.deepEqual(await picked(), ["DeepSeek V4 Flash"]);
          assert.equal(await ticked(), 1);
          assert.equal(await editor.locator(".mchips .mchip.auto").count(), 0);
          await editor.getByText(w.picked).waitFor();
          // a second one joins it; one clicked again leaves
          await chip("Kimi K3").click();
          assert.deepEqual(await picked(), ["Kimi K3", "DeepSeek V4 Flash"]);
          await chip("Kimi K3").click();
          assert.deepEqual(await picked(), ["DeepSeek V4 Flash"]);
          await editor.getByRole("button", { name: w.save, exact: true }).click();
          await editor.waitFor({ state: "detached" });
          assert.deepEqual(saves[1], [id, ["deepseek-v4-flash"]]);
          await open();
          assert.deepEqual(await picked(), ["DeepSeek V4 Flash"]);
          assert.equal(await page.locator("select").count(), 0);
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }
      await t.test(`${lang}: Cline's 4 free models clicked till ticked save 4 (#702)`, async () => {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], saves = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, saves));
        await page.goto("http://magpie.test/?view=providers");
        const editor = page.locator("#modal:not([hidden]) .editor");
        await page.locator(".row.provider", { hasText: "Cline" }).first().click();
        await editor.locator(".mchips .mchip").first().waitFor();
        assert.equal(await editor.locator(".mchips .mchip.auto").count(), 22);
        for (const id of clineFree) {
          const c = editor.locator(".mchips .mchip", { has: page.locator(`span:text-is("${id.split("/")[1]} (free)")`) });
          for (let i = 0; i < 3 && !(await c.evaluate((e) => e.classList.contains("on"))); i++) await c.click();
        }
        assert.equal(await editor.locator(".mchips .mchip.on").count(), 4);
        await editor.getByRole("button", { name: w.save, exact: true }).click();
        await editor.waitFor({ state: "detached" });
        assert.deepEqual(saves, [["cline", clineFree]]);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
