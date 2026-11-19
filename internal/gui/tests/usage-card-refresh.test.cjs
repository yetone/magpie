// Run with Node's test runner and Playwright on the module path; see README.md.
// A card's own refresh (Hu9956, #840: 在更新时间左侧加一个刷新按钮，鼠标悬停
// 在套餐卡片上时显示，可以单独刷新某个套餐的用量): after "Updated …", or
// after why a reading failed, or under a balance, opening as the card is
// hovered; at rest the line starts where the card's others do, and the text
// doesn't move when it opens (opening left of it pushed it right on every
// hover). A click reads that card alone
// (POST /api/usage/quotas/refresh?provider=&user=), turns while it does, and
// draws what came back; no other card is asked, and the page doesn't scroll.
// Button controls in English and Chinese; refresh ordering, deferred
// readings and icon alignment in all five languages, wide and narrow.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { inView } = require("./reader.cjs");

const assets = process.env.MAGPIE_QUOTA_ASSETS || path.resolve(__dirname, "../assets");
const H = 3600e3, M = 60e3;
const iso = (ms) => new Date(ms).toISOString();

function fixtures(now) {
  const r5 = now + 2 * H;
  return [
    { provider: "claude", name: "Claude Code", icon: "claude-color", plan: "Max", user: "a@x.com", readAt: iso(now - 40 * M),
      windows: [{ name: "5 hours", used: 28, resetsAt: iso(r5) }] },
    { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: "b@x.com", readAt: iso(now - 40 * M),
      windows: [{ name: "5 hours", used: 50, resetsAt: iso(r5) }] },
    { provider: "kimi", name: "Kimi", icon: "kimi-color", error: "HTTP 503", windows: [] },
    { provider: "relay", name: "Relay", icon: "openai", balance: "$2.00", readAt: iso(now - 40 * M), windows: [] },
  ];
}

function serve(lang, state) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: state.providers || [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") {
      state.gets++;
      const snapshot = JSON.parse(JSON.stringify(state.quotas));
      if (state.readGate) { state.readStarted(); await state.readGate; }
      return json(snapshot);
    }
    if (url.pathname === "/api/usage/quotas/refresh") {
      assert.equal(route.request().method(), "POST");
      state.refreshes.push(url.search);
      await state.gate;
      const q = state.quotas.find((x) => x.provider === url.searchParams.get("provider") && (x.user || "") === (url.searchParams.get("user") || ""));
      if (q?.windows?.length) { q.windows[0].used = 61; q.readAt = iso(Date.now()); }
      if (q?.error) { delete q.error; q.windows = [{ name: "Week", used: 10 }]; q.readAt = iso(Date.now()); }
      if (q) { delete q.asOf; delete q.readDeferred; }
      if (q && state.nameOnRefresh && !q.user) q.user = "a@x.com";
      return json(state.quotas);
    }
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, cost: 0, days: [], agents: [], models: [], keys: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const deferredWords = {
  en: "not refreshed this time",
  zh: "本次未主动刷新",
  "zh-TW": "本次未主動重新整理",
  ja: "今回は更新を要求していません",
  de: "diesmal nicht neu abgefragt",
};
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(deferredWords)) {
    for (const named of [true, false]) test(`${engine} ${lang}: a deferred ${named ? "named" : "unnamed"} card is not a failure, and a late background reply cannot undo its refresh`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", e => errors.push(e.message));
      const state = { quotas: fixtures(Date.now()), gets: 0, refreshes: [], nameOnRefresh: !named };
      if (!named) state.quotas[0].user = "";
      state.quotas[0].asOf = state.quotas[0].readAt;
      state.quotas[0].readDeferred = true;
      await page.route("**/*", serve(lang, state));
      await page.goto("http://magpie.test/?view=usage");
      const claude = page.locator(".subscription-card", { hasText: "Claude Code" });
      const read = claude.locator(".quota-read");
      await read.waitFor();
      assert.ok((await read.textContent()).includes(deferredWords[lang]), "a request not made was described as failed");
      // a failed read's stand-in says why it failed (tmr11235 on X:
      // agy-cli's quota stood days old with no reason); a deferred one
      // failed at nothing
      const why = "agy usage query timed out.";
      const asOf = state.quotas[0].readAt;
      assert.ok((await page.evaluate(q => asOfText(q), { asOf, readError: why })).endsWith(" · " + why), "a failed read hid why");
      assert.ok(!(await page.evaluate(q => asOfText(q), { asOf, readError: why, readDeferred: true })).includes(why));
      assert.ok(!(await page.evaluate(q => asOfText(q, true), { asOf, readError: why })).includes(why), "the short line stays short");

      let release, readStarted;
      const started = new Promise(r => { readStarted = r; });
      state.readStarted = readStarted;
      state.readGate = new Promise(r => { release = r; });
      t.after(() => release());
      // Hold an old whole-page response, then click the real card button.
      await page.evaluate(() => { void loadQuotas(); });
      await started;
      await claude.hover();
      await claude.locator(".quota-refresh").click();
      await claude.locator(".quota-n", { hasText: "61%" }).waitFor();
      release();
      await page.evaluate(() => quotasLoading);
      assert.match(await claude.locator(".quota-n").textContent(), /61%/, "an older reply undid the click");
      assert.equal(await read.evaluate(e => e.classList.contains("stale")), false);
      assert.deepEqual(state.refreshes, [named ? "?provider=claude&user=a%40x.com" : "?provider=claude"]);
      assert.deepEqual(errors, []);
    });
  }
}

