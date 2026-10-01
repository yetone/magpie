// Run with Node's test runner and Playwright on the module path; see README.md.
// #479: an agent row's handle (its logo, which turns into a grip on hover;
// a click opens Move up, Move down, Hide) showed nothing until the pointer
// was on it, so hiding an agent was found only by right-clicking, and a
// hidden one's way back, the Show button in the fold at the foot of the
// list, came up only with the pointer on its row. Now every row in the list
// carries a faint grip in its left margin, there with the pointer away: it is
// the handle's own, so dragging it moves the row and clicking it opens the
// menu. Hidden from that menu, the row goes into the fold, whose Show button
// is there without the pointer on it and brings the row back. No click moves
// the page. In the window and the tray panel, Chromium and WebKit, English
// and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture(lang) {
  const state = { agents: ["alpha", "beta", "gamma"].map((id) => ({ id, name: id, icon: "generic", path: "/fixture/" + id, fields: [] })), profiles: [], settings: { lang, theme: "light" } };
  const posted = [];
  const route = async (route) => {
    const request = route.request(), url = new URL(request.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/agents/arrange") {
      const body = request.postDataJSON();
      posted.push(body);
      Object.assign(state.settings, { agentOrder: body.order, agentsHidden: body.hidden, agentsShown: body.shown });
      return json(state.settings);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
  return { state, posted, route };
}

const order = (page) => page.locator("#agents > .row.agent").evaluateAll((rs) => rs.map((r) => r.dataset.id));
// the faint grip of each row in the list, as drawn with the pointer away
const grips = (page) => page.locator("#agents > .row.agent").evaluateAll((rs) => rs.map((r) => {
  const h = r.querySelector(".ag-handle"), s = getComputedStyle(h, "::before"), hb = h.getBoundingClientRect(), rb = r.getBoundingClientRect();
  const left = hb.left + parseFloat(s.left), w = parseFloat(s.width), ht = parseFloat(s.height);
  return {
    id: r.dataset.id, content: s.content, image: s.backgroundImage, opacity: Number(s.opacity), visibility: s.visibility, w, ht,
    inRow: left >= rb.left && left + w <= hb.left + 0.5, x: left + w / 2, y: hb.top + hb.height / 2,
  };
}));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an agent row shows it can be taken hold of, and a hidden one its way back", { timeout: 120000 }, async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) for (const mode of ["window", "panel"]) {
      await t.test(`${lang}, ${mode}`, async () => {
        const ctx = await browser.newContext({ viewport: mode === "panel" ? { width: 380, height: 560 } : { width: 1000, height: 640 }, reducedMotion: "reduce" });
        const page = await ctx.newPage(), f = fixture(lang);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", f.route);
        await page.goto(`http://magpie.test/?view=agents&mode=${mode}`);
        await page.locator("#agents > .row.agent").nth(2).waitFor();
        await page.mouse.move(1, 1);

        // with the pointer away, every row's grip is drawn, in its margin
        let g = await grips(page);
        for (const x of g) {
          assert.notEqual(x.content, "none", `${x.id} has no grip with the pointer away`);
          assert.match(x.image, /gradient/, `${x.id}'s grip draws nothing`);
          assert(x.opacity > 0 && x.visibility === "visible" && x.w >= 4 && x.ht >= 8, `${x.id}'s grip can't be seen: ${JSON.stringify(x)}`);
          assert(x.inRow, `${x.id}'s grip is off its row or over its logo: ${JSON.stringify(x)}`);
        }

        // dragging by the grip moves the row
        const before = await order(page);
        await page.mouse.move(g[0].x, g[0].y);
        await page.mouse.down();
        await page.mouse.move(g[0].x, g[1].y + 14, { steps: 12 });
        await page.locator("#agents > .row.dragging").waitFor();
        await page.mouse.up();
        await page.waitForFunction((id) => document.querySelector("#agents > .row.agent").dataset.id === id, before[1]);
        assert.deepEqual(f.posted.at(-1).order.slice(0, 3), [before[1], before[0], before[2]]);
        await page.mouse.move(1, 1);

        // a click on the grip opens the row's menu, which hides it
        g = await grips(page);
        const id = g[2].id, y0 = await page.evaluate(() => [scrollY, document.querySelector("#view-agents").scrollTop]);
        await page.mouse.click(g[2].x, g[2].y);
        const hide = await page.evaluate(() => t("Hide"));
        await page.getByRole("menuitem", { name: hide }).click();
        await page.waitForFunction((id) => !document.querySelector(`#agents > .row.agent[data-id="${id}"]`), id);
        assert.deepEqual(f.posted.at(-1).hidden, [id]);

        // the fold says something is hidden; opened, its Show is there with
        // the pointer away, and brings the row back
        const more = page.locator("#agents > .agent-more");
        assert.match(await more.textContent(), /1/);
        await more.click();
        await page.mouse.move(1, 1);
        const show = page.locator(`.agent-fold .row.put-away[data-id="${id}"] .ag-show`);
        await page.waitForFunction((id) => {
          const b = document.querySelector(`.agent-fold .row.put-away[data-id="${id}"] .ag-show`);
          return b && getComputedStyle(b).opacity === "1" && b.getBoundingClientRect().width > 0;
        }, id);
        assert(await show.isVisible());
        await show.click();
        await page.waitForFunction((id) => !!document.querySelector(`#agents > .row.agent[data-id="${id}"]`), id);
        assert.deepEqual(f.posted.at(-1).hidden, []);
        assert.deepEqual(await page.evaluate(() => [scrollY, document.querySelector("#view-agents").scrollTop]), y0, "a click moved the page");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
