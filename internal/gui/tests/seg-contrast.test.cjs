// Run with Node's test runner and Playwright on the module path; see README.md.
// #477: in light mode the header's view switch's other views (#85858d on the
// #f1f1f4 track) read at 3.25:1, and a text button at 3.33:1 on the page and
// 3.66:1 on a card, under WCAG AA's 4.5:1 for small text; in dark mode the
// other views passed by 0.08, and in both the thumb on the view shown stood
// off the track at 1.13:1 (light) and 1.36:1 (dark). The window is drawn
// light, dark, and dark by the system's choice, and the colours read as the
// browser computed them, each background laid over the ones under it: the
// other views and a text button on the page, a card and the inset editor
// read at 4.5:1 with some margin to spare (4.7), the view shown on its thumb
// too, and the thumb is told from the track by more than it was. They are
// read once the page's transitions are over; and the page is first drawn in
// its theme, before app.js runs: a window kept dark under a light system was
// first drawn light, its header's buttons fading to dark as they were read,
// so they measured mid-way (3.96:1 for the view shown). Chromium
// and WebKit, English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang, theme, gate) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/app.js") await gate;
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme } } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } } });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// every ratio the page draws, worked out in the page from computed colours
const ratios = (page) => page.evaluate(() => {
  const rgba = (s) => {
    const m = s.match(/rgba?\(([^)]+)\)/);
    if (!m) throw new Error("not a colour: " + s);
    const [r, g, b, a = 1] = m[1].split(/[ ,/]+/).filter(Boolean).map(Number);
    return [r, g, b, a];
  };
  const over = (top, under) => {
    const a = top[3];
    return [0, 1, 2].map((i) => top[i] * a + under[i] * (1 - a)).concat(1);
  };
  // what is under el: its ancestors' backgrounds, deepest last, laid down
  // from the first opaque one
  const behind = (el, own) => {
    const layers = [];
    for (let e = el; e; e = e.parentElement) layers.push(rgba(getComputedStyle(e).backgroundColor));
    if (own) layers.unshift(rgba(getComputedStyle(own).backgroundColor));
    let i = layers.findIndex((c) => c[3] >= 1);
    if (i < 0) { i = layers.length; layers.push([255, 255, 255, 1]); }
    let c = layers[i];
    for (let j = i - 1; j >= 0; j--) c = over(layers[j], c);
    return c;
  };
  const lum = (c) => {
    const [r, g, b] = c.slice(0, 3).map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  const ratio = (x, y) => { const a = lum(x), b = lum(y); return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05); };
  const text = (el, bg) => ratio(over(rgba(getComputedStyle(el).color), bg), bg);

  const seg = document.querySelector("#nav"), thumb = seg.querySelector(".thumb");
  const track = behind(seg), onThumb = behind(seg, thumb);
  const out = {
    "other view on the track": Math.min(...[...seg.querySelectorAll("button:not(.on)")].map((b) => text(b, track))),
    "view shown on its thumb": text(seg.querySelector("button.on"), onThumb),
    "thumb against the track": ratio(onThumb, track),
  };
  // a text button on each surface it is put on
  for (const [name, bg] of [["page", ""], ["card", "var(--card)"], ["editor", "var(--card-2)"]]) {
    const box = document.createElement("div");
    if (bg) box.style.background = bg;
    const b = document.createElement("button");
    b.className = "text";
    b.textContent = "Refresh";
    box.append(b);
    document.querySelector("main, #view-agents, body").append(box);
    out[`text button on the ${name}`] = text(b, behind(b));
    box.remove();
  }
  return out;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the header's switch and text buttons read at WCAG AA", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) for (const [theme, scheme] of [["light", "light"], ["dark", "light"], ["system", "dark"]]) {
      await t.test(`${lang}, ${theme}${theme === "system" ? " (" + scheme + ")" : ""}`, async () => {
        const ctx = await browser.newContext({ viewport: { width: 1000, height: 640 }, colorScheme: scheme });
        const page = await ctx.newPage();
        page.on("pageerror", (e) => errors.push(e.message));
        let release;
        const gate = new Promise((r) => { release = r; });
        await page.route("**/*", server(lang, theme, gate));
        // the page as first drawn, app.js held back: already in its theme
        const loaded = page.goto("http://magpie.test/");
        await page.waitForSelector("#nav", { state: "attached" });
        const first = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
        release();
        await loaded;
        await page.waitForSelector("#nav button.on");
        await page.waitForFunction(() => document.querySelector("#nav .thumb")?.getBoundingClientRect().width > 0);
        assert.equal(first, await page.evaluate(() => getComputedStyle(document.body).backgroundColor), "the page was first drawn in another theme");
        // what is drawn once it has settled, not a colour on its way
        await page.evaluate(() => Promise.all(document.getAnimations().filter((a) => a.transitionProperty).map((a) => a.finished.catch(() => {}))));
        const r = await ratios(page), at = JSON.stringify(r);
        for (const [what, v] of Object.entries(r)) {
          if (what === "thumb against the track") continue;
          assert(v >= 4.7, `${what} reads at ${v.toFixed(2)}:1, under 4.5:1 with margin; ${at}`);
        }
        const dark = theme === "dark" || scheme === "dark";
        // before: 1.13 (light), 1.36 (dark, the thumb laid over the track)
        assert(r["thumb against the track"] >= (dark ? 1.5 : 1.2), `the thumb is hard to tell from the track (${r["thumb against the track"].toFixed(2)}:1); ${at}`);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
