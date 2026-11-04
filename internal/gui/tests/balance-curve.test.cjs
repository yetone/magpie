// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's balance over time (TJHHHH on Discord): under a balance's figure on
// the Usage page, its readings as a line, the least-squares line since the
// last top-up dashed on to zero when that is within what is shown, and
// "Runs out in 3d at this pace" in the curve's head, with what is spent a
// day in its legend. A click on the plot makes it larger, another back, and
// neither moves the page; Enter does the same. A key of one reading has no
// curve, and the allowances' "Off" takes it away too. A plugin account's
// balance alone (Raccoon's credits) has the same curve.
// English and Chinese, light and dark; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();

function fixtures(now) {
  // a top-up to ¥50 two days ago, ¥10 a day spent since
  const points = [[96, 5], [72, 3], [48, 50], [36, 45], [24, 40], [12, 35], [0, 30]].map(([h, v]) => ({ at: iso(now - h * H), amount: v }));
  return [
    { provider: "deepseek", name: "DeepSeek", icon: "deepseek", windows: [], balance: "¥30.00", readAt: iso(now),
      balanceTrend: { points, fitFrom: iso(now - 48 * H), fitStart: 50, fitNow: 30, perDay: 10, runsOut: iso(now + 72 * H) } },
    { provider: "moonshot", name: "Moonshot", icon: "moonshot", windows: [], balance: "$12.00", readAt: iso(now),
      balanceTrend: { points: [{ at: iso(now), amount: 12 }] } },
    // a plugin's account told by its balance alone, as Raccoon's is when its
    // bills don't add up to it (corddis_114 on Discord): the same curve
    { provider: "raccoon", name: "Raccoon", icon: "raccoon", user: "138****0000", plan: "Free", windows: [], balance: "5900 credits", readAt: iso(now),
      balanceTrend: { points: [[48, 6122], [24, 6000], [0, 5900]].map(([h, v]) => ({ at: iso(now - h * H), amount: v })) } },
  ];
}

function serve(lang, theme, quotas, colorblind = false) {
  const settings = { theme, lang, tray: "panel", quotaLeft: false, currency: "usd", colorblind };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true,colorblind:${colorblind}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { head: "Balance over time", out: "Runs out in 3d at this pace", balance: "Balance", pace: "about ¥10.00 a day" },
  zh: { head: "余额走势", out: "按此速度，预计 3 天后用完", balance: "余额", pace: "约每天 ¥10.00" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a balance's card draws it over time and says when it runs out`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [name, p] of pages) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-balance-curve-${name}.png`), fullPage: true });
        }
        await browser.close();
      });
      const errors = [];
      const open = async (name, theme, range) => {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 520 }, reducedMotion: "reduce" })).newPage();
        pages.push([name, page]);
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        if (range) await page.addInitScript((r) => localStorage.setItem("magpie.quotaRange", r), range);
        await page.route("**/*", serve(lang, theme, fixtures(Date.now())));
        await page.goto("http://magpie.test/?view=usage");
        return page;
      };

      for (const theme of ["light", "dark"]) {
        const page = await open(theme, theme);
        const card = page.locator(".subscription-card", { hasText: "DeepSeek" });
        const curve = card.locator(".balance-curve");
        await curve.waitFor();
        assert.equal(await curve.locator(".qc-head > span").first().textContent(), w.head);
        assert.equal(await curve.locator(".qc-range").textContent(), w.out);
        assert.deepEqual(await curve.locator(".qc-key").allTextContents(), [w.balance + "¥30.00", w.pace]);
        assert.equal(await curve.locator("path.qc-line").count(), 1);
        assert.equal(await curve.locator("line.qc-now").count(), 1, "now, with the dashes carried past it");
        // the fit, from the top-up to zero at the plot's right edge
        const fit = curve.locator("line.bc-fit");
        assert.equal(await fit.count(), 1);
        const [x1, y1, x2, y2] = await fit.evaluate((e) => ["x1", "y1", "x2", "y2"].map((a) => +e.getAttribute(a)));
        assert.ok(Math.abs(x2 - 300) < 0.5 && Math.abs(y2 - 60) < 0.5, `the dashes end at zero on the right: ${x2},${y2}`);
        assert.ok(x1 > 50 && x1 < 150 && y1 < 10, `they start at the top-up: ${x1},${y1}`);
        assert.ok(await page.locator("#quotaHead #quotaTrend").isVisible(), "the range pick is there for balances alone too");
        assert.equal(await page.locator(".subscription-card", { hasText: "Moonshot" }).locator(".balance-curve").count(), 0, "one reading, no curve");
        const wallet = page.locator(".subscription-card", { hasText: "Raccoon" }).locator(".balance-curve");
        assert.equal(await wallet.count(), 1, "an account's balance alone has its curve too");
        assert.equal(await wallet.locator("path.qc-line").count(), 1);
        assert.equal(await page.locator(".quota-curve select, .quota-curve button").count(), 0, "no control on the card");
        const left = await curve.evaluate((e) => [...e.querySelectorAll("*")].filter((x) => parseFloat(getComputedStyle(x).borderLeftWidth) > 0).length);
        assert.equal(left, 0, "no left borders");
        const stroke = await curve.locator("path.qc-line").evaluate((e) => getComputedStyle(e).stroke);
        const c1 = await page.evaluate(() => { const s = document.createElement("i"); s.style.color = "var(--c1)"; document.body.append(s); const c = getComputedStyle(s).color; s.remove(); return c; });
        assert.equal(stroke, c1, "the theme's chart colour");

        // a click enlarges it, another takes it back; the page stays put
        const plot = curve.locator(".qc-plot");
        const small = (await plot.boundingBox()).height;
        const top = await page.evaluate(() => { const s = document.scrollingElement; s.scrollTop = 30; return s.scrollTop; });
        await plot.click();
        assert.equal(await plot.getAttribute("aria-expanded"), "true");
        const big = (await plot.boundingBox()).height;
        assert.ok(big >= small * 2.5, `enlarged: ${small} → ${big}`);
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top, "the click moved the page");
        const box = await card.boundingBox(), pb = await plot.boundingBox();
        assert.ok(pb.x >= box.x && pb.x + pb.width <= box.x + box.width + 0.5, "the large plot fits its card");
        await plot.click();
        assert.equal(await plot.getAttribute("aria-expanded"), "false");
        assert.ok(Math.abs((await plot.boundingBox()).height - small) < 1, "back to its size");
        await plot.focus();
        await page.keyboard.press("Enter");
        assert.equal(await plot.getAttribute("aria-expanded"), "true", "Enter enlarges it too");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top, "Enter moved the page");
      }

      // the allowances' Off takes the balance's curve away too
      const off = await open("off", "light", "off");
      await off.locator(".subscription-card", { hasText: "DeepSeek" }).locator(".quota-balance").waitFor();
      assert.equal(await off.locator(".balance-curve").count(), 0);
      assert.ok(await off.locator("#quotaHead #quotaTrend").isVisible(), "and can be turned on again");
      assert.deepEqual(errors, []);
    });
  }
}

