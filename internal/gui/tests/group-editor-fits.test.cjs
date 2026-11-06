// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's editor fits its column (碳碳双键 on Discord, zh, a ~770px
// window): members with long names, their chips, own reasoning and fast
// mode pushed the Models rows past the editor's edge, where they were cut
// off, and the Patterns box below no longer lined up with them. Now each
// row stays inside the editor, the name giving way (ellipsized, whole on
// hover), every chip whole, and the pattern field spans the same column
// as the models. In both engines, at 1100, 770 and 420, desktop and web
// (which wraps a row on a phone), in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const X = { efforts: ["low", "medium", "high"], context: 262144, images: true, canFast: true };
const long = "nvidia/nemotron-3-super-120b-a12b:free";
const models = [
  { id: "or/a:free", name: long, providerName: "OpenRouter", icon: "generic", ...X },
  { id: "or/c:free", name: "qwen/qwen3-coder-480b-a35b-instruct:free", providerName: "OpenRouter", icon: "generic", ...X },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic", ...X },
];
const members = ["an/claude-opus-5-5", "or/a:free", "or/c:free"];
const groups = () => ({
  models, pools: [],
  groups: [{ id: "free", name: "Free", members, match: ["or/*:free"], matched: ["or/a:free", "or/c:free"], fast: [], off: [], routing: "order", ready: true,
    patterns: [{ pattern: "or/*:free", models: 2 }],
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

function serve(lang, web) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a group's editor keeps its models and patterns inside it`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const web of [false, true]) {
      for (const width of [1100, 770, 420]) {
        for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
          const at = `${web ? "web" : "desktop"} ${width} ${lang}`;
          const context = await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, web));
          await page.goto("http://magpie.test/?view=groups");
          const card = page.locator(".rt-group", { hasText: "Free" });
          await card.waitFor();
          await card.locator("button.text").first().click();
          await page.locator(".rt-gedit .rt-pats input").waitFor();
          const got = await page.evaluate(() => {
            const box = (e) => e.getBoundingClientRect();
            const ed = document.querySelector(".rt-gedit"), er = box(ed);
            const members = ed.querySelector(".fallback:not(.rt-pats):not(.rt-rules)");
            const pats = ed.querySelector(".rt-pats");
            const out = [];
            for (const e of [...members.querySelectorAll("*"), ...pats.querySelectorAll("*")]) {
              const r = box(e);
              if (r.width && (r.right > er.right + 0.5 || r.left < er.left - 0.5)) out.push(`${e.className || e.tagName} ${Math.round(r.left)}–${Math.round(r.right)}`);
            }
            // a chip, button or switch is never cut: only the name and
            // its note give way
            const cut = [...members.querySelectorAll(".fbrow > :not(.n):not(.grow)")].filter((e) => e.scrollWidth > e.clientWidth + 1 && getComputedStyle(e).overflow !== "visible").map((e) => e.className);
            const name = [...members.querySelectorAll(".fbrow .n > span")].map((e) => e.title);
            return {
              out, cut, name,
              page: document.documentElement.scrollWidth - document.documentElement.clientWidth,
              cols: [members, pats.querySelector(".rt-patadd")].map((e) => [Math.round(box(e).left), Math.round(box(e).right)]),
            };
          });
          assert.deepEqual(got.out, [], `${at}: nothing past the editor's edge`);
          assert.deepEqual(got.cut, [], `${at}: no chip cut`);
          assert.equal(got.page, 0, `${at}: the page doesn't scroll sideways`);
          assert.deepEqual(got.cols[1], got.cols[0], `${at}: the pattern field spans the models' column`);
          assert(got.name.includes(long), `${at}: the whole name on hover`);
          assert.deepEqual(errors, [], at);
          await context.close();
        }
      }
    }
  });
}
