// Run with Node's test runner and Playwright on the module path; see README.md.
// In turn says which unit goes round, and draws it (Wing8Aqua on X: "each
// turn of a conversation goes to the next one" read as every message of a
// session moving, and a session as the unit expected). The hint names the
// message you send, the tool calls that stay with it, and Stays: Session
// for a whole conversation on one. Under it a small figure: two
// conversations of three messages each, a pill naming the account each
// went to, then its tool calls in that account's shade — moved on per
// message under Auto, one account a conversation under Session, every
// request under Off, as the gateway's affine routes them. In a provider's
// editor and a routing group's, every language, wide and narrow, Chromium
// and WebKit; the figure fits, nothing scrolls sideways, and it is gone
// for any other routing.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ROTATE_ASSETS || path.join(__dirname, "../assets"));
const sub = {
  icon: "generic", catalog: "", models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], agents: [], fallback: [], headers: {}, routing: "rotate", affinity: "", ready: true,
  id: "codex", name: "Codex", chat: "", responses: "", anthropic: "", keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: "one@example.test", plan: "Plus",
    logins: ["one", "two", "three"].map((n, i) => ({ user: `${n}@example.test`, plan: "Plus", active: i === 0, on: true })),
  },
};
const models = [
  { id: "a/sol", name: "sol", providerName: "A", icon: "generic" },
  { id: "b/sol", name: "sol", providerName: "B", icon: "generic" },
];
const members = models.map((m) => m.id);
const groups = () => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, routing: "rotate", ready: true, memberInfo: members.map((id) => ({ id, ready: true })) }],
});

