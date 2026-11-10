// Run with Node's test runner and Playwright on the module path; see README.md.
// omp's roles share one square after the model picker, as Claude Code's
// tiers do (#325, #1397): subagents, smol, slow, plan, vision, advisor,
// commit, tiny and memory (every chat role omp 18.8 has), each one line
// with the model it is on, "· level" after it once a level is set
// (KKKKKKKEM: the slow role at its own level), or what it takes unset.
// Picking a role opens the model picker for it, "Same as model" first for
// a role that follows the model; a model picked, the level slider comes
// next and writes that role's level (hongzaitou on X: no line of its own
// for the level). OpenCode's small model,
// which doesn't follow the model, stays a picker; a named profile of omp
// (omp#work) has the same square. In the connected agent's
// opened row, in English, both Chinese, Japanese and German, wide and at 390px.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [
  { value: "magpie/deepseek/pro", label: "DeepSeek Pro", ref: "deepseek/pro" },
  { value: "magpie/deepseek/flash", label: "DeepSeek Flash", ref: "deepseek/flash" },
];
const LEVELS = ["auto", "off", "minimal", "low", "medium", "high", "xhigh", "max"].map((value) => ({ value }));
const field = (key, label, value) => ({ key, label, value, options });
// a role's level is offered while the role is on a model, as omp.go does
const level = (key, label, on) => ({ key: key + "_thinking", label: label + " thinking", value: "", options: on ? LEVELS : [] });
const fresh = () => ({
  agents: [
    {
      id: "omp", name: "omp", path: "/test/config.yml", icon: "omp", wired: true,
      fields: [
        field("model", "model", "magpie/deepseek/pro"),
        field("subagent", "subagents", ""), level("subagent", "subagents", false),
        field("small", "smol", ""), level("small", "smol", false),
        field("slow", "slow", "magpie/deepseek/flash"), level("slow", "slow", true),
        field("plan", "plan", ""), level("plan", "plan", false),
        field("vision", "vision", ""), level("vision", "vision", false),
        field("advisor", "advisor", ""), level("advisor", "advisor", false),
        field("commit", "commit", ""), level("commit", "commit", false),
        field("tiny", "tiny", ""), level("tiny", "tiny", false),
        field("memory", "memory", ""), level("memory", "memory", false),
        { key: "effort", label: "thinking", value: "", options: LEVELS.filter((l) => l.value !== "off") },
      ],
    },
    // a named profile of omp (#1233) has the same roles
    { id: "omp#work", name: "omp · work", path: "/test/profiles/work/config.yml", icon: "omp", wired: true,
      fields: [field("model", "model", "magpie/deepseek/pro"), field("slow", "slow", "magpie/deepseek/flash"), level("slow", "slow", true), field("plan", "plan", ""), level("plan", "plan", false)] },
    { id: "opencode", name: "OpenCode", path: "/test/opencode.json", wired: true, fields: [field("model", "model", "magpie/deepseek/pro"), field("small", "small", "")] },
  ],
  profiles: [],
});

