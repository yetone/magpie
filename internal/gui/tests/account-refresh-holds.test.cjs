// Run with Node's test runner and Playwright on the module path; see README.md.
// 墨雨藏锋 (Discord, 11-09), a plugin subscription's editor with several
// accounts: "供应商的卡片点击刷新额度的时候会把窗口往下挪一点 然后…窗口太矮了
// 基本只能放几行账号".
// A refresh redraws the editor, and every account's refresh has the same
// label: the focus given back went to the first account's, and holding
// that one instead of the pressed one let the pressed row move as the rows
// above it grew. The pressed account's refresh stays where it was, and
// keeps the focus. And a sheet pressed while short, then grown to the
// window's foot, held low with its accounts cut to a few rows, rises into
// the room above as they are scrolled, lifted by the scroll before them.
// In every language, wide, narrow and in a phone's browser, both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_GUI_ASSETS || path.resolve(__dirname, "../assets"));
const prov = (users) => ({
  id: "monkeycode", name: "MonkeyCode", icon: "", chat: "", responses: "", anthropic: "", catalog: "", routing: "",
  models: [{ id: "m", name: "", on: true }], agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
  account: { agent: "monkeycode", agentName: "MonkeyCode", user: users[0], plan: "pro", logins: users.map((user, i) => ({ user, plan: "pro", active: i === 0, on: true })) },
});
// read again, an account shows six pools, its line wrapping at any width
const windows = (pools) => ({ windows: pools
  ? ["Alpha", "Beta", "Gamma", "Delta", "Epsilon", "Zeta"].flatMap((pool, k) => ["5h", "7d"].map((name, j) => ({ name, pool, members: [pool + " one", pool + " two"], used: 10 + k * 5 + j * 20, limit: 100 })))
  : [{ name: "5h", used: 12, limit: 100 }, { name: "7d", used: 40, limit: 100 }] });
const serve = (lang, web, ctl) => async (route) => {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:${web}};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
  if (url.pathname === "/api/providers") return json({ providers: [prov(ctl.users)], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [{ id: "monkeycode", pid: "monkeycode", name: "MonkeyCode", spec: "@magpie-community/opencode-monkeycode-auth" }] });
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname === "/api/login/usage") return json(Object.fromEntries(ctl.users.map((u, i) => [u, ctl.usage(i)]).filter(([, q]) => q)));
  if (url.pathname === "/api/usage/quotas/refresh") {
    ctl.refreshed = true;
    await new Promise((resolve) => setTimeout(resolve, 250));
    return json([{ provider: "monkeycode", user: url.searchParams.get("user"), windows: [{ name: "5h", used: 64, limit: 100 }] }]);
  }
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
};
const sizes = [[false, 1000], [false, 560], [true, 390]];
const langs = ["en", "zh", "zh-TW", "ja", "de"];
async function open(browser, lang, web, width, height, ctl) {
  const context = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, web, ctl));
  await page.goto("http://magpie.test/?view=providers");
  await page.locator(".row.provider", { hasText: "MonkeyCode" }).first().click();
  await page.locator(`.editor .acc[data-account-id="${ctl.users.at(-1)}"] .quota-refresh`).waitFor();
  await page.waitForFunction(() => !loginUsage.monkeycode?.loading);
  await page.waitForTimeout(400);
  return { context, page, errors };
}
const settled = (page) => page.waitForFunction(() => !document.querySelector(".editor .quota-refresh.busy") && !loginUsage.monkeycode?.loading).then(() => page.waitForTimeout(400));