const words = {
  en: { updated: "Updated 40 minutes ago", label: "Refresh this card", now: /^Updated (just now|\d+ seconds? ago)$/ },
  zh: { updated: "40分钟前更新", label: "刷新这张卡片", now: /^(刚刚|\d+秒前)更新$/ },
};

function layoutFixtures(now) {
  const quotas = fixtures(now);
  quotas[0].asOf = quotas[0].readAt;
  quotas[0].windows[0].resetsAt = iso(now - H);
  quotas[1].readAt = iso(now);
  // The provider's real missing-access-key reply, including its settings action.
  quotas[2] = { provider: "volcengine", name: "Volcengine Ark", icon: "volcengine", plan: "Coding Plan", windows: [], needsAccessKey: true,
    error: "this plan's usage is read with the account's access key: give its AccessKey ID and Secret in Volcengine Ark's settings" };
  return { quotas, gets: 0, refreshes: [], providers: [
    { id: "volcengine", name: "Volcengine Ark", icon: "volcengine", preset: "volcengine", chat: "https://ark.cn-beijing.volces.com/api/coding/v3",
      responses: "", anthropic: "", catalog: "", models: [], agents: [], fallback: [], headers: {}, key: { set: true, masked: "test-key" }, keyList: [],
      accessKey: { vendor: "volcengine", id: "", secretSet: false } },
  ] };
}

// where the line's text starts, line by line
const textAt = (row) => row.evaluate((e) => {
  const range = document.createRange();
  range.selectNodeContents(e);
  return [...range.getClientRects()].filter((r) => r.width).map((r) => Math.round(r.x * 4) / 4)[0];
});

async function showRefresh(row) {
  const was = await textAt(row);
  await row.hover();
  await row.locator(".quota-refresh").evaluate(e => new Promise(done => {
    const tick = () => getComputedStyle(e).opacity === "1" && e.getBoundingClientRect().width >= 14 ? done() : requestAnimationFrame(tick);
    tick();
  }));
  assert.equal(await textAt(row), was, "the text stays where it was as the refresh opens");
}

async function assertRefreshAligned(row) {
  const boxes = await row.evaluate((e) => {
    const button = e.querySelector(".quota-refresh");
    const text = e.querySelector(".quota-ago")?.firstChild || [...e.childNodes].find(n => n.nodeType === Node.TEXT_NODE && n.textContent.trim());
    const range = document.createRange();
    range.selectNodeContents(text);
    const rect = (r) => ({ x: r.x, right: r.right, cy: r.y + r.height / 2 });
    return { icon: rect(button.querySelector("svg").getBoundingClientRect()), text: rect([...range.getClientRects()].at(-1)) };
  });
  // after the text: on its last line, or on a line of its own below it
  const below = boxes.icon.cy > boxes.text.cy + 4;
  assert.ok(below || boxes.icon.x >= boxes.text.right, "the refresh icon comes after the text: " + JSON.stringify(boxes));
  if (!below) assert.ok(Math.abs(boxes.icon.cy - boxes.text.cy) <= 0.75,
    "the refresh icon and the text's line must share a vertical center: " + JSON.stringify(boxes));
}

