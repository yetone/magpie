// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group may let a member go that is slow to begin its answer
// (Chen on X: every answer waits on a slow vendor). The group editor's
// "Slow to start" row picks Wait, 30 s, 1 min or 2 min, or Custom and as
// many minutes as are typed (Kismet Alan on Discord), its hint saying what
// each does; saved, the group is sent with firstToken in seconds and its
// card says "Next after 60 s without a first token". Wait sends 0 and the
// card says nothing. In five languages, Chromium and WebKit, at 1100 and 420.
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
  "zh-TW": { edit: "編輯", save: "儲存", label: "遲遲不出字", wait: "一直等", min: "1 分鐘", tag: "60 秒 無首字換下一個", waits: "每個成員都會一直等" },
  ja: { edit: "編集", save: "保存", label: "応答開始が遅いとき", wait: "待つ", min: "1 分", tag: "最初のトークンが 60 秒 なければ次へ", waits: "どのメンバーも" },
  de: { edit: "Bearbeiten", save: "Speichern", label: "Langsamer Start", wait: "Warten", min: "1 Min.", tag: "Nächstes nach 60 s ohne erstes Token", waits: "Auf jedes Mitglied wird gewartet" },
};
const custom = { en: "Custom", zh: "自定义", "zh-TW": "自定義", ja: "カスタム", de: "Benutzerdefiniert" };

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
  for (const lang of Object.keys(words)) for (const width of [1100, 420]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}: a group lets a slow-to-start member go`, async (t) => {
      saved.firstToken = 0;
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width, height: 2000 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-first-token.png`) });
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
      assert.equal(await card.locator(".tag", { hasText: w.tag }).count(), 0, "off by default");

      // at 420 the editor runs past the window's foot, and WebKit doesn't
      // scroll the page's own scroller to a click: wheel it into view
      const reach = async (loc) => {
        await loc.waitFor();
        await page.mouse.move(width / 2, 600);
        for (let i = 0; i < 30; i++) {
          const b = await loc.boundingBox();
          if (b && b.y >= 0 && b.y + b.height <= 2000) return loc;
          await page.mouse.wheel(0, b && b.y < 0 ? -400 : 400);
          await page.waitForTimeout(50);
        }
        return loc;
      };
      const save = async () => {
        const ed = page.locator(".rt-gedit");
        await (await reach(ed.locator("button.primary", { hasText: w.save }))).click();
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        return posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
      };
      await (await reach(card.locator("button", { hasText: w.edit }))).click();
      const ed = page.locator(".rt-gedit");
      const row = ed.locator("label", { hasText: w.label }).locator("xpath=following-sibling::div[1]");
      await row.waitFor();
      await reach(row);
      assert.equal(await row.locator(".opt.on").textContent(), w.wait);
      assert((await row.locator(".hint").textContent()).startsWith(w.waits));
      await row.locator(".opt", { hasText: w.min }).click();
      const secs = (n) => page.evaluate((n) => t("{n} s", { n }), n);
      assert((await row.locator(".hint").textContent()).includes(await secs(60)));
      assert.equal((await save()).firstToken, 60);
      await card.locator(".tag", { hasText: w.tag }).waitFor();

      await (await reach(card.locator("button", { hasText: w.edit }))).click();
      await row.waitFor();
      await reach(row);
      assert.equal(await row.locator(".opt.on").textContent(), w.min, "the editor opens at what was saved");
      await row.locator(".opt", { hasText: w.wait }).click();
      assert.equal((await save()).firstToken, 0);
      await page.waitForFunction((tag) => ![...document.querySelectorAll(".rt-group .tag")].some((x) => x.textContent === tag), w.tag);

      // Custom: as many minutes as are typed, from 5 when it was Wait
      await (await reach(card.locator("button", { hasText: w.edit }))).click();
      await row.waitFor();
      await reach(row);
      const minutes = row.locator("input.first-in");
      assert.equal(await minutes.isVisible(), false, "no minutes box until Custom is picked");
      await row.locator(".opt", { hasText: custom[lang] }).click();
      assert.equal(await minutes.isVisible(), true);
      assert.equal(await minutes.inputValue(), "5");
      assert.equal(await page.evaluate(() => document.activeElement?.classList.contains("first-in")), true, "the box is focused to type in");
      assert((await row.locator(".hint").textContent()).includes(await secs(300)));
      await minutes.fill("abc");
      assert.equal(await minutes.getAttribute("aria-invalid"), "true");
      assert((await row.locator(".hint").textContent()).includes("abc"), "the hint says what is wrong");
      await minutes.fill("2.5");
      assert.equal(await minutes.getAttribute("aria-invalid"), "false");
      assert((await row.locator(".hint").textContent()).includes(await secs(150)));
      // the row fits the editor at this width
      const fit = await row.evaluate((r) => {
        const box = r.getBoundingClientRect(), ed = r.closest(".rt-gedit").getBoundingClientRect();
        return [...r.querySelectorAll(".segs, input.first-in, .first-unit")].every((x) => {
          const b = x.getBoundingClientRect();
          return b.left >= ed.left - 0.5 && b.right <= ed.right + 0.5 && b.right <= box.right + 0.5;
        }) && document.scrollingElement.scrollWidth <= document.scrollingElement.clientWidth;
      });
      assert.equal(fit, true, "the Custom row overflows at " + width);
      assert.equal((await save()).firstToken, 150);
      const tag150 = w.tag.replace(await secs(60), await secs(150));
      await card.locator(".tag", { hasText: tag150 }).waitFor();

      await (await reach(card.locator("button", { hasText: w.edit }))).click();
      await row.waitFor();
      await reach(row);
      assert.equal(await row.locator(".opt.on").textContent(), custom[lang], "a time of its own opens as Custom");
      assert.equal(await minutes.inputValue(), "2.5");
      await row.locator(".opt", { hasText: w.min }).click();
      assert.equal(await minutes.isVisible(), false);
      assert.equal((await save()).firstToken, 60);

      const missing = await page.evaluate(() => [
        "Wait", "30 s", "1 min", "2 min", "Slow to start", "slow to start", "Next after {n} without a first token",
        "Every member is waited for, however long it takes to begin answering.",
        "A member that hasn't begun answering after {n} — no text, reasoning or tool call yet — is let go and the next one asked, before any of it reaches the agent. Let go once, it doesn't rest; let go twice in a row, its model rests on that account, a minute and longer each time, until it answers again. The last one left is always waited for. Only for streamed requests.",
        "{who} hadn't begun answering after {ms}, so the request went on to the next before any of it reached {agent}. Nothing is wrong with {who}, so it doesn't rest.",
        "{who} hadn't begun answering after {ms}, {n} times in a row, so the request went on to the next before any of it reached {agent}. It rests {d} for {model} alone, longer each time it is slow again; its other models answer as before.",
        "In magpie", "Vendor's first token",
        "Custom", "e.g. 5", "Minutes", "min", "{v} is not a number of minutes",
      ].filter((k) => !["zh", "zh-TW", "ja", "de"].every((l) => I18N[l][k])));
      assert.deepEqual(missing, [], "every string has its zh, zh-TW, ja and de");
      assert.deepEqual(errors, []);
    });
  }
}
