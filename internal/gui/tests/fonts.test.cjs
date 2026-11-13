// Installed desktop fonts: real assets with an isolated API, in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");
const assets = process.env.MAGPIE_FONT_ASSETS || path.resolve(__dirname, "../assets");
const face = (family, name = "Regular", weight = 400, style = "normal", stretch = 100) => ({ family, name, weight, style, stretch });
const harmony = [face("HarmonyOS Sans SC"), face("HarmonyOS Sans SC", "Medium", 500), face("HarmonyOS Sans SC", "Bold", 700)];
const mono = [face("Consolas"), face("Consolas", "Italic", 400, "italic")];
const odd = face('Reader\'s "中文" \\ family');
const catalogue = [
  { name: harmony[0].family, styles: harmony }, { name: mono[0].family, styles: mono }, { name: odd.family, styles: [odd] },
  ...Array.from({ length: 200 }, (_, i) => ({ name: "Test Family " + i, styles: [face("Test Family " + i)] })),
];

async function open(browser, lang = "en", options = {}) {
  const context = await browser.newContext({ locale: lang === "zh" ? "zh-CN" : "en-US", viewport: options.viewport || { width: 1000, height: 820 } });
  const api = { posts: [], reads: 0, fonts: structuredClone(options.list || catalogue), fontError: false, saveError: false, delay: 0,
    cur: { lang, theme: "light", textSize: 100, currency: "usd", tray: "panel", version: "0.1.0", visionModels: [], imageGenModels: [], ...options.settings } };
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", e => { errors.push(e.message); console.error(e.message); });
  await page.route("**/*", async route => {
    const req = route.request(), url = new URL(req.url());
    const json = data => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs=${JSON.stringify({ ...api.cur, web: !!options.web, ...(options.omarchy ? { omarchy: { mode: "dark", stamp: "one", name: "Test theme", vars: { "--font": '"Theme Font", sans-serif', "--code": '"Theme Mono", monospace' } } } : {}) })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/fonts") {
      api.reads++;
      return api.fontError ? route.fulfill({ status: 503, body: "could not read installed fonts" }) : json(api.fonts);
    }
    if (url.pathname === "/api/settings") {
      if (options.settingsDelay) await new Promise(r => setTimeout(r, options.settingsDelay));
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        api.posts.push(body);
        if (api.delay) await new Promise(r => setTimeout(r, api.delay));
        if (api.saveError) return route.fulfill({ status: 400, body: "test save failed" });
        api.cur = { ...api.cur, ...body };
      }
      return json(api.cur);
    }
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: api.cur });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { return await route.fulfill({ body: await fs.readFile(file), contentType }); }
    catch { return route.fulfill({ status: 404, body: "not found" }); }
  });
  // every frame's rows, from the first paint (Norcleeh: a row flashed and went)
  if (options.frames) await page.addInitScript(() => {
    window.__frames = [];
    const ids = ["uiFontRow", "codeFontRow", "textSizeRow", "loginRow", "trayUsageRow"];
    const look = () => {
      const tray = document.getElementById("traySegs")?.parentElement;
      if (document.getElementById("uiFontRow")) window.__frames.push(Object.fromEntries([...ids.map(id => [id, !!document.getElementById(id)?.getClientRects().length]), ["tray", !!tray?.getClientRects().length]]));
      requestAnimationFrame(look);
    };
    requestAnimationFrame(look);
  });
  if (options.init) await page.addInitScript(options.init, options.arg);
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=general");
  await settingsDrawn(page); // rows found are the ones that stay (#1659)
  await page.locator("#themeSegs .opt").first().waitFor({ state: "attached" });
  // A real assertion, also usable against the original assets, rather than
  // a timeout or a compile error as the negative-control result.
  assert.equal(await page.locator("#uiFontRow").count(), 1, "Settings must offer an interface font picker");
  // (a row left hidden, as before 10-28 in a nogui magpie's tab, fails at its assertion, not here)
  await page.waitForFunction(() => fontList !== null || fontListError || document.getElementById("uiFontRow").hidden);
  await page.evaluate(async () => {
    await document.fonts.ready;
    await Promise.all(document.getAnimations().map(a => a.finished.catch(() => {})));
  });
  return { page, api, errors, close: () => context.close() };
}

