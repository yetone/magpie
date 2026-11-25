// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's classifier may be told the group's own instructions in place of
// magpie's (sloceanxc on X: magpie's misled theirs). The field sits under
// the editor's Advanced, folded unless it is set, and shows only once a rule
// has an intent, as the classifier is asked only then. What is typed is
// saved trimmed; every save the page makes of the group carries it back (the
// editor's Save and a pick in the card's routing menu); a group without one
// saves "". In every language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic" },
  { id: "rl/glm-5", name: "glm-5", providerName: "Relay", icon: "generic" },
];
const members = ["oa/gpt-6.1-sol", "rl/glm-5"];
const own = "The user writes fiction. Continuing a scene is a story, even when it names code.";
const group = (id, name, extra) => ({ id, name, members, routing: "order", ready: true, classifier: "oa/gpt-6.1-sol",
  rules: [{ intent: "a story", to: "oa/gpt-6.1-sol" }], memberInfo: members.map((m) => ({ id: m, ready: true })), ...extra });
const groups = () => ({ models, pools: [], groups: [
  group("own", "Own", { classifyPrompt: own }),
  group("plain", "Plain", { routing: "" }), // nothing else under Advanced set, so it is folded
  group("bare", "Bare", { rules: [], classifier: "" }),
] });

const words = {
  en: { edit: "Edit", save: "Save", label: "Classifier prompt" },
  zh: { edit: "编辑", save: "保存", label: "意图判断提示词" },
  "zh-TW": { edit: "編輯", save: "儲存", label: "意圖判斷提示詞" },
  ja: { edit: "編集", save: "保存", label: "意図判定プロンプト" },
  de: { edit: "Bearbeiten", save: "Speichern", label: "Klassifikator-Prompt" },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: a group's classifier prompt is under Advanced and saved`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 2000 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const last = () => posts.filter((p) => p.path === "/api/groups/save").at(-1)?.body;
      const ed = page.locator(".rt-gedit");
      const field = ed.locator("textarea.rt-cls-prompt");
      const open = async (id) => {
        await page.locator(`.rt-group[data-id="${id}"]`).locator("button", { hasText: w.edit }).click();
        await ed.waitFor();
      };
      const save = async () => {
        await ed.locator("button.primary", { hasText: w.save }).click();
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        return last();
      };
      const pick = async (id) => {
        const n = posts.length;
        await page.locator(`.rt-group[data-id="${id}"] button.rt-groute`).click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        await menu.locator('.pm-item[aria-checked="false"]').first().click();
        for (let i = 0; i < 100 && posts.length === n; i++) await page.waitForTimeout(20); // the post is the state waited for
        return last();
      };
      const advOpen = () => ed.locator("details.rt-gadv").evaluate((d) => d.open);

      await page.locator('.rt-group[data-id="own"]').waitFor();

      // set: Advanced opens on it, the field shows it under its label
      await open("own");
      assert.equal(await advOpen(), true, "Advanced is open when a prompt is set");
      await field.waitFor();
      assert.equal(await field.inputValue(), own);
      assert.equal(await ed.locator("details.rt-gadv label", { hasText: w.label }).count(), 1, "labelled in " + lang);
      assert.equal((await save()).classifyPrompt, own, "the editor's Save keeps it");

      // unset: folded away, saved as "", and what is typed is saved trimmed
      await open("plain");
      assert.equal(await advOpen(), false, "Advanced stays folded without one");
      assert.equal((await save()).classifyPrompt, "", "a group without one saves none");
      await open("plain");
      await ed.locator("details.rt-gadv > summary").click();
      await field.waitFor();
      await field.fill("  Kinds are about tone.\n");
      assert.equal((await save()).classifyPrompt, "Kinds are about tone.", "typed, saved trimmed");

      // no intent rule: no classifier asked, so no field
      await open("bare");
      await ed.locator("details.rt-gadv > summary").click();
      assert.equal(await field.isVisible(), false, "hidden without an intent rule");
      await save();

      const picked = await pick("own");
      assert.notEqual(picked.routing, "order", "the menu's pick was saved");
      assert.equal(picked.classifyPrompt, own, "a routing pick keeps it");
      assert.deepEqual(errors, []);
    });
  }
}
