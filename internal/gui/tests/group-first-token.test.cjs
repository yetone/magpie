// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group may let a member go that is slow to begin its answer
// (Chen on X: every answer waits on a slow vendor). The group editor's
// "Slow to start" row picks Wait, 30 s, 1 min or 2 min, its hint saying
// what each does; saved, the group is sent with firstToken in seconds and
// its card says "Next after 60 s without a first token". Wait sends 0 and
// the card says nothing. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic", efforts: ["low", "medium", "high", "xhigh"], canFast: true },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic", canFast: true },
  { id: "rl/glm-5", name: "glm-5", providerName: "Relay", icon: "generic" },
];
const members = ["oa/gpt-6.1-sol:high", "an/claude-opus-5-5", "rl/glm-5"];
const saved = { firstToken: 0 };
const groups = () => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, routing: "order", ready: true, firstToken: saved.firstToken,
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", label: "Slow to start", wait: "Wait", min: "1 min", tag: "Next after 60 s without a first token", waits: "Every member is waited for" },
  zh: { edit: "编辑", save: "保存", label: "迟迟不出字", wait: "一直等", min: "1 分钟", tag: "60 秒 无首字换下一个", waits: "每个成员都会一直等" },
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
      saved.firstToken = body.firstToken || 0;
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
    test(`${engine} ${lang}: a group lets a slow-to-start member go`, async (t) => {
      saved.firstToken = 0;
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-first-token.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const card = page.locator(".rt-group", { hasText: "Sol" });
      await card.waitFor();
      assert.equal(await card.locator(".tag", { hasText: w.tag }).count(), 0, "off by default");

      const save = async () => {
        const ed = page.locator(".rt-gedit");
        await ed.locator("button.primary", { hasText: w.save }).click();
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        return posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
      };
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const row = ed.locator("label", { hasText: w.label }).locator("xpath=following-sibling::div[1]");
      await row.waitFor();
      assert.equal(await row.locator(".opt.on").textContent(), w.wait);
      assert((await row.locator(".hint").textContent()).startsWith(w.waits));
      await row.locator(".opt", { hasText: w.min }).click();
      assert((await row.locator(".hint").textContent()).includes(lang === "zh" ? "60 秒" : "60 s"));
      assert.equal((await save()).firstToken, 60);
      await card.locator(".tag", { hasText: w.tag }).waitFor();

      await card.locator("button", { hasText: w.edit }).click();
      await row.waitFor();
      assert.equal(await row.locator(".opt.on").textContent(), w.min, "the editor opens at what was saved");
      await row.locator(".opt", { hasText: w.wait }).click();
      assert.equal((await save()).firstToken, 0);
      await page.waitForFunction((tag) => ![...document.querySelectorAll(".rt-group .tag")].some((x) => x.textContent === tag), w.tag);

      const missing = await page.evaluate(() => [
        "Wait", "30 s", "1 min", "2 min", "Slow to start", "slow to start", "Next after {n} without a first token",
        "Every member is waited for, however long it takes to begin answering.",
        "A member that hasn't begun answering after {n} — no text, reasoning or tool call yet — is let go and the next one asked, before any of it reaches the agent. It doesn't rest; the last one left is always waited for. Only for streamed requests.",
        "{who} hadn't begun answering after {ms}, so the request went on to the next before any of it reached {agent}. Nothing is wrong with {who}, so it doesn't rest.",
        "In magpie", "Vendor's first token",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