// Colorblind mode (#2073) dashes a quota curve's windows by their place in
// it; the balance curve has one line, kept solid beside its dashed fit, and
// its legend keeps its dot in the line's colour.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: colorblind mode leaves the balance curve's solid line, dashed fit and dot as they were`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const errors = [];
    const read = async (theme, colorblind) => {
      const page = await (await browser.newContext({ viewport: { width: 900, height: 520 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve("en", theme, fixtures(Date.now()), colorblind));
      await page.goto("http://magpie.test/?view=usage");
      const curve = page.locator(".subscription-card", { hasText: "DeepSeek" }).locator(".balance-curve");
      await curve.waitFor();
      const got = await curve.evaluate((c) => {
        const line = getComputedStyle(c.querySelector("path.qc-line")), fit = getComputedStyle(c.querySelector("line.bc-fit"));
        const dot = getComputedStyle(c.querySelector(".qc-key i"));
        return { cb: document.documentElement.dataset.colorblind || "", line: line.strokeDasharray, stroke: line.stroke, fit: fit.strokeDasharray,
          dot: dot.display, dotColor: dot.backgroundColor, dotShadow: dot.boxShadow, title: c.title };
      });
      await page.context().close();
      return got;
    };
    for (const theme of ["light", "dark"]) {
      const plain = await read(theme, false), cb = await read(theme, true);
      assert.equal(cb.cb, "1");
      assert.equal(cb.line, "none", `the balance stays solid: ${JSON.stringify(cb)}`);
      assert.equal(cb.fit, plain.fit, "its fit keeps its dashes");
      assert.notEqual(cb.fit, "none");
      assert.notEqual(cb.dot, "none", "its legend keeps the dot");
      assert.equal(cb.dotShadow, "none", "a filled dot, not a ring");
      assert.equal(cb.dotColor, cb.stroke, `the dot is the line's colour: ${JSON.stringify(cb)}`);
      assert.equal(cb.title, plain.title, "its tooltip still says solid and dashed");
    }
    assert.deepEqual(errors, []);
  });
}
