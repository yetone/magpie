// Run with Node's test runner and Playwright on the module path; see README.md.
// Every Advanced fold is laid out as one (the owner, 11-27: 「这个布局对吗？
// 彻底避免这种垃圾布局。从根上避免这种垃圾布局再次出现。」). billion-context's
// fold had the Library toolbar's .lib-more class, an inline-flex row, so
// its summary sat vertically centred at the left and its content, indented
// 15px more, in a narrower column beside it. Now every fold is made by
// advFold (adv-fold-builder checks that) and measured here, opened, on the
// Settings tabs and Library's billion-context, Memory, subagent editor
// and rule editor: a block, its summary above what it folds, and on a
// page its content as wide as the card beside it, edge for edge. Both
// engines, every language, a wide and a narrow window.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { server } = require("./app-pages.cjs");

// open every fold in sight under root, then what is wrong with each
async function foldFaults(root) {
  const shown = (e) => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== "hidden";
  const folds = [...root.querySelectorAll("details")].filter(shown);
  for (const d of folds) d.open = true;
  await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  const out = [];
  for (const d of folds) {
    const s = d.querySelector(":scope > summary");
    const name = `${d.className || "details"} "${(s?.textContent || "").trim()}"`;
    const display = getComputedStyle(d).display;
    if (display !== "block") out.push(`${name}: display ${display}, not block`);
    const parts = [...d.children].filter((c) => c !== s && shown(c));
    if (!s || !parts.length) { out.push(`${name}: nothing shows in it opened`); continue; }
    const sr = s.getBoundingClientRect(), rs = parts.map((c) => c.getBoundingClientRect());
    const top = Math.min(...rs.map((r) => r.top));
    if (sr.bottom > top + 0.5) out.push(`${name}: its summary ends ${(sr.bottom - top).toFixed(1)}px below its content's top`);
    // on a page: the card beside it, nearest first
    let list = null;
    for (const dir of ["previousElementSibling", "nextElementSibling"]) {
      for (let n = d[dir]; n && !list; n = n[dir]) if (n.matches(".list") && shown(n)) list = n;
      if (list) break;
    }
    if (!list) continue;
    const lr = list.getBoundingClientRect();
    const left = Math.min(...rs.map((r) => r.left)), right = Math.max(...rs.map((r) => r.right));
    if (Math.abs(left - lr.left) > 1) out.push(`${name}: its content's left edge is ${(left - lr.left).toFixed(1)}px off the card's beside it`);
    if (Math.abs(right - lr.right) > 1) out.push(`${name}: its content's right edge is ${(right - lr.right).toFixed(1)}px off the card's beside it`);
  }
  return { count: folds.length, out };
}

const LANGS = process.env.LANGS ? process.env.LANGS.split(",") : ["en", "zh", "zh-TW", "ja", "de"];
const WIDTHS = process.env.WIDTHS ? process.env.WIDTHS.split(",").map(Number) : [1180, 420];
const settled = (page) => page.waitForFunction(() => !document.querySelector("#modal").getAnimations({ subtree: true }).length);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": every Advanced fold is laid out as one", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      for (const width of WIDTHS) {
        await t.test(`${lang} ${width}px`, async (st) => {
          const ctx = await browser.newContext({ viewport: { width, height: 1600 }, reducedMotion: "reduce" });
          st.after(() => ctx.close());
          const page = await ctx.newPage();
          page.setDefaultTimeout(10000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, { folds: true }));
          const bad = [], seen = {};
          const check = async (where, root) => {
            const r = await page.evaluate(`(${foldFaults.toString()})(document.querySelector(${JSON.stringify(root)}))`);
            seen[where] = r.count;
            for (const f of r.out) bad.push(`${where}: ${f}`);
          };

          // Settings, tab by tab
          await page.goto("http://magpie.test/");
          await page.locator("#prefs").first().click();
          await page.locator("#view-settings .row").first().waitFor();
          const tabs = await page.evaluate(() => [...document.querySelectorAll("#setTabs [data-set]")].filter((b) => !b.hidden).map((b) => b.dataset.set));
          for (const tab of tabs) {
            await page.evaluate((id) => document.getElementById("setTab-" + id).click(), tab);
            await page.waitForTimeout(50);
            await check("settings › " + tab, `#setPage-${tab}`);
          }

          // Library's tabs that have a fold, each from a fresh load
          const libTab = async (id, ready) => {
            await page.evaluate((x) => localStorage.setItem("magpie.libTab", x), id);
            await page.goto("http://magpie.test/");
            await page.locator('#nav button[data-view="library"]').first().click();
            await page.locator(ready).first().waitFor({ timeout: 30000 });
            await page.evaluate(() => document.fonts.ready);
          };
          await libTab("bili", "#view-library .lib-bili-set");
          await check("library › billion-context", "#view-library");
          await libTab("memory", "#view-library .lib-memmore");
          await check("library › memory", "#view-library");
          for (const [id, row, ed] of [["subagents", ".lib-subagent", ".lib-subagentedit"], ["rules", ".lib-rule", ".lib-ruleedit"]]) {
            await libTab(id, `#view-library ${row}`);
            await page.locator(`#view-library ${row}`).first().click();
            await page.locator(`#modal ${ed}`).waitFor();
            await settled(page);
            await check(`library › ${id} editor`, "#modal");
          }

          for (const where of ["settings › usage", "settings › network", "library › billion-context", "library › memory", "library › subagents editor", "library › rules editor"]) {
            assert.ok(seen[where] >= 1, `no fold was found on ${where}`);
          }
          if (process.env.FOLDS_SEEN) console.log(lang, width, JSON.stringify(seen));
          assert.deepEqual(bad, [], `${bad.length} folds laid out wrong`);
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
