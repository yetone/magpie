// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider with none of its models picked serves agents its vendor's
// list (#614: the one model unticked and saved was still "1 model" on the
// card, and the editor showed it unticked as if left out). With no pick the
// models agents are served are drawn as served (dashed, .auto), their title
// says why, and the hint says how to use none, from the moment the editor
// opens. A served one clicked is picked alone (#681: it used to be left out,
// the rest picked in its place). Select none is saved as noModels and opens
// again as none (#1818: it was saved as no picks, and the card, the gateway
// and the auto groups all served 17). Kept for routing groups alone with
// none picked, the models the groups use are drawn dashed too (#1818: drawn
// as none while the groups used them). What it serves is said by a
// control, Vendor's list or Picked only, and the last pick unpicked leaves
// none, as Select none does, not the vendor's list again (#1831, kchen0x).
// None is the control's third option, Usage only, one click away, and the
// card says it (#2068, keleus: it took Picked only, then Select none, then
// Save). Picked only from there picks nothing (yuancjun: it ticked the
// first 24 the vendor's list serves, which read as Select all and was saved
// as picks): it says to pick a model first, and a Save is refused until one
// is picked; Select none from there is Usage only.
// In every language, Chromium and WebKit, at 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const base = {
  icon: "generic", responses: "", anthropic: "", catalog: "",
  models: [{ id: "glm-5.3-free", name: "glm-5.3-free", on: true }, { id: "glm-5.2", name: "glm-5.2", on: true }], chosen: [], fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const relay = { ...base, id: "router", name: "Router", host: "router.example.com", chat: "https://router.example.com/v1" };
// as /api/providers sends one saved as none: none of its models on (Exposed)
const watched = { ...base, id: "watched", name: "Watched", host: "watched.example.com", chat: "https://watched.example.com/v1", noModels: true, exposed: 0, models: base.models.map((m) => ({ ...m, on: false })) };
const grouped = { ...base, id: "grouped", name: "Grouped", host: "grouped.example.com", chat: "https://grouped.example.com/v1", unlisted: true, groups: { "glm-5.2": ["group/auto-glm-5-2"], "glm-5.3-free": ["group/auto-glm-5-3-free"] } };