async function family(page, row, name) {
  await page.locator(`#${row} .font-family`).click();
  await page.locator(".font-menu .sc-q").fill(name);
  await page.locator(".font-menu .pm-item").filter({ has: page.locator(".pm-name", { hasText: name }) }).first().click();
}
async function style(page, row, name) {
  await page.locator(`#${row} .font-style`).click();
  await page.locator(".font-menu .pm-item").filter({ has: page.locator(".pm-name", { hasText: name }) }).click();
}
async function reset(page, row) {
  await page.locator(`#${row} .font-family`).click();
  await page.locator(".font-menu .pm-item").first().click();
}
const settled = page => page.waitForFunction(() => prefsBusy === 0 && Object.keys(fontDrafts).length === 0);
const css = (page, selector) => page.locator(selector).evaluate(e => {
  const s = getComputedStyle(e);
  return { family: s.fontFamily, weight: s.fontWeight, style: s.fontStyle, stretch: s.fontStretch };
});
const engines = [["chromium", chromium], ["webkit", webkit]].filter(([name]) => !process.env.BROWSER || process.env.BROWSER === name);

for (const [engine, runtime] of engines) {
  for (const lang of ["en", "zh"]) test(`${engine} ${lang}: independent installed fonts, real styles, persistence and reset`, async () => {
    const browser = await runtime.launch();
    try {
      const { page, api, errors } = await open(browser, lang);
      const before = await css(page, "#codeFontRow .font-preview");
      assert.equal(await page.locator("select").count(), 0);
      await family(page, "uiFontRow", "HarmonyOS Sans SC");
      await style(page, "uiFontRow", "Medium");
      await settled(page);
      assert.equal(api.cur.uiFont.weight, 500);
      assert.match((await css(page, "body")).family, /^"?HarmonyOS Sans SC/);
      assert.equal((await css(page, "body")).weight, "500");
      assert.deepEqual(await css(page, "#codeFontRow .font-preview"), before);
      await family(page, "codeFontRow", "Consolas");
      await style(page, "codeFontRow", "Italic");
      await settled(page);
      assert.equal((await css(page, "#codeFontRow .font-preview")).style, "italic");
      assert.equal((await css(page, "body")).style, "normal");
      // Other saved preferences must carry both choices through the queue.
      await page.locator("#themeSegs .opt").nth(2).click();
      await settled(page);
      assert.equal(api.cur.uiFont.weight, 500);
      assert.equal(api.cur.codeFont.style, "italic");
      await page.reload();
      await page.locator("#uiFontRow .font-family").waitFor();
      await page.waitForFunction(() => fontList !== null && !fontFlight);
      await page.evaluate(async () => { await document.fonts.ready; await Promise.all(document.getAnimations().map(a => a.finished.catch(() => {}))); });
      assert.equal((await css(page, "body")).weight, "500");
      assert.equal((await css(page, "#codeFontRow .font-preview")).style, "italic");
      await family(page, "uiFontRow", "Consolas");
      await settled(page);
      assert.equal(api.cur.uiFont.name, "Regular", "a family without Medium chooses a regular face");
      await reset(page, "uiFontRow");
      await settled(page);
      assert.equal(api.cur.uiFont, null);
      assert.equal(api.cur.codeFont.style, "italic");
      await reset(page, "codeFontRow");
      await settled(page);
      assert.deepEqual(await css(page, "#codeFontRow .font-preview"), before);
      assert.deepEqual(errors, []);
    } finally { await browser.close(); }
  });

  test(`${engine}: queued choices and failed writes restore the saved face`, async () => {
    const browser = await runtime.launch();
    try {
      const { page, api, errors } = await open(browser);
      api.delay = 250;
      await family(page, "uiFontRow", "HarmonyOS Sans SC");
      await style(page, "uiFontRow", "Medium");
      await family(page, "codeFontRow", "Consolas");
      await settled(page);
      assert.equal(api.cur.uiFont.name, "Medium");
      assert.equal(api.cur.codeFont.family, "Consolas");
      api.saveError = true;
      await style(page, "uiFontRow", "Bold");
      await settled(page);
      assert.equal((await css(page, "body")).weight, "500");
      assert.equal(await page.locator("#uiFontRow .font-style span").textContent(), "Medium");
      assert.deepEqual(errors, []);
    } finally { await browser.close(); }
  });

  test(`${engine}: refresh errors, missing faces, empty lists and literal names`, async () => {
    const browser = await runtime.launch();
    try {
      const { page, api, errors } = await open(browser, "en", { settings: { uiFont: harmony[1] } });
      api.fontError = true;
      await page.locator("#uiFontRow .font-family").click();
      await page.getByRole("menuitemradio", { name: "Refresh fonts" }).click();
      await page.waitForFunction(() => fontListError);
      assert.equal((await css(page, "body")).weight, "500", "failed discovery must not erase the last collection");
      api.fontError = false;
      api.fonts = [{ name: "Consolas", styles: mono }, { name: odd.family, styles: [odd] }];
      await page.locator("#uiFontRow .font-family").click();
      await page.getByRole("menuitemradio", { name: /Refresh fonts/ }).click();
      await page.waitForFunction(() => !fontFlight && !fontListError);
      assert.match(await page.locator("#uiFontRow .font-note").textContent(), /unavailable/);
      assert.doesNotMatch((await css(page, "body")).family, /HarmonyOS/);
      assert.equal(api.cur.uiFont.family, harmony[0].family, "missing font choice stays saved");
      await family(page, "uiFontRow", odd.family);
      await settled(page);
      assert.equal(await page.locator("#uiFontRow .font-family span").textContent(), odd.family);
      assert.match((await css(page, "body")).family, /Reader/);
      api.fonts = [];
      await page.locator("#uiFontRow .font-family").click();
      await page.getByRole("menuitemradio", { name: "Refresh fonts" }).click();
      await page.waitForFunction(() => !fontFlight && fontList.length === 0);
      await reset(page, "uiFontRow");
      await settled(page);
      assert.equal(api.cur.uiFont, null);
      assert.match(await page.locator("#uiFontRow .font-note").textContent(), /No installed fonts/);
      assert.deepEqual(errors, []);
    } finally { await browser.close(); }
  });

  // #2439: the pointer resting on the menu while a search narrows it got a
  // mouseenter from the redraw, which took the focus out of the field, so
  // Enter chose "Refresh fonts" (the item now under the pointer).
  test(`${engine}: a search narrowing the menu under a still pointer keeps the typing in its field`, async () => {
    const browser = await runtime.launch();
    try {
      const { page, api, errors } = await open(browser, "zh", { viewport: { width: 560, height: 620 } });
      await page.locator("#uiFontRow .font-family").click();
      const second = await page.locator(".font-menu .pm-item").nth(1).boundingBox();
      await page.mouse.move(second.x + second.width / 2, second.y + second.height / 2);
      await page.locator(".font-menu .sc-q").fill("HarmonyOS");
      // the item now under the pointer is Refresh fonts. The redraw's
      // mouseenter comes on the engine's own hover timer (WebKit's is
      // later than a frame), and what is checked is that nothing came, so
      // there is no state to wait for: 300ms is past both engines' timers.
      await page.waitForTimeout(300);
      assert.equal(await page.evaluate(() => document.activeElement.className), "sc-q", "the search field keeps the focus");
      const reads = api.reads;
      await page.keyboard.press("Enter");
      await settled(page);
      assert.equal(await page.locator("#uiFontRow .font-family span").textContent(), "HarmonyOS Sans SC");
      assert.equal(api.reads, reads, "Enter didn't refresh the font list");
      // a pointer that moves still takes an item's focus
      await page.locator("#uiFontRow .font-family").click();
      const item = page.locator(".font-menu .pm-item").nth(2);
      const box = await item.boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2 + 1);
      assert.equal(await item.evaluate(e => e === document.activeElement), true, "a moved pointer focuses the item it's on");
      assert.deepEqual(errors, []);
    } finally { await browser.close(); }
  });

  test(`${engine}: font menu search and keys fit narrow windows without scrolling the page`, async () => {
    const browser = await runtime.launch();
    try {
      const { page, errors } = await open(browser, "zh", { viewport: { width: 560, height: 620 } });
      // the reader scrolls the row well into the short window, as Playwright's
      // click would otherwise scroll it in itself, by a pixel, and that is no reader's
      await page.mouse.move(280, 300);
      await page.mouse.wheel(0, 200);
      await page.waitForFunction(() => document.querySelector("#view-settings").scrollTop > 0);
      await page.waitForFunction(() => new Promise(r => { const v = document.querySelector("#view-settings"), a = v.scrollTop; setTimeout(() => r(v.scrollTop === a), 150); }));
      const top = await page.locator("#view-settings").evaluate(e => e.scrollTop);
      await page.locator("#uiFontRow .font-family").click();
      const menu = page.locator(".font-menu");
      const box = await menu.boundingBox();
      assert(box.x >= 0 && box.x + box.width <= 560 && box.y >= 0 && box.y + box.height <= 620);
      await menu.hover();
      await page.mouse.wheel(0, 240);
      await page.waitForFunction(() => document.querySelector(".font-menu").scrollTop > 0);
      assert.equal(await page.locator("#view-settings").evaluate(e => e.scrollTop), top);
      await page.locator(".font-menu .sc-q").fill("HarmonyOS");
      await page.keyboard.press("Enter");
      await settled(page);
      assert.equal(await page.locator("#uiFontRow .font-family span").textContent(), "HarmonyOS Sans SC");
      assert.equal(await page.locator("#view-settings").evaluate(e => e.scrollTop), top);
      await page.locator("#uiFontRow .font-style").click();
      await page.keyboard.press("ArrowDown");
      await page.keyboard.press("Enter");
      await settled(page);
      assert.equal(await page.locator("#uiFontRow .font-style span").textContent(), "Medium");
      await page.locator("#uiFontRow .font-style").click();
      await page.keyboard.press("Escape");
      assert.equal(await page.locator(".font-menu").count(), 0);
      for (const width of [440, 560, 1000]) {
        await page.setViewportSize({ width, height: 820 });
        assert(await page.locator("#view-settings").evaluate(e => e.scrollWidth <= e.clientWidth));
        for (const row of ["uiFontRow", "codeFontRow"]) {
          const bounds = await page.locator(`#${row} .font-controls`).boundingBox();
          assert(bounds.x >= 0 && bounds.x + bounds.width <= width, `${row} at ${width}`);
        }
      }
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await style(page, "uiFontRow", "Medium");
        await family(page, "codeFontRow", "Consolas");
        await settled(page);
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `fonts-${engine}-zh.png`) });
      }
      assert.deepEqual(errors, []);
    } finally { await browser.close(); }
  });

  test(`${engine}: explicit fonts override Omarchy and browser mode keeps its defaults`, async () => {
    const browser = await runtime.launch();
    try {
      const { page, errors } = await open(browser, "en", { omarchy: true, settings: { uiFont: harmony[1], codeFont: mono[0] } });
      assert.match((await css(page, "body")).family, /HarmonyOS/);
      await reset(page, "uiFontRow");
      await settled(page);
      assert.match((await css(page, "body")).family, /Theme Font/);
      assert.match((await css(page, "#codeFontRow .font-preview")).family, /Consolas/);
      assert.deepEqual(errors, []);
    } finally { await browser.close(); }
  });

  // Norcleeh on Discord: a browser tab had no font rows. It has them now,
  // with the families this browser draws, and its choice is its own.
  for (const lang of ["en", "zh"]) test(`${engine} ${lang}: a browser tab picks its own font from the ones it draws`, async () => {
    const browser = await runtime.launch();
    try {
      const real = ["Georgia", "Courier New", "DejaVu Serif", "Liberation Serif"];
      const list = [{ name: "No Such Family Here", styles: [face("No Such Family Here")] }, ...real.map(n => ({ name: n, styles: [face(n), face(n, "Bold", 700)] }))];
      const { page, api, errors } = await open(browser, lang, { web: true, settings: { uiFont: harmony[1] } });
      api.fonts = list;
      assert.equal(await page.locator("#uiFontRow").isVisible(), true, "a browser tab offers the interface font");
      assert.equal(await page.locator("#codeFontRow").isVisible(), true, "and the code font");
      assert.doesNotMatch((await css(page, "body")).family, /HarmonyOS/, "the app's own choice stays the app's");
      await page.locator("#uiFontRow .font-family").click();
      await page.locator(".font-menu .pm-item").last().click(); // Refresh fonts
      await page.waitForFunction(() => !fontFlight);
      const shown = await page.evaluate(() => fontList.map(f => f.name));
      assert(!shown.includes("No Such Family Here"), "a family this browser doesn't draw isn't offered: " + shown);
      const pick = shown[0];
      assert(real.includes(pick), "an installed family is offered: " + shown);
      await family(page, "uiFontRow", pick);
      await style(page, "uiFontRow", "Bold");
      assert.match((await css(page, "body")).family, new RegExp("^\"?" + pick));
      assert.equal((await css(page, "body")).weight, "700");
      await family(page, "codeFontRow", pick);
      assert.match((await css(page, "#codeFontRow .font-preview")).family, new RegExp("^\"?" + pick));
      assert.equal(api.posts.filter(p => "uiFont" in p || "codeFont" in p).length, 0, "the browser's choice isn't sent as the app's");
      assert.equal(api.cur.uiFont.family, harmony[1].family);
      await page.reload();
      await page.locator("#uiFontRow .font-family").waitFor();
      await page.waitForFunction(() => fontList !== null && !fontFlight);
      assert.match((await css(page, "body")).family, new RegExp("^\"?" + pick), "kept across a reload");
      assert.equal((await css(page, "body")).weight, "700");
      assert.equal(await page.locator("#uiFontRow .font-family span").textContent(), pick);
      await reset(page, "uiFontRow");
      assert.doesNotMatch((await css(page, "body")).family, new RegExp(pick));
      assert.match((await css(page, "#codeFontRow .font-preview")).family, new RegExp("^\"?" + pick));
      assert.deepEqual(errors, []);
    } finally { await browser.close(); }
  });

  // Norcleeh again, after v0.1.1788: on a refresh a row "flashed and went".
  // A nogui magpie (Docker, magpie-cli) has no boot `fonts`, so the font
  // rows never showed, and Text size painted until the settings hid it.
  // The tab's rows show whatever the server can read, with System default
  // when the browser draws none of its families, and stay through every
  // later settings read; the app's own rows never paint at all.
  for (const lang of ["en", "zh"]) test(`${engine} ${lang}: a browser tab's font rows show on a nogui magpie and nothing flashes`, async () => {
    const browser = await runtime.launch();
    try {
      for (const fonts of [[{ name: "Georgia", styles: [face("Georgia"), face("Georgia", "Bold", 700)] }, { name: "No Such Family Here", styles: [face("No Such Family Here")] }], []]) {
        const { page, errors, close } = await open(browser, lang, { web: true, frames: true, settingsDelay: 300, list: fonts, settings: { uiFont: harmony[1] } });
        // what a later refresh of the settings or the state redraws
        await page.evaluate(() => loadSettings());
        await page.evaluate(() => new Promise(r => setTimeout(r, 500)));
        const frames = await page.evaluate(() => window.__frames);
        assert(frames.length > 5, "frames were watched: " + frames.length);
        for (const row of ["uiFontRow", "codeFontRow"]) {
          assert.equal(await page.locator("#" + row).isVisible(), true, row + " shows in a browser tab");
          const first = frames.findIndex(f => f[row]);
          assert(first >= 0, row + " never painted");
          assert(frames.slice(first).every(f => f[row]), row + " painted and then went: " + frames.map(f => +f[row]).join(""));
        }
        for (const row of ["textSizeRow", "loginRow", "trayUsageRow", "tray"]) {
          assert(!frames.some(f => f[row]), row + " is the app's and painted in a browser tab: " + frames.map(f => +f[row]).join(""));
        }
        const shown = await page.evaluate(() => fontList.map(f => f.name));
        assert(!shown.includes("No Such Family Here"), "a family this browser doesn't draw isn't offered: " + shown);
        await page.locator("#codeFontRow .font-family").click();
        const names = await page.locator(".font-menu .pm-item .pm-name").allTextContents();
        assert.equal(names[0], lang === "zh" ? "系统默认" : "System default", "System default is offered: " + names);
        if (!fonts.length) assert.deepEqual(shown, [], "no family this browser draws");
        await page.keyboard.press("Escape");
        assert.deepEqual(errors, []);
        await close();
      }
    } finally { await browser.close(); }
  });

  // unscientific9: a tab's first open of Settings stuck while the probe
  // measured every server family in the click's task. Each measure is made
  // to cost 1 ms here, so 400 families held one task for 1.6 s before; the
  // probe now yields, and measures the two generics once.
  for (const lang of ["en", "zh"]) test(`${engine} ${lang}: a browser tab's font probe never holds the page`, async () => {
    const browser = await runtime.launch();
    try {
      const fonts = Array.from({ length: 400 }, (_, i) => ({ name: "Probe Family " + i, styles: [face("Probe Family " + i)] }));
      fonts.push({ name: "Georgia", styles: [face("Georgia")] });
      const init = () => {
        const measure = CanvasRenderingContext2D.prototype.measureText;
        window.__probe = { calls: 0, longest: 0 };
        let start = null;
        CanvasRenderingContext2D.prototype.measureText = function (text) {
          const now = performance.now();
          if (start === null) start = now, setTimeout(() => { start = null; });
          for (const end = now + 1; performance.now() < end;);
          window.__probe.calls++;
          window.__probe.longest = Math.max(window.__probe.longest, performance.now() - start);
          return measure.call(this, text);
        };
      };
      const { page, errors, close } = await open(browser, lang, { web: true, list: fonts, init });
      const probe = await page.evaluate(() => window.__probe);
      const shown = await page.evaluate(() => fontList.map(f => f.name));
      assert(!shown.some(n => n.startsWith("Probe Family")), "a family this browser doesn't draw isn't offered");
      assert(probe.calls >= fonts.length, "every family was measured: " + probe.calls);
      assert(probe.calls <= 2 * fonts.length + 4, "the generics are measured once: " + probe.calls + " measures");
      assert(probe.longest < 300, "the probe held one task for " + probe.longest.toFixed(0) + " ms");
      assert.deepEqual(errors, []);
      await close();
    } finally { await browser.close(); }
  });

  // Norcleeh (magpie in Docker, its web GUI): the tab listed the server's
  // fonts, the container's, never the ones on his own computer. Where the
  // browser has the Local Font Access API, the press that opens a family
  // menu asks for them, once; they are offered with their real styles.
  // queryLocalFonts is stubbed in both engines (a real one needs a press,
  // a permission prompt and a secure origin).
  const ownFonts = [
    { family: "Norcleeh Sans", fullName: "Norcleeh Sans Regular", postscriptName: "NorcleehSans-Regular", style: "Regular" },
    { family: "Norcleeh Sans", fullName: "Norcleeh Sans Bold Italic", postscriptName: "NorcleehSans-BoldItalic", style: "Bold Italic" },
    { family: "Norcleeh Sans", fullName: "Norcleeh Sans SemiBold Condensed", postscriptName: "NorcleehSans-SemiBoldCond", style: "SemiBold Condensed" },
    { family: "Norcleeh Mono", fullName: "Norcleeh Mono Regular", postscriptName: "NorcleehMono-Regular", style: "Regular" },
  ];
  const localFonts = (fonts) => {
    window.__asked = [];
    navigator.permissions.query = async () => ({ state: "prompt" });
    window.queryLocalFonts = async () => {
      window.__asked.push(navigator.userActivation ? navigator.userActivation.isActive : "unknown");
      return fonts;
    };
  };
  for (const lang of ["en", "zh"]) for (const width of [1000, 440]) test(`${engine} ${lang} ${width}px: a browser tab lists the fonts of the computer it runs on`, async () => {
    const browser = await runtime.launch();
    try {
      const { page, api, errors, close } = await open(browser, lang, { web: true, viewport: { width, height: 820 }, list: [], init: localFonts, arg: ownFonts });
      assert.deepEqual(await page.evaluate(() => window.__asked), [], "nothing is asked before the menu opens (it needs a press)");
      await page.locator("#uiFontRow .font-family").click();
      await page.locator(".font-menu").waitFor();
      const asked = await page.evaluate(() => window.__asked);
      assert.equal(asked.length, 1, "the press that opens the menu asks once");
      if (engine === "chromium") assert.equal(asked[0], true, "asked within the press");
      const names = await page.locator(".font-menu .pm-item:not([hidden]) .pm-name").allTextContents();
      assert(names.includes("Norcleeh Sans") && names.includes("Norcleeh Mono"), "this computer's families are offered: " + names);
      const box = await page.locator(".font-menu").boundingBox();
      assert(box.x >= 0 && box.x + box.width <= width, "the menu fits the window: " + JSON.stringify(box));
      await page.locator(".font-menu .sc-q").fill("Norcleeh Sans");
      await page.locator(".font-menu .pm-item").filter({ has: page.locator(".pm-name", { hasText: "Norcleeh Sans" }) }).first().click();
      assert.match((await css(page, "body")).family, /^"?Norcleeh Sans/);
      await page.locator("#uiFontRow .font-style").click();
      const styles = await page.locator(".font-menu .pm-item .pm-name").allTextContents();
      assert.deepEqual(styles, ["Regular", "SemiBold Condensed", "Bold Italic"], "its real styles, by weight");
      await page.keyboard.press("Escape");
      await style(page, "uiFontRow", "SemiBold Condensed");
      const look = await css(page, "body");
      assert.equal(look.weight, "600");
      assert.equal(look.stretch, "75%");
      await style(page, "uiFontRow", "Bold Italic");
      assert.equal((await css(page, "body")).style, "italic");
      await page.locator("#codeFontRow .font-family").click();
      await page.locator(".font-menu").waitFor();
      assert.equal((await page.evaluate(() => window.__asked)).length, 1, "asked once a page");
      await page.keyboard.press("Escape");
      assert.equal(api.posts.filter(p => "uiFont" in p || "codeFont" in p).length, 0, "the browser's choice isn't sent as the app's");
      assert.equal(await page.locator("select").count(), 0);
      assert.deepEqual(errors, []);
      await close();
    } finally { await browser.close(); }
  });

  // Where there is no such API (Safari, Firefox, a plain-http address) or
  // the user refused it, a family is typed into the menu's search: it is
  // taken when this browser draws it, and kept across a reload though the
  // server has no such font.
  for (const lang of ["en", "zh"]) for (const [how, width] of [["missing", 1000], ["refused", 440]]) test(`${engine} ${lang} ${width}px: a browser tab takes a typed font when the API is ${how}`, async () => {
    const browser = await runtime.launch();
    try {
      const init = how === "missing" ? () => { delete window.queryLocalFonts; }
        : () => { window.queryLocalFonts = async () => { throw new DOMException("refused", "NotAllowedError"); }; };
      const { page, api, errors, close } = await open(browser, lang, { web: true, viewport: { width, height: 820 }, list: [], init });
      const real = await page.evaluate(() => ["Georgia", "Courier New", "Times New Roman", "DejaVu Serif", "Liberation Serif"].find(n => {
        const ctx = document.createElement("canvas").getContext("2d"), w = f => (ctx.font = "72px " + f, ctx.measureText("mmmlli WwQ@ 0123").width);
        return ["monospace", "serif"].some(b => w(`"${n}",${b}`) !== w(b));
      }));
      assert(real, "the test machine draws one of the families tried");
      await page.locator("#uiFontRow .font-family").click();
      const q = page.locator(".font-menu .sc-q");
      assert.equal(await q.getAttribute("placeholder"), lang === "zh" ? "搜索或输入字体名称…" : "Search or type a font name…");
      await q.fill("No Such Family Here");
      const typed = page.locator(".font-menu .typed-font");
      assert.equal(await typed.isVisible(), true, "a typed family is answered");
      assert.equal(await typed.isDisabled(), true, "one this browser doesn't draw can't be chosen");
      assert.equal(await typed.locator(".pm-note").textContent(), lang === "zh" ? "这个浏览器找不到这款字体" : "This browser can't find this font");
      await q.fill(real);
      assert.equal(await typed.isDisabled(), false);
      assert.equal(await typed.locator(".pm-name").textContent(), lang === "zh" ? `使用“${real}”` : `Use “${real}”`);
      await q.press("Enter");
      await page.locator(".font-menu").waitFor({ state: "detached" });
      assert.match((await css(page, "body")).family, new RegExp("^\"?" + real));
      assert.equal(await page.locator("#uiFontRow .font-family span").textContent(), real);
      await style(page, "uiFontRow", "Bold");
      assert.equal((await css(page, "body")).weight, "700");
      assert.equal(api.posts.filter(p => "uiFont" in p || "codeFont" in p).length, 0, "the browser's choice isn't sent as the app's");
      await page.reload();
      await page.locator("#uiFontRow .font-family").waitFor();
      await page.waitForFunction(() => fontList !== null && !fontFlight);
      assert.match((await css(page, "body")).family, new RegExp("^\"?" + real), "kept across a reload, though the server lists no such font");
      assert.equal((await css(page, "body")).weight, "700");
      assert.equal(await page.locator("#uiFontRow .font-family span").textContent(), real);
      assert.equal(await page.locator("#uiFontRow .font-note").isVisible(), false, "not called unavailable");
      assert.equal(await page.locator("select").count(), 0);
      assert.deepEqual(errors, []);
      await close();
    } finally { await browser.close(); }
  });

  test(`${engine}: native notifications update the other webview without another save`, async () => {
    const browser = await runtime.launch();
    try {
      const { page, api, errors } = await open(browser);
      api.cur.uiFont = harmony[1];
      api.cur.codeFont = mono[1];
      await page.evaluate(s => window.receiveFonts(s), api.cur);
      assert.equal((await css(page, "body")).weight, "500");
      assert.equal((await css(page, "#codeFontRow .font-preview")).style, "italic");
      assert.equal(await page.locator("#uiFontRow .font-style span").textContent(), "Medium");
      assert.equal(api.posts.length, 0, "a notification must not echo another save");
      api.cur.uiFont = null;
      await page.evaluate(s => window.receiveFonts(s), api.cur);
      assert.doesNotMatch((await css(page, "body")).family, /HarmonyOS/);
      assert.equal((await css(page, "#codeFontRow .font-preview")).style, "italic");
      // A local queued choice wins over an older notice sent before its reply.
      api.delay = 150;
      await family(page, "uiFontRow", "HarmonyOS Sans SC");
      await page.evaluate(s => window.receiveFonts(s), { uiFont: mono[0], codeFont: null });
      await settled(page);
      assert.match((await css(page, "body")).family, /HarmonyOS/);
      assert.equal((await css(page, "#codeFontRow .font-preview")).style, "italic");
      assert.deepEqual(errors, []);
    } finally { await browser.close(); }
  });

  test(`${engine}: named variable traits retain fractional weight and custom width`, async () => {
    const browser = await runtime.launch();
    try {
      const { page, api, errors } = await open(browser);
      const book = face("Variable Sans", "Book", 425.5, "normal", 25);
      api.fonts.push({ name: book.family, styles: [book] });
      await page.locator("#uiFontRow .font-family").click();
      await page.getByRole("menuitemradio", { name: "Refresh fonts" }).click();
      await page.waitForFunction(() => !fontFlight && fontList.some(f => f.name === "Variable Sans"));
      await family(page, "uiFontRow", book.family);
      await settled(page);
      assert.equal(api.cur.uiFont.weight, 425.5);
      assert.equal(api.cur.uiFont.stretch, 25);
      assert.equal((await css(page, "body")).weight, "425.5");
      assert.equal((await css(page, "body")).stretch, "25%");
      assert.deepEqual(errors, []);
    } finally { await browser.close(); }
  });
}
