// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's keys serving one model fold into one row on the Routing page
// (#1319, tkhs101: 53 keys for agnes-3.0-flash were 53 rows alike). On the
// stage, three keys or more of one provider for one model are one row: its
// name, how many keys, the one answering or that answered by name, how many
// are there to route to and how many rest. It opens in place to head each
// key, wired from it as a group in the group heads its models, and folds
// again; the row clicked stays where it is, and which are open is
// remembered across a reload. Two keys stay two rows, one key one. A
// request that comes flies to the fold and says there which key it went
// to; a key that fails tags the fold; opened, the key's own row lights.
// Under the requests, Accounts and keys sums a provider's keys up the same
// way and opens to each. At 440px nothing in the rows spills. English,
// Chinese, Japanese and German; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (s) => new Date(now.getTime() - s * 1e3).toISOString();
const ahead = (m) => new Date(now.getTime() + m * 60e3).toISOString();
const pad = (i) => String(i).padStart(2, "0");
const M = "agnes-3.0-flash";
const rest = (k) => ({ why: "rate", until: ahead(3), key: k, for: 180e9 });
const agnes = (i) => `agnes#k${pad(i)}`;
// the reporter's: 53 keys for one model on one provider (two resting), and
// beside them in the group a provider with two keys and one with one
const order = (resting = [5, 10]) => [
  ...Array.from({ length: 53 }, (_, i) => ({ id: agnes(i + 1), provider: "agnes", name: "Agnes", who: `agnes-key-${pad(i + 1)}`, kind: "key", model: M,
    ...(resting.includes(i + 1) ? { rest: rest(agnes(i + 1)) } : {}) })),
  ...[1, 2].map((i) => ({ id: `relay#r${i}`, provider: "relay", name: "Relay", who: `relay-${i}`, kind: "key", model: M })),
  { id: "solo#s1", provider: "solo", name: "Solo", who: "solo-1", kind: "key", model: M },
];
const group = { id: "flash", name: "Flash", routing: "", members: ["agnes/" + M, "relay/" + M, "solo/" + M] };
const route = (id, tries, extra = {}) => ({ id, seq: id, time: ago(60 - id), agent: "codex", model: "flash", provider: "agnes", done: tries.every((x) => x.done),
  status: 200, ms: 900, tokens: 1200, group, order: order(), tries, ...extra });
const first = route(1, [
  { id: agnes(5), model: M, start: ago(59), done: true, status: 429, fail: "rate", ms: 300, rest: rest(agnes(5)) },
  { id: agnes(1), model: M, start: ago(59), done: true, status: 200, ms: 900 },
]);

function serve(lang, live) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) {
        // live updates: each waits until the test sends one
        if (live.queued.length) return json(live.queued.shift());
        live.pending.push(request);
        return;
      }
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1, rerouted: 1, errors: 0 }, routes: [first] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, lang, width = 1100) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const live = { pending: [], queued: [], seq: 1 };
  await page.route("**/*", serve(lang, live));
  const ready = async () => {
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-accts li.rt-fold").first().waitFor();
    // each row's state is filled on the next frame
    await page.waitForFunction(() => document.querySelector(".rt-accts li.rt-fold em")?.textContent.trim());
  };
  await ready();
  // send sends the gateway's next trace update
  const send = (...routes) => {
    const d = { mine: true, now: new Date().toISOString(), seq: ++live.seq, totals: { requests: live.seq }, routes };
    const r = live.pending.shift();
    if (r) r.fulfill({ json: d }); else live.queued.push(d);
  };
  const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
    const s = (lang !== "en" && I18N[lang]?.[key]) || key;
    return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
  }, [lang, key, vars]);
  const fold = page.locator(".rt-accts li.rt-fold");
  const keyRow = (who) => page.locator(".rt-accts li", { has: page.locator(".who", { hasText: new RegExp(`^${who}$`) }) });
  const names = () => page.locator(".rt-accts > li:not(.rt-sub)").evaluateAll((lis) => lis.map((li) => li.classList.contains("rt-fold")
    ? "fold:" + li.querySelector(".who").textContent : li.querySelector(".who")?.textContent));
  return { page, errors, send, say, fold, keyRow, names, ready };
}

