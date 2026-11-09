// The app deliberately undoes unsolicited scrollIntoView (including Playwright's
// automatic scroll). Bring a control into sight with real wheel input first.
// Wholly in sight: one cut off by a fraction of a pixel at the view's foot
// was scrolled the rest of the way by Playwright's click itself, before the
// app saw the click, and the page moved 1px under it (860 to 861 on the
// Usage page's Fetch from archive, once the archive switch in its header
// had moved the rows 4px down).
async function inView(page, locator) {
  await locator.waitFor({state:"visible"});
  if (await locator.evaluate(e => !e.closest("#view-usage"))) return;
  const view = page.locator("#view-usage");
  const v = await view.boundingBox();
  await page.mouse.move(v.x + v.width / 2, v.y + v.height / 2);
  for (let i = 0; i < 80; i++) {
    const b = await locator.boundingBox();
    if (!b) { await page.waitForTimeout(40); continue; }
    // one taller than the view: its top in sight
    if (b.y >= v.y && (b.y + b.height <= v.y + v.height || (b.height > v.height && b.y < v.y + v.height))) {
      await page.waitForTimeout(300);
      return;
    }
    const delta = b.y < v.y ? -20 : 20;
    await page.mouse.wheel(0, delta);
    await page.waitForTimeout(40);
  }
  throw new Error("control did not come into sight: " + JSON.stringify({view:v,target:await locator.boundingBox(),scroll:await view.evaluate(e=>e.scrollTop)}));
}

// reach is inView for any page: the reader wheels the view the control is
// in (its .view) until the control is wholly in that view and nothing drawn
// over it (a sticky bar, the window's foot) takes the point Playwright will
// press. Playwright's own scroll is put back by the app, as any scroll that
// isn't the reader's, and one partly in sight it doesn't scroll at all, so
// its press can wait on a control out of sight or under a bar till it times
// out (in WebKit; Chromium gets through some of these by a few pixels).
// Each wheel step waits for its scroll to land before the control is
// looked at again: a wheel's scroll reaches the page a frame or more after
// it is sent, and on a busy machine a fixed pause read where the control
// was before it. The next step was sent on that stale reading, the view
// went past the control, under the header, and the click that followed
// was on a control out of sight (#2370).
async function reach(page, locator) {
  await locator.waitFor({ state: "visible" });
  const id = await viewOf(page, locator);
  if (!id) return;
  const v = await page.locator("#" + id).boundingBox();
  await page.mouse.move(v.x + v.width / 2, v.y + v.height / 2);
  let stalls = 0;
  // 20px a wheel: 120 of them reached only 2,400px down, and once Settings'
  // lines wrapped whole (arculongus on Discord) Allowances' last account
  // was further than that at 420px. The view's end stops it sooner.
  for (let i = 0; i < 400; i++) {
    // found and read in one call (evaluateAll): locator.evaluate finds the
    // control in one call and reads it in the next, and a part the page
    // draws again in between (Routing's account rows, a row whose numbers
    // moved) hands it the element just replaced, out of the page, a box of
    // zeros at the window's top that reads as in sight (#2658)
    const at = await locator.evaluateAll((es) => {
      const e = es.length === 1 ? es[0] : null, view = e?.closest(".view");
      if (!view) return null;
      const b = e.getBoundingClientRect(), v = view.getBoundingClientRect();
      const x = b.left + b.width / 2, y = b.top + b.height / 2;
      const inside = b.top >= v.top && b.bottom <= v.bottom, hit = document.elementFromPoint(x, y);
      return { clear: inside && !!hit && (e === hit || e.contains(hit)), down: y > v.top + v.height / 2 };
    });
    // drawn again between two looks, or not drawn yet: look again
    if (!at) { await page.waitForTimeout(40); continue; }
    if (at.clear) { await page.waitForTimeout(300); return; }
    // landed, or the view is at its end and the wheel moves nothing: three
    // of those in turn and the control can't be brought clear
    stalls = (await wheeled(page, id, at.down ? 20 : -20)) ? 0 : stalls + 1;
    if (stalls >= 3) break;
  }
  throw new Error("control did not come clear into sight: " + JSON.stringify({ view: v, target: await locator.boundingBox() }));
}

