// Run with Node's test runner and Playwright on the module path; see README.md.
// #2447 (EdwinDigital): in the tray panel, WorkBuddy's row said its two
// models in words ("gpt-6.1-sol · Microsoft Foundry, claude-opus-5-5 ·
// Microsoft Foundry +1"), and those words took the row's whole width: the
// name was squeezed to 10px at 550px, to nothing at 440px and below, and to
// nothing at any width once the pointer was on the row and the model's logo
// came in. The name keeps its width now and the words cut first, on every
// row with a summary, hovered or not, in every language. No backend: the API
// is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const magpie = [{ value: "magpie", label: "magpie", icon: "magpie" }];
const LONG = "claude-opus-5-5 · Microsoft Foundry";
// A list's words name its models only while one or two are ticked; more read
// "Showing x / N models", all of them "All N models" (menuSaid, 6915c8cde).
// So each list here has two of its models ticked, to be said by name.
const AGENTS = [
  // the reporter's row: their two models, as menuSaid words them now
  { id: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", path: "/fixture/models.json", wired: true, menu: true,
    models: { shown: 2, listed: 3, names: ["gpt-6.1-sol · Microsoft Foundry", LONG] },
    fields: [{ key: "provider", label: "provider", value: "magpie", options: magpie }] },
  // another agent whose own menu lists the models, a longer name
  { id: "claude-desktop", name: "Claude Desktop", path: "/fixture/claude-desktop.json", wired: true, menu: true,
    models: { shown: 2, listed: 9, names: [LONG, "gpt-6.1-sol · Microsoft Foundry"] },
    fields: [{ key: "provider", label: "provider", value: "magpie", options: magpie }] },
  // one model, said by a long label, with its logo and effort
  { id: "codex", name: "Codex", path: "~/.codex/config.toml",
    fields: [{ key: "model", label: "model", value: "m", options: [{ value: "m", label: LONG, icon: "magpie" }] },
      { key: "effort", label: "effort", value: "high", options: ["low", "high"].map((value) => ({ value })) }] },
  // and a short one beside them
  { id: "crush", name: "Crush", path: "~/.crush/crush.json", fields: [{ key: "model", label: "model", value: "a", options: [{ value: "a", label: "a" }] }] },
];
const IDS = AGENTS.map((a) => a.id);

function server(lang) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: AGENTS, profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/agents/cli" && req.method() === "GET") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/agents/install") return route.fulfill({ json: [] });
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
const noSideScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);
// each row's name, words and edges, as drawn
const measure = (page) => page.evaluate((ids) => Object.fromEntries(ids.map((id) => {
  const r = document.querySelector(`.row.agent[data-id="${id}"]`);
  const name = r.querySelector(".who .name"), sum = r.querySelector(":scope > .ag-sum"), vt = sum.querySelector(".vt");
  const rb = r.getBoundingClientRect(), sb = sum.getBoundingClientRect(), pad = parseFloat(getComputedStyle(r).paddingRight);
  return [id, {
    name: name.getBoundingClientRect().width, nameFull: name.scrollWidth, nameCut: name.scrollWidth > name.clientWidth + 1,
    wordsCut: vt.scrollWidth > vt.clientWidth + 1, sumRight: sb.right, sumLeft: sb.left,
    rowInner: rb.right - pad, nameRight: name.getBoundingClientRect().right,
  }];
})), IDS);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a panel row's name keeps its width beside a long model summary", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [550, 440, 360]) {
        await t.test(`${lang} ${width}`, async () => {
          const page = await (await browser.newContext({ viewport: { width, height: 640 } })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang));
          await page.goto("http://magpie.test/?mode=panel");
          for (const id of IDS) await page.locator(`${row(id)} .ag-sum`).waitFor();
          for (const hovered of [false, true]) {
            if (hovered) {
              await page.locator(`${row("workbuddy")} .name`).hover();
              // the logo eased all the way in
              await page.locator(`${row("workbuddy")} .ag-sum .mi`).evaluate((mi) => new Promise((done) => {
                const check = () => (mi.getBoundingClientRect().width > 4 && !mi.getAnimations().length ? done() : requestAnimationFrame(check));
                check();
              }));
            }
            const m = await measure(page);
            const at = `${lang} ${width}${hovered ? " hovered" : ""}`;
            if (process.env.WIDTHS) console.log(engine, at, JSON.stringify(Object.fromEntries(IDS.map((id) => [id, +m[id].name.toFixed(2)]))));
            for (const id of IDS) {
              // the whole name, never cut nor squeezed
              assert(!m[id].nameCut, `${at}: ${id}'s name cut to ${m[id].name}px of ${m[id].nameFull}`);
              assert(m[id].name >= m[id].nameFull - 1, `${at}: ${id}'s name ${m[id].name}px of ${m[id].nameFull}`);
              // the words after it, inside the row
              assert(m[id].sumLeft >= m[id].nameRight, `${at}: ${id}'s words over its name`);
              assert(m[id].sumRight <= m[id].rowInner + 1, `${at}: ${id}'s words past the row`);
              // and their right edges in one column down the list
              assert(Math.abs(m[id].sumRight - m.crush.sumRight) < 1, `${at}: ${id}'s words out of the column`);
            }
            // the model lists cut, not the name; one model's label only where
            // it doesn't fit
            for (const id of ["workbuddy", "claude-desktop"]) assert(m[id].wordsCut, `${at}: ${id}'s words not cut`);
            if (width === 360) assert(m.codex.wordsCut, `${at}: codex's words not cut`);
            assert(!m.crush.wordsCut, `${at}: a short model cut`);
            assert(await noSideScroll(page), `${at}: the panel scrolls sideways`);
          }
          await page.screenshot({ path: path.join(process.env.SHOTS || "/tmp", `panel-name-width-${engine}-${lang}-${width}.png`) }).catch(() => {});
          await page.context().close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
