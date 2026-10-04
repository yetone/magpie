// Run with Node's test runner and Playwright on the module path; see README.md.
// Turning Claude Code's ultracode on folded every other agent away under
// "Show 17 more" (开启 ultracode 后为啥其他 agent 就不见了啊？): how an agent's
// own model is run — ultracode, an effort, a tier's effort — doesn't make it
// set up, so with nothing else set every agent stays in view. A model picked
// on one still folds the ones nothing is set on. Since #726 a connected
// agent is set up for being connected, so Claude Code connected folds the
// rest from the start, and turning ultracode on in its row moves nothing.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const opt = (v) => ({ value: v, label: v });
function agents({ ultracode = "", effort = "", tierEffort = "", subEffort = "", model = "", wired = true } = {}) {
  return [
    {
      id: "claude", name: "Claude Code", path: "/test/claude.json", icon: "claudecode-color", wired,
      fields: [
        { key: "model", label: "model", value: model, options: [{ ...opt("claude-sonnet-4.5"), ref: "claude/claude-sonnet-4.5" }] },
        { key: "effort", label: "effort", value: effort, options: [opt("high"), opt("xhigh")] },
        { key: "ultracode", label: "ultracode", value: ultracode, options: [opt("on")] },
        { key: "opus_effort", label: "opus effort", value: tierEffort, options: [opt("high")] },
      ],
    },
    {
      id: "codex", name: "Codex", path: "/test/codex.toml", icon: "codex-color",
      fields: [
        { key: "model", label: "model", value: "", options: [opt("gpt-5.5")] },
        { key: "effort", label: "effort", value: "", options: [opt("high")] },
        { key: "subagent_effort", label: "subagent effort", value: subEffort, options: [opt("high")] },
      ],
    },
    { id: "gemini", name: "Gemini CLI", path: "/test/gemini.json", icon: "gemini-color", fields: [{ key: "model", label: "model", value: "", options: [opt("gemini-3")] }] },
    { id: "opencode", name: "OpenCode", path: "/test/opencode.json", icon: "opencode", fields: [{ key: "model", label: "model", value: "", options: [opt("x")] }] },
  ];
}
const IDS = ["claude", "codex", "gemini", "opencode"];

function serve(first) {
  let state = { agents: agents(first), profiles: [], settings: { lang: "en", theme: "light" } };
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/set") {
      const { agent, field, value } = req.postDataJSON();
      assert.equal(agent, "claude");
      assert.equal(field, "ultracode");
      state = { ...state, agents: agents({ ...first, ultracode: value }) };
      return json(state);
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the arrangement app.js computes, and the rows the Agents page draws
async function arranged(page) {
  return page.evaluate(() => {
    const { shown, folded } = arrangeAgents();
    const ids = (list) => list.map((a) => a.id);
    const out = [...document.querySelectorAll(".row.agent[data-id]")].filter((r) => !r.closest(".agent-fold")).map((r) => r.dataset.id);
    return { shown: ids(shown), folded: ids(folded), drawn: out, more: !!document.querySelector(".agent-more") };
  });
}

async function open(t, engine, first) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width: 1000, height: 800 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(first));
  await page.goto("http://magpie.test/");
  await page.locator('.row.agent[data-id="claude"]').waitFor();
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: turning ultracode on moves no agent`, async (t) => {
    const { page, errors } = await open(t, engine, {});
    const before = await arranged(page);
    assert.deepEqual(before.shown, ["claude"], "Claude Code, connected, is in view");
    assert.deepEqual(before.folded, ["codex", "gemini", "opencode"]);
    // in its connected row, opened
    await page.locator('.row.agent[data-id="claude"] .ag-link').click();
    const toggle = page.locator('.row.agent[data-id="claude"] [data-key="ultracode"]');
    await toggle.click();
    await page.waitForFunction(() => document.querySelector('.row.agent[data-id="claude"] [data-key="ultracode"]')?.getAttribute("aria-pressed") === "true");
    assert.deepEqual(await arranged(page), before, "ultracode on moves nothing");
    assert.deepEqual(errors, []);
  });

  for (const [what, first] of [
    ["an effort", { effort: "high", wired: false }],
    ["a tier's effort", { tierEffort: "high", wired: false }],
    ["a subagent effort", { subEffort: "high", wired: false }],
    ["ultracode and an effort", { ultracode: "on", effort: "xhigh", wired: false }],
  ]) {
    test(`${engine}: ${what} set alone keeps every agent in view`, async (t) => {
      const { page, errors } = await open(t, engine, first);
      const r = await arranged(page);
      assert.deepEqual(r.folded, []);
      assert.deepEqual(r.drawn, IDS);
      assert.equal(r.more, false);
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine}: a model set on one folds the others`, async (t) => {
    const { page, errors } = await open(t, engine, { model: "claude-sonnet-4.5", ultracode: "on" });
    const r = await arranged(page);
    assert.deepEqual(r.shown, ["claude"]);
    assert.deepEqual(r.folded, ["codex", "gemini", "opencode"]);
    assert.deepEqual(r.drawn, ["claude"]);
    assert.equal(r.more, true, "the rest are under Show more");
    assert.deepEqual(errors, []);
  });
}
