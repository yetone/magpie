// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group whose every member is busy at once — overloaded,
// unavailable, rate limited for a moment — asks them again within a budget
// rather than handing the agent the last one's error (#1418, Aliceonly).
// The group editor's "Every member busy" row picks Off, 1 min (the
// default, saved as 0), 2 min or 4 min, its hint saying what each does;
// saved, the group is sent with patience in seconds, -1 for off. In
// English and Chinese, Chromium and WebKit; every string has its Chinese,
// Traditional Chinese, Japanese and German.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "plus-b/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "Plus B", icon: "generic" },
  { id: "sub2api/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "sub2api", icon: "generic" },
];
const members = models.map((m) => m.id);
const saved = { patience: 0 };
const groups = () => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, routing: "order", ready: true, patience: saved.patience,
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", label: "Every member busy", off: "Off", def: "1 min", four: "4 min", on: "each is asked again after a pause", offHint: "gets the last one's error at once" },
  zh: { edit: "编辑", save: "保存", label: "成员全都忙", off: "关闭", def: "1 分钟", four: "4 分钟", on: "依次再问一遍每个成员", offHint: "立刻拿到最后一个的错误" },
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
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push({ path: url.pathname, body });
      saved.patience = body.patience || 0;
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
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a group waits out every member busy`, async (t) => {
      saved.patience = 0;
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 2000 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-patience.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group", { hasText: "Sol" });
      await card.waitFor();

      const save = async () => {
        const ed = page.locator(".rt-gedit");
        await ed.locator("button.primary", { hasText: w.save }).click();
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        return posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
      };
      const open = async () => {
        await card.locator("button", { hasText: w.edit }).click();
        const row = page.locator(".rt-gedit label", { hasText: w.label }).locator("xpath=following-sibling::div[1]");
        await row.waitFor();
        return row;
      };
      let row = await open();
      assert.equal(await row.locator(".opt.on").textContent(), w.def, "1 min by default");
      assert((await row.locator(".hint").textContent()).includes(w.on));
      await row.locator(".opt", { hasText: w.four }).click();
      assert((await row.locator(".hint").textContent()).includes(lang === "zh" ? "4 分" : "4 min"));
      assert.equal((await save()).patience, 240);

      row = await open();
      assert.equal(await row.locator(".opt.on").textContent(), w.four, "the editor opens at what was saved");
      await row.locator(".opt", { hasText: w.off }).click();
      assert((await row.locator(".hint").textContent()).includes(w.offHint));
      assert.equal((await save()).patience, -1);

      row = await open();
      assert.equal(await row.locator(".opt.on").textContent(), w.off);
      await row.locator(".opt", { hasText: w.def }).click();
      assert.equal((await save()).patience, 0, "the default is saved as none set");

      const missing = await page.evaluate(() => {
        const keys = [
          "Off", "1 min", "2 min", "4 min", "Every member busy",
          "When every member has failed, the agent gets the last one's error at once.",
          "When every member has failed with an error that may pass — overloaded, unavailable, rate limited for a moment — and nothing of the reply has reached the agent, each is asked again after a pause, longer each time and never shorter than the vendor asks, for up to {n} more. A streaming agent is kept waiting meanwhile.",
          "{who} answered {status} · {fail}, and every member had failed with an error that may pass, so after {d} the {n} of them are asked again, in turn, before any of the reply reaches {agent}. The group waits up to {p} for one to answer.",
        ];
        return ["zh", "zh-TW", "ja", "de"].flatMap((l) => keys.filter((k) => !I18N[l][k]).map((k) => l + ": " + k));
      });
      assert.deepEqual(missing, [], "every string has its translations");

      // "an error that may pass" is the plain term 暂时性错误 / 暫時性錯誤
      // in Chinese, not a literal 会过去的错误 (Outvi V on Discord).
      const unplain = await page.evaluate(() => {
        const keys = [
          "When every member has failed with an error that may pass — overloaded, unavailable, rate limited for a moment — and nothing of the reply has reached the agent, each is asked again after a pause, longer each time and never shorter than the vendor asks, for up to {n} more. A streaming agent is kept waiting meanwhile.",
          "{who} answered {status} · {fail}, and every member had failed with an error that may pass, so after {d} the {n} of them are asked again, in turn, before any of the reply reaches {agent}. The group waits up to {p} for one to answer.",
          "{who} answered {status} · {fail}, and nobody else is left to ask — a failure that may pass, so it is tried again in {d}, before any of the reply reaches {agent}.",
        ];
        const term = { zh: "暂时性错误", "zh-TW": "暫時性錯誤" };
        return Object.entries(term).flatMap(([l, w]) => keys.filter((k) => !(I18N[l][k] || "").includes(w)).map((k) => l + ": " + I18N[l][k]));
      });
      assert.deepEqual(unplain, [], "a passing error is named 暂时性错误 in Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
