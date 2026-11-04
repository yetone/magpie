// Run with Node's test runner and Playwright on the module path; see README.md.
// A Zhipu key on a team's GLM Coding Plan (#236): the editor of a Zhipu or
// Z.ai key provider, saved or being added, has Team org ID and Team
// project ID, which Save posts trimmed as zhipuTeam, {} when both are
// empty; one without the other is refused before anything is posted.
// Other providers have neither and post none. A click in them leaves the
// page where it is. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const zhipu = {
  id: "zhipu", name: "Zhipu GLM", icon: "zhipu-color", preset: "zhipu", chat: "https://open.bigmodel.cn/api/coding/paas/v4", responses: "", anthropic: "", catalog: "zhipuai",
  models: [{ id: "glm-5", name: "GLM-5", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "…ab12" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  zhipuTeam: { org: "org-0", project: "proj-0" },
};
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const presets = [
  { id: "zhipu", name: "Zhipu GLM", icon: "zhipu-color", kind: "vendor", chat: "https://open.bigmodel.cn/api/paas/v4", added: true, zhipuTeam: true,
    regionLabel: "Plan", regions: [{ id: "api", name: "Pay as you go", chat: "https://open.bigmodel.cn/api/paas/v4" }, { id: "coding", name: "Coding Plan", chat: "https://open.bigmodel.cn/api/coding/paas/v4" }] },
  { id: "zai", name: "Z.ai", icon: "zai", kind: "vendor", chat: "https://api.z.ai/api/paas/v4", added: false, zhipuTeam: true },
  { id: "deepseek", name: "DeepSeek", icon: "deepseek", kind: "vendor", chat: "https://api.deepseek.com/v1", added: false },
];

function serve(lang, posts) {
  const providers = { providers: [zhipu, relay], presets, excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { org: "Team org ID", project: "Team project ID", save: "Save", add: "Add", cancel: "Cancel", one: "Team plan: give both the organization ID and the project ID", console: "Zhipu GLM's console" },
  zh: { org: "团队组织 ID", project: "团队项目 ID", save: "保存", add: "添加", cancel: "取消", one: "团队套餐：请同时填写组织 ID 和项目 ID", console: "Zhipu GLM 控制台" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const start = async (t, name) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-zhipu-team.png`) });
        }
        await browser.close();
      });
      // short, so the editor scrolls and a click that moved it would show
      const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      return { page, errors, posts };
    };
    const edit = async (page, name) => {
      await page.locator(".row.provider", { hasText: name }).click();
      // drawn: the proxy is under Advanced, folded
      await page.locator(".editor .proxy-mode").waitFor({ state: "attached" });
    };
    const org = (page) => page.locator(".editor .team-org");
    const proj = (page) => page.locator(".editor .team-project");
    // a click in a field, the page left where it was
    const click = async (page, loc) => {
      await loc.scrollIntoViewIfNeeded();
      await page.waitForTimeout(200);
      const before = await loc.evaluate((e) => e.getBoundingClientRect().top);
      await loc.click();
      await page.waitForTimeout(250);
      const after = await loc.evaluate((e) => e.getBoundingClientRect().top);
      assert(Math.abs(after - before) <= 1, `moved from ${before} to ${after}`);
    };
    const press = async (page, posts, label) => {
      const n = posts.length;
      const b = page.locator(".editor .bar").getByRole("button", { name: label, exact: true });
      await b.scrollIntoViewIfNeeded();
      await b.click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.length > n ? posts.at(-1) : null;
    };

    test(`${engine} ${lang}: a Zhipu key's team is shown, typed and saved`, async (t) => {
      const { page, errors, posts } = await start(t, "saved");
      await edit(page, "Zhipu GLM");
      const labels = await page.locator(".editor label").allTextContents();
      assert(labels.includes(w.org) && labels.includes(w.project), labels.join(" | "));
      assert.equal(await org(page).inputValue(), "org-0", "what it has is shown");
      assert.equal(await proj(page).inputValue(), "proj-0");
      const hint = await proj(page).locator("xpath=following-sibling::div[contains(@class,'hint')]").textContent();
      assert(hint.includes(w.console), hint);

      await click(page, org(page));
      await org(page).fill(" org-1 ");
      await click(page, proj(page));
      await proj(page).fill("proj-1\t");
      let saved = await press(page, posts, w.save);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.id, "zhipu");
      assert.deepEqual(saved.body.zhipuTeam, { org: "org-1", project: "proj-1" });

      // one without the other is refused, nothing posted
      await edit(page, "Zhipu GLM");
      await proj(page).fill("");
      assert.equal(await press(page, posts, w.save), null);
      assert.equal(await page.locator(".editor .editor-error").textContent(), w.one);
      assert.equal(posts.length, 1);

      // both empty clears them
      await org(page).fill("");
      saved = await press(page, posts, w.save);
      assert.deepEqual(saved.body.zhipuTeam, {});

      const missing = await page.evaluate(() => [
        "Team org ID", "Team project ID", "optional · only for a team plan",
        "Only for a key on a team's GLM Coding Plan: both are in {p}'s console, under the team's organization and project. With them the Usage page shows the team's 5-hour and weekly windows.",
        "Team plan: give both the organization ID and the project ID",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      const border = await page.evaluate(() => [...document.querySelectorAll(".editor, .editor *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no border stripes");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: another provider has no team fields and posts none`, async (t) => {
      const { page, errors, posts } = await start(t, "relay");
      await edit(page, "Relay");
      assert.equal(await org(page).count(), 0);
      assert.equal(await proj(page).count(), 0);
      const saved = await press(page, posts, w.save);
      assert.equal(saved.body.id, "relay");
      assert(!("zhipuTeam" in saved.body));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a Zhipu key being added takes its team, DeepSeek's doesn't`, async (t) => {
      const { page, errors, posts } = await start(t, "add");
      await page.locator("#addProvider").click();
      const sheet = page.locator("#addSheet");
      await sheet.locator('.tile[data-pick="DeepSeek"]').click();
      await page.locator(".editor.new").waitFor();
      assert.equal(await org(page).count(), 0, "DeepSeek has none");
      await page.locator(".editor .bar").getByRole("button", { name: w.cancel, exact: true }).click();
      await page.locator(".editor").waitFor({ state: "detached" });

      // the sheet is still open under it
      await sheet.locator('.tile[data-pick="Z.ai"]').click();
      await org(page).waitFor();
      await page.locator(".editor.new input[type=password]:not(.proxy-credential)").fill("zk-1");
      await click(page, org(page));
      await org(page).fill("org-9");
      await proj(page).fill(" proj-9 ");
      const saved = await press(page, posts, w.add);
      assert.equal(saved.body.new, true);
      assert.equal(saved.body.preset, "zai");
      assert.deepEqual(saved.body.zhipuTeam, { org: "org-9", project: "proj-9" });
      assert.deepEqual(errors, []);
    });
  }
}
