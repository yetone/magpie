// Run with Node's test runner and Playwright on the module path; see README.md.
// A group in the group and a provider's accounts fold on the Routing page
// (Aiirobyte on Discord: a routing group of many accounts took so much of
// the page that its groups, to edit, were a long scroll down). On the
// stage, a group in the group folds from its heading: folded, it says how
// many it routes to and what they do together, a request to one of them
// flies to the heading, and a failure tags it. A provider's accounts for
// one model, three or more, fold into one row as its keys do. Long lists
// start folded (six or more), short ones open; whichever the reader picks
// is kept across a reload. Under the requests, Accounts and keys sums a
// provider's accounts up the same way. A click never moves the page. At
// 420px nothing spills. English, Chinese (both), Japanese and German; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (s) => new Date(now.getTime() - s * 1e3).toISOString();
const ahead = (m) => new Date(now.getTime() + m * 60e3).toISOString();
const M = "gpt-6";
const rest = (k) => ({ why: "rate", until: ahead(3), key: k, for: 180e9 });
const acct = (p, name, user, extra = {}) => ({ id: `${p}@${user}`, provider: p, name, who: user, kind: "account", model: M, plan: "Pro", ...extra });
// the group: a group in it of eight ChatGPT accounts, then a team of
// three accounts, a provider of seven and one key
const pool = Array.from({ length: 8 }, (_, i) => `p${i + 1}@x.test`);
const big = Array.from({ length: 7 }, (_, i) => `b${i + 1}@x.test`);
const team = ["t1@x.test", "t2@x.test", "t3@x.test"];
const order = (resting = ["p2@x.test"]) => [
  ...pool.map((u) => acct("chatgpt", "ChatGPT", u, { via: ["pool"], ...(resting.includes(u) ? { rest: rest(`chatgpt@${u}`) } : {}) })),
  ...team.map((u) => acct("team", "Team", u)),
  ...big.map((u) => acct("big", "Big", u)),
  { id: "solo#s1", provider: "solo", name: "Solo", who: "solo-1", kind: "key", model: M },
];
const group = { id: "main", name: "Main", routing: "", members: ["chatgpt/" + M, "team/" + M, "big/" + M, "solo/" + M],
  subs: [{ id: "pool", name: "Pool", routing: "order", in: "main" }], via: ["pool", "", "", ""] };
const route = (id, tries, extra = {}) => ({ id, seq: id, time: ago(60 - id), agent: "codex", model: "main", provider: "chatgpt", done: tries.every((x) => x.done),
  status: 200, ms: 900, tokens: 1200, group, order: order(), tries, ...extra });
const first = route(1, [{ id: "chatgpt@p1@x.test", model: M, start: ago(59), done: true, status: 200, ms: 900 }]);

function serve(lang, live) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) {
        if (live.queued.length) return json(live.queued.shift());
        live.pending.push(request);
        return;
      }
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [first] });
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
    await page.locator(".rt-accts li.rt-sub").first().waitFor();
    await page.waitForFunction(() => document.querySelector(".rt-accts li.rt-fold em")?.textContent.trim());
  };
  await ready();
  const send = (...routes) => {
    const d = { mine: true, now: new Date().toISOString(), seq: ++live.seq, totals: { requests: live.seq }, routes };
    const r = live.pending.shift();
    if (r) r.fulfill({ json: d }); else live.queued.push(d);
  };
  const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
    const s = (lang !== "en" && I18N[lang]?.[key]) || key;
    return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
  }, [lang, key, vars]);
  const sub = page.locator(".rt-accts li.rt-sub");
  const fold = (name) => page.locator(".rt-accts li.rt-fold", { has: page.locator(".who", { hasText: new RegExp(`^${name}$`) }) });
  // what the stage lists, top to bottom: a group in the group's heading,
  // a fold (+ open or - folded), or an account's or key's row
  const names = () => page.locator(".rt-accts > li").evaluateAll((lis) => lis.map((li) => li.classList.contains("rt-sub") ? "sub:" + li.querySelector("b").textContent + (li.classList.contains("open") ? "+" : "-")
    : li.classList.contains("rt-fold") ? "fold:" + li.querySelector(".who").textContent + (li.classList.contains("open") ? "+" : "-") : li.querySelector(".who")?.textContent));
  const viewTop = () => page.evaluate(() => document.querySelector("#view-routing").scrollTop);
  return { page, errors, send, say, sub, fold, names, ready, viewTop };
}