function server(lang, sets) {
  let cur = fresh();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push({ agent: body.agent, field: body.field, value: body.value });
      cur = JSON.parse(JSON.stringify(cur));
      const fields = cur.agents.find((a) => a.id === body.agent).fields;
      fields.find((f) => f.key === body.field).value = body.value;
      const lv = fields.find((f) => f.key === body.field + "_thinking");
      if (lv) lv.options = body.value ? LEVELS : [];
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the words as i18n.js has them
const W = {
  en: {
    roles: ["subagents", "smol", "slow", "plan", "vision", "advisor", "commit", "tiny", "memory"],
    follows: "same as model (DeepSeek Pro)", own: "omp's own pick", advisor: "the slow role's model, else omp's own pick",
    smol: "the smol role's model", memory: "the tiny role's model, else the smol role's",
    level: "slow thinking", smolLevel: "smol thinking", same: "Same as model", summary: "roles: slow", high: "high", summarySet: "roles: slow (high)",
  },
  zh: {
    roles: ["子代理", "小模型", "慢模型", "规划", "视觉", "顾问", "提交", "微模型", "记忆"],
    follows: "同主模型（DeepSeek Pro）", own: "由 omp 自己挑选", advisor: "慢模型角色的模型，未设置时由 omp 自己挑选",
    smol: "小模型角色的模型", memory: "微模型角色的模型，未设置时用小模型角色的",
    level: "慢模型思考", smolLevel: "小模型思考", same: "同主模型", summary: "角色：慢模型", high: "高", summarySet: "角色：慢模型 (高)",
  },
  "zh-TW": {
    summary: "角色：慢模型",
    roles: ["子代理", "小模型", "慢模型", "規劃", "視覺", "顧問", "提交", "微模型", "記憶"],
    own: "由 omp 自己挑選", advisor: "慢模型角色的模型，未設定時由 omp 自己挑選",
    smol: "小模型角色的模型", memory: "微模型角色的模型，未設定時用小模型角色的",
  },
  ja: {
    summary: "ロール：低速",
    roles: ["サブエージェント", "小", "低速", "プラン", "ビジョン", "アドバイザー", "コミット", "極小", "メモリー"],
    own: "omp が自分で選ぶ", advisor: "slow ロールのモデル、未設定なら omp が自分で選ぶ",
    smol: "smol ロールのモデル", memory: "tiny ロールのモデル、未設定なら smol ロールのモデル",
  },
  de: {
    summary: "Rollen: langsam",
    roles: ["Subagenten", "klein (smol)", "langsam", "Planung", "Vision", "Berater", "Commit", "winzig (tiny)", "Gedächtnis"],
    own: "wählt omp selbst", advisor: "das Modell der Rolle slow, sonst wählt omp selbst",
    smol: "das Modell der Rolle smol", memory: "das Modell der Rolle tiny, sonst das der Rolle smol",
  },
};

const omp = '.row.agent[data-id="omp"]';
const items = (page) => page.locator("#pop:not([hidden]) #list li").evaluateAll((es) => es.map((e) => [e.querySelector(".v")?.textContent, e.querySelector(".n")?.textContent || ""]));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 390]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang} ${width}px: omp's roles and their thinking levels are one square, OpenCode's small model is a picker`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width, height: 760 } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], sets = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, sets));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-omp-roles.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/");
      await page.locator(omp).waitFor();
      await page.locator(`${omp} .ag-link`).click();

      // one square for the roles; the model and the session's thinking are the pickers
      const square = page.locator(`${omp} .extras-cell .field.extra[data-key="tiers"]`);
      assert.equal(await square.count(), 1, "no roles square");
      assert.equal(await page.locator(`${omp} .field.extra`).count(), 1, "a role kept a square of its own");
      const pickers = await page.locator(`${omp} .field:not(.extra)`).evaluateAll((es) => es.map((e) => e.dataset.key));
      assert.ok(pickers.includes("model") && pickers.every((k) => k === "model" || k === "effort"), String(pickers));
      assert.ok((await square.getAttribute("aria-label")).startsWith(w.summary), await square.getAttribute("aria-label"));
      assert.equal(await square.evaluate((e) => e.classList.contains("set")), true);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the row scrolls sideways");
      const y = await page.evaluate(() => scrollY);

      // each role one line, with its model or what it takes unset
      await square.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      const rows = await items(page);
      assert.deepEqual(rows.map((r) => r[0]), w.roles);
      assert.deepEqual(rows.slice(3).map((r) => r[1]), [w.own, w.own, w.advisor, w.smol, w.smol, w.memory]);
      // commit's picker: Default says what omp takes unset, the smol
      // role's model, and no "Same as model"
      const commitPicker = async () => {
        if (!(await page.locator("#pop:not([hidden]) #list li").count())) await square.click();
        const at = (await items(page)).findIndex((r) => r[0] === w.roles[6]);
        await page.locator("#pop:not([hidden]) #list li").nth(at).click();
        await page.locator("#pop:not([hidden]) #list li", { hasText: "DeepSeek Flash" }).first().waitFor();
        const top = await page.locator("#pop:not([hidden]) #list li").first().innerText();
        assert.ok(top.includes(w.smol), top);
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => !document.querySelector("#pop:not([hidden])"));
      };
      // ja and de check the new roles' words; the rest of the walk is en's and zh's
      if (!w.follows) {
        await commitPicker();
        assert.deepEqual(sets, []);
        assert.deepEqual(errors, []);
        return;
      }
      assert.deepEqual(rows.slice(0, 3).map((r) => r[1]), [w.follows, w.follows, "DeepSeek Flash"]);
      assert.equal(await page.locator("#pop:not([hidden]) #effortControl:not([hidden])").count(), 0);

      // the level slider, opened as the model's next step: it writes the role's level
      const levelStep = async (title, value) => {
        await page.waitForFunction(() => document.querySelector("#effortRange")?.offsetParent);
        assert.equal(await page.locator("#effortTitle").textContent(), title);
        const at = await page.evaluate(() => Number(document.querySelector("#effortRange").max));
        const i = LEVELS.findIndex((l) => l.value === value) + (at === LEVELS.length ? 1 : 0);
        await page.locator("#effortRange").evaluate((r, i) => { r.value = String(i); r.dispatchEvent(new Event("input", { bubbles: true })); r.dispatchEvent(new Event("change", { bubbles: true })); }, i);
      };

      // slow: its model picked again (no write), then its level
      await page.locator("#pop:not([hidden]) #list li").nth(2).click();
      await page.locator("#pop:not([hidden]) #list li", { hasText: "DeepSeek Flash" }).first().click();
      await levelStep(w.level, "high");
      await page.waitForFunction(() => document.querySelector("#status")?.textContent);
      assert.deepEqual(sets, [{ agent: "omp", field: "slow_thinking", value: "high" }]);
      await page.keyboard.press("Escape");
      await page.waitForFunction((s) => document.querySelector('.row.agent[data-id="omp"] .field.extra[data-key="tiers"]')?.getAttribute("aria-label").startsWith(s), w.summarySet);
      // and its line says both
      await square.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      assert.deepEqual((await items(page))[2], [w.roles[2], "DeepSeek Flash · " + w.high]);
      assert.equal((await items(page)).length, w.roles.length, "a level kept a line of its own");

      // smol: the model picker, following the model first; a model picked, its level next
      await page.locator("#pop:not([hidden]) #list li").nth(1).click();
      await page.locator("#pop:not([hidden]) #list li", { hasText: "DeepSeek Flash" }).first().waitFor();
      assert.match(await page.locator("#pop:not([hidden]) #list li").first().innerText(), new RegExp(`${w.same}[\\s\\S]*DeepSeek Pro`));
      await page.locator("#pop:not([hidden]) #list li", { hasText: "DeepSeek Flash" }).first().click();
      await levelStep(w.smolLevel, "low");
      for (let i = 0; sets.length < 3 && i < 100; i++) await page.waitForTimeout(50);
      await page.keyboard.press("Escape");
      assert.deepEqual(sets.slice(1), [{ agent: "omp", field: "small", value: "magpie/deepseek/flash" }, { agent: "omp", field: "small_thinking", value: "low" }]);

      // Same as model: nothing to pick a level for, so no level step
      await square.click();
      await page.locator("#pop:not([hidden]) #list li").nth(1).click();
      await page.locator("#pop:not([hidden]) #list li", { hasText: w.same }).first().click();
      await page.waitForFunction((same) => document.querySelector("#status")?.textContent.includes(same), w.same);
      await page.evaluate(() => new Promise(requestAnimationFrame));
      assert.deepEqual(sets[3], { agent: "omp", field: "small", value: "" });
      assert.equal(await page.locator("#pop:not([hidden])").count(), 0, "a level step for a role on no model of its own");

      // plan: no "Same as model", omp picks unset
      await square.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      const planAt = (await items(page)).findIndex((r) => r[0] === w.roles[3]);
      await page.locator("#pop:not([hidden]) #list li").nth(planAt).click();
      await page.locator("#pop:not([hidden]) #list li", { hasText: "DeepSeek Flash" }).first().waitFor();
      const first = await page.locator("#pop:not([hidden]) #list li").first().innerText();
      assert.doesNotMatch(first, new RegExp(w.same));
      assert.match(first, new RegExp(w.own));
      await page.keyboard.press("Escape");
      assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");

      await commitPicker();

      // omp's named profile: its roles share the square as well
      const work = '.row.agent[data-id="omp#work"]';
      await page.locator(`${work} .ag-link`).click();
      const workSquare = page.locator(`${work} .extras-cell .field.extra[data-key="tiers"]`);
      await workSquare.waitFor();
      assert.ok((await workSquare.getAttribute("aria-label")).startsWith(w.summary), await workSquare.getAttribute("aria-label"));
      assert.equal(await page.locator(`${work} .field[data-key="plan"]`).count(), 0, "a profile's role kept a picker of its own");

      // OpenCode's small model doesn't follow the model: a picker, not a square
      const oc = '.row.agent[data-id="opencode"]';
      await page.locator(`${oc} .ag-link`).click();
      await page.locator(`${oc} .ag-exp`).waitFor();
      assert.equal(await page.locator(`${oc} .field.extra`).count(), 0);
      assert.equal(await page.locator(`${oc} .field[data-key="small"]`).count(), 1);
      assert.deepEqual(errors, []);
    });
  }
}
