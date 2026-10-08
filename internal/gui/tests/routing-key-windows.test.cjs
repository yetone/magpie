// Run with Node's test runner and Playwright on the module path; see README.md.
// A key whose own usage windows magpie reads (a sub2api key given a 5-hour,
// day or 7-day limit) is told on the Routing page as an account is: its
// row says how much of its window is used and when it renews, with a bar,
// and the story says why it went first by its allowance — beside an
// account in a group, or among a provider's keys, all at 90% or more. A
// key not known is told as before. A lone key bought as a plan (#1016:
// Kimi Code's, 49% of its 5 hours used) says what it has left too, at
// 440px as well. In English, Chinese, Japanese and German.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (minutes) => new Date(now.getTime() - minutes * 60e3).toISOString();
const ahead = (hours) => new Date(now.getTime() + hours * 36e5).toISOString();
const week = (used, hours) => ({ known: true, used, renews: [ahead(hours)], amount: used * 8, limit: 800, unit: "USD" });

const scenes = {
  // a group: the sub2api key's week renews in a day, the account's in five
  group: {
    group: { id: "g", name: "Daily", routing: "" },
    order: [
      { id: "sub2api", provider: "sub2api", name: "Sub2API", kind: "provider", model: "gpt-6-astra", ...week(10, 24) },
      { id: "codex", provider: "codex", name: "Codex", who: "me@example.com", kind: "account", agent: "codex", model: "gpt-6-astra", known: true, used: 30, renews: [ahead(120)] },
    ],
  },
  // a provider's two keys, both at 90% or more
  low: {
    order: [
      { id: "relay#a", provider: "relay", name: "Relay", who: "relay-a", kind: "key", model: "gpt-6-astra", ...week(92, 30) },
      { id: "relay#b", provider: "relay", name: "Relay", who: "relay-b", kind: "key", model: "gpt-6-astra", ...week(95, 50) },
    ],
  },
  // a key not read yet beside one read: weighed with it, not in order
  mixed: {
    order: [
      { id: "relay#b", provider: "relay", name: "Relay", who: "relay-b", kind: "key", model: "gpt-6-astra" },
      { id: "relay#a", provider: "relay", name: "Relay", who: "relay-a", kind: "key", model: "gpt-6-astra", ...week(95, 30) },
    ],
  },
  mixedUsage: {
    order: [
      { id: "relay#b", provider: "relay", name: "Relay", who: "relay-b", kind: "key", model: "gpt-6-astra", routing: "usage", tokens: 900 },
      { id: "relay#a", provider: "relay", name: "Relay", who: "relay-a", kind: "key", model: "gpt-6-astra", routing: "usage", tokens: 100, ...week(95, 30) },
    ],
  },
  // #1016: Kimi Code (China) with one key on, its 5 hours 49% used
  lone: {
    order: [
      { id: "kimi-code-cn", provider: "kimi-code-cn", name: "Kimi Code (China)", kind: "provider", model: "kimi-for-coding", known: true, used: 49, renews: [ahead(26), ahead(2)] },
    ],
  },
  // keys whose windows aren't read: as before
  plain: {
    order: [
      { id: "relay#a", provider: "relay", name: "Relay", who: "relay-a", kind: "key", model: "gpt-6-astra" },
      { id: "relay#b", provider: "relay", name: "Relay", who: "relay-b", kind: "key", model: "gpt-6-astra" },
    ],
  },
};