// nothing in a row spills or is cut off at its side
const spills = (page, sel) => page.locator(sel).evaluateAll((els) => els.filter((e) => e.scrollWidth > e.clientWidth + 1 || e.getBoundingClientRect().right > document.documentElement.clientWidth)
  .map((e) => e.className + ": " + e.textContent.trim().slice(0, 60)));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: 53 keys for one model are one row on the stage, opened in place and remembered`, async (t) => {
      const { page, errors, say, fold, keyRow, names, ready } = await open(t, engine, lang);
      // folded: one row for Agnes's 53 keys; Relay's two and Solo's one as they were
      assert.deepEqual(await names(), ["fold:Agnes", "relay-1", "relay-2", "solo-1"]);
      assert.equal(await fold.locator(".n").textContent(), await say("{n} keys", { n: 53 }));
      assert.equal(await fold.locator("code.mdl").textContent(), M);
      // what they say together: the key that answered, by name, 51 there to route to, 2 resting
      const answered = await say("answered this request");
      assert.equal((await fold.locator("em").textContent()).trim(),
        [`agnes-key-01: ${answered}`, await say("{n} available", { n: 51 }), await say("{n} resting", { n: 2 })].join(" · "));
      assert.equal(await fold.evaluate((li) => li.classList.contains("on")), true);
      assert.equal(await fold.getAttribute("aria-expanded"), "false");
      assert.equal(await fold.getAttribute("title"), await say("Show each key"));
      // its wire lights, and no folded key has one drawn
      assert.equal(await page.locator(".rt-wires path.live").count() >= 1, true);
      assert.equal(await page.locator(".rt-wires path[d]").count(), 1 /* agent */ + 4 /* fold, relay ×2, solo */);

      // opened in place: the row clicked stays where it is, each key under it
      const top = await fold.evaluate((li) => li.getBoundingClientRect().top);
      await fold.click();
      await keyRow("agnes-key-53").waitFor();
      const shown = await names();
      assert.equal(shown.length, 1 + 53 + 3);
      assert.deepEqual(shown.slice(0, 3), ["fold:Agnes", "agnes-key-01", "agnes-key-02"]);
      assert.deepEqual(shown.slice(-4), ["agnes-key-53", "relay-1", "relay-2", "solo-1"]);
      assert.equal(await fold.evaluate((li) => li.getBoundingClientRect().top), top);
      assert.equal(await fold.getAttribute("aria-expanded"), "true");
      assert.equal(await fold.getAttribute("title"), await say("Fold these keys into one row"));
      // set in under it, the one that answered lit, the resting dashed, each saying its own
      assert.equal(await keyRow("agnes-key-01").evaluate((li) => getComputedStyle(li).getPropertyValue("--depth").trim()), "1");
      assert.equal(await keyRow("agnes-key-01").evaluate((li) => li.classList.contains("on")), true);
      assert.equal(await keyRow("agnes-key-05").evaluate((li) => li.classList.contains("rest")), true);
      assert.equal((await keyRow("agnes-key-01").locator("em").textContent()).trim(), answered);
      // a wire each, from under the fold's heading
      assert.equal(await page.locator(".rt-wires path[d]").count(), 1 + 1 + 53 + 3);
      const [hx, kx] = await page.evaluate(() => {
        const s = document.querySelector(".rt-stage").getBoundingClientRect(), f = document.querySelector(".rt-fold").getBoundingClientRect();
        return [f.left - s.left, document.querySelectorAll(".rt-accts > li")[1].getBoundingClientRect().left - s.left];
      });
      assert.ok(kx > hx, `keys ${kx} set in from the heading ${hx}`);

      // remembered across a reload
      await ready();
      await keyRow("agnes-key-53").waitFor();
      assert.equal(await fold.getAttribute("aria-expanded"), "true");
      // and folded again, by the keyboard as well
      await fold.focus();
      await page.keyboard.press("Enter");
      await keyRow("agnes-key-53").waitFor({ state: "detached" });
      assert.deepEqual(await names(), ["fold:Agnes", "relay-1", "relay-2", "solo-1"]);
      await ready();
      assert.deepEqual(await names(), ["fold:Agnes", "relay-1", "relay-2", "solo-1"]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a request that comes flies to the fold, which says which key; opened, the key's row lights`, async (t) => {
      const { page, errors, send, say, fold, keyRow } = await open(t, engine, lang);
      const em = () => fold.locator("em").textContent().then((s) => s.trim());
      // agnes-key-07 is answering a new request
      const r2 = route(2, [{ id: agnes(7), model: M, start: ago(1), done: false }]);
      send(r2);
      const answering = await say("answering…");
      await page.waitForFunction((want) => document.querySelector(".rt-fold em").textContent.startsWith(want), `agnes-key-07: ${answering}`);
      assert.equal(await fold.evaluate((li) => li.classList.contains("on")), true);
      // it answers
      send({ ...r2, done: true, tries: [{ ...r2.tries[0], done: true, status: 200, ms: 800 }] });
      const answered = await say("answered this request");
      await page.waitForFunction((want) => document.querySelector(".rt-fold em").textContent.startsWith(want), `agnes-key-07: ${answered}`);
      // agnes-key-08 fails and rests, agnes-key-09 is asked next: the fold is tagged with the failure, and counts 3 resting
      const r3 = { ...route(3, [
        { id: agnes(8), model: M, start: ago(1), done: true, status: 429, fail: "rate", ms: 200, rest: rest(agnes(8)) },
        { id: agnes(9), model: M, start: ago(1), done: false },
      ]), order: order([5, 10]) };
      send(r3);
      await page.waitForFunction((want) => document.querySelector(".rt-fold em").textContent.includes(want), await say("{n} resting", { n: 3 }));
      assert.equal(await em(), [`agnes-key-09: ${answering}`, await say("{n} available", { n: 50 }), await say("{n} resting", { n: 3 })].join(" · "));
      assert.match(await fold.locator(".tag").textContent(), /^429 · /);
      // opened while it answers: agnes-key-09's own row is lit, its fold's too, the resting dashed
      await fold.click();
      await keyRow("agnes-key-09").waitFor();
      assert.equal(await keyRow("agnes-key-09").evaluate((li) => li.classList.contains("on")), true);
      assert.equal((await keyRow("agnes-key-09").locator("em").textContent()).trim(), answering);
      assert.equal(await keyRow("agnes-key-08").evaluate((li) => li.classList.contains("rest")), true);
      assert.equal(await fold.evaluate((li) => li.classList.contains("on")), true);
      // and the next one comes to agnes-key-12, flown to its own row
      send({ ...r3, done: true, tries: [r3.tries[0], { ...r3.tries[1], done: true, status: 200, ms: 500 }] });
      const r4 = route(4, [{ id: agnes(12), model: M, start: ago(0), done: false }]);
      send(r4);
      await page.waitForFunction(() => [...document.querySelectorAll(".rt-accts li")].find((li) => li.querySelector(".who")?.textContent === "agnes-key-12")?.classList.contains("on"));
      assert.ok((await em()).startsWith(`agnes-key-12: ${answering}`));
      assert.equal(await keyRow("agnes-key-09").evaluate((li) => li.classList.contains("on")), false);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: Accounts and keys sums a provider's keys up in one row, opened to each`, async (t) => {
      const { page, errors, say } = await open(t, engine, lang);
      const acts = page.locator(".rt-acts");
      const sum = acts.locator(".rt-act.rt-keys");
      await sum.waitFor();
      assert.equal(await sum.count(), 1);
      assert.equal((await sum.locator(".nm b").textContent()).trim(), await say("{n} keys", { n: 53 }));
      assert.equal((await sum.locator(".st").textContent()).trim(), [await say("{n} available", { n: 51 }), await say("{n} resting", { n: 2 })].join(" · "));
      // tried twice in all, answered once, one rate limited
      const tally = await sum.locator(".tally span").allTextContents();
      assert.equal(tally[0], await say("tried {n}", { n: 2 }));
      assert.equal(tally[1], await say("answered {n}", { n: 1 }));
      assert.equal(tally.length, 4);
      // Relay's two keys and Solo's one, each its own row
      const rows = () => acts.locator(".rt-act:not(.rt-keys) .nm b").allTextContents();
      assert.deepEqual(await rows(), ["relay-1", "relay-2", "solo-1"]);
      // wheeled to, as a reader would (the page holds still for a script's scroll)
      await page.mouse.move(300, 300);
      for (let i = 0; i < 20 && (await sum.boundingBox()).y > 500; i++) {
        await page.mouse.wheel(0, 120);
        await page.waitForTimeout(150);
      }
      await page.waitForTimeout(300);
      const top = await sum.evaluate((e) => e.getBoundingClientRect().top);
      const sb = await sum.boundingBox();
      await page.mouse.click(sb.x + 40, sb.y + 10);
      await page.waitForFunction(() => document.querySelectorAll(".rt-acts .rt-act:not(.rt-keys)").length === 56);
      const all = await rows();
      assert.deepEqual(all.slice(0, 2), ["agnes-key-01", "agnes-key-02"]);
      assert.deepEqual(all.slice(-3), ["relay-1", "relay-2", "solo-1"]);
      assert.equal(await sum.getAttribute("aria-expanded"), "true");
      assert.equal(await sum.evaluate((e) => e.getBoundingClientRect().top), top);
      // the list is drawn again each second: it stays open
      await page.waitForTimeout(1200);
      assert.equal(await acts.locator(".rt-act:not(.rt-keys)").count(), 56);
      await sum.click();
      await page.waitForFunction(() => document.querySelectorAll(".rt-acts .rt-act:not(.rt-keys)").length === 3);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: at 440px the fold and its keys fit`, async (t) => {
      const { page, errors, say, fold, keyRow } = await open(t, engine, lang, 440);
      // the list is laid out by its own width, once it is measured
      await page.locator(".rt-accts.max460").waitFor();
      await page.waitForTimeout(300);
      assert.deepEqual(await spills(page, ".rt-accts > li > b, .rt-accts > li"), []);
      // what it says goes under its name, the arrow beside the name
      const [b, em, chev] = await fold.evaluate((li) => [li.querySelector("b"), li.querySelector("em"), li.querySelector(".chev")].map((e) => e.getBoundingClientRect()).map((r) => ({ top: r.top, bottom: r.bottom, right: r.right })));
      assert.ok(em.top >= b.bottom - 1, `em ${em.top} under the name ${b.bottom}`);
      assert.ok(chev.top < b.bottom, "the arrow is beside the name");
      assert.ok((await fold.locator("em").textContent()).includes(await say("{n} resting", { n: 2 })));
      await fold.click();
      await keyRow("agnes-key-53").waitFor();
      assert.deepEqual(await spills(page, ".rt-accts > li"), []);
      assert.deepEqual(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true);
      assert.deepEqual(errors, []);
    });
  }
}
