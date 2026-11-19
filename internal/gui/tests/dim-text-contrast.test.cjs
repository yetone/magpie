// Run with Node's test runner and Playwright on the module path; see README.md.
// CHENGJIAHEGOXIS on X: 「其他地方会有这种特别暗的字」, Library › MCP's
// Project servers help ("将部分服务器作为项目服务器提供给项目中的
// Agent…", "Qwen Code 没有 magpie 可写入的 MCP 配置") on a dark page with
// a background image. That text was --faint, #56565f: 2.39:1 on the dark
// page, 2.16:1 on a card. Now the dark theme's --faint and --muted read at
// WCAG AA (4.5:1) on every dark ground text sits on (the page, a card, an
// editor, a menu, the tray panel's), still below the text above them, and
// the help reads at 4.5:1 on the page with a dark or a mid-grey picture
// behind it, as appearance.go veils it.
// And 「脱敏这块字体……瘦长的比较别扭」: Insights › Masking's big number
// was SF Pro Display at weight 100; it is the interface font at the weight
// and tracking the app's other big numbers have.
// Both engines, every language, 1100 and 420px. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const langs = ["zh", "en", "zh-TW", "ja", "de"];

// how much of the theme's background appearance.go lays over the picture
const veil = (async () => {
  const src = await fs.readFile(path.resolve(__dirname, "../appearance.go"), "utf8");
  const m = src.match(/wallVeil = "color-mix\(in srgb,var\(--bg\) (\d+)%,transparent\)"/);
  assert.ok(m, "appearance.go's wallVeil is a color-mix of --bg");
  return { pct: +m[1], css: `color-mix(in srgb,var(--bg) ${m[1]}%,transparent)` };
})();

const svg = (hex) => `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><rect width="64" height="64" fill="${hex}"/></svg>`;

