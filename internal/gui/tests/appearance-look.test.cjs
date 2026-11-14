// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → General's Accent color and Background image (CHENGJIAHEGOXIS
// on X: "other colours of my own, and a picture from my computer"). In both
// engines, with the API faked, in every language, wide and at a phone's
// width:
// - the Accent color row offers magpie's own, six presets and a colour of
//   the reader's own; picking one saves it as accent and the page takes
//   the server's stylesheet for it (/api/appearance/css) with no reload;
// - a colour from the system's picker is saved, and shown as the chosen
//   swatch;
// - Background image: Choose… sends the picture as base64 JSON, a thumbnail
//   with Change and Remove shows, and Remove sends {remove: true};
// - a page whose boot.js drew the look already asks for no stylesheet;
// - the rows fit a narrow window, and their words are the language's own.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// a 1×1 PNG
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
// appearance.go's lookCSS for the colours picked here, as it draws them
// (TestLookFixture keeps the file its own): the light theme's block first,
// then the dark one's, --mark the ink of what is chosen, --mark-fill the
// thumb under it, and the grounds tinted with the colour
const LOOK = require("./fixtures/look-accents.json");
const varOf = (a, name, dark) => {
  const blocks = LOOK[a].split("\n");
  const m = blocks[dark ? 2 : 0].match(new RegExp(`${name}:([^;}]+)`));
  return m && m[1];
};
const accentCSS = (a) => (a ? LOOK[a] : "");