for (const [engine, browserType] of [["chromium", chromium], ["webkit", webkit]]) {
  test(engine + ": an account's refresh stays where it was pressed, with the focus", async (t) => {
    const browser = await browserType.launch();
    t.after(() => browser.close());
    for (const [web, width] of sizes) for (const lang of langs) {
      const at = `${lang} ${web ? "web " : ""}${width}`;
      const ctl = { users: Array.from({ length: 12 }, (_, i) => `u${i}@example.com`), refreshed: false };
      // read again, the two accounts above the pressed one take more room
      ctl.usage = (i) => windows(ctl.refreshed && i < 2);
      const { context, page, errors } = await open(browser, lang, web, width, 760, ctl);
      await page.evaluate(() => { const b = document.querySelector(".editor .ebody"); b.scrollTop = 80; });
      await page.waitForTimeout(200);
      const tall = () => page.locator('.editor .acc[data-account-id="u0@example.com"]').evaluate((r) => r.getBoundingClientRect().height);
      const was = await tall();
      const button = page.locator('.editor .acc[data-account-id="u3@example.com"] .quota-refresh');
      const before = await button.evaluate((b) => b.getBoundingClientRect().top);
      await page.evaluate(() => {
        window.__seen = [];
        const tick = () => {
          const b = document.querySelector('.editor .acc[data-account-id="u3@example.com"] .quota-refresh');
          if (b) window.__seen.push([b.getBoundingClientRect().top, document.querySelector("#modal").firstElementChild.getBoundingClientRect().top]);
          window.__raf = requestAnimationFrame(tick);
        };
        tick();
      });
      const dialogTop = await page.evaluate(() => document.querySelector("#modal").firstElementChild.getBoundingClientRect().top);
      const box = await button.boundingBox();
      await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
      await page.waitForFunction(() => document.querySelector(".editor .quota-refresh.busy"));
      await settled(page);
      const seen = await page.evaluate(() => { cancelAnimationFrame(window.__raf); return window.__seen; });
      const grew = await tall() - was;
      assert.ok(grew >= 10, `${engine} ${at}: the rows above grew (${grew}px)`);
      const off = Math.max(...seen.map(([y]) => Math.abs(y - before)));
      assert.ok(off <= 1, `${engine} ${at}: the pressed refresh moved ${off}px (from ${before})`);
      const moved = Math.max(...seen.map(([, y]) => Math.abs(y - dialogTop)));
      assert.ok(moved <= 1, `${engine} ${at}: the dialog moved ${moved}px`);
      const focused = await page.evaluate(() => document.activeElement?.classList.contains("quota-refresh") ? document.activeElement.closest(".acc")?.dataset.accountId : null);
      if (engine === "chromium") assert.equal(focused, "u3@example.com", `${engine} ${at}: the focus stays on the pressed refresh`);
      else assert.ok(focused == null || focused === "u3@example.com", `${engine} ${at}: the focus went to ${focused}'s refresh`);
      assert.deepEqual(errors, [], `${engine} ${at}: page errors`);
      await context.close();
    }
  });

  test(engine + ": a sheet pressed while short rises into the room as its accounts scroll", async (t) => {
    const browser = await browserType.launch();
    t.after(() => browser.close());
    for (const [web, width] of sizes) for (const lang of langs) {
      const at = `${lang} ${web ? "web " : ""}${width}`;
      const ctl = { users: Array.from({ length: 12 }, (_, i) => `u${i}@example.com`), refreshed: false };
      // no readings at first; read again, every account's line wraps
      ctl.usage = () => ctl.refreshed ? windows(true) : null;
      const { context, page, errors } = await open(browser, lang, web, width, 900, ctl);
      // a window with room for the sheet and 160px more
      const need = await page.evaluate(() => { const m = document.querySelector("#modal"), d = m.firstElementChild, b = d.querySelector(".ebody"), c = getComputedStyle(m); return d.offsetHeight + b.scrollHeight - b.clientHeight + parseFloat(c.paddingTop) + parseFloat(c.paddingBottom); });
      await page.setViewportSize({ width, height: Math.ceil(need) + 160 });
      await page.waitForTimeout(300);
      const d = () => page.evaluate(() => { const r = document.querySelector("#modal").firstElementChild.getBoundingClientRect(); return { top: r.top, bottom: r.bottom }; });
      const short = await d();
      assert.ok(short.top > 60, `${engine} ${at}: the sheet opens short, centred (top ${short.top})`);
      await page.locator('.editor .acc[data-account-id="u0@example.com"] .quota-refresh').click();
      await page.waitForFunction(() => document.querySelector(".editor .quota-refresh.busy"));
      await settled(page);
      const low = await d();
      const body = page.locator(".editor .ebody");
      const over = await body.evaluate((b) => b.scrollHeight - b.clientHeight);
      assert.ok(over > 200, `${engine} ${at}: the accounts outgrew the sheet (${over})`);
      assert.ok(Math.abs(low.top - short.top) <= 1, `${engine} ${at}: the press held the sheet (${short.top} → ${low.top})`);
      // the scroll lifts the sheet with its accounts, its foot staying
      const row = page.locator('.editor .acc[data-account-id="u1@example.com"]');
      const rowAt = await row.evaluate((r) => r.getBoundingClientRect().top);
      await body.evaluate((b) => { b.scrollTop += 40; });
      await page.waitForTimeout(150);
      const rose = await d();
      assert.ok(rose.top <= low.top - 39, `${engine} ${at}: the sheet rose as its accounts scrolled (${low.top} → ${rose.top})`);
      assert.ok(Math.abs(rose.bottom - low.bottom) <= 1, `${engine} ${at}: its foot stayed (${low.bottom} → ${rose.bottom})`);
      const rowNow = await row.evaluate((r) => r.getBoundingClientRect().top);
      assert.ok(Math.abs(rowNow - (rowAt - 40)) <= 1, `${engine} ${at}: the accounts moved as scrolled (${rowAt} → ${rowNow})`);
      assert.equal(await body.evaluate((b) => b.scrollTop), 0, `${engine} ${at}: the sheet rose before its accounts scrolled`);
      // scrolled on, it takes the whole room it needs, centred no higher
      await body.evaluate((b) => { b.scrollTop += 2000; });
      await page.waitForTimeout(150);
      const top = await d();
      const pad = await page.evaluate(() => parseFloat(getComputedStyle(document.querySelector("#modal")).paddingTop));
      assert.ok(Math.abs(top.top - pad) <= 1, `${engine} ${at}: the sheet took the room above (top ${top.top}, padding ${pad})`);
      assert.deepEqual(errors, [], `${engine} ${at}: page errors`);
      await context.close();
    }
  });
}
