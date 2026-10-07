// Run with Node's test runner and Playwright on the module path; see README.md.
// A Settings option pressed while the page is drawn again still picks it:
// a save's answer redraws Settings (renderSettings), and a second option
// pressed while the first one's save came back was let go on a new button.
// Down and up were on two buttons, the browser sent the click to neither,
// and nothing was saved. Pressed, redrawn and let go on the same option,
// Lightweight mode → On is saved once; dragged off to another option, or to
// the same option of another control, nothing is; a click with no redraw
// is saved once. Every segs control on every Settings tab is pressed the
// same way and its new self clicked once. Chromium and WebKit, in English,
// Chinese, Japanese and German, in a wide and a narrow window, the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const words = { en: { on: "On", off: "Off" }, zh: { on: "开启", off: "关闭" }, ja: { on: "オン", off: "オフ" }, de: { on: "An", off: "Aus" } };

function serve(lang, posted, st) {
  const settings = () => ({ lang, theme: "light", searchVendors: [], searchAPIs: [], ...st });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname.startsWith("/api/settings")) {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        posted.push(b);
        if (url.pathname === "/api/settings") Object.assign(st, b);
      }
      return json(settings());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    // sync set up, so its own controls are drawn
    if (url.pathname === "/api/davsync") return json({ on: true, kind: "webdav", url: "https://dav.example.test/dav/", user: "u", auto: 3 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, tab, posted, st, t, touch = false) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1000 }, hasTouch: touch })).newPage();
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posted, st));
  await page.goto("http://magpie.test/?view=settings&tab=" + tab);
  await page.locator(`#setPage-${tab} .segs .opt`).first().waitFor();
  return { page, errors };
}

