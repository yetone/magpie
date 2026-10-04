// Run with Node's test runner and Playwright on the module path; see README.md.
// #779: the Routing page's groups are put in the user's own order, as the
// Agents page's rows are (#57): a group's logos are its handle, with a grip
// in the row's margin drawn only while the row is under the pointer (always
// there it is clutter, the owner: 一直出现太丑了); dragging it moves the row,
// right-clicking the row (or clicking the handle) opens Move up and Move
// down, and Alt+↑/↓ moves it from the keyboard, which stays on it. Each move
// posts groups/arrange with every group by id, the removed ones last, and
// the list is drawn as the answer has it. No click moves the page. In
// English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "b/m", name: "m", providerName: "B", icon: "generic" },
  { id: "a/x", name: "x", providerName: "A", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const all = {
  one: { id: "one", name: "One", members: ["a/x"], ready: true, memberInfo: info(["a/x"]) },
  two: { id: "two", name: "Two", members: ["b/m"], ready: true, memberInfo: info(["b/m"]) },
  // its providers' logos stacked, wider than one logo's handle
  "auto-m": { id: "auto-m", name: "Model M", members: ["a/m", "b/m"], auto: true, ready: true,
    memberInfo: [{ id: "a/m", ready: true, provider: "a", icon: "deepseek-color" }, { id: "b/m", ready: true, provider: "b", icon: "qoder" }] },
  "auto-z": { id: "auto-z", name: "auto-z", members: [], auto: true, hidden: true, memberInfo: [] },
};

const words = {
  en: { saved: "Group order saved", up: "Move up", down: "Move down" },
  zh: { saved: "路由组顺序已保存", up: "上移", down: "下移" },
};

function serve(lang, posts) {
  let order = ["one", "two", "auto-m", "auto-z"];
  const groups = () => ({ models, pools: [], deciders: [], found: true, groups: order.map((id) => all[id]) });
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/groups/arrange") {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push(body.order);
      order = body.order;
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const listed = (page) => page.locator(".rt-groups > .rt-group").evaluateAll((rs) => rs.map((r) => r.dataset.id));
// each row's margin grip: what it draws, how much shows, and where
const grips = (page) => page.locator(".rt-groups > .rt-group").evaluateAll((rs) => rs.map((r) => {
  const h = r.querySelector(".rt-ghandle"), s = getComputedStyle(h, "::before"), hb = h.getBoundingClientRect(), rb = r.getBoundingClientRect();
  const left = hb.left + parseFloat(s.left), w = parseFloat(s.width);
  return { id: r.dataset.id, image: s.backgroundImage, opacity: s.opacity, inRow: left >= rb.left && left + w <= hb.left + 0.5, x: left + w / 2, y: hb.top + hb.height / 2 };
}));
const opacities = (page) => page.evaluate(() => [...document.querySelectorAll(".rt-groups > .rt-group .rt-ghandle")].map((h) => getComputedStyle(h, "::before").opacity));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: routing groups put in order by hand`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-arrange.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-groups > .rt-group").nth(2).waitFor();
      assert.deepEqual(await listed(page), ["one", "two", "auto-m"]);
      // a stack of logos is held whole in its handle, in its card, clear of
      // the name (ARNO on Discord: past the card's left edge, over the name)
      const fit = await page.locator('.rt-group[data-id="auto-m"]').evaluate((r) => {
        const b = (e) => e.getBoundingClientRect(), row = b(r), h = b(r.querySelector(".rt-ghandle")), st = b(r.querySelector(".ic-stack")), m = b(r.querySelector(".main"));
        return { stackIn: st.left >= h.left - 0.5 && st.right <= h.right + 0.5, inRow: h.left >= row.left, clear: h.right <= m.left };
      });
      assert.deepEqual(fit, { stackIn: true, inRow: true, clear: true });
      await page.locator(".rt-gsec").evaluate((x) => x.scrollIntoView({ block: "center" })); // as the reader would
      await page.waitForTimeout(200);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";"));
      const before = await at();
      const saved = () => page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), w.saved);

      // the grip: drawn by every row, shown by none with the pointer away,
      // by the row under it alone, in the row's margin, not over its logos
      await page.mouse.move(1, 1);
      let g = await grips(page);
      for (const x of g) {
        assert.match(x.image, /gradient/, `${x.id}'s grip draws nothing`);
        assert.equal(x.opacity, "0", `${x.id}'s grip is drawn with the pointer away`);
        assert(x.inRow, `${x.id}'s grip is off its row or over its logos: ${JSON.stringify(x)}`);
      }
      await page.locator(".rt-groups > .rt-group").nth(1).locator(".main").hover();
      await page.waitForFunction(() => {
        const os = [...document.querySelectorAll(".rt-groups > .rt-group .rt-ghandle")].map((h) => getComputedStyle(h, "::before").opacity);
        return os[1] === "1" && os.every((o, i) => i === 1 || o === "0");
      });
      await page.mouse.move(1, 1);
      await page.waitForFunction(() => [...document.querySelectorAll(".rt-groups > .rt-group .rt-ghandle")].every((h) => getComputedStyle(h, "::before").opacity === "0"));
      assert.deepEqual(await opacities(page), ["0", "0", "0"]);

      // right-click: Move up is off on the first, Move down moves it, and
      // the order sent has every group, the removed one last
      await page.locator(".rt-group[data-id=one] .main").click({ button: "right" });
      const menu = page.locator(".row-menu");
      await menu.waitFor();
      assert.equal(await menu.getByRole("menuitem", { name: w.up }).isDisabled(), true, "Move up on the first group");
      await menu.getByRole("menuitem", { name: w.down }).click();
      await saved();
      assert.deepEqual(posts.at(-1), ["two", "one", "auto-m", "auto-z"]);
      assert.deepEqual(await listed(page), ["two", "one", "auto-m"]);
      assert.equal(await page.locator(".rt-gedit").count(), 0, "the right-click opened the editor");

      // Alt+↓ on a focused row's handle moves it, and the keyboard stays on it
      await page.locator(".rt-group[data-id=one] .rt-ghandle").focus();
      await page.keyboard.press("Alt+ArrowDown");
      await page.waitForFunction(() => [...document.querySelectorAll(".rt-groups > .rt-group")].map((r) => r.dataset.id).join() === "two,auto-m,one");
      assert.deepEqual(posts.at(-1), ["two", "auto-m", "one", "auto-z"]);
      await page.waitForFunction(() => document.activeElement?.closest(".rt-group")?.dataset.id === "one");
      await page.keyboard.press("Alt+ArrowUp");
      await page.waitForFunction(() => [...document.querySelectorAll(".rt-groups > .rt-group")].map((r) => r.dataset.id).join() === "two,one,auto-m");
      assert.deepEqual(posts.at(-1), ["two", "one", "auto-m", "auto-z"]);

      // a click on the handle opens the same menu, not the editor
      await page.mouse.move(1, 1);
      await page.locator(".rt-group[data-id=auto-m] .rt-ghandle").click();
      await menu.waitFor();
      assert.equal(await menu.getByRole("menuitem", { name: w.down }).isDisabled(), true, "Move down on the last group");
      assert.equal(await page.locator(".rt-gedit").count(), 0, "the handle opened the editor");
      await menu.getByRole("menuitem", { name: w.up }).click();
      await page.waitForFunction(() => [...document.querySelectorAll(".rt-groups > .rt-group")].map((r) => r.dataset.id).join() === "two,auto-m,one");
      assert.deepEqual(posts.at(-1), ["two", "auto-m", "one", "auto-z"]);

      // dragging by the grip moves the row to the top
      await page.mouse.move(1, 1);
      g = await grips(page);
      const n = posts.length;
      await page.mouse.move(g[2].x, g[2].y);
      await page.mouse.down();
      await page.mouse.move(g[2].x, g[0].y - 10, { steps: 12 });
      await page.locator(".rt-groups > .rt-group.dragging").waitFor();
      await page.mouse.up();
      await page.waitForFunction(() => document.querySelector(".rt-groups > .rt-group")?.dataset.id === "one");
      assert.equal(posts.length, n + 1);
      assert.deepEqual(posts.at(-1), ["one", "two", "auto-m", "auto-z"]);
      assert.equal(await page.locator(".rt-gedit").count(), 0, "letting go opened the editor");
      assert.equal(await page.locator(".row-menu").count(), 0, "letting go opened the menu");

      assert.equal(await at(), before, "a click moved the page");
      const missing = await page.evaluate(() => [
        "Group order saved", "Move up", "Move down", "Arrange {agent}",
        "Drag to reorder — agents' model lists show the groups in this order · Alt+↑/↓ to move",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