function serve(lang, theme, opts = {}) {
  const settings = { theme, lang, tray: "panel", currency: "usd", redact: true };
  const lib = {
    dir: "/u/.magpie/library", backups: "/u/.magpie/backups", home: "/u", mcpHub: false, hubPath: "/mcp/magpie/hub/",
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: "/u/.claude/skills", mcp: "/u/.claude.json" }, { id: "qwen", name: "Qwen Code", skills: "/u/.qwen/skills" }],
    instructions: { agents: [], sets: [] }, foundSkills: [], skills: [], problems: [], foundServers: [], projects: [],
    servers: [{ name: "notes", transport: "stdio", command: "uvx", args: ["notes-mcp"], agents: ["claude"] }],
  };
  const day = (back) => { const d = new Date(Date.now() - back * 86400e3); return d.toISOString().slice(0, 10); };
  const summary = { days: Array.from({ length: 7 }, (_, i) => ({ day: day(6 - i), requests: 3, n: 8000 + i, distinct: 4 })), requests: 4210, n: 60341, distinct: 913, blocked: 0, hours: [],
    kinds: [{ name: "API_KEY", n: 40000, distinct: 500 }], by: { rule: 4000 }, agents: [{ name: "claude", n: 60341, distinct: 913 }], providers: [{ name: "anthropic", n: 60341, distinct: 913 }],
    checked: [], values: [], since: new Date(Date.now() - 7 * 86400e3).toISOString() };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") {
      // the look appearance.go's lookCSS draws for a background image
      const look = opts.wall ? `html:not(.omarchy) body:not(.panel){background:linear-gradient(${opts.veil},${opts.veil}),url("/api/appearance/wallpaper?v=1") center/cover no-repeat var(--bg)}\nhtml:not(.omarchy) body:not(.panel) .sidebar{background:transparent}` : "";
      return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:${!!opts.web}};` +
        (look ? `{const s=document.createElement("style");s.textContent=${JSON.stringify(look)};document.head.append(s);}` : "") });
    }
    if (url.pathname === "/api/appearance/wallpaper") return route.fulfill({ contentType: "image/svg+xml", body: svg(opts.wall || "#000") });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", fields: [] }, { id: "qwen", name: "Qwen Code", fields: [] }], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/library") return json(lib);
    if (url.pathname === "/api/library/mcp/check") return json({ servers: {} });
    if (url.pathname === "/api/masking/summary") return json(summary);
    if (url.pathname === "/api/masking/records") return json({ records: [], more: false });
    if (url.pathname === "/api/privacy-model") return json({ supported: true, installed: false, size: 1.6e9 });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999", calls: [] } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const rgb = (s) => { const m = s.match(/[\d.]+/g).map(Number); return [m[0], m[1], m[2], m[3] ?? 1]; };
const hex = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const lum = (c) => { const f = (v) => { v /= 255; return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(c[0]) + 0.7152 * f(c[1]) + 0.0722 * f(c[2]); };
const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => p - q); return (y + 0.05) / (x + 0.05); };
const over = (top, under) => { const a = top[3] ?? 1; return [0, 1, 2].map((i) => top[i] * a + under[i] * (1 - a)); };

// the theme's text and grounds, as the page resolves them on body
const tokens = (page) => page.evaluate(() => {
  const out = {};
  const probe = document.createElement("i");
  document.body.append(probe);
  for (const k of ["fg", "fg-2", "muted", "faint", "bg", "card", "card-2", "pop-bg"]) {
    probe.style.color = `var(--${k})`;
    out[k] = getComputedStyle(probe).color;
  }
  probe.remove();
  return out;
});

async function launch(engine) {
  return engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });
}

async function open(browser, lang, theme, width, where, opts = {}) {
  const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce", colorScheme: theme });
  await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, theme, opts));
  await page.goto("http://magpie.test/" + where);
  return { ctx, page, errors };
}

const shot = async (page, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, name + ".png") });
};

const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the dark theme's quiet text reads at 4.5:1 on every dark ground, below the text above it`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    for (const where of ["?view=library", "?mode=panel"]) {
      const { ctx, page, errors } = await open(browser, "en", "dark", where === "?mode=panel" ? 360 : 1100, where);
      await page.waitForFunction((panel) => panel ? document.body.classList.contains("panel") : !!document.querySelector("#view-library .lib-aside"), where === "?mode=panel");
      const k = await tokens(page);
      const bg = rgb(k.bg);
      const grounds = { bg, card: over(rgb(k.card), bg), "card-2": over(rgb(k["card-2"]), bg), "pop-bg": over(rgb(k["pop-bg"]), over(rgb(k.card), bg)) };
      for (const tok of ["faint", "muted"]) {
        for (const [g, c] of Object.entries(grounds)) {
          const r = ratio(rgb(k[tok]), c);
          assert.ok(r >= 4.5, `${where}: --${tok} ${k[tok]} on --${g} reads at ${r.toFixed(2)}:1`);
        }
      }
      // the steps stay: fg-2 over muted over faint, on the page
      const on = (tok) => ratio(rgb(k[tok]), bg);
      assert.ok(on("fg-2") > on("muted") + 1 && on("muted") > on("faint") + 0.3, `${where}: fg-2 ${on("fg-2").toFixed(2)}, muted ${on("muted").toFixed(2)}, faint ${on("faint").toFixed(2)}`);
      assert.deepEqual(errors, [], where);
      await ctx.close();
    }
  });

  test(`${engine}: Library › MCP's project help reads at 4.5:1 on the dark page, with a picture behind it too`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const v = await veil;
    for (const wall of [null, "#262626", "#808080"]) {
      for (const lang of langs) {
        for (const width of [1100, 420]) {
          const where = `${wall || "no picture"} ${lang} ${width}px`;
          const { ctx, page, errors } = await open(browser, lang, "dark", width, "?view=library", { wall, veil: v.css, web: width < 800 });
          const asides = page.locator("#view-library .lib-aside");
          await asides.first().waitFor();
          // the page fades in (kept under reduced motion): read the words
          // once every box over them has landed, not mid-fade
          await page.waitForFunction(() => [...document.querySelectorAll("#view-library .lib-aside")].every((e) => { for (let x = e; x; x = x.parentElement) if (getComputedStyle(x).opacity !== "1") return false; return true; }));
          if (lang === "zh" && width === 1100) await shot(page, `${engine}-library-mcp-dark-${wall ? wall.slice(1) : "plain"}`);
          assert.equal(await asides.count(), 2, `${where}: the project help and the agents without MCP`);
          const k = await tokens(page);
          // the page under the words: the theme's background, veiled over
          // the picture as appearance.go draws it
          const bg = rgb(k.bg);
          const ground = wall ? [0, 1, 2].map((i) => bg[i] * v.pct / 100 + hex(wall)[i] * (1 - v.pct / 100)) : bg;
          if (wall) assert.ok((await page.evaluate(() => getComputedStyle(document.body).backgroundImage)).includes("/api/appearance/wallpaper"), `${where}: the picture is drawn`);
          const seen = await asides.evaluateAll((es) => es.map((e) => {
            let op = 1;
            for (let x = e; x; x = x.parentElement) op *= parseFloat(getComputedStyle(x).opacity);
            const transparent = (() => { for (let x = e; x && x !== document.body; x = x.parentElement) { const c = getComputedStyle(x).backgroundColor.match(/[\d.]+/g).map(Number); if ((c[3] ?? 1) > 0.05) return false; } return true; })();
            return { text: e.textContent.trim().slice(0, 30), color: getComputedStyle(e).color, op, transparent };
          }));
          for (const s of seen) {
            assert.ok(s.transparent, `${where}: "${s.text}" sits on the page`);
            const c = rgb(s.color);
            const r = ratio(over([c[0], c[1], c[2], c[3] * s.op], ground), ground);
            assert.ok(r >= 4.5, `${where}: "${s.text}" ${s.color} reads at ${r.toFixed(2)}:1`);
          }
          assert.ok(await sideways(page) <= 0, `${where}: sideways scroll`);
          assert.deepEqual(errors, [], where);
          await ctx.close();
        }
      }
    }
  });

  test(`${engine}: the masking count is drawn as the app's other big numbers`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    for (const theme of ["dark", "light"]) {
      for (const lang of langs) {
        for (const width of [1100, 420]) {
          const where = `${theme} ${lang} ${width}px`;
          const { ctx, page, errors } = await open(browser, lang, theme, width, "?view=masking", { web: true });
          const num = page.locator("#maskCard .msk-num");
          await num.waitFor();
          if (lang === "zh" && width === 1100) await shot(page, `${engine}-masking-${theme}`);
          const got = await num.evaluate((e) => {
            const s = getComputedStyle(e);
            // the big numbers elsewhere, as app.css draws them
            const rule = (sel) => { for (const sh of document.styleSheets) { let rs; try { rs = sh.cssRules; } catch { continue; } for (const r of rs) if (r.selectorText === sel) return r.style; } return null; };
            const big = ["si-pct", "kpi"].map((c) => { const p = document.createElement("div"); p.className = c; const b = document.createElement("b"); p.append(b); document.body.append(p); const w = getComputedStyle(b).fontWeight; p.remove(); return w; });
            return { weight: s.fontWeight, family: s.fontFamily, body: getComputedStyle(document.body).fontFamily, spacing: parseFloat(s.letterSpacing) / parseFloat(s.fontSize), text: e.textContent, big, si: rule(".si-pct b")?.letterSpacing, right: e.getBoundingClientRect().right, cardRight: e.closest(".msk-card").getBoundingClientRect().right };
          });
          assert.equal(got.text, (60341).toLocaleString(lang === "zh-TW" ? "zh-TW" : lang), where);
          assert.deepEqual(got.big, [got.weight, got.weight], `${where}: weight ${got.weight}, the app's big numbers ${got.big}`);
          assert.equal(got.family, got.body, `${where}: the number's font is the interface's`);
          assert.ok(got.spacing >= parseFloat(got.si) - 1e-6, `${where}: tracking ${got.spacing.toFixed(3)}em, the app's ${got.si}`);
          assert.ok(got.right <= got.cardRight, `${where}: the number fits its card`);
          assert.ok(await sideways(page) <= 0, `${where}: sideways scroll`);
          assert.deepEqual(errors, [], where);
          await ctx.close();
        }
      }
    }
  });
}