function serve(lang, theme) {
  const state = { agents: [{ id: "pi", name: "Pi", path: "/test/pi", fields: [] }], profiles: [], settings: { lang, theme } };
  const providers = { providers: [sub], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:${JSON.stringify(theme)},web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
// the hint names the message you send as what goes round
const SAYS = { en: /Each message you send goes to the next/i, zh: /你每发一条消息/, "zh-TW": /你每發一則訊息/, ja: /メッセージを送るたびに/, de: /Jede Nachricht, die Sie senden/ };
const HINTS = [
  "Each message you send goes to the next one, round the list; the agent's tool calls for it stay on the same one. A move costs some prompt cache; with Stays on Session, a whole conversation stays on one instead.",
  "In turn: each message you send goes to the next member's account or key, round the list; the agent's tool calls for it stay on the same one. A move costs some prompt cache; with Stays on Session, a whole conversation stays on one instead.",
  "In turn: each message you send goes to the next account, round the list; the agent's tool calls for it stay on the same one. With Stays on Session, a whole conversation stays on one instead.",
  "Your message", "Tool calls",
];
// what the figure draws: each conversation's three messages, the account
// each went to, and the accounts its tool calls went to
const fig = (scope) => scope.locator("svg.rot-fig").evaluate((s) => ({
  affinity: s.dataset.affinity,
  msgs: [...s.querySelectorAll(".rot-msg")].map((g) => g.dataset.to).join(""),
  calls: [...s.querySelectorAll(".rot-call")].map((c) => [...c.classList].find((k) => /^rot-[abc]$/.test(k)).slice(4).toUpperCase()).join(""),
  legend: [...s.querySelectorAll(".rot-legend")].map((x) => x.textContent),
  box: (() => { const r = s.getBoundingClientRect(); return { x: r.x, r: r.right, w: r.width, h: r.height }; })(),
}));
const fits = async (page, scope, name) => {
  const f = await fig(scope);
  const ed = await scope.boundingBox();
  assert.ok(f.box.w > 100 && f.box.h > 20, `${name}: the figure is drawn: ${JSON.stringify(f.box)}`);
  assert.ok(f.box.x >= ed.x - 1 && f.box.r <= ed.x + ed.width + 1, `${name}: the figure past the editor's edge: ${JSON.stringify(f.box)} in ${JSON.stringify(ed)}`);
  // the legend's two words don't run into each other
  const [a, b] = await scope.locator("svg.rot-fig .rot-legend").evaluateAll((ts) => ts.map((x) => { const r = x.getBoundingClientRect(); return { l: r.left, r: r.right }; }));
  assert.ok(a.r < b.l - 6, `${name}: the legend's words overlap: ${JSON.stringify([a, b])}`);
  assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), true, `${name}: no sideways scroll`);
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  const launch = () => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });
  for (const lang of LANGS) {
    const tr = (page, s) => page.evaluate(([k, lang]) => lang === "en" ? k : (I18N[lang][k] ?? window.ROUTING_I18N?.[lang]?.[k] ?? k), [s, lang]);
    for (const width of [900, 420]) {
      const theme = width === 420 ? "dark" : "light";
      test(`${engine} ${lang} ${width}px ${theme}: a provider's In turn says and draws the message going round`, async (t) => {
        const browser = await launch();
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: sub.name }).first().click();
        await page.locator(".editor .accts").first().waitFor();
        if (lang !== "en") assert.deepEqual(await page.evaluate(([keys, lang]) => keys.filter((k) => !I18N[lang][k]), [[HINTS[0], HINTS[3], HINTS[4]], lang]), [], "translated");
        const ed = page.locator(".editor").first();
        const routing = ed.locator("label", { hasText: new RegExp(`^${await tr(page, "Routing")}$`) }).locator("xpath=following-sibling::div[1]");
        assert.match(await routing.locator(".hint.expl").first().textContent(), SAYS[lang]);
        // Auto: each message on to the next, its tool calls with it, a new
        // conversation one further along
        let f = await fig(routing);
        assert.equal(f.affinity, "");
        assert.equal(f.msgs, "ABCBCA");
        assert.equal(f.calls, "AABBCCBBCCAA");
        assert.deepEqual(f.legend, [await tr(page, "Your message"), await tr(page, "Tool calls")]);
        await fits(page, ed, "provider");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await routing.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-provider-rotate.png`) });
        }
        const stays = ed.locator("label", { hasText: new RegExp(`^${await tr(page, "Stays")}$`) }).locator("xpath=following-sibling::div[1]");
        // Session: a whole conversation on one
        await stays.locator(".segs .opt", { hasText: new RegExp(`^${await tr(page, "Session")}$`) }).click();
        f = await fig(routing);
        assert.equal(f.msgs, "AAABBB");
        assert.equal(f.calls, "AAAAAABBBBBB");
        // Off: every request on to the next
        await stays.locator(".segs .opt", { hasText: new RegExp(`^${await tr(page, "Off")}$`) }).click();
        f = await fig(routing);
        assert.equal(f.msgs, "AAABBB");
        assert.equal(f.calls, "BCBCBCCACACA");
        // another routing: no figure
        await routing.locator(".segs .opt", { hasText: new RegExp(`^${await tr(page, "In order")}$`) }).click();
        assert.equal(await routing.locator("svg.rot-fig").count(), 0, "drawn for In turn only");
        assert.equal(await routing.locator(".rot-wrap").isHidden(), true);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px ${theme}: a routing group's In turn says and draws the message going round`, async (t) => {
        const browser = await launch();
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme));
        await page.goto("http://magpie.test/?view=groups");
        const card = page.locator(".rt-group", { hasText: "Sol" });
        await card.waitFor();
        await card.locator("button", { hasText: new RegExp(`^${await tr(page, "Edit")}$`) }).click();
        const adv = page.locator(".rt-gedit .rt-gadv");
        await adv.waitFor({ state: "attached" });
        if (!(await adv.evaluate((e) => e.open))) await adv.locator(":scope > summary").click();
        const ed = page.locator(".rt-gedit");
        const routing = ed.locator("label", { hasText: new RegExp(`^${await tr(page, "Routing")}$`) }).locator("xpath=following-sibling::div[1]");
        const hint = await routing.locator(".hint").first().textContent();
        assert.match(hint, SAYS[lang]);
        if (lang !== "en") assert.notEqual(hint, HINTS[1], "translated");
        let f = await fig(routing);
        assert.equal(f.msgs, "ABCBCA");
        await fits(page, ed, "group");
        if (process.env.ARTIFACT_DIR) await routing.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-rotate.png`) });
        const stays = ed.locator("label", { hasText: new RegExp(`^${await tr(page, "Stays")}$`) }).locator("xpath=following-sibling::div[1]");
        await stays.locator(".segs .opt", { hasText: new RegExp(`^${await tr(page, "Session")}$`) }).click();
        f = await fig(routing);
        assert.equal(f.affinity, "session");
        assert.equal(f.msgs, "AAABBB");
        await routing.locator(".segs .opt", { hasText: new RegExp(`^${await tr(page, "Smart")}$`) }).click();
        assert.equal(await routing.locator("svg.rot-fig").count(), 0, "drawn for In turn only");
        assert.deepEqual(errors, []);
      });
    }
  }
}
