// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's Routing (Smart, In order, In turn…) and Stays (Auto,
// Session…) are picked in its editor and made with its Save, as the rest of
// the editor is; Cancel drops them. A click used to post provider/route and
// redraw the editor at once, so looking through what each option says
// lagged, and saved a pick only looked at (01huadalang on Discord, on a
// WorkBuddy AI subscription with several accounts). Stays, which a
// provider's Routing page row had alone, is beside Routing in the editor
// too. Accounts and keys alike; nothing is sent, redrawn or scrolled before
// the Save. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = { icon: "generic", catalog: "", models: [{ id: "glm-5", name: "GLM-5", on: true }], agents: [], fallback: [], headers: {}, routing: "", affinity: "", ready: true };
const sub = {
  ...base, id: "workbuddy-ai", name: "WorkBuddy AI", chat: "", responses: "", anthropic: "", keyList: [],
  account: {
    agent: "workbuddy-ai", agentName: "WorkBuddy AI", user: "one@example.test", plan: "Pro",
    logins: ["one", "two", "three"].map((n, i) => ({ user: `${n}@example.test`, plan: "Pro", active: i === 0, on: true })),
  },
};
const relay = {
  ...base, id: "relay", name: "Relay", chat: "https://relay.example.invalid/v1", responses: "", anthropic: "", key: { set: true, masked: "sk-…1234" },
  keyList: [1, 2].map((i) => ({ id: `key-${i}`, name: `Key ${i}`, masked: `sk-…000${i}`, active: i === 1, on: true })),
};

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const providers = { providers: [sub, relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (route.request().method() === "POST" && url.pathname.startsWith("/api/provider")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

const ROUTINGS = [
  ["", "Smart", "智能"], ["order", "In order", "按顺序"], ["rotate", "In turn", "轮流"],
  ["usage", "Least used first", "用量少的优先"], ["pace", "Weekly pace", "重置前用完"],
];
const STAYS = [["", "Auto", "自动"], ["session", "Session", "整个会话"], ["turn", "Within a turn", "一轮之内"], ["off", "Off", "关闭"]];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const zh = lang === "zh";
    const L = { routing: zh ? "路由" : "Routing", stays: zh ? "会话保持" : "Stays", unsaved: zh ? "未保存" : "unsaved", save: zh ? "保存" : "Save", cancel: zh ? "取消" : "Cancel" };
    for (const p of [sub, relay]) {
      test(`${engine} ${lang}: ${p.name}'s Routing and Stays are made with its Save, not at each click`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width: 900, height: 720 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        const open = async () => {
          await page.locator(".row.provider", { hasText: p.name }).first().click();
          await page.locator(".editor .accts").first().waitFor();
        };
        // the field under a label: the label's next sibling
        const fieldOf = (label) => page.locator(".editor label", { hasText: new RegExp(`^${label}$`) }).locator("xpath=following-sibling::div[1]");
        const opt = (label, name) => fieldOf(label).locator(".segs .opt", { hasText: new RegExp(`^${name}$`) });
        const hint = (label) => fieldOf(label).locator(":scope > .hint").first();
        const unsaved = (label) => fieldOf(label).getByText(L.unsaved, { exact: true });
        const said = (s) => page.evaluate(([k, zh]) => zh ? I18N.zh[k] : k, [s, zh]);
        await open();
        const missing = await page.evaluate(() => [
          "Stays", "Auto", "Session", "Within a turn", "Off", "unsaved", "Made when the provider is saved; Cancel drops it",
          "A conversation stays with the account or key that answered it while what the vendor cached of it is worth keeping — within a turn always, across turns while it's fresh.",
          "A conversation stays with the account or key that answered it for the whole session, while it can answer.",
          "A conversation stays put within a turn, while the agent sends tool results back; when you speak again, routing decides afresh.",
          "Every request is routed afresh, whoever answered its conversation before.",
        ].filter((k) => !I18N.zh[k]));
        assert.deepEqual(missing, [], "every string has its Chinese");
        const editor = await page.locator(".editor").elementHandle();
        const accts = await page.locator(".editor .accts").first().elementHandle();
        const y = await page.evaluate(() => document.scrollingElement.scrollTop);
        assert.equal(await unsaved(L.routing).isVisible(), false);
        // each option shows what it does: nothing sent, redrawn or scrolled
        for (const [id, en, cn] of [...ROUTINGS.slice(1), ROUTINGS[0]]) {
          await opt(L.routing, zh ? cn : en).click();
          assert.match(await opt(L.routing, zh ? cn : en).getAttribute("class"), /\bon\b/);
          const desc = await page.evaluate((id) => ROUTINGS.find((r) => r[0] === id)[2], id);
          assert.equal(await hint(L.routing).textContent(), await said(desc), `${en} says what it does`);
          assert.equal(await unsaved(L.routing).isVisible(), id !== "", `${en}: unsaved said`);
        }
        for (const [id, en, cn] of STAYS.slice(1)) {
          await opt(L.stays, zh ? cn : en).click();
          const desc = await page.evaluate((id) => STAYS.find((r) => r[0] === id)[2], id);
          assert.equal(await hint(L.stays).textContent(), await said(desc), `${en} says what it does`);
          assert.equal(await unsaved(L.stays).isVisible(), true);
        }
        await page.waitForTimeout(200);
        assert.deepEqual(posts, [], "nothing is sent before the Save");
        assert(await editor.evaluate((e) => e.isConnected) && await accts.evaluate((e) => e.isConnected), "the editor is not redrawn");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y, "a click never scrolls the page");
        // Cancel drops them; opened again, the editor is as saved
        await page.getByRole("button", { name: L.cancel, exact: true }).click();
        await page.getByRole("alertdialog").locator("button").last().click();
        await open();
        assert.match(await opt(L.routing, zh ? "智能" : "Smart").getAttribute("class"), /\bon\b/);
        assert.match(await opt(L.stays, zh ? "自动" : "Auto").getAttribute("class"), /\bon\b/);
        assert.equal(await unsaved(L.routing).isVisible(), false);
        assert.equal(await unsaved(L.stays).isVisible(), false);
        assert.deepEqual(posts, []);
        // picked again and saved: the Save carries them
        await opt(L.routing, zh ? "轮流" : "In turn").click();
        await opt(L.stays, zh ? "整个会话" : "Session").click();
        await page.getByRole("button", { name: L.save, exact: true }).click();
        await page.waitForFunction(() => !document.querySelector(".editor"));
        assert.deepEqual(posts.map((x) => x.path), ["/api/provider/save"]);
        assert.equal(posts[0].body.routing, "rotate");
        assert.equal(posts[0].body.affinity, "session");
        assert.deepEqual(errors, []);
      });
    }
  }
}
