// Run with Node's test runner and Playwright on the module path; see README.md.
// A request that is over doesn't look called on the Routing page (#2386,
// Moody-Sin: magpie on a server, the agent long stopped, and the page and
// the app still showed the account that answered the last request as being
// called, however often reloaded). The row that answered the request shown
// stays lit (its dot filled, its wire drawn in the agent's colour), but
// only a request in flight moves: the row is `now`, its dot pulses and its
// wire flows. The same for a fold of keys and for the row it folds. Once
// the request is done nothing on the stage flows. English, Chinese
// (simplified and traditional), Japanese and German, at 1100px and 420px;
// no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (s) => new Date(now.getTime() - s * 1e3).toISOString();
const M = "gemini-3.8-flash";
// the reporter's: a provider's accounts (three, so they fold) and one more
// account of another beside them in a group
const order = [
  ...[1, 2, 3].map((i) => ({ id: `ag#a${i}`, provider: "ag", name: "Antigravity", who: `ag-${i}`, kind: "key", model: M })),
  { id: "solo#s1", provider: "solo", name: "Solo", who: "solo-1", kind: "key", model: M },
];
const group = { id: "flash", name: "Flash", routing: "", members: ["ag/" + M, "solo/" + M] };
const route = (id, tries) => ({ id, seq: id, time: ago(60 - id), agent: "zcode", model: "flash", provider: tries[0].id.split("#")[0], done: tries.every((x) => x.done),
  status: 200, ms: 900, tokens: 1200, group, order, tries });
const solo = (done) => ({ id: "solo#s1", model: M, start: ago(50), done, ...(done ? { status: 200, ms: 900 } : {}) });
const ag = (done) => ({ id: "ag#a2", model: M, start: ago(5), done, ...(done ? { status: 200, ms: 900 } : {}) });