const settle = (page) => page.evaluate(() => new Promise((ok) => setTimeout(ok, 80)));
// brought out from under the header and the foot by the reader's wheel, as
// code's scrolls are put back, till the pointer at its middle is on it
async function reveal(page, loc) {
  for (let i = 0; i < 40; i++) {
    const b = await loc.boundingBox();
    const h = page.viewportSize().height;
    if (b && await loc.evaluate((o, [x, y]) => o.contains(document.elementFromPoint(x, y)), mid(b))) return b;
    await page.mouse.move(page.viewportSize().width / 2, h / 2);
    await page.mouse.wheel(0, b && b.y < h / 2 ? -150 : 150);
    await page.waitForTimeout(60);
  }
  throw new Error("could not bring the option into view");
}
const mid = (b) => [b.x + b.width / 2, b.y + b.height / 2];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    for (const width of [900, 440]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: an option pressed while Settings is redrawn is picked once, and nothing when dragged off`, async (t) => {
        const posted = [], st = {};
        const { page, errors } = await open(engine, lang, width, "general", posted, st, t);
        const opt = (id, name) => page.locator(`#${id} .opt`, { hasText: new RegExp(`^${name}$`) });
        const on = () => page.locator("#lightweightSegs .opt.on").innerText();
        const saves = () => posted.filter((b) => "lightweight" in b).map((b) => b.lightweight);
        assert.equal(await on(), w.off, "off by default");

        // pressed, the page redrawn as a save's answer does, let go on the same spot
        const b = await reveal(page, opt("lightweightSegs", w.on));
        await page.mouse.move(...mid(b));
        await page.mouse.down();
        const swapped = await page.evaluate(() => {
          const was = document.querySelector("#lightweightSegs .opt:nth-of-type(2)");
          renderSettings();
          return !was.isConnected;
        });
        assert(swapped, "the redraw left the pressed button in place, so this tested nothing");
        await page.mouse.up();
        for (let i = 0; i < 40 && !saves().length; i++) await page.waitForTimeout(50);
        await settle(page);
        assert.deepEqual(saves(), [true], `posted ${JSON.stringify(posted)}`);
        assert.equal(st.lightweight, true);
        assert.equal(await on(), w.on);

        // which control's option, and which of its options, the pointer is on
        const under = () => page.evaluate(() => {
          const o = document.elementFromPoint(...window.lastPointer)?.closest(".segs > .opt");
          return o ? o.parentElement.parentElement.id + " " + o.textContent : "";
        });
        await page.evaluate(() => addEventListener("pointermove", (e) => { window.lastPointer = [e.clientX, e.clientY]; }, true));

        // dragged off to the other option: nothing, as for any button
        const n = posted.length;
        const off = await reveal(page, opt("lightweightSegs", w.off));
        await page.mouse.move(...mid(off));
        await page.mouse.down();
        await page.evaluate(() => renderSettings());
        await page.mouse.move(...mid(await opt("lightweightSegs", w.on).boundingBox()), { steps: 4 });
        assert.equal(await under(), "lightweightSegs " + w.on);
        await page.mouse.up();
        await page.waitForTimeout(400);
        assert.equal(posted.length, n, `dragged off, posted ${JSON.stringify(posted.slice(n))}`);
        assert.equal(await on(), w.on);

        // dragged to the same option of another control (Keep awake's Off): nothing
        await reveal(page, opt("keepAwakeSegs", w.off));
        await page.mouse.move(...mid(await reveal(page, opt("lightweightSegs", w.off))));
        await page.mouse.down();
        await page.evaluate(() => renderSettings());
        await page.mouse.move(...mid(await opt("keepAwakeSegs", w.off).boundingBox()), { steps: 4 });
        assert.equal(await under(), "keepAwakeSegs " + w.off);
        await page.mouse.up();
        await page.waitForTimeout(400);
        assert.equal(posted.length, n, `dragged to another control, posted ${JSON.stringify(posted.slice(n))}`);
        assert.equal(await on(), w.on);

        // a plain click with no redraw is saved once, not twice
        await page.mouse.click(...mid(await reveal(page, opt("lightweightSegs", w.off))));
        for (let i = 0; i < 40 && saves().length < 2; i++) await page.waitForTimeout(50);
        await page.waitForTimeout(300);
        assert.deepEqual(saves(), [true, false], `posted ${JSON.stringify(posted)}`);
        assert.equal(await on(), w.off);
        assert.deepEqual(errors, []);
      });

      // a tap is the browser's to click: it clicks what is under the finger
      // as it lifts, the new On, and that is the only pick. Playwright can't
      // hold a tap in WebKit, so it is Chromium's, through its DevTools.
      if (engine === "chromium") test(`${engine} ${lang} ${width}px: an option tapped while Settings is redrawn is picked once, not twice`, async (t) => {
        const posted = [], st = {};
        const { page, errors } = await open(engine, lang, width, "general", posted, st, t, true);
        const opt = page.locator("#lightweightSegs .opt", { hasText: new RegExp(`^${w.on}$`) });
        const [x, y] = mid(await reveal(page, opt));
        const cdp = await page.context().newCDPSession(page);
        await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y }] });
        const swapped = await page.evaluate(() => {
          const was = document.querySelector("#lightweightSegs .opt:nth-of-type(2)");
          renderSettings();
          return !was.isConnected;
        });
        assert(swapped, "the redraw left the tapped button in place, so this tested nothing");
        await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
        for (let i = 0; i < 40 && !posted.length; i++) await page.waitForTimeout(50);
        await page.waitForTimeout(500);
        assert.deepEqual(posted.filter((b) => "lightweight" in b).map((b) => b.lightweight), [true]);
        assert.equal(await page.locator("#lightweightSegs .opt.on").innerText(), w.on);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: every Settings option pressed through a redraw clicks its new self once`, async (t) => {
        const posted = [];
        // the warm-up and check-in tabs, two Codex accounts' own warm-up
        // times, a card beside the tray icon and the local network's address
        // shown, so their controls are drawn. Not here: Gateway mode (a
        // browser's page only) and the Omarchy bar icon (Omarchy only).
        const st = { workbuddy: true, trae: true, minimax: true, qoder: true, codexUsers: ["a@example.test", "b@example.test"],
          checkinPlugins: [{ id: "one", name: "One", on: false }, { id: "two", name: "Two", on: true }],
          trayUsages: ["codex|a@example.test"], lan: true, lanURLs: ["http://192.168.1.2:3999"] };
        const { page, errors } = await open(engine, lang, width, "general", posted, st, t);
        // each click on an option is noted and stopped there, so no pick is
        // saved and the page stays as it is from one control to the next
        // a control is known by its place, as segs() knows it: the nearest
        // element with an id, and which of the same options it is there
        await page.evaluate(() => {
          window.placeOf = (box) => {
            const home = box.parentElement.closest("[id]"), kind = box.dataset.kind;
            return kind + "@" + home.id + ":" + [...home.querySelectorAll(".segs")].filter((x) => x.dataset.kind === kind).indexOf(box);
          };
          window.optClicks = [];
          document.addEventListener("click", (e) => {
            const b = e.target.closest?.(".segs > .opt");
            if (!b) return;
            e.stopPropagation();
            optClicks.push({ place: placeOf(b.parentElement), i: [...b.parentElement.querySelectorAll(":scope > .opt")].indexOf(b), live: b.isConnected });
          }, true);
        });
        const done = new Set(), missed = [];
        const sweep = async (tab) => {
          const boxes = page.locator(`#setPage-${tab} .segs`);
          for (let k = 0; k < await boxes.count(); k++) {
            const box = boxes.nth(k);
            if (!await box.isVisible()) continue;
            const place = await box.evaluate((x) => placeOf(x));
            if (done.has(place)) continue;
            done.add(place);
            const opts = box.locator(":scope > .opt");
            const now = await opts.evaluateAll((os) => os.findIndex((o) => o.classList.contains("on")));
            const i = (now + 1) % await opts.count();
            await page.mouse.move(...mid(await reveal(page, opts.nth(i))));
            await page.evaluate(() => { optClicks.length = 0; });
            await page.mouse.down();
            const swapped = await box.evaluate((x) => { renderSettings(); return !x.isConnected; });
            if (!swapped) { await page.mouse.up(); missed.push(place); continue; }
            await page.mouse.up();
            await settle(page);
            assert.deepEqual(await page.evaluate(() => optClicks), [{ place, i, live: true }], `${tab}: ${place} option ${i}`);
          }
        };
        for (const tab of ["general", "usage", "network", "models", "privacy", "otel", "sync", "about"]) {
          await page.evaluate((tab) => document.querySelector("#setTab-" + tab).click(), tab);
          await page.locator(`#setPage-${tab}`).waitFor();
          await settle(page);
          if (tab === "usage") {
            for (const id of ["codex", "claude", "wb", "trae", "minimax", "qoder", "plugins"]) {
              await page.locator("#warmTab-" + id).click();
              await sweep(tab);
            }
          } else if (tab === "sync") {
            // its form opened, for the kind of server it syncs to
            await page.locator("#syncList .row.pref").first().locator(".val button").nth(1).click();
            await page.locator("#syncList .sync-form .segs").waitFor();
            await sweep(tab);
          } else await sweep(tab);
        }
        assert.deepEqual(missed, [], "controls the redraw left in place");
        // Theme, Language, Text size, … each warm-up's and check-in's, Proxy, the local network's, Privacy's, OTel's, Sync's, About's
        assert(done.size >= 49, `only ${done.size} controls: ${[...done].join(", ")}`);
        assert.deepEqual(posted, [], "a stopped click saved something");
        assert.deepEqual(errors, []);
      });
    }
  }

  // The same, for a list of rows drawn again by what was saved (Library's
  // way each agent gets its skills): a row gone or come in above the one
  // pressed, or the rows moved, puts another row's control under the
  // pointer at the same place, and that one is not clicked: nothing is, as
  // before. The rows known by their data-* and the rows that aren't alike.
  test(`${engine}: a press on a list's option picks nothing when the list is drawn again with its rows changed`, async (t) => {
    const { page, errors } = await open(engine, "en", 900, "general", [], {}, t);
    await page.evaluate(() => {
      window.picks = [];
      const host = document.createElement("div");
      host.id = "segsRows";
      host.style.cssText = "position: fixed; left: 20px; top: 80px; z-index: 999; padding: 8px; background: var(--card)";
      document.body.append(host);
      window.drawRows = (names, keyed) => host.replaceChildren(...names.map((n) => {
        const row = document.createElement("div");
        if (keyed) row.dataset.agent = n;
        row.append(n, segs([["", "Library's way"], ["link", "Links"], ["copy", "Copies"]], "", (v) => picks.push(n + ":" + v)));
        return row;
      }));
    });
    // Y's Copies pressed, the rows drawn again as `after`, let go on the same spot
    const press = async (keyed, after) => {
      await page.evaluate((keyed) => { picks.length = 0; drawRows(["X", "Y", "Z"], keyed); }, keyed);
      await settle(page);
      await page.mouse.move(...mid(await page.locator("#segsRows > div").nth(1).locator(".opt").nth(2).boundingBox()));
      await page.mouse.down();
      await page.evaluate(([keyed, after]) => drawRows(after, keyed), [keyed, after]);
      await page.mouse.up();
      await settle(page);
      return page.evaluate(() => picks);
    };
    for (const keyed of [true, false]) {
      assert.deepEqual(await press(keyed, ["X", "Y", "Z"]), ["Y:copy"], `${keyed ? "keyed" : "plain"} rows drawn again the same`);
      assert.deepEqual(await press(keyed, ["Y", "Z"]), [], `${keyed ? "keyed" : "plain"} rows, X gone: Z is where Y was`);
      assert.deepEqual(await press(keyed, ["W", "X", "Y", "Z"]), [], `${keyed ? "keyed" : "plain"} rows, W come in: X is where Y was`);
    }
    assert.deepEqual(await press(true, ["X", "Z", "Y"]), [], "keyed rows moved: Z is where Y was");
    assert.deepEqual(errors, []);
  });
}