function serve(lang, saves) {
  const providers = { providers: [relay, watched, grouped], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/provider/save") { saves.push(req.postDataJSON()); return json(providers); }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// why: a dashed chip's title; hint: the hint with none picked; none: the
// hint after Select none; groupsWhy, groupsHint: those kept for routing
// groups alone
const words = {
  en: { pickFirst: "Pick a model first.", save: "Save", selectNone: "Select none", list: "Vendor's list", picked: "Picked only", usage: "Usage only", card: "usage only", why: "Agents see it: none are picked", hint: "Vendor's list: agents see up to 24 of its models", none: "None selected: no agent or routing group uses its models", groupsWhy: "Routing groups use it: none are picked", groupsHint: "the routing groups its models are in use up to 24" },
  zh: { pickFirst: "请先选择模型。", save: "保存", selectNone: "全不选", list: "供应商列表", picked: "仅所选", usage: "仅看用量", card: "仅看用量", why: "Agent 能看到它：没有勾选任何模型时", hint: "供应商列表：Agent 看到最多 24 个模型", none: "全不选：Agent 和路由分组都不使用它的模型", groupsWhy: "路由分组会用到它", groupsHint: "模型所在的路由分组最多使用其中 24 个" },
  "zh-TW": { pickFirst: "請先選擇模型。", save: "儲存", selectNone: "全不選", list: "供應商列表", picked: "僅所選", usage: "僅看用量", card: "僅看用量", why: "Agent 能看到它：沒有勾選任何模型時", hint: "供應商列表：Agent 看到最多 24 個模型", none: "全不選：Agent 和路由分組都不使用它的模型", groupsWhy: "路由分組會用到它", groupsHint: "模型所在的路由分組最多使用其中 24 個" },
  ja: { pickFirst: "先にモデルを選んでください。", save: "保存", selectNone: "選択をすべて解除", list: "ベンダーのリスト", picked: "選択したもののみ", usage: "使用量のみ", card: "使用量のみ", why: "エージェントに表示されます：何も選択されていない", hint: "エージェントにはモデルが最大 24 件", none: "選択なし：エージェントもルーティンググループも", groupsWhy: "ルーティンググループが使います", groupsHint: "ルーティンググループが最大 24 件" },
  de: { pickFirst: "Wählen Sie zuerst ein Modell.", save: "Speichern", selectNone: "Keine auswählen", list: "Liste des Anbieters", picked: "Nur gewählte", usage: "Nur Nutzung", card: "nur Nutzung", why: "Agenten sehen es: Nichts ist gewählt", hint: "Agenten sehen bis zu 24 seiner Modelle", none: "Keine ausgewählt: Weder Agenten noch Weiterleitungsgruppen", groupsWhy: "Weiterleitungsgruppen nutzen es", groupsHint: "Weiterleitungsgruppen seiner Modelle nutzen bis zu 24" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
   const w = words[lang];
   for (const width of [1100, 420]) {
    test(`${engine} ${lang} ${width}: with none picked, the models agents are served are drawn as served`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-provider-none-picked.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const saves = [];
      await page.route("**/*", serve(lang, saves));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Router" }).click();
      const editor = page.locator("#modal:not([hidden]) .editor");
      const chip = (id) => editor.locator(".mchips .mchip", { hasText: id });
      await chip("glm-5.2").waitFor();
      const cls = (id) => chip(id).evaluate((c) => [c.classList.contains("on"), c.classList.contains("auto")]);
      // which of Vendor's list / Picked only / Usage only is on, and the
      // control fits
      const serves = async () => {
        const seg = editor.locator(".mserves");
        const box = await seg.boundingBox(), ed = await editor.boundingBox();
        assert.ok(box && box.x >= ed.x - 1 && box.x + box.width <= ed.x + ed.width + 1, "the control fits the editor");
        return seg.locator(".opt.on").innerText();
      };

      // opened with none picked, both are drawn as served, not as picked
      // (#614: they were drawn ticked, and the next Save made them picks)
      for (const id of ["glm-5.3-free", "glm-5.2"]) {
        assert.deepEqual(await cls(id), [false, true], id + " drawn as served");
        assert.ok((await chip(id).getAttribute("title")).includes(w.why), id + " title");
      }
      assert.equal(await chip("glm-5.2").evaluate((c) => getComputedStyle(c).borderTopStyle), "dashed");
      await editor.getByText(w.hint, { exact: false }).waitFor();
      assert.equal(await serves(), w.list);

      // a served one clicked is picked, alone (#681)
      await chip("glm-5.2").click();
      assert.deepEqual(await cls("glm-5.2"), [true, false]);
      assert.deepEqual(await cls("glm-5.3-free"), [false, false]);
      assert.equal(await editor.getByText(w.hint, { exact: false }).count(), 0);
      assert.equal(await serves(), w.picked);
      // and unpicked, it leaves none picked, which serves none, drawn as
      // Select none draws it (#1831: it went back to the vendor's list)
      await chip("glm-5.2").click();
      for (const id of ["glm-5.3-free", "glm-5.2"]) assert.deepEqual(await cls(id), [false, false], id + " after the last pick unpicked");
      await editor.getByText(w.none, { exact: false }).waitFor();
      assert.equal(await serves(), w.usage);
      // Vendor's list goes back to it
      await editor.locator(".mserves .opt[data-serves=list]").click();
      for (const id of ["glm-5.3-free", "glm-5.2"]) assert.deepEqual(await cls(id), [false, true], id + " vendor's list again");
      await editor.getByText(w.hint, { exact: false }).waitFor();
      assert.equal(await serves(), w.list);
      // Usage only is none, at a click (#2068)
      await editor.locator(".mserves .opt[data-serves=none]").click();
      for (const id of ["glm-5.3-free", "glm-5.2"]) assert.deepEqual(await cls(id), [false, false], id + " usage only");
      await editor.getByText(w.none, { exact: false }).waitFor();
      assert.equal(await serves(), w.usage);
      // Picked only from there picks nothing: the vendor's list served
      // ticking them all, which read as Select all and was saved as picks
      await editor.locator(".mserves .opt[data-serves=picked]").click();
      for (const id of ["glm-5.3-free", "glm-5.2"]) assert.deepEqual(await cls(id), [false, false], id + " not picked by Picked only");
      assert.equal(await serves(), w.picked);
      await editor.getByText(w.pickFirst, { exact: true }).first().waitFor();
      // and a Save is refused, rather than serving the vendor's list
      await editor.locator(".bar").getByRole("button", { name: w.save, exact: true }).click();
      await editor.locator(".editor-error", { hasText: w.pickFirst }).waitFor();
      assert.equal(saves.length, 0, "nothing saved with nothing picked");
      // Select none from there is Usage only
      await editor.getByRole("button", { name: w.selectNone, exact: true }).click();
      await editor.getByText(w.none, { exact: false }).waitFor();
      assert.equal(await serves(), w.usage);
      await chip("glm-5.2").click();
      // and another clicked joins it
      await chip("glm-5.3-free").click();
      assert.deepEqual(await cls("glm-5.2"), [true, false]);
      assert.deepEqual(await cls("glm-5.3-free"), [true, false]);

      // Select none: none drawn as served, the hint says so, and the Save
      // keeps it (#1818: saved as no picks, which serves the vendor's list)
      await editor.getByRole("button", { name: w.selectNone, exact: true }).click();
      for (const id of ["glm-5.3-free", "glm-5.2"]) assert.deepEqual(await cls(id), [false, false], id + " after Select none");
      await editor.getByText(w.none, { exact: false }).waitFor();
      assert.equal(await serves(), w.usage);
      await editor.locator(".bar").getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 60 && !saves.length; i++) await page.waitForTimeout(50);
      assert.equal(saves.length, 1, "saved");
      assert.equal(saves[0].noModels, true, "noModels sent");
      assert.deepEqual(saves[0].models || [], [], "no picks sent");
      await page.locator("#modal:not([hidden]) .editor").waitFor({ state: "detached" }).catch(() => {});

      // one saved so says so on its card, opens as none, and a pick then
      // clears it
      assert.ok((await page.locator(".row.provider", { hasText: "Watched" }).locator(".sub").innerText()).endsWith(w.card), "the card says usage only");
      await page.locator(".row.provider", { hasText: "Watched" }).click();
      await chip("glm-5.2").waitFor();
      assert.equal(await serves(), w.usage);
      for (const id of ["glm-5.3-free", "glm-5.2"]) assert.deepEqual(await cls(id), [false, false], id + " saved as none");
      await editor.getByText(w.none, { exact: false }).waitFor();
      await chip("glm-5.2").click();
      assert.deepEqual(await cls("glm-5.2"), [true, false]);
      assert.equal(await editor.getByText(w.none, { exact: false }).count(), 0);
      assert.deepEqual(errors, []);
    });

   }
    test(`${engine} ${lang}: kept for routing groups with none picked, the models the groups use are drawn as served`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, []));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Grouped" }).click();
      const editor = page.locator("#modal:not([hidden]) .editor");
      const chip = (id) => editor.locator(".mchips .mchip", { hasText: id });
      await chip("glm-5.2").waitFor();
      for (const id of ["glm-5.3-free", "glm-5.2"]) {
        assert.deepEqual(await chip(id).evaluate((c) => [c.classList.contains("on"), c.classList.contains("auto")]), [false, true], id + " drawn as served");
        assert.ok((await chip(id).getAttribute("title")).includes(w.groupsWhy), id + " title");
      }
      await editor.getByText(w.groupsHint, { exact: false }).waitFor();
      assert.deepEqual(errors, []);
    });
  }
}