// viewOf: the id of the view the control is in ("" for none), read once
// it is in the page: an element just drawn again over is out of it, and in
// no view (#2658)
async function viewOf(page, locator) {
  for (let i = 0; i < 50; i++) {
    const id = await locator.evaluateAll((es) => es.length === 1 ? es[0].closest(".view")?.id || "" : null);
    if (id != null) return id;
    await page.waitForTimeout(40);
  }
  throw new Error("control not in the page: " + locator);
}

// wheeled sends one wheel over the view and waits for its scroll to land;
// false when the view didn't move in a second (it is at its end). The
// wheel is sent over a point of the view with no box of its own that
// scrolls under it: one under the pointer takes the wheel first, as
// Routing's steps did once they scrolled in 200px (ca9b5048e), and the
// view never moved.
async function wheeled(page, id, dy) {
  const { was, x, y } = await page.locator("#" + id).evaluate((v) => {
    const r = v.getBoundingClientRect();
    const scrolls = (e) => e.scrollHeight > e.clientHeight && /auto|scroll/.test(getComputedStyle(e).overflowY);
    for (let fy = 0.5; fy < 1; fy += 0.1) for (const fx of [0.5, 0.15, 0.85, 0.02]) for (const s of [1, -1]) {
      const x = r.left + r.width * fx, y = r.top + r.height * (s > 0 ? fy : 1 - fy);
      let e = document.elementFromPoint(x, y);
      if (!e || !v.contains(e)) continue;
      while (e !== v && !scrolls(e)) e = e.parentElement;
      if (e === v) return { was: v.scrollTop, x, y };
    }
    return { was: v.scrollTop, x: r.left + r.width / 2, y: r.top + r.height / 2 };
  });
  await page.mouse.move(x, y);
  await page.mouse.wheel(0, dy);
  return page.waitForFunction(([id, was]) => document.getElementById(id).scrollTop !== was, [id, was], { timeout: 1000 }).then(() => true, () => false);
}

// wheelTo scrolls the page the way a reader does, down till the control's
// top is at y or above, so a click that moved the page would show; then
// reach makes sure it is clear in sight. Each wheel lands before the
// control is looked at again (#2370).
async function wheelTo(page, locator, y) {
  await locator.waitFor({ state: "visible" });
  const id = await viewOf(page, locator);
  const v = await page.locator("#" + id).boundingBox();
  await page.mouse.move(v.x + v.width / 2, v.y + v.height / 2);
  for (let i = 0; i < 80; i++) {
    const top = await locator.evaluateAll((es) => es.length === 1 && es[0].closest(".view") ? es[0].getBoundingClientRect().top : null);
    if (top == null) { await page.waitForTimeout(40); continue; }
    if (top <= y) break;
    if (!(await wheeled(page, id, 50))) break;
  }
  await reach(page, locator);
}

async function click(page, locator) {
  await inView(page, locator);
  await locator.click();
}

// Settings is drawn from each read of the settings, and opening the page
// reads them twice: the page's own read, then the window's load() once the
// state is in. Every row is replaced by the second draw, so a row found
// after the first is detached under whatever the test does to it next
// (#1653: scrollIntoViewIfNeeded in WebKit on a busy machine). Call
// watchSettingsReads before page.goto; settingsDrawn then waits until load()
// has asked for its read and every read asked for has been drawn. A page is
// watched once however many times it is opened.
const watched = new WeakSet();
async function watchSettingsReads(page) {
  if (watched.has(page)) return;
  watched.add(page);
  await page.addInitScript(() => {
    const reads = window.__settingsReads = { asked: 0, drawn: 0 };
    const fetch0 = window.fetch;
    window.fetch = function (input, init) {
      const p = fetch0.apply(this, arguments);
      const url = new URL(typeof input === "string" ? input : input.url, location.href);
      if (url.pathname !== "/api/settings" || (init?.method || "GET") !== "GET") return p;
      reads.asked++;
      // drawn a task after its body is in: loadSettings draws in the
      // microtasks that follow
      const drawn = () => setTimeout(() => reads.drawn++);
      return p.then((res) => {
        const text = res.text;
        res.text = function () { return text.call(this).finally(drawn); };
        return res;
      }, (e) => { drawn(); throw e; });
    };
  });
}

