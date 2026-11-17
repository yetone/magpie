// Run with Node's test runner and Playwright on the module path; see README.md.
// The sidebar's torch (navlamp.js) lights a row's icon as well as its word:
// a sub-list row's icon (Settings' General, Appearance…) and a page row's
// (Providers), and Settings' gear. Under the pointer the icon is lit as
// the word's heart is, near the text's own ink and well off its resting
// --muted; with the pointer at the word's far end it is lit less; and once
// the pointer leaves it is back to --muted. The selected row's icon is
// never lit. Both engines, every language, light and dark, at the
// narrowest width the sidebar shows and a wide one.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, theme) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:${JSON.stringify(theme)},web:false,sideGateway:{url:"http://127.0.0.1:3425",running:true}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/settings") return json({ lang, theme, tray: "panel", version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", lanURLs: [], visionModels: [], imageGenModels: [], workbuddyCheckins: [], redactWords: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// an icon's state: its box, its colour as sRGB, whether lit, and the
// colours it is judged against (--muted, its resting ink; --fg, the text's)
const look = (page, sel) => page.$eval(sel, (ic) => {
  const c = document.createElement("canvas").getContext("2d", { willReadFrequently: true });
  const rgb = (v) => { c.clearRect(0, 0, 1, 1); c.fillStyle = "#010203"; c.fillStyle = v; c.fillRect(0, 0, 1, 1); return [...c.getImageData(0, 0, 1, 1).data.slice(0, 3)]; };
  const root = getComputedStyle(document.documentElement), b = ic.getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2, lit: ic.classList.contains("lit"), raw: getComputedStyle(ic).color, color: rgb(getComputedStyle(ic).color), muted: rgb(root.getPropertyValue("--muted").trim()), fg: rgb(root.getPropertyValue("--fg").trim()) };
});
const far = (a, b) => Math.hypot(a[0] - b[0], a[1] - b[1], a[2] - b[2]);

async function lights(page, sel, word, label) {
  await page.mouse.move(700, 600);
  await page.waitForFunction((s) => !document.querySelector(s).classList.contains("lit"), sel);
  await page.waitForTimeout(300); // its own colour transition settling
  const rest = await look(page, sel);
  assert(far(rest.color, rest.muted) < 6, `${label} rests in --muted: ${JSON.stringify(rest)}`);
  const span = far(rest.muted, rest.fg);
  // under the pointer: lit as the word's heart
  await page.mouse.move(rest.x, rest.y);
  await page.waitForFunction((s) => { const e = document.querySelector(s); return e.classList.contains("lit") && +e.style.getPropertyValue("--si") > 0.99; }, sel);
  await page.waitForTimeout(400); // the light catching the pointer up
  const on = await look(page, sel);
  assert(far(on.color, on.muted) > span * 0.6 && far(on.color, on.fg) < span * 0.6, `${label} lit under the pointer: ${JSON.stringify(on)}`);
  // at the word's far end: lit less
  const w = await page.$eval(word, (s) => { const b = s.getBoundingClientRect(); return { x: b.right - 2, y: b.top + b.height / 2 }; });
  await page.mouse.move(w.x, w.y, { steps: 6 });
  await page.waitForTimeout(600);
  const end = await look(page, sel);
  assert(far(end.color, end.muted) + 8 < far(on.color, on.muted), `${label} lit less with the pointer at the word's end: ${JSON.stringify({ on: on.color, end: end.color })}`);
  // gone: back to --muted
  await page.mouse.move(700, 600);
  await page.waitForFunction((s) => !document.querySelector(s).classList.contains("lit"), sel);
  await page.waitForTimeout(300);
  const back = await look(page, sel);
  assert(far(back.color, back.muted) < 6, `${label} back in --muted: ${JSON.stringify(back)}`);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the torch lights a sidebar row's icon too`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const langs = ["en", "zh", "zh-TW", "ja", "de"];
    for (const [i, lang] of langs.entries()) for (const width of [1024, 1440]) {
      const theme = (i + width) % 2 ? "dark" : "light";
      await t.test(`${lang} ${width}px ${theme}`, async () => {
        const context = await browser.newContext({ viewport: { width, height: 800 }, colorScheme: theme });
        t.after(() => context.close());
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme));
        await page.goto("http://magpie.test/?view=settings");
        await page.waitForSelector("body.side-nav .nav-sub > .nav-s + .nav-s");
        await page.mouse.move(40, 300); // over the sidebar, which loads the lamp
        await page.waitForFunction(() => document.documentElement.classList.contains("navlamp"));
        await page.waitForTimeout(600); // the list's own unrolling

        // a sub-list row with an icon, not the selected one
        const n = await page.$$eval(".nav-sub > .nav-s", (all) => all.findIndex((b) => !b.classList.contains("cur") && b.querySelector(":scope > svg") && (b.dataset.lampTest = "1")));
        assert(n >= 0, "a sub-list row with an icon");
        const row = `.nav-sub > .nav-s[data-lamp-test]`;
        await lights(page, `${row} > svg`, `${row} > span`, "a sub-list row's icon");
        // a page row's (Providers); Settings' gear is open here, so it is
        // tried from another page below
        await lights(page, `#nav > button[data-view="providers"] > .nav-ic`, `#nav > button[data-view="providers"] > span`, "a page row's icon");

        // the selected sub-row's icon is never lit
        const cur = await page.$(".nav-sub > .nav-s.cur > svg");
        if (cur) {
          const b = await cur.boundingBox();
          await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
          await page.waitForTimeout(200);
          assert(!(await cur.evaluate((e) => e.classList.contains("lit"))), "the selected row's icon keeps its own ink");
        }

        await page.mouse.move(700, 600);
        await page.goto("http://magpie.test/?view=agents");
        await page.waitForSelector("body.side-nav #prefs > svg");
        await page.mouse.move(40, 300);
        await page.waitForFunction(() => document.documentElement.classList.contains("navlamp"));
        await lights(page, "#prefs > svg", "#prefs > span", "Settings' gear");
        // the open page's icon is never lit
        const open = await page.$("#nav > button.on > .nav-ic");
        const b = await open.boundingBox();
        await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
        await page.waitForTimeout(200);
        assert(!(await open.evaluate((e) => e.classList.contains("lit"))), "the open page's icon keeps its own ink");
        assert.deepEqual(errors, []);
      });
    }
  });
}