function serve(lang, scene) {
  const s = scenes[scene];
  const route = {
    id: 1, seq: 1, time: ago(1), agent: "codex", model: "gpt-6-astra", provider: s.order[0].provider, done: true, status: 200, ms: 900, tokens: 1200,
    ...(s.group ? { group: s.group } : {}), order: s.order,
    tries: [{ id: s.order[0].id, model: "gpt-6-astra", start: ago(1), done: true, status: 200, ms: 900 }],
  };
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, lang, scene, width = 1100) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/*", serve(lang, scene));
  await page.goto("http://magpie.test/?view=routing");
  // the one request is opened already; below the fold at 440px, WebKit
  // never finds it still enough to click
  const req = page.locator(".rt-req").first();
  await req.waitFor();
  if (await req.getAttribute("aria-pressed") !== "true") await req.click();
  await page.locator(".rt-steps li.why").first().waitFor();
  // each row's state is filled on the next frame, after the story
  await page.waitForFunction(() => [...document.querySelectorAll("li")].filter((li) => li.querySelector(".who")).every((li) => li.querySelector("em")?.textContent.trim()));
  // what a string says in lang, its {names} filled in
  const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
    const s = (lang !== "en" && I18N[lang]?.[key]) || key;
    return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
  }, [lang, key, vars]);
  const row = (who) => page.locator("li", { has: page.locator(".who", { hasText: who }) }).first();
  return { page, errors, say, row, why: () => page.locator(".rt-steps li.why").first().textContent() };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: a key that reads its windows goes first in a group by them`, async (t) => {
      const { page, errors, say, row, why } = await open(t, engine, lang, "group");
      const head = (await say("{who} goes first: of those with quota to spare, its allowance renews soonest — in {d} — and what it has left then is lost. {other} renews later and keeps its own.", { who: "Sub2API" })).split("{d}")[0];
      assert.ok((await why()).startsWith(head), `${await why()}\nwant ${head}…`);
      // its row has a bar, as an account's; what it did says its window
      const key = row("Sub2API");
      assert.equal(await key.locator(".bar i").evaluate((i) => i.style.width), "10%");
      assert.equal(await key.evaluate((li) => li.classList.contains("nobar")), false);
      // a provider's own row is under its name, named by its model
      const act = page.locator(".rt-prov", { hasText: "Sub2API" }).locator("xpath=following-sibling::div[contains(@class, 'rt-act')][1]");
      await act.locator(".st", { hasText: "80 / 800" }).waitFor();
      const st = (await act.locator(".st").textContent()).trim();
      assert.ok(st.includes("10%") && st.includes("80 / 800"), st);
      assert.equal(await act.locator(".bar i").evaluate((i) => i.style.width), "10%");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: keys all at 90% or more say so, keys and all`, async (t) => {
      const { page, errors, say, row, why } = await open(t, engine, lang, "low");
      const want = await say("Every key and account here is at 90% or more of its allowance, so the one with the most left goes first: {who}, at {n}.", { who: "relay-a", n: "92%" });
      assert.equal((await why()).trim(), want);
      // the other key's place is told as an account's would be
      const aside = await say("{who} is at {n} — kept for when the others can't.", { who: "relay-b", n: "95%" });
      const steps = await page.locator(".rt-steps li").allTextContents();
      assert.ok(steps.some((s) => s.includes(aside)), steps.join("\n"));
      assert.match(await row("relay-b").locator("em").textContent(), /95%/);
      // the header tells Smart as it weighs them, not as keys in their order
      assert.equal((await page.locator(".rt-mode").textContent()).trim(),
        await say("Smart: of the accounts with quota to spare, the one whose allowance renews soonest goes first — what it has left would be lost at the reset. The week decides; an account with five hours and no week goes by its five hours. One at 90% or more waits until the others can't answer; one resting after a failure goes last."));
      assert.deepEqual(errors, []);
    });
    for (const width of [1100, 440]) {
      test(`${engine} ${lang} ${width}px: one key on still says what it has left`, async (t) => {
        const { page, errors, say, row, why } = await open(t, engine, lang, "lone", width);
        assert.equal((await why()).trim(), await say("{name} has one key on — nothing to choose between.", { name: "Kimi Code (China)" }));
        const used = (await say("{n} used · renews in {d}", { n: "49%" })).split("{d}")[0];
        // its row in the story has a bar; it answered, so it says that
        const key = row("Kimi Code (China)");
        assert.equal(await key.locator(".bar i").evaluate((i) => i.style.width), "49%");
        assert.equal(await key.evaluate((li) => li.classList.contains("nobar")), false);
        // its own row under its name says what is used and when it renews
        const act = page.locator(".rt-prov", { hasText: "Kimi Code (China)" }).locator("xpath=following-sibling::div[contains(@class, 'rt-act')][1]");
        await act.locator(".st", { hasText: "49%" }).waitFor();
        const st = (await act.locator(".st").textContent()).trim();
        assert.ok(st.startsWith(used), `${st}\nwant ${used}…`);
        assert.equal(await act.locator(".bar i").evaluate((i) => i.style.width), "49%");
        const fits = await act.locator(".st").evaluate((e) => e.scrollWidth <= e.clientWidth + 1);
        assert.ok(fits, `${st} is cut at this width`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a key not read yet beside one read is told as weighed with it`, async (t) => {
      const { page, errors, say, why } = await open(t, engine, lang, "mixed");
      assert.equal((await why()).trim(), await say("{who} goes first.", { who: "relay-b" }));
      const aside = await say("{who} is at {n} — kept for when the others can't.", { who: "relay-a", n: "95%" });
      const steps = await page.locator(".rt-steps li").allTextContents();
      assert.ok(steps.some((s) => s.includes(aside)), steps.join("\n"));
      assert.equal((await page.locator(".rt-mode").textContent()).trim(),
        await say("Smart: of the accounts with quota to spare, the one whose allowance renews soonest goes first — what it has left would be lost at the reset. The week decides; an account with five hours and no week goes by its five hours. One at 90% or more waits until the others can't answer; one resting after a failure goes last."));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: least used, a key not read yet beside one read isn't told by its tokens`, async (t) => {
      const { errors, say, why } = await open(t, engine, lang, "mixedUsage");
      assert.equal((await why()).trim(), await say("Least used first: {who} goes first.", { who: "relay-b" }));
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: keys whose windows aren't read are told as before`, async (t) => {
      const { errors, say, row, why } = await open(t, engine, lang, "plain");
      assert.equal((await why()).trim(), await say("{who} goes first: keys go in their order, those that suit the request first.", { who: "relay-a" }));
      assert.equal((await row("relay-b").locator("em").textContent()).trim(), await say("API key"));
      assert.equal(await row("relay-b").evaluate((li) => li.classList.contains("nobar")), true);
      assert.deepEqual(errors, []);
    });
  }
}