// load.done is set in the same run of load() that then asks for the
// settings, with no wait between, so once it is set the read is counted.
const settingsDrawn = (page) => page.waitForFunction(() => load.done && __settingsReads.asked === __settingsReads.drawn);

// openPart opens a part of Settings (a tab, or its entry in the sidebar's
// strip) the way the keyboard does. A click waits for the tab to hold still
// over two animation frames, and on a loaded machine WebKit draws no frames
// for seconds while the page itself runs on, so the click can sit out the
// test's whole timeout over a tab that isn't moving (#2150). Enter on the
// focused button clicks it with no frame to wait for.
async function openPart(page, id) {
  await page.locator(`:is(#setTab-${id}, .nav-s[data-of=setTab-${id}]):visible`).press("Enter");
}

// agentRow is a connected agent as /api/state lists it, opened by its
// Details; openAgentSettings opens it on the Agents page, where its own
// settings are (Settings had them till the owner, 10-20), and waits till
// they are read and drawn. Watch the settings reads first.
const agentRow = (id, name) => ({ id, name, icon: id, wired: true, fields: [{ key: "model", label: "model", value: "relay/m1", options: [{ value: "relay/m1", label: "m1", ref: "relay/m1" }] }] });
async function openAgentSettings(page, id = "codex") {
  const link = page.locator(`.row.agent[data-id="${id}"] .ag-link`);
  await reach(page, link);
  await link.click();
  await page.locator(`.row.agent[data-id="${id}"] .ag-prefs`).waitFor();
  await page.waitForFunction(() => __settingsReads.asked > 0 && __settingsReads.asked === __settingsReads.drawn);
}

// settled waits for the page's motion to end: a routing group opening
// into its editor moves its height and parts for ~700ms (91b0be836), and
// where a control will be is read once that has stopped. Two frames pass
// first: WebKit lists an animation a click started only a frame later, so
// a look right after the click found none and went on.
async function settled(page) {
  await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  await page.waitForFunction(() => !document.getAnimations().some((a) => a.playState === "running" && Number.isFinite(a.effect?.getComputedTiming().endTime)));
}

// pickItem presses an item of an open picker where the reader sees it.
// A model picker is drawn again when its first read of /api/model-speeds
// answers (readSpeeds, once a minute and after every load), so an item
// found before that is detached by the time its box is asked for, and
// Playwright gives null (#4274: searcher-pick's Automatic after a reload).
// The item is found again each frame until its box is the same twice.
async function pickItem(page, locator) {
  let last = "";
  for (let i = 0; i < 200; i++) {
    const b = await locator.boundingBox().catch(() => null);
    const now = JSON.stringify(b);
    if (b && now === last) {
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      return;
    }
    last = now;
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  }
  throw new Error("the picker's item never settled: " + last);
}