function serve(lang, live) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "zcode", name: "ZCode", path: "/test/config.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) {
        if (live.queued.length) return json(live.queued.shift());
        live.pending.push(request);
        return;
      }
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route(1, [solo(true)])] });
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: the row that answered stays lit and still; only a request in flight flows`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        // motion on: what is checked is whether the dot pulses
        const page = await browser.newPage({ viewport: { width, height: 900 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (error) => errors.push(error.message));
        const live = { pending: [], queued: [], seq: 1 };
        await page.route("**/*", serve(lang, live));
        const send = (...routes) => {
          const d = { mine: true, now: new Date().toISOString(), seq: ++live.seq, totals: { requests: live.seq }, routes };
          const r = live.pending.shift();
          if (r) r.fulfill({ json: d }); else live.queued.push(d);
        };
        const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
          const s = (lang !== "en" && I18N[lang]?.[key]) || key;
          return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
        }, [lang, key, vars]);
        const row = page.locator(".rt-accts li:not(.rt-fold)", { has: page.locator(".who", { hasText: /^solo-1$/ }) });
        const fold = page.locator(".rt-accts li.rt-fold");
        // what the stage shows: each lit row, whether it is now, whether its
        // dot moves, and how many drawn wires flow and how many are lit
        const stage = () => page.evaluate(() => ({
          on: [...document.querySelectorAll(".rt-accts li.on")].map((li) => li.querySelector(".who")?.textContent + (li.classList.contains("now") ? ":now" : "")),
          moving: [...document.querySelectorAll(".rt-accts li .dot")].filter((d) => getComputedStyle(d, "::after").animationName !== "none").length,
          flowing: document.querySelectorAll(".rt-wires path.live[d]").length,
          lit: document.querySelectorAll(".rt-wires path.lit[d]").length,
        }));
        await page.goto("http://magpie.test/?view=routing");
        await row.waitFor();
        await page.waitForFunction(() => document.querySelector(".rt-accts li.on em")?.textContent.trim());

        // the reporter's: the last request is over, its account answered it
        assert.equal((await row.locator("em").textContent()).trim(), await say("answered this request"));
        assert.deepEqual(await stage(), { on: ["solo-1"], moving: 0, flowing: 0, lit: 1 });

        // a request comes and is with one of the folded keys: the fold flows
        send(route(2, [ag(false)]));
        await page.waitForFunction(() => document.querySelector(".rt-accts li.rt-fold.now"));
        let s = await stage();
        assert.deepEqual(s.on, ["Antigravity:now"]);
        assert.equal(s.moving, 1);
        assert.equal(s.flowing, 2, "the agent's wire and the fold's");
        assert.equal(s.lit, 0);
        // each flows as its copy in .rt-flows, a layer of its own, and is
        // itself hidden: in the svg of every wire, WebView2 drew all of them
        // again each frame one flowed (#3164)
        // (a wire's d set anew is copied on the next frame)
        const same = () => {
          const ds = (q) => [...document.querySelectorAll(q)].map((p) => p.getAttribute("d")).sort().join();
          return document.querySelectorAll(".rt-flows path[d]").length === 2 && ds(".rt-wires path.live[d]") === ds(".rt-flows path[d]");
        };
        await page.waitForFunction(same);
        assert.deepEqual(await page.evaluate(() => {
          const live = [...document.querySelectorAll(".rt-wires path.live[d]")], copies = [...document.querySelectorAll(".rt-flows path[d]")];
          return {
            hidden: live.filter((p) => getComputedStyle(p).visibility === "hidden" && getComputedStyle(p).animationName === "none").length,
            animated: copies.filter((p) => getComputedStyle(p).animationName !== "none").length,
            layer: getComputedStyle(document.querySelector(".rt-flows")).willChange,
          };
        }), { hidden: 2, animated: 0, layer: "transform" });
        // the copies' dashes move, from a timer 15 times a second and not
        // a CSS animation, which Chromium drew on the main thread every
        // frame: with five requests in flight that was a quarter of a core
        // in WebView2 (chenxiang, #5467). The dot held at the account fades
        // from the same timer, not a CSS animation either.
        const offsets = () => page.evaluate(() => [...document.querySelectorAll(".rt-flows path[d]")].map((p) => getComputedStyle(p).strokeDashoffset).join());
        const was = await offsets();
        await page.waitForFunction((was) => [...document.querySelectorAll(".rt-flows path[d]")].map((p) => getComputedStyle(p).strokeDashoffset).join() !== was, was);
        const ticks = await page.evaluate(() => new Promise((res) => {
          let n = 0;
          const o = new MutationObserver(() => n++);
          o.observe(document.querySelector(".rt-flows"), { attributes: true, attributeFilter: ["style"] });
          setTimeout(() => { o.disconnect(); res(n); }, 1000);
        }));
        assert(ticks >= 1 && ticks <= 30, `the dashes moved ${ticks} times in a second, not every frame`);
        // the dot let go at the account fades and comes back, by the timer
        await page.waitForFunction(() => document.querySelector(".rt-sky .pkt.held"), null, { timeout: 15000 });
        assert.equal(await page.evaluate(() => getComputedStyle(document.querySelector(".rt-sky .pkt.held")).animationName), "none");
        const fade = () => page.evaluate(() => getComputedStyle(document.querySelector(".rt-sky .pkt.held")).opacity);
        const fadeWas = await fade();
        await page.waitForFunction((was) => getComputedStyle(document.querySelector(".rt-sky .pkt.held")).opacity !== was, fadeWas);
        assert.match((await fold.locator("em").textContent()).trim(), new RegExp("^ag-2: " + (await say("answering…")).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));

        // opened, the key with the request is the one that flows
        await fold.click();
        await page.locator(".rt-accts li", { has: page.locator(".who", { hasText: /^ag-2$/ }) }).waitFor();
        s = await stage();
        assert.deepEqual(s.on.sort(), ["Antigravity:now", "ag-2:now"].sort());

        // it is answered: still lit, nothing moves
        send(route(2, [ag(true)]));
        await page.waitForFunction(() => !document.querySelector(".rt-accts li.now"));
        s = await stage();
        assert.deepEqual(s.on.sort(), ["Antigravity", "ag-2"].sort());
        assert.equal(s.moving, 0);
        assert.equal(s.flowing, 0);
        assert.equal(s.lit, 2, "the fold's wire and the key's");
        await page.waitForFunction(() => !document.querySelector(".rt-flows path"));
        assert.equal(await page.locator(".rt-wires path.flown").count(), 0);
        // and nothing flowing, the timer is gone: --rt-dash is let go and
        // set no more
        await page.waitForFunction(() => !document.querySelector(".rt-flows").style.getPropertyValue("--rt-dash"));
        assert.equal(await page.evaluate(() => new Promise((res) => {
          let n = 0;
          const o = new MutationObserver(() => n++);
          o.observe(document.querySelector(".rt-flows"), { attributes: true, attributeFilter: ["style"] });
          setTimeout(() => { o.disconnect(); res(n); }, 400);
        })), 0);

        // a reload shows it the same: lit and still
        await page.reload();
        await row.waitFor();
        await page.waitForFunction(() => document.querySelector(".rt-accts li.on em")?.textContent.trim());
        s = await stage();
        assert.equal(s.moving, 0);
        assert.equal(s.flowing, 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