const spills = (page, sel) => page.locator(sel).evaluateAll((els) => els.filter((e) => e.scrollWidth > e.clientWidth + 1 || e.getBoundingClientRect().right > document.documentElement.clientWidth)
  .map((e) => e.className + ": " + e.textContent.trim().slice(0, 60)));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a group in the group of eight accounts starts folded, a short list open, a long one folded; opened and folded in place, kept`, async (t) => {
      const { page, errors, say, sub, fold, names, ready, viewTop } = await open(t, engine, lang);
      // eight in Pool: folded at its heading; Team's three open under their fold; Big's seven folded
      assert.deepEqual(await names(), ["sub:Pool-", "fold:Team+", ...team, "fold:Big-", "solo-1"]);
      assert.equal(await sub.locator(".n").textContent(), await say("{n} on", { n: 8 }));
      const answered = await say("answered this request");
      assert.equal((await sub.locator("em").textContent()).trim(),
        [`p1@x.test: ${answered}`, await say("{n} available", { n: 7 }), await say("{n} resting", { n: 1 })].join(" · "));
      assert.equal(await sub.getAttribute("aria-expanded"), "false");
      assert.equal(await sub.getAttribute("title"), await say("Show what this group routes to"));
      assert.equal(await sub.evaluate((li) => li.classList.contains("on")), true);
      assert.equal(await fold("Team").locator(".n").textContent(), await say("{n} accounts", { n: 3 }));
      assert.equal(await fold("Team").getAttribute("title"), await say("Fold these accounts into one row"));
      assert.equal(await fold("Big").locator(".n").textContent(), await say("{n} accounts", { n: 7 }));
      assert.equal(await fold("Big").getAttribute("title"), await say("Show each account"));
      // a wire each for the agent, the heading, Team's fold and its three, Big's fold and Solo: none to a folded account
      assert.equal(await page.locator(".rt-wires path[d]").count(), 1 + 1 + 1 + 3 + 1 + 1);

      // opened from its heading: it stays where it is, the page doesn't move,
      // and its eight accounts are a fold of their own, folded (eight is long)
      const top = await sub.evaluate((li) => li.getBoundingClientRect().top), vt = await viewTop();
      await sub.click();
      await page.waitForFunction(() => document.querySelector(".rt-accts li.rt-sub").classList.contains("open"));
      assert.deepEqual(await names(), ["sub:Pool+", "fold:ChatGPT-", "fold:Team+", ...team, "fold:Big-", "solo-1"]);
      assert.equal(await sub.evaluate((li) => li.getBoundingClientRect().top), top);
      assert.equal(await viewTop(), vt);
      assert.equal(await sub.getAttribute("title"), await say("Fold this group into one row"));
      assert.equal((await sub.locator("em").textContent()).trim(), "");
      await fold("ChatGPT").click();
      await page.waitForFunction(() => document.querySelectorAll(".rt-accts > li").length === 1 + 1 + 8 + 1 + 3 + 1 + 1);
      assert.deepEqual((await names()).slice(0, 4), ["sub:Pool+", "fold:ChatGPT+", "p1@x.test", "p2@x.test"]);
      // set in under the fold, under the heading
      const depth = (li) => li.evaluate((x) => getComputedStyle(x).getPropertyValue("--depth").trim());
      assert.equal(await depth(fold("ChatGPT")), "1");
      assert.equal(await depth(page.locator(".rt-accts > li").nth(2)), "2");
      // Team folded by the reader, Big opened
      await fold("Team").click();
      await fold("Big").click();
      await page.waitForFunction(() => document.querySelectorAll(".rt-accts > li").length === 1 + 1 + 8 + 1 + 1 + 7 + 1);

      // kept across a reload
      await ready();
      assert.deepEqual(await names(), ["sub:Pool+", "fold:ChatGPT+", ...pool, "fold:Team-", "fold:Big+", ...big, "solo-1"]);
      // and folded again from the keyboard: kept so too
      await sub.focus();
      await page.keyboard.press("Enter");
      await page.waitForFunction(() => !document.querySelector(".rt-accts li.rt-sub").classList.contains("open"));
      assert.deepEqual(await names(), ["sub:Pool-", "fold:Team-", "fold:Big+", ...big, "solo-1"]);
      await ready();
      assert.deepEqual(await names(), ["sub:Pool-", "fold:Team-", "fold:Big+", ...big, "solo-1"]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a request to an account in a folded group in the group flies to its heading, which says which and is tagged`, async (t) => {
      const { page, errors, send, say, sub } = await open(t, engine, lang);
      const em = () => sub.locator("em").textContent().then((s) => s.trim());
      const answering = await say("answering…");
      // p3 fails and rests, p4 is asked next and answers
      const r2 = { ...route(2, [
        { id: "chatgpt@p3@x.test", model: M, start: ago(1), done: true, status: 429, fail: "rate", ms: 200, rest: rest("chatgpt@p3@x.test") },
        { id: "chatgpt@p4@x.test", model: M, start: ago(1), done: false },
      ]), order: order(["p2@x.test", "p3@x.test"]) };
      send(r2);
      await page.waitForFunction((want) => document.querySelector(".rt-accts li.rt-sub em").textContent.startsWith(want), `p4@x.test: ${answering}`);
      assert.equal(await em(), [`p4@x.test: ${answering}`, await say("{n} available", { n: 6 }), await say("{n} resting", { n: 2 })].join(" · "));
      assert.equal(await sub.evaluate((li) => li.classList.contains("on")), true);
      assert.match(await sub.locator(".tag").textContent(), /^429 · /);
      // no account of it is drawn while it is folded
      assert.equal(await page.locator(".rt-accts > li", { has: page.locator(".who", { hasText: /^p\d@x\.test$/ }) }).count(), 0);
      send({ ...r2, done: true, tries: [r2.tries[0], { ...r2.tries[1], done: true, status: 200, ms: 500 }] });
      await page.waitForFunction((want) => document.querySelector(".rt-accts li.rt-sub em").textContent.startsWith(want), `p4@x.test: ${await say("answered this request")}`);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: Accounts and keys sums a provider's accounts up in one row, long lists folded, short ones open`, async (t) => {
      const { page, errors, say } = await open(t, engine, lang);
      const acts = page.locator(".rt-acts");
      await acts.locator(".rt-act.rt-keys").first().waitFor();
      const sums = await acts.locator(".rt-act.rt-keys").evaluateAll((rs) => rs.map((r) => [r.querySelector(".nm b").textContent, r.getAttribute("aria-expanded")]));
      assert.deepEqual(sums.sort(), [
        [await say("{n} accounts", { n: 7 }), "false"],
        [await say("{n} accounts", { n: 8 }), "false"],
        [await say("{n} accounts", { n: 3 }), "true"],
      ].sort());
      // Team's three are listed under their row; Big's and ChatGPT's are not, Solo's key is
      const rows = () => acts.locator(".rt-act:not(.rt-keys) .nm b").allTextContents();
      assert.deepEqual((await rows()).sort(), [...team, "solo-1"].sort());
      const big7 = acts.locator(".rt-act.rt-keys", { hasText: await say("{n} accounts", { n: 7 }) });
      assert.equal(await big7.getAttribute("title"), await say("Show each account"));
      await page.mouse.move(300, 300);
      for (let i = 0; i < 20 && (await big7.boundingBox()).y > 500; i++) {
        await page.mouse.wheel(0, 120);
        await page.waitForTimeout(150);
      }
      await page.waitForTimeout(300);
      const top = await big7.evaluate((e) => e.getBoundingClientRect().top);
      const bb = await big7.boundingBox();
      await page.mouse.click(bb.x + 40, bb.y + 10);
      await page.waitForFunction(() => document.querySelectorAll(".rt-acts .rt-act:not(.rt-keys)").length === 3 + 1 + 7);
      assert.equal(await big7.getAttribute("aria-expanded"), "true");
      assert.equal(await big7.evaluate((e) => e.getBoundingClientRect().top), top);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: at 420px a folded group in the group and the folds fit`, async (t) => {
      const { page, errors, say, sub } = await open(t, engine, lang, 420);
      await page.locator(".rt-accts.max460").waitFor();
      await page.waitForTimeout(300);
      assert.ok((await sub.locator("em").textContent()).includes(await say("{n} resting", { n: 1 })));
      assert.deepEqual(await spills(page, ".rt-accts > li"), []);
      // what it says goes under its name, on a line of its own
      const [b, em] = await sub.evaluate((li) => [li.querySelector("b"), li.querySelector("em")].map((e) => e.getBoundingClientRect()).map((r) => ({ top: r.top, bottom: r.bottom })));
      assert.ok(em.top >= b.bottom - 1, `em ${em.top} under the name ${b.bottom}`);
      await sub.click();
      await page.waitForFunction(() => document.querySelector(".rt-accts li.rt-sub").classList.contains("open"));
      assert.deepEqual(await spills(page, ".rt-accts > li"), []);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true);
      assert.deepEqual(errors, []);
    });
  }
}
