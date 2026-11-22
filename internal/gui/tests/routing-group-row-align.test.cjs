// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's row is its logos, its name and lines, its tags, Edit and
// its switch. The owner (11-22, 这个排版布局是正确的吗？) found the rows out
// of line with each other: the logos' cell was as wide as each row's stack,
// so a two-logo row's name started 34px left of a row with three and a "+1";
// a group magpie found has no switch, so its tags and Edit sat 45px further
// right than every other row's; and the logos were centred on a row of four
// or five lines instead of meeting its name. Each row's name, tags' right
// edge and Edit now stand at one x whatever its logos and switch, and the
// logos' top meets the top of the name's letters (usage-row-logo-top,
// partner-tile-top), measured on the rendered pixels at 3x. Chromium and
// WebKit, en/zh/zh-TW/ja/de, the window at 1280px and 640px.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, process.env.MAGPIE_ASSETS || "../assets");
const LOGO = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#111"/></svg>';
const prov = ["claude", "codex", "factory", "copilot", "kiro"];
const models = prov.map((p) => ({ id: `${p}/opus`, name: "Opus 5.5", providerName: p[0].toUpperCase() + p.slice(1), icon: "file:logo.svg" }));
const info = (n) => models.slice(0, n).map((m, i) => ({ id: m.id, ready: true, icon: "file:logo.svg", provider: prov[i], billing: "plan" }));
const grp = (id, name, n, more) => ({ id, name, members: models.slice(0, n).map((m) => m.id), ready: true, memberInfo: info(n), offers: [], shared: [], ...more });
const groups = { models, pools: [], groups: [
  grp("review", "审码", 4, { note: "Alma 侧审码：同 Claude Code 的 review 组", rules: [{ use: models[0].id, tokens: 1000 }, { use: models[1].id, tokens: 2000 }],
    patterns: [{ pattern: "opus-*", models: 3 }, { pattern: "fable-*", models: 0 }], classifierFailed: { at: new Date().toISOString(), error: "timeout" } }),
  grp("coord", "coord", 4, { note: "常驻会话（main、daily、loop）：Opus high", affinity: "session" }),
  grp("build", "Build", 3, { patterns: [{ pattern: "opus-*", models: 3 }] }),
  grp("solo", "solo", 1, {}),
  grp("gpt-5.4", "GPT-5.4", 2, { auto: true, ref: "auto/gpt-5.4", routing: "quota" }),
] };

function serve(lang) {
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname.startsWith("/api/icons/")) return r.fulfill({ contentType: "image/svg+xml", body: LOGO });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/t", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

// the top of the name's ink in a row, in CSS px from the row's top
async function nameInk(page, row) {
  const rb = await row.boundingBox();
  const nb = await row.locator(".nm b").boundingBox();
  const png = (await page.screenshot({ clip: rb })).toString("base64");
  return page.evaluate(async ({ png, rb, b }) => {
    const img = new Image();
    img.src = "data:image/png;base64," + png;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width; c.height = img.height;
    const x = c.getContext("2d");
    x.drawImage(img, 0, 0);
    const k = img.width / rb.width, d = x.getImageData(0, 0, c.width, c.height).data;
    const bg = [d[(2 * c.width + 2) * 4], d[(2 * c.width + 2) * 4 + 1], d[(2 * c.width + 2) * 4 + 2]];
    const x0 = Math.max(0, Math.round((b.x - rb.x) * k)), x1 = Math.min(c.width, Math.round((b.x + b.width - rb.x) * k));
    const y1 = Math.min(c.height, Math.round((b.y + b.height - rb.y) * k));
    for (let y = 0; y < y1; y++) for (let xx = x0; xx < x1; xx++) {
      const i = (y * c.width + xx) * 4;
      if (Math.abs(d[i] - bg[0]) + Math.abs(d[i + 1] - bg[1]) + Math.abs(d[i + 2] - bg[2]) > 120) return y / k;
    }
    return -1;
  }, { png, rb, b: nb });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": routing group rows stand in one line", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1280, 640]) {
      await t.test(lang + " " + width, async (st) => {
        const ctx = await browser.newContext({ viewport: { width, height: 1000 }, deviceScaleFactor: 3, reducedMotion: "reduce" });
        st.after(() => ctx.close());
        const page = await ctx.newPage();
        page.setDefaultTimeout(15000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=groups");
        const rows = page.locator(".rt-groups > .rt-group");
        await rows.nth(groups.groups.length - 1).waitFor();
        await page.mouse.move(0, 0);
        await page.evaluate(() => Promise.all([...document.querySelectorAll(".rt-group img")].map((i) => i.decode().catch(() => {}))));
        const seen = [];
        for (let i = 0; i < await rows.count(); i++) {
          const row = rows.nth(i);
          await row.scrollIntoViewIfNeeded();
          const m = await row.evaluate((r) => {
            const rb = r.getBoundingClientRect(), at = (s) => r.querySelector(s)?.getBoundingClientRect();
            const disc = at(".rt-ghandle .disc, .rt-ghandle > .ic"), edit = [...r.children].find((e) => e.matches("button.text"));
            return { id: r.dataset.id, rowTop: rb.top, mainX: at(".main").left, logoTop: disc.top - rb.top, logoX: disc.left, lines: r.querySelectorAll(".main > *").length,
              tagsRight: at(".tags").right, editX: edit.getBoundingClientRect().left };
          });
          seen.push({ ...m, nameY: await nameInk(page, row) });
        }
        if (process.env.SHOW) console.log(engine, lang, width, JSON.stringify(seen.map((s) => [s.id, s.lines, +s.mainX.toFixed(2), +s.logoX.toFixed(2), +s.editX.toFixed(2), +s.tagsRight.toFixed(2), +s.logoTop.toFixed(2), +s.nameY.toFixed(2)])));
        assert(seen.some((s) => s.lines >= 4), "a row of four lines or more");
        const one = (key, what) => {
          const xs = seen.map((s) => s[key]);
          assert(Math.max(...xs) - Math.min(...xs) <= 0.5, `every row's ${what} at one x: ${seen.map((s) => s.id + " " + s[key].toFixed(2)).join(", ")}`);
        };
        one("mainX", "name and lines");
        one("logoX", "first logo");
        one("editX", "Edit");
        one("tagsRight", "tags' right edge");
        // the logos' disc lands on a whole pixel and the letters needn't:
        // within 1px of a name's capitals; a CJK name's letters stand a
        // pixel taller than capitals, so within 1.25px; a lower-case name
        // starts at its x-height or ascenders, lower by up to 2.5px
        for (const s of seen) {
          assert(s.nameY >= 0, s.id + ": no name drawn");
          const name = groups.groups.find((g) => g.id === s.id).name;
          const at = `${name} (${s.lines} lines): the logos' top is at ${s.logoTop.toFixed(2)}px, its name's letters start at ${s.nameY.toFixed(2)}px`;
          const d = s.nameY - s.logoTop;
          if (/^[A-Z]/.test(name)) assert(Math.abs(d) <= 1, at);
          else if (/^[\u3000-\u9fff]/.test(name)) assert(Math.abs(d) <= 1.25, at);
          else assert(d >= -0.5 && d <= 2.5, at);
        }
        assert.deepEqual(errors, []);
      });
    }
  });
}