async function assertInlineSettings(row, multiline) {
  const layout = await row.evaluate(e => {
    const range = document.createRange();
    range.selectNodeContents([...e.childNodes].find(n => n.nodeType === Node.TEXT_NODE && n.textContent.trim()));
    const rect = r => ({ x: r.x, y: r.y, right: r.right, bottom: r.bottom, width: r.width, height: r.height });
    return { row: rect(e.getBoundingClientRect()), text: [...range.getClientRects()].map(rect), settings: rect(e.querySelector(".quota-fix").getBoundingClientRect()) };
  });
  if (multiline) assert.ok(layout.text.length > 1, "the narrow-window fixture really wraps");
  for (const line of [...layout.text, layout.settings]) {
    assert.ok(line.x >= layout.row.x - 0.75 && line.right <= layout.row.right + 0.75, "the error and settings fit the card: " + JSON.stringify(layout));
  }
  for (const line of layout.text.slice(1)) assert.ok(Math.abs(line.x - layout.row.x) < 0.75, "wrapped text uses the full row, not an indented flex item");
  const last = layout.text.at(-1), go = layout.settings;
  if (last.right + go.width + 5 < layout.row.right) {
    assert.ok(go.y < last.bottom && go.bottom > last.y && go.x >= last.right,
      "settings follows the last text line when it fits: " + JSON.stringify(layout));
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a refresh keeps its turn and reading position through redraws, then finishes a whole turn`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 1250, height: 900 }, reducedMotion: "no-preference" });
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", e => errors.push(e.message));
    let release, releaseRead, readStarted;
    const state = { quotas: fixtures(Date.now()), gets: 0, refreshes: [], gate: new Promise(r => { release = r; }) };
    t.after(() => { release(); releaseRead?.(); });
    await page.route("**/*", serve("zh", state));
    await page.goto("http://magpie.test/?view=usage");
    const row = page.locator('.subscription-card[data-provider="codex"] .quota-read');
    await showRefresh(row);
    const started = new Promise(r => { readStarted = r; });
    state.readStarted = readStarted;
    state.readGate = new Promise(r => { releaseRead = r; });
    await page.evaluate(() => { void loadQuotas(); });
    await started;
    await row.locator(".quota-refresh").click();
    await row.evaluate(e => {
      const button = e.querySelector(".quota-refresh"), animation = button.querySelector("path").getAnimations()[0];
      animation.pause();
      animation.currentTime = 1200; // Mid-turn, independent of the runner's speed.
      window.refreshMotion = { button, animation, x: e.querySelector(".quota-ago").getBoundingClientRect().x };
    });
    releaseRead();
    await page.evaluate(() => quotasLoading);
    assert.equal(await row.evaluate(e => e.querySelector(".quota-refresh") === window.refreshMotion.button), true, "a background reading replaced the spinning control");
    assert.equal(await row.evaluate(e => e.querySelector(".quota-refresh path").getAnimations()[0] === window.refreshMotion.animation), true, "a background reading restarted the turn");
    release();
    await page.locator('.subscription-card[data-provider="codex"] .quota-n', { hasText: "61%" }).waitFor();
    const end = await row.evaluate(e => {
      const { button, animation, x } = window.refreshMotion;
      return { same: e.querySelector(".quota-refresh") === button, busy: button.classList.contains("busy"), time: animation.currentTime,
        turns: animation.effect.getTiming().iterations, duration: animation.effect.getTiming().duration, shift: e.querySelector(".quota-ago").getBoundingClientRect().x - x };
    });
    assert.deepEqual(end, { same: true, busy: true, time: 1200, turns: 2, duration: 800, shift: 0 }, "the reply interrupted the turn or shifted the reading");
    await page.evaluate(() => window.refreshMotion.animation.finish());
    await row.locator(".quota-refresh:not(.busy)").waitFor();
    assert.equal(await row.evaluate(e => e.querySelector(".quota-refresh") === window.refreshMotion.button && getComputedStyle(window.refreshMotion.button).opacity === "1"), true, "finishing the turn hid or replaced the button");
    // A response already available still gets one complete turn.
    state.gate = undefined;
    await row.locator(".quota-refresh").click();
    await page.waitForFunction(() => document.querySelector('.subscription-card[data-provider="codex"] .quota-refresh path').getAnimations()[0]?.effect.getTiming().iterations === 1);
    await row.evaluate(e => e.querySelector(".quota-refresh path").getAnimations()[0].finish());
    await row.locator(".quota-refresh:not(.busy)").waitFor();
    assert.deepEqual(errors, []);
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(deferredWords)) {
    for (const width of [900, 420]) test(`${engine} ${lang} ${width}px: refresh icons align with their reading or error`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", e => errors.push(e.message));
      const now = Date.now();
      const state = layoutFixtures(now);
      await page.route("**/*", serve(lang, state));
      await page.goto("http://magpie.test/?view=usage");

      // Fresh, stale and balance readings, plus a failed reading's first line.
      for (const name of ["Codex", "Claude Code", "Relay", "Volcengine Ark"]) {
        const card = page.locator(".subscription-card", { hasText: name });
        const row = card.locator(".quota-read, .subscription-error");
        await inView(page, row);
        await showRefresh(row);
        await assertRefreshAligned(row);
      }
      const error = page.locator(".subscription-error");
      await assertInlineSettings(error, width === 420);
      await inView(page, error);
      await error.locator(".quota-fix").click();
      await page.waitForURL(/view=providers/);
      assert.equal(new URL(page.url()).searchParams.get("edit"), "volcengine");
      await page.locator(".editor .volc-ak").waitFor();
      assert.deepEqual(state.refreshes, [], "showing a card's refresh doesn't read it");
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a quota card is refreshed on its own`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      // short, so the page scrolls and a click that moved it would show
      const page = await (await browser.newContext({ viewport: { width: 900, height: 420 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      let open;
      const state = { quotas: fixtures(Date.now()), gets: 0, refreshes: [], gate: new Promise((r) => { open = r; }) };
      await page.route("**/*", serve(lang, state));
      await page.goto("http://magpie.test/?view=usage");

      const claude = page.locator(".subscription-card", { hasText: "Claude Code" });
      const read = claude.locator(".quota-read");
      await read.waitFor();
      assert.equal(await read.textContent(), w.updated, "the line reads as it did");
      const btn = read.locator("button.quota-refresh");
      assert.equal(await btn.getAttribute("aria-label"), w.label);
      const ago = read.locator(".quota-ago");
      const opacity = () => btn.evaluate((e) => getComputedStyle(e).opacity);

      // at rest: not there, the line starting where the account's does
      await page.mouse.move(890, 5);
      await btn.evaluate((e) => new Promise((done) => { const tick = () => getComputedStyle(e).opacity === "0" ? done() : requestAnimationFrame(tick); tick(); }));
      const user = await claude.locator(".subscription-account .user").boundingBox();
      assert.ok(Math.abs((await ago.boundingBox()).x - user.x) < 1, "the time lines up with the card at rest");

      // hovered: after the time, on its line
      await claude.hover();
      await btn.evaluate((e) => new Promise((done) => { const tick = () => getComputedStyle(e).opacity === "1" && e.getBoundingClientRect().width >= 14 ? done() : requestAnimationFrame(tick); tick(); }));
      assert.equal(await opacity(), "1");
      const [b, when] = [await btn.boundingBox(), await ago.boundingBox()];
      assert.ok(b.width >= 14 && b.x >= when.x + when.width - 0.5 && Math.abs(b.y + b.height / 2 - (when.y + when.height / 2)) < 4, JSON.stringify({ b, when }));
      await assertRefreshAligned(read);

      // a failed reading and a balance have one too
      const kimi = page.locator(".subscription-card", { hasText: "Kimi" });
      assert.equal(await kimi.locator(".subscription-error button.quota-refresh").count(), 1);
      assert.equal(await page.locator(".subscription-card", { hasText: "Relay" }).locator(".quota-read button.quota-refresh").count(), 1);

      // the click: that card alone, turning meanwhile, the page where it was
      await claude.scrollIntoViewIfNeeded();
      await page.evaluate(() => window.scrollBy(0, 40));
      const y = await page.evaluate(() => [scrollY, document.querySelector("#view-usage").scrollTop, document.scrollingElement.scrollTop]);
      const gets = state.gets;
      await claude.hover();
      await btn.click();
      await page.waitForFunction(() => document.querySelector(".quota-refresh.busy"));
      assert.equal(await claude.locator(".quota-refresh.busy").count(), 1, "turns while it reads");
      await assertRefreshAligned(read);
      open();
      await claude.locator(".quota-n", { hasText: "61%" }).waitFor();
      assert.deepEqual(state.refreshes, ["?provider=claude&user=a%40x.com"]);
      assert.equal(state.gets, gets, "no other card asked for");
      assert.match(await claude.locator(".quota-read").textContent(), w.now);
      assert.equal(await claude.locator(".quota-refresh.busy").count(), 0);
      assert.match(await page.locator(".subscription-card", { hasText: "Codex" }).locator(".quota-n").textContent(), /50%/, "the others as they were");
      assert.deepEqual(await page.evaluate(() => [scrollY, document.querySelector("#view-usage").scrollTop, document.scrollingElement.scrollTop]), y, "the click didn't scroll");

      // a failed one read again from its own
      await kimi.hover();
      await kimi.locator(".quota-refresh").click();
      await kimi.locator(".quota-n").waitFor();
      assert.deepEqual(state.refreshes.slice(1), ["?provider=kimi"]);
      assert.deepEqual(errors, []);
    });
  }
}