// provPrefsProviders: /api/providers with a signed-in subscription for each
// id, so its editor holds its warm-ups, check-in and resets (PROV_PREFS);
// an id with no built-in row comes from a plugin of that name
const BUILTIN_SUBS = new Set(["codex", "claude", "workbuddy", "qoder", "qoder-cn"]);
function provPrefsProviders(ids, names = {}) {
  const base = { icon: "generic", chat: "https://example.invalid/v1", models: [], agents: [], fallback: [], headers: {}, keyList: [], routing: "" };
  return {
    providers: ids.map((id) => ({ ...base, id, name: names[id] || id, account: { agent: id, agentName: names[id] || id, user: "ann@example.test", logins: [{ agent: id, user: "ann@example.test", plan: "Pro", active: true, on: true }] } })),
    plugins: ids.filter((id) => !BUILTIN_SUBS.has(id)).map((id) => ({ id, pid: id, name: names[id] || id, spec: "fixture-" + id, methods: [] })),
    presets: [], excluded: [], gateway: { running: true, window: true },
  };
}
// openProvPrefs: the provider's editor, its Advanced opened on its own
// settings (fields there since the owner's 11-09)
async function openProvPrefs(page, id) {
  await page.goto(`http://magpie.test/?view=providers&edit=${encodeURIComponent(id)}`);
  const adv = page.locator(".editor details.advanced");
  await adv.waitFor({ timeout: 60000 });
  if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
  await page.locator(".editor .prov-prefs .row").filter({ visible: true }).first().waitFor({ timeout: 60000 });
}
// inEditor brings a control in a provider's editor into sight. The editor
// is in the modal, whose body isn't a view, so the app lets the scroll
// stand; then it waits till no box has scrolled for ten frames, so a check
// that a click left the page where it was reads it at rest.
async function inEditor(page, locator) {
  // a code scroll is put back (gui-shell.md, "A click never moves"): the
  // wheel over the editor's body, as a reader, till the element is inside
  // it, clear of the bar under it; a save draws the lists again, so the
  // element is found again each time
  for (let round = 0; round < 5; round++) {
  await locator.waitFor({ state: "visible" });
  let inside = false;
  for (let i = 0; i < 200; i++) {
    const at = await locator.evaluateAll((es) => {
      const e = es.length === 1 ? es[0] : null, body = e?.closest(".ebody");
      if (!e || !e.isConnected) return null;
      if (!body) return { none: true };
      const r = e.getBoundingClientRect(), v = body.getBoundingClientRect();
      // a point whose nearest box that can scroll is the body: one inside
      // it (a list of its own) takes the wheel first, even with nothing
      // to scroll in Chromium, and the body never moves
      const scrolls = (n) => /auto|scroll/.test(getComputedStyle(n).overflowY);
      let px = v.left + v.width / 2, py = (v.top + v.bottom) / 2;
      find: for (let fy = 0.5; fy < 1; fy += 0.1) for (const fx of [0.5, 0.15, 0.85, 0.02]) for (const s of [1, -1]) {
        const x = v.left + v.width * fx, y = v.top + v.height * (s > 0 ? fy : 1 - fy);
        let n = document.elementFromPoint(x, y);
        if (!n || !body.contains(n)) continue;
        while (n !== body && !scrolls(n)) n = n.parentElement;
        if (n === body) { px = x; py = y; break find; }
      }
      // and nothing lies over it (a bar held at the editor's foot): a
      // click there would be scrolled to by Playwright first
      const h = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      const over = !!h && h !== e && !e.contains(h);
      return { top: r.top, bottom: r.bottom, vt: v.top, vb: v.bottom, px, py, over, low: r.top > (v.top + v.bottom) / 2 };
    });
    if (!at) { await page.waitForTimeout(40); continue; }
    if (at.none) { await locator.scrollIntoViewIfNeeded(); inside = true; break; }
    if (at.top >= at.vt + 8 && at.bottom <= at.vb - 8 && !at.over) { inside = true; break; }
    await page.mouse.move(at.px, at.py);
    await page.mouse.wheel(0, at.top < at.vt + 8 || at.over && !at.low ? -40 : 40);
    await page.waitForTimeout(30);
  }
  await page.evaluate(() => new Promise((done) => {
    let last = "", same = 0;
    const at = () => [scrollY, ...[...document.querySelectorAll("*")].map((e) => e.scrollTop)].join(",");
    const f = () => { const now = at(); same = now === last ? same + 1 : 0; last = now; if (same >= 10) done(); else requestAnimationFrame(f); };
    requestAnimationFrame(f);
  }));
  // the editor drawn again meanwhile (a load come in) starts at its top:
  // reached again
  if (inside && await locator.evaluateAll((es) => {
    const e = es.length === 1 ? es[0] : null, body = e?.closest(".ebody");
    if (!e || !body) return !!e;
    const r = e.getBoundingClientRect(), v = body.getBoundingClientRect();
    return r.top >= v.top && r.bottom <= v.bottom;
  })) return;
  }
  throw new Error("not brought into the editor's sight: " + locator);
}

// The Usage page's Requests: a row's click opens that one request on a page
// of its own (#ledReqPane, geesehowardt7 on X), and Requests ‹ goes back to
// the list where it was left. openRequest gives the page's .led-detail.
async function openRequest(page, row) {
  await row.click();
  const d = page.locator("#ledReqPane .led-detail");
  await d.waitFor();
  return d;
}
async function closeRequest(page) {
  await page.locator("#ledReqPane .led-req-back").click();
  await page.locator("#ledReqPane").waitFor({ state: "hidden" });
}

module.exports = { openRequest, closeRequest, provPrefsProviders, openProvPrefs, inEditor, settled, inView, reach, wheelTo, click, openPart, watchSettingsReads, settingsDrawn, agentRow, openAgentSettings, pickItem };
