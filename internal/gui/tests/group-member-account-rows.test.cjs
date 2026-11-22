// Run with Node's test runner and Playwright on the module path; see README.md.
// A group of many models, each sent on an account of its own (liaobode96 on
// X: twelve Antigravity models, each on a picked account, in a window ~690px
// wide). A picked account's chip is wider than "Any account", and the row
// then had more than one line's worth of chips: they wrapped onto a second
// line that the row's box did not grow for, so each row's account chip and
// its Up, Down and Remove were drawn over the next row, and the last row's
// under the list beside "Add another model". Every control of a row stays
// inside its own row, no two rows overlap, and the account chip ellipsizes
// a long address instead of pushing past the editor's edge: in the desktop
// app (whose row doesn't wrap) and the browser page, light and dark, at a
// wide and a narrow window, in every language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const P = "antigravity";
const names = ["Gemini 4 Nebula Pro (High)", "Gemini 4 Argon Pro Preview", "Gemini 4 Hydrogen Pro Thinking", "Gemini 3.8 Flash Experimental"];
const models = names.map((name, i) => ({
  id: `${P}/m${i}`, name, provider: P, providerName: "Antigravity", icon: "generic",
  efforts: ["low", "medium", "high", "xhigh"], images: true, context: 1000000,
}));
const emails = ["codyjayaraman.longname@gmail.com", "mealorsonja5@gmail.com", "kupravashako4@gmail.com"];
const ids = ["a1b2c3d4e5f60718", "0f1e2d3c4b5a6978", "1122334455667788"];
const accounts = { [P]: emails.map((name, i) => ({ id: `${P}/${ids[i]}`, name, provider: P, providerName: "Antigravity", account: true, plan: "Pro" })) };
// twelve members: each model on each account, the last three switched off
const members = [];
for (let i = 0; i < 12; i++) members.push(`${P}/m${i % 4}#${ids[Math.floor(i / 4)]}`);
const groups = () => ({
  models, pools: [], accounts,
  groups: [{ id: "pro", name: "Pro", members, off: members.slice(9), routing: "order", ready: true, accounts: [],
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = { en: "Edit", zh: "编辑", "zh-TW": "編輯", ja: "編集", de: "Bearbeiten" };

function serve(lang, theme, web) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}"${web ? ",web:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const [width, web, theme] of [[690, false, "light"], [1100, false, "dark"], [560, false, "dark"], [420, true, "light"], [1100, true, "dark"]]) {
      test(`${engine} ${lang} ${width}px ${web ? "browser" : "app"} ${theme}: each member's account and controls stay in its row`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${web ? "web" : "app"}-${theme}-group-member-account-rows.png`), fullPage: true });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme, web));
        await page.goto("http://magpie.test/?view=groups");
        const card = page.locator(".rt-group", { hasText: "Pro" });
        await card.waitFor();
        await card.locator("button", { hasText: words[lang] }).click();
        await page.locator(".rt-gedit .rt-macct.on").nth(11).waitFor();
        const got = await page.evaluate(() => {
          const out = [];
          const rows = [...document.querySelectorAll(".rt-gedit .fallback:not(.rt-rules) .fbl > .fbrow")];
          const box = (e) => e.getBoundingClientRect();
          const ed = box(document.querySelector(".rt-gedit"));
          rows.forEach((row, i) => {
            const r = box(row);
            for (const c of row.children) {
              const b = box(c);
              if (!b.width && !b.height) continue;
              if (b.top < r.top - 1 || b.bottom > r.bottom + 1 || b.left < r.left - 1 || b.right > r.right + 1)
                out.push(`row ${i + 1}: ${c.className || c.tagName} (${Math.round(b.left)},${Math.round(b.top)}–${Math.round(b.right)},${Math.round(b.bottom)}) is outside its row (${Math.round(r.left)},${Math.round(r.top)}–${Math.round(r.right)},${Math.round(r.bottom)})`);
            }
            if (r.right > ed.right + 1) out.push(`row ${i + 1} sticks past the editor by ${Math.round(r.right - ed.right)}px`);
            if (i && r.top < box(rows[i - 1]).bottom - 1) out.push(`row ${i + 1} overlaps row ${i}`);
          });
          const acct = document.querySelector(".rt-gedit .rt-macct");
          const span = acct.firstElementChild;
          return { out, rows: rows.length, page: document.documentElement.scrollWidth - document.documentElement.clientWidth,
            cut: span.scrollWidth > span.clientWidth + 1 ? getComputedStyle(span).textOverflow : "fits" };
        });
        assert.equal(got.rows, 12);
        assert.deepEqual(got.out, []);
        assert(got.page <= 0, `the page scrolls sideways by ${got.page}px`);
        // an address too long for the row is cut with an ellipsis, not clipped bare
        assert(got.cut === "fits" || got.cut === "ellipsis", `the account's name is cut with ${got.cut}`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
