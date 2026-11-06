// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group switched off stays readable (jiabubu on Discord: at half
// opacity its words were too faint to read). Its name and its members read
// at a contrast an on group's members have, and it still looks switched
// off: its switch is off, its tag says so, its name is quieter than an on
// group's and its logos are grey. In both engines and themes, in English
// and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "or/a:free", name: "a:free", providerName: "OpenRouter", icon: "generic" },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const groups = () => ({
  models, pools: [],
  groups: [{ id: "on", name: "Lit", members: ["an/claude-opus-5-5"], match: [], matched: [], fast: [], off: [], routing: "order", ready: true, patterns: [], memberInfo: info(["an/claude-opus-5-5"]) },
  // switched off, and with that no member ready either: dimmed once, not twice
  { id: "dark", name: "Quiet", members: ["or/a:free"], match: [], matched: [], fast: [], off: [], routing: "order", ready: false, disabled: true, patterns: [], memberInfo: info(["or/a:free"]) }],
});

function serve(lang, theme, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
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

// contrast gives the WCAG contrast of an element's text against what is
// drawn under it: its colour with every opacity above it, over the
// backgrounds of the boxes it sits in, composited from the page down
const contrast = (loc) => loc.evaluate((e) => {
  const rgba = (c) => { const m = c.match(/[\d.]+/g).map(Number); return [m[0], m[1], m[2], m.length > 3 ? m[3] : 1]; };
  const chain = [];
  for (let n = e; n; n = n.parentElement) chain.unshift(n);
  let bg = [255, 255, 255];
  for (const n of chain) {
    const [r, g, b, a] = rgba(getComputedStyle(n).backgroundColor);
    bg = [r, g, b].map((v, i) => v * a + bg[i] * (1 - a));
  }
  let alpha = 1;
  for (const n of chain) alpha *= Number(getComputedStyle(n).opacity);
  const [r, g, b, a] = rgba(getComputedStyle(e).color);
  const fg = [r, g, b].map((v, i) => v * a * alpha + bg[i] * (1 - a * alpha));
  const lum = (c) => c.map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }).reduce((s, v, i) => s + v * [0.2126, 0.7152, 0.0722][i], 0);
  const [hi, lo] = [lum(fg), lum(bg)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const theme of ["light", "dark"]) {
      test(`${engine} ${lang} ${theme}: a switched-off group stays readable and looks off`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce", colorScheme: theme })).newPage();
        t.after(() => browser.close());
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme, posts));
        await page.goto("http://magpie.test/?view=routing");
        const on = page.locator(".rt-group", { hasText: "Lit" });
        const off = page.locator(".rt-group.disabled", { hasText: "Quiet" });
        await off.waitFor();
        await on.waitFor();
        await page.evaluate(() => document.fonts.ready);
        if (process.env.ARTIFACT_DIR) await page.locator(".rt-groups").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${theme}-group-disabled.png`) });
        const nameOff = await contrast(off.locator(".nm b")), nameOn = await contrast(on.locator(".nm b"));
        const memOff = await contrast(off.locator(".mem").first()), memOn = await contrast(on.locator(".mem").first());
        const idOff = await contrast(off.locator(".nm code.mdl"));
        t.diagnostic(`off: name ${nameOff.toFixed(2)}, members ${memOff.toFixed(2)}, id ${idOff.toFixed(2)}; on: name ${nameOn.toFixed(2)}, members ${memOn.toFixed(2)}`);
        // readable: its name as body text is, its members and id as an on group's members are
        assert.ok(nameOff >= 4.5, `the name reads at ${nameOff.toFixed(2)}:1`);
        assert.ok(memOff >= Math.min(memOn, 3.4) - 0.01, `the members read at ${memOff.toFixed(2)}:1 (an on group's: ${memOn.toFixed(2)}:1)`);
        assert.ok(idOff >= 4.5, `the id reads at ${idOff.toFixed(2)}:1`);
        // and still visibly off
        assert.ok(nameOff < nameOn - 0.5, `the name is quieter than an on group's (${nameOff.toFixed(2)} vs ${nameOn.toFixed(2)})`);
        assert.equal(await off.locator(".rt-gon").getAttribute("aria-checked"), "false");
        assert.equal(await off.locator(".tag.idle").count(), 1, "its tag says it is switched off");
        assert.equal(await on.locator(".tag.idle").count(), 0);
        const look = await off.locator(".rt-ghandle").evaluate((e) => ({ filter: getComputedStyle(e).filter, opacity: Number(getComputedStyle(e).opacity) }));
        assert.match(look.filter, /grayscale\(1\)/, "its logos are grey");
        assert.ok(look.opacity < 1);
        assert.equal(await on.locator(".rt-ghandle").evaluate((e) => getComputedStyle(e).filter), "none", "an on group's logos keep their colours");
        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
      });
    }
  }
}