function serve(st, log) {
  const settings = () => ({ lang: "en", theme: "light", searchVendors: [], searchAPIs: [], ...st });
  const theme = () => settings().theme;
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") {
      const look = (st.accent || "") + "," + (st.wallpaper || "");
      const css = accentCSS(st.accent);
      return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang: st.lang || "en", theme: theme(), web: false, look })};` + (css ? `(function(){var e=document.createElement("style");e.id="lookCSS";e.textContent=${JSON.stringify(css)};document.head.appendChild(e)})();` : "") });
    }
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        log.push(["settings", b]);
        Object.assign(st, b);
      }
      return json(settings());
    }
    if (url.pathname === "/api/appearance/css") {
      log.push(["css"]);
      return json({ css: accentCSS(st.accent), key: (st.accent || "") + "," + (st.wallpaper || "") });
    }
    if (url.pathname === "/api/appearance/wallpaper") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        log.push(["wallpaper", b]);
        st.wallpaper = b.remove ? 0 : 1700000000000 + log.length;
        return json(settings());
      }
      return r.fulfill({ body: PNG, contentType: "image/png" });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const NAMES = { en: "Accent color", zh: "强调色", "zh-TW": "強調色", ja: "アクセントカラー", de: "Akzentfarbe" };
const PICTURE = { en: "Background image", zh: "背景图片", "zh-TW": "背景圖片", ja: "背景画像", de: "Hintergrundbild" };

async function open(browser, st, width, query = "view=settings", wait = "#accentPick .swatch") {
  const page = await (await browser.newContext({ viewport: { width, height: 800 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], log = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(st, log));
  await page.goto("http://magpie.test/?" + query);
  await page.locator(wait).first().waitFor();
  return { page, errors, log };
}

const accentNow = (page) => page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--accent").trim());
const chosen = (page) => page.evaluate(() => [...document.querySelectorAll("#accentPick .swatch.on")].map((b) => b.dataset.accent ?? "own:" + b.style.getPropertyValue("--sw")));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: an accent colour of the reader's own`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const st = {};
    const { page, errors, log } = await open(browser, st, 1000);
    const sw = page.locator("#accentPick .swatch");
    assert.equal(await sw.count(), 8, "magpie's own, six presets and the reader's own");
    assert.deepEqual(await chosen(page), [""], "magpie's own is chosen at first");
    assert.equal(await accentNow(page), "#4f46e5");
    assert.deepEqual(log.filter(([k]) => k === "css"), [], "magpie's own look needs no stylesheet");

    await page.locator('#accentPick .swatch[data-accent="#0d9488"]').click();
    await page.waitForFunction((w) => getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() === w, varOf("#0d9488", "--accent"));
    assert.equal(log.find(([k]) => k === "settings")[1].accent, "#0d9488");
    await page.waitForFunction(() => document.querySelector("#accentPick .swatch.on")?.dataset.accent === "#0d9488");

    // the system's picker: its input reports the colour when it closes
    await page.locator("#accentPick .swatch.own input").evaluate((i) => { i.value = "#7c3aed"; i.dispatchEvent(new Event("change", { bubbles: true })); });
    await page.waitForFunction(() => getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() === "#7c3aed");
    assert.equal(log.filter(([k]) => k === "settings").at(-1)[1].accent, "#7c3aed");
    await page.waitForFunction(() => document.querySelector("#accentPick .swatch.own.on"));
    assert.deepEqual(await chosen(page), ["own:#7c3aed"]);

    // back to magpie's own: nothing drawn over it
    await page.locator('#accentPick .swatch[data-accent=""]').click();
    await page.waitForFunction(() => getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() === "#4f46e5");
    assert.equal(log.filter(([k]) => k === "settings").at(-1)[1].accent, "");
    assert.deepEqual(errors, []);
  });

  // CHENGJIAHEGOXIS on X: 「选中任意强调色没反应」. The accent reached
  // only buttons, links and bars, none of them on Settings, so a colour
  // picked changed nothing in sight. What is chosen (the open page in the
  // sidebar, a switch's or tab's chosen option) is in --mark: the text's
  // own ink by default, the colour picked once there is one, in the window
  // and the panel, light and dark, over a picture too.
  for (const theme of ["light", "dark"]) {
    // the chosen option's ink, and the probe's for a colour
    const inks = (page, sel) => page.evaluate((sel) => {
      const ink = (v) => { const p = document.createElement("i"); p.style.color = v; document.body.append(p); const c = getComputedStyle(p).color; p.remove(); return c; };
      return { got: sel.map((s) => { const e = document.querySelector(s); return e ? getComputedStyle(e).color : "missing " + s; }), fg: ink("var(--fg)"), mark: ink("var(--mark)") };
    }, sel);
    const same = (r, want, what) => assert.deepEqual(r.got, r.got.map(() => want), what);
    // what is chosen eases to its colour (.18s): read it once it stands still
    const settled = async (page, sel) => {
      let last = "";
      for (let i = 0; i < 40; i++) {
        const r = await inks(page, sel), now = JSON.stringify(r);
        if (now === last) return r;
        last = now;
        await page.waitForTimeout(100);
      }
      return JSON.parse(last);
    };

    test(`${engine}: ${theme}, a colour picked shows on what is chosen, in the window`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const st = { theme, wallpaper: 1700000000000 };
      const { page, errors } = await open(browser, st, 1200);
      await page.waitForFunction(() => document.body.classList.contains("side-nav"));
      const sel = ["#themeSegs .opt.on", "#birdSegs .opt.on", "body.side-nav #prefs.on svg"];
      const before = await settled(page, sel);
      same(before, before.fg, "magpie's own look: what is chosen is in the text's ink");
      // 「主题深色依然还是灰色」: the chosen option's thumb is tinted too,
      // and is its own grey, exactly as before, with no accent
      const thumbs = ["#themeSegs .segs > .thumb", "#birdSegs .segs > .thumb", "#reduceMotionSegs .segs > .thumb"];
      const grounds = () => page.evaluate((sel) => sel.map((s) => { const e = document.querySelector(s); return e ? getComputedStyle(e).backgroundColor : "missing " + s; }), thumbs);
      const card = await page.evaluate(() => { const p = document.createElement("i"); p.style.background = "var(--card)"; document.body.append(p); const c = getComputedStyle(p).backgroundColor; p.remove(); return c; });
      assert.deepEqual(await grounds(), thumbs.map(() => card), "magpie's own look: the thumb is the card's grey");
      const rgbOf = (h) => `rgb(${parseInt(h.slice(1, 3), 16)}, ${parseInt(h.slice(3, 5), 16)}, ${parseInt(h.slice(5, 7), 16)})`;
      for (const v of ["#0d9488", "#db2777"]) {
        await page.locator(`#accentPick .swatch[data-accent="${v}"]`).click();
        const want = varOf(v, "--mark", theme === "dark");
        await page.waitForFunction((w) => getComputedStyle(document.documentElement).getPropertyValue("--mark").trim() === w, want);
        const r = await settled(page, sel);
        assert.notEqual(r.mark, r.fg);
        same(r, r.mark, `${v}: what is chosen is in the colour picked`);
        assert.deepEqual(await grounds(), thumbs.map(() => rgbOf(varOf(v, "--mark-fill", theme === "dark"))), `${v}: the chosen thumb is tinted`);
      }
      await page.locator('#accentPick .swatch[data-accent=""]').click();
      await page.waitForFunction(() => !document.getElementById("lookCSS")?.textContent);
      const back = await settled(page, sel);
      same(back, back.fg, "back to magpie's own");
      assert.deepEqual(await grounds(), thumbs.map(() => card), "back to magpie's own thumb");
      assert.deepEqual(errors, []);
    });

    test(`${engine}: ${theme}, the header's tabs and the panel's take the colour`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const [query, width, sel] of [["view=agents", 900, ["#nav button.on"]], ["mode=panel", 380, [".ptabs button.on"]]]) {
        for (const accent of ["", "#0d9488"]) {
          const { page, errors } = await open(browser, { theme, accent }, width, query, sel[0]);
          const r = await settled(page, sel);
          if (accent) assert.notEqual(r.mark, r.fg);
          same(r, accent ? r.mark : r.fg, `${query} ${accent || "default"}`);
          assert.deepEqual(errors, []);
          await page.context().close();
        }
      }
    });
    // CHENGJIAHEGOXIS on X: 「目前我个人感觉深色比较灰」, and on v0.1.4116,
    // with the chosen thumbs tinted, 「依然没变」. The theme's own grounds
    // (the page, the sidebar, Settings' list, the panel) take the colour's
    // hue once one is picked, and are app.css's grey again on Default.
    test(`${engine}: ${theme}, a colour picked tints the theme's grounds, in the window and the panel`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const rgbOf = (h) => `rgb(${parseInt(h.slice(1, 3), 16)}, ${parseInt(h.slice(3, 5), 16)}, ${parseInt(h.slice(5, 7), 16)})`;
      const dark = theme === "dark";
      // the window, as in the report: Settings › General at 1200 with the
      // sidebar, with and without a picture behind the pages
      for (const wallpaper of [0, 1700000000000]) {
        const { page, errors } = await open(browser, { theme, wallpaper }, 1200);
        await page.waitForFunction(() => document.body.classList.contains("side-nav"));
        const grounds = () => page.evaluate(() => {
          const bg = (e) => (e ? getComputedStyle(e).backgroundColor : "missing");
          return { list: bg(document.querySelector("#setPage-general .list.prefs")), sidebar: bg(document.querySelector(".sidebar")), body: getComputedStyle(document.body).backgroundColor };
        });
        const grey = await grounds();
        await page.locator('#accentPick .swatch[data-accent="#db2777"]').click();
        await page.waitForFunction((w) => getComputedStyle(document.documentElement).getPropertyValue("--mark").trim() === w, varOf("#db2777", "--mark", dark));
        const pink = await grounds();
        const card = varOf("#db2777", dark ? "--card" : "--bg", dark);
        if (dark) assert.equal(pink.list, rgbOf(card), "Settings' list is on the tinted card");
        assert.notEqual(pink.list === grey.list && pink.sidebar === grey.sidebar && pink.body === grey.body, true, `wallpaper=${wallpaper}: something tinted`);
        if (!wallpaper) {
          assert.equal(pink.body, rgbOf(varOf("#db2777", "--bg", dark)), "the page's ground is tinted");
          assert.equal(pink.sidebar, rgbOf(varOf("#db2777", "--bg", dark)), "the sidebar's ground is tinted");
        }
        await page.locator('#accentPick .swatch[data-accent=""]').click();
        await page.waitForFunction(() => !document.getElementById("lookCSS")?.textContent);
        assert.deepEqual(await grounds(), grey, "back to magpie's own grey");
        assert.deepEqual(errors, []);
        await page.context().close();
      }
      // the panel draws its own grounds on body.panel: tinted too
      const panelBg = async (accent) => {
        const { page, errors } = await open(browser, { theme, accent }, 380, "mode=panel", ".ptabs button.on");
        const v = await page.evaluate(() => { const p = document.createElement("i"); p.style.background = "var(--bg)"; document.body.append(p); const c = getComputedStyle(p).backgroundColor; p.remove(); return c; });
        assert.deepEqual(errors, []);
        await page.context().close();
        return v;
      };
      const plain = await panelBg(""), tinted = await panelBg("#db2777");
      assert.notEqual(tinted, plain, "the panel's ground is tinted");
      const want = LOOK["#db2777"].match(new RegExp(`root\\[data-theme="${theme}"\\] body\\.panel\\{--bg:(#[0-9a-f]{6})`))[1];
      assert.equal(tinted, rgbOf(want));
    });
  }

  // CHENGJIAHEGOXIS on X, on v0.1.4124: 「这三个颜色……都显示是一样的」,
  // 「尤其是黑色始终一直是灰色」. Default, Graphite and a black of their own
  // drew the same grey page, and black's ink was Graphite's grey. Black now
  // draws a black page on the dark theme with a light ink of its own, and
  // black ink on the light theme; Graphite keeps the grey page.
  for (const theme of ["light", "dark"]) {
    test(`${engine}: ${theme}, Default, Graphite and black are three looks`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const dark = theme === "dark";
      const rgbOf = (h) => `rgb(${parseInt(h.slice(1, 3), 16)}, ${parseInt(h.slice(3, 5), 16)}, ${parseInt(h.slice(5, 7), 16)})`;
      const { page, errors } = await open(browser, { theme }, 1200);
      await page.waitForFunction(() => document.body.classList.contains("side-nav"));
      const look = () => page.evaluate(() => {
        const bg = (e) => (e ? getComputedStyle(e).backgroundColor : "missing");
        return { body: getComputedStyle(document.body).backgroundColor, list: bg(document.querySelector("#setPage-general .list.prefs")), accent: getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() };
      });
      const want = (v) => varOf(v, "--accent", dark);
      const own = await look();
      await page.locator('#accentPick .swatch[data-accent="#52525b"]').click();
      await page.waitForFunction((w) => getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() === w, want("#52525b"));
      const graphite = await look();
      await page.locator("#accentPick .swatch.own input").evaluate((i) => { i.value = "#000000"; i.dispatchEvent(new Event("change", { bubbles: true })); });
      await page.waitForFunction((w) => getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() === w, want("#000000"));
      const blk = await look();
      assert.deepEqual(new Set([own.accent, graphite.accent, blk.accent]).size, 3, `three inks: ${own.accent} ${graphite.accent} ${blk.accent}`);
      assert.equal(graphite.body, own.body, "Graphite keeps the grey page");
      if (dark) {
        assert.equal(blk.body, rgbOf(varOf("#000000", "--bg", true)), "black's page");
        const [r, g, b] = blk.body.match(/\d+/g).map(Number);
        assert.ok(r + g + b <= 9, `black's page is black, not ${blk.body}`);
        assert.equal(blk.list, rgbOf(varOf("#000000", "--card", true)), "Settings' list is on black's card");
        assert.notEqual(blk.list, blk.body, "the card stands a step above the page");
        // its ink is light, and the page's text reads on it
        assert.ok(parseInt(blk.accent.slice(1, 3), 16) > 0xc0, `black's ink is light, not ${blk.accent}`);
      } else {
        assert.equal(blk.accent, "#000000", "black is black ink on the light theme");
        assert.equal(blk.body, own.body, "the light page keeps its ground");
      }
      // the chosen swatch is black's own, drawn black
      assert.deepEqual(await chosen(page), ["own:#000000"]);
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine}: a look boot.js drew is not asked for again`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const { page, errors, log } = await open(browser, { accent: "#0d9488" }, 1000);
    assert.equal(await accentNow(page), varOf("#0d9488", "--accent"), "drawn before the first paint");
    assert.deepEqual(await chosen(page), ["#0d9488"]);
    await page.waitForTimeout(300);
    assert.deepEqual(log.filter(([k]) => k === "css"), []);
    assert.deepEqual(errors, []);
  });

  test(`${engine}: a picture behind the pages`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const st = {};
    const { page, errors, log } = await open(browser, st, 1000);
    const row = page.locator("#wallpaperPick");
    assert.deepEqual(await row.locator("button").allTextContents(), ["Choose…"]);
    await row.locator('input[type="file"]').setInputFiles({ name: "beach.png", mimeType: "image/png", buffer: PNG });
    await row.locator(".wall-thumb").waitFor();
    const sent = log.find(([k]) => k === "wallpaper")[1];
    assert.equal(sent.data, PNG.toString("base64"));
    assert.deepEqual(await row.locator("button").allTextContents(), ["Change", "Remove"]);
    assert.match(await row.locator(".wall-thumb").getAttribute("src"), /^\/api\/appearance\/wallpaper\?v=\d+$/);
    // the look changed: the page takes the server's stylesheet again
    await page.waitForFunction(() => document.getElementById("lookCSS"));
    assert.ok(log.some(([k]) => k === "css"));

    await row.locator("button", { hasText: "Remove" }).click();
    await page.waitForFunction(() => !document.querySelector("#wallpaperPick .wall-thumb"));
    assert.deepEqual(log.filter(([k]) => k === "wallpaper").at(-1)[1], { remove: true });
    assert.deepEqual(await row.locator("button").allTextContents(), ["Choose…"]);
    assert.deepEqual(errors, []);
  });

  test(`${engine}: the rows keep their height while their controls load`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1000, height: 800 } })).newPage();
    page.setDefaultTimeout(5000);
    const log = [];
    let release;
    const held = new Promise((r) => (release = r));
    await page.route("**/*", serve({ wallpaper: 1700000000000 }, log));
    // appearance.js is held back, so the page shows the rows before their controls
    await page.route("**/appearance.js", async (r) => { await held; await r.fallback(); });
    await page.goto("http://magpie.test/?view=settings");
    await page.locator("#colorblindRow").waitFor();
    const ids = ["accentRow", "wallpaperRow", "colorblindRow", "reduceMotionRow", "birdRow"];
    const heights = () => page.evaluate((ids) => ids.map((id) => [id, Math.round(document.getElementById(id)?.getBoundingClientRect().height)]), ids);
    await page.waitForFunction(() => document.querySelector("#view-settings")?.offsetParent !== null);
    const before = await heights();
    release();
    await page.locator("#wallpaperPick .wall-thumb").waitFor();
    await page.locator("#birdSegs .segs").waitFor();
    assert.deepEqual(await heights(), before, "a row grew once its controls came, and moved the page under the reader");
  });

  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [420, 1000]) {
      test(`${engine}: ${lang} at ${width}px, the rows fit and speak the language`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const { page, errors } = await open(browser, { lang, accent: "#7c3aed", wallpaper: 1700000000000 }, width);
        await page.locator("#wallpaperPick .wall-thumb").waitFor();
        assert.equal((await page.locator("#accentRow .name").textContent()).trim(), NAMES[lang]);
        assert.equal((await page.locator("#wallpaperRow .name").textContent()).trim(), PICTURE[lang]);
        const fit = await page.evaluate(() => {
          const out = [];
          for (const id of ["accentRow", "wallpaperRow"]) {
            const row = document.getElementById(id), r = row.getBoundingClientRect();
            if (row.scrollWidth > row.clientWidth + 1) out.push(id + " scrolls");
            for (const c of row.querySelectorAll(".swatch, button, .wall-thumb")) {
              const b = c.getBoundingClientRect();
              if (b.right > r.right + 1 || b.left < r.left - 1) out.push(id + " " + (c.dataset.accent ?? c.textContent) + " outside");
            }
          }
          // the presets' names are said in the language too
          const titles = [...document.querySelectorAll("#accentPick .swatch")].map((b) => b.title);
          return { out, titles, page: document.documentElement.scrollWidth <= innerWidth };
        });
        assert.deepEqual(fit.out, []);
        assert.ok(fit.page, "the page scrolls sideways");
        if (lang !== "en") assert.notDeepEqual(fit.titles.slice(1, 4), ["Blue", "Teal", "Green"]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
