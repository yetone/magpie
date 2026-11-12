// Run with Node's test runner and Playwright on the module path; see README.md.
// A state sends each fields' option list once (shareOptions, statewrite.go):
// with real accounts 13 agents' model pickers offered the same 51KB
// catalog, 650KB of a 690KB /api/state. A field names its list by List,
// from 1, and api() puts the list back before anything reads the state.
// Two agents' model pickers sharing one list each show it whole, a field
// with its own options keeps them, and the state kept from the last read
// (?last=1), shared the same way, draws too. A fastFor sent as "*" is
// the agent's own id again in each agent's list. Chromium and WebKit, wide
// and narrow, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const catalog = [
  { value: "magpie/codex/gpt-5.5", ref: "codex/gpt-5.5", label: "GPT-5.5", note: "", group: "ChatGPT" },
  { value: "magpie/zai/glm-5", ref: "zai/glm-5", label: "GLM-5", note: "", group: "Z.ai" },
  { value: "magpie/kimi/k2", ref: "kimi/k2", label: "Kimi K2", note: "", group: "Kimi" },
  // sent fast for the agent whose list it is: "*" on the wire
  { value: "magpie/codex/gpt-5.5-fast", ref: "codex/gpt-5.5-fast", label: "GPT-5.5 Fast", note: "", group: "ChatGPT", fastFor: "*" },
];
const crushOwn = { value: "crush/own", label: "Crush Own", note: "" };
const effort = [{ value: "low", note: "" }, { value: "high", note: "" }];

function sharedState(lang, last) {
  return {
    agents: [
      { id: "codex", name: "Codex", path: "/test/codex", wired: true, fields: [
        { key: "model", label: "model", value: "magpie/zai/glm-5", options: null, list: 1 },
        { key: "effort", label: "effort", value: "high", options: effort },
      ] },
      { id: "crush", name: "Crush", path: "/test/crush", wired: true, fields: [
        { key: "model", label: "model", value: "magpie/kimi/k2", options: null, list: 2 },
      ] },
    ],
    profiles: [], settings: { lang, theme: "light" }, served: 3,
    // each option once, a list its indexes: crush's has one of its own first
    options: [...catalog, crushOwn],
    optionLists: [catalog.map((_, i) => i), [catalog.length, ...catalog.map((_, i) => i)]],
    ...(last ? { last: true } : {}),
  };
}

function serve(lang, fresh) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") {
      if (url.searchParams.get("last") === "1") return json(sharedState(lang, true));
      await fresh; // the kept state draws first
      return json(sharedState(lang, false));
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const pop = "#pop:not([hidden]) #list li";
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const cases = [...LANGS.map((lang) => [lang, 1000]), ["en", 420], ["zh", 420]];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": fields sharing one option list each show it whole", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, width] of cases) {
      await t.test(`${lang} ${width}px`, async () => {
        const errors = [];
        let release;
        const fresh = new Promise((r) => { release = r; });
        const page = await (await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, fresh));
        await page.goto("http://magpie.test/?view=agents");
        // drawn from the kept state, before the fresh one comes
        await page.locator('.row.agent[data-id="crush"]').waitFor();
        const read = () => page.evaluate(() => state.agents.map((a) => [a.id, a.fields.map((f) => [f.key, (f.options || []).map((o) => o.value + (o.fastFor ? " " + o.fastFor : ""))])]));
        const own = (id) => catalog.map((o) => o.value + (o.fastFor ? " " + id : ""));
        const want = [
          ["codex", [["model", own("codex")], ["effort", ["low", "high"]]]],
          ["crush", [["model", ["crush/own", ...own("crush")]]]],
        ];
        assert.deepEqual(await read(), want, "the kept state's lists put back");
        release();
        await page.waitForFunction(() => !state.last);
        assert.deepEqual(await read(), want, "the fresh state's lists put back");
        for (const id of ["codex", "crush"]) {
          const field = page.locator(`.row.agent[data-id="${id}"] .field[data-key="model"]`).first();
          if (!(await field.isVisible())) await page.locator(`.row.agent[data-id="${id}"] .ag-sum`).click();
          await field.click();
          await page.locator(pop).first().waitFor();
          for (const o of catalog) assert.equal(await page.locator(pop).filter({ hasText: o.label }).count() > 0, true, `${id}'s picker lists ${o.label}`);
          await page.keyboard.press("Escape");
          await page.locator("#pop").waitFor({ state: "hidden" });
        }
        // fast for one agent is fast for that agent only, though the list is shared
        const fast = await page.evaluate(() => {
          const opt = (id) => state.agents.find((a) => a.id === id).fields[0].options.find((o) => o.fastFor);
          setFast(opt("codex"), true);
          return [opt("codex").fast, !!opt("crush").fast];
        });
        assert.deepEqual(fast, [true, false], "codex's fast leaves crush's alone");
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
