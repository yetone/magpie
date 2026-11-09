// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Requests show the gateway's bandwidth (songlairui on X:
// many calls with base64 images weigh on the network). The chart's metrics
// take Traffic: columns of each hour's bytes up and down, stacked by model,
// an axis in bytes, a tooltip that splits a column into "↑ up · ↓ down",
// and a ranking whose values are bytes with the same split under each; a
// period with no bytes says so. Under the Requests card, while requests are
// in flight, "↑ 1.0 MB/s · ↓ 40 KB/s" says how fast traffic flows now, read
// from /api/usage/traffic a second apart; it goes when the gateway is idle,
// says nothing when another magpie serves the gateway ({}), and the page
// stops asking once the Requests aren't looked at. At 1180 and 420 wide the
// page never scrolls sideways. Chromium and WebKit, every language; no
// backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);

// a day by the hour, from 02:00: glm-5 sends 2 MB up and reads 400 KB back
// an hour, kimi half that
const GLM = { up_bytes: 2_000_000, down_bytes: 400_000 }, KIMI = { up_bytes: 1_000_000, down_bytes: 200_000 };
function page(bytes) {
  const series = Array.from({ length: 24 }, (_, h) => {
    const on = h >= 2 ? 1 : 0;
    const part = (b) => ({ calls: 10 * on, tokens: 5000 * on, cost: 0, ...(bytes ? { up_bytes: b.up_bytes * on, down_bytes: b.down_bytes * on } : {}) });
    const glm = part(GLM), kimi = part(KIMI);
    return {
      label: String(h), time: new Date(midnight.getTime() + h * 3600e3).toISOString(), calls: 20 * on, errors: 0, input: 10000 * on, output: 0, cache_read: 0, cache_write: 0, cost: 0,
      ...(bytes ? { up_bytes: 3_000_000 * on, down_bytes: 600_000 * on } : {}),
      by: { model: { "glm-5": glm, kimi }, provider: { zcode: glm }, agent: { claude: glm } },
    };
  });
  const share = (id, b) => ({ id, name: id, calls: 220, errors: 0, input: 110000, output: 0, cache_read: 0, cache_write: 0, cost: 0, ...(bytes ? { up_bytes: b.up_bytes * 22, down_bytes: b.down_bytes * 22 } : {}) });
  return {
    period: "today", rows: [], offset: 0, total: 440, calls: 440, errors: 0, input: 220000, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0, bucket: "hour", series,
    ...(bytes ? { up_bytes: 66_000_000, down_bytes: 13_200_000 } : {}),
    by: { model: [share("glm-5", GLM), share("kimi", KIMI)], provider: [share("zcode", GLM)], agent: [share("claude", GLM)] },
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }], providers: [{ id: "zcode", name: "ZCode", icon: "generic" }],
  };
}

// the gateway's counters: flowing (1 MB/s up, 40 KB/s down, two requests
// in flight), idle (still, none in flight) or served elsewhere ({})
function gatewayAt(state) {
  const t0 = Date.now();
  let frozen = null;
  return () => {
    if (state.mode === "elsewhere") return {};
    if (state.mode === "idle") return { ...(frozen ||= { up: 0, down: 0 }), requests: 0 };
    const ms = Date.now() - t0;
    frozen = { up: ms * 1000, down: ms * 40 };
    return { ...frozen, requests: 2 };
  };
}

function server(lang, bytes, state) {
  const traffic = gatewayAt(state);
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/traffic") { state.asked++; return json(traffic()); }
    if (url.pathname === "/api/usage/requests") return json(page(bytes));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 1, errors: 0, input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [] });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: { traffic: "Traffic", none: "No traffic counted in this period" },
  zh: { traffic: "流量", none: "这段时间没有计入流量" },
  "zh-TW": { traffic: "流量", none: "這段時間沒有計入流量" },
  ja: { traffic: "通信量", none: "この期間に数えた通信量はありません" },
  de: { traffic: "Datenverkehr", none: "In diesem Zeitraum wurde kein Datenverkehr gezählt" },
};

async function open(browser, lang, width, bytes, state) {
  const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
  const p = await ctx.newPage();
  const errors = [];
  p.setDefaultTimeout(5000);
  p.setDefaultNavigationTimeout(60000);
  p.on("pageerror", (e) => errors.push(e.message));
  await p.route("**/*", server(lang, bytes, state));
  await p.goto("http://magpie.test/");
  await p.locator('[data-view="usage"]').first().click();
  await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
  await p.locator("#ledChart svg").waitFor();
  return { ctx, p, errors };
}

// "↑ 1.0 MB/s · ↓ 40 KB/s" as bytes a second, up and down
function rates(s) {
  const m = /^↑ ([\d.]+) (B|KB|MB)\/s · ↓ ([\d.]+) (B|KB|MB)\/s$/.exec(s);
  assert(m, "the rate line: " + s);
  const k = { B: 1, KB: 1e3, MB: 1e6 };
  return [+m[1] * k[m[2]], +m[3] * k[m[4]]];
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the gateway's traffic in Requests", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const shots = process.env.ARTIFACT_DIR;
    if (shots) await fs.mkdir(shots, { recursive: true });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const w = L[lang];
      for (const width of [1180, 420]) {
        await t.test(lang + " " + width, async () => {
          const state = { mode: "flowing", asked: 0 };
          const { ctx, p, errors } = await open(browser, lang, width, true, state);
          try {
            // how fast it flows now, under the Requests card
            const rate = p.locator("#ledKpi .led-rate:not([hidden])");
            await rate.waitFor();
            const [up, down] = rates((await rate.textContent()).trim());
            assert(up > 0.7e6 && up < 1.3e6, `up ${up} B/s, the gateway sends 1 MB/s`);
            assert(down > 28e3 && down < 52e3, `down ${down} B/s, the gateway reads 40 KB/s`);

            // the chart's Traffic
            const pill = p.locator("#ledMetric .opt").nth(4);
            assert.equal((await pill.textContent()).trim(), w.traffic);
            await reader.click(p, pill);
            await p.locator("#ledChart svg rect.col").first().waitFor();
            const axis = await p.locator("#ledChart svg text.axis[text-anchor=end]").allTextContents();
            assert.equal(axis.length, 5, "five side labels: " + axis);
            for (const a of axis) assert.match(a, /^\d+(\.\d)? (B|KB|MB)$/, "an axis label in bytes");
            assert(axis.includes("4 MB"), "the top of 3.6 MB an hour: " + axis);

            // a column's tooltip: its total, and of it what went up and came back
            await reader.inView(p, p.locator("#ledChart svg"));
            const box = await p.locator("#ledChart svg").boundingBox();
            await p.mouse.move(box.x + box.width * 0.62, box.y + box.height / 2);
            await p.locator("#ledChart .tip").waitFor();
            assert.equal((await p.locator("#ledChart .tip .io em").textContent()).trim(), "↑ 3.0 MB · ↓ 600 KB");
            assert((await p.locator("#ledChart .tip").textContent()).includes("3.6 MB"), "the column's total");
            await p.mouse.move(1, 1);

            // the ranking: bytes, the most first, split up and down
            const rk = p.locator("#ledRank .rk");
            assert.deepEqual((await rk.locator(".rk-val").allTextContents()).map((s) => s.trim()), ["53 MB", "26 MB"]);
            const sub = (await rk.locator(".rk-b").allTextContents()).map((s) => s.split(" · ").slice(0, 2).join(" · "));
            assert.deepEqual(sub, ["↑ 44 MB · ↓ 8.8 MB", "↑ 22 MB · ↓ 4.4 MB"]);

            const sideways = await p.evaluate(() => [document.documentElement.scrollWidth - innerWidth, ...[...document.querySelectorAll("#ledKpi .blk, #ledMetric")].map((b) => b.scrollWidth - b.clientWidth)]);
            assert(sideways.every((n) => n <= 1), `scrolls sideways: ${sideways}`);
            if (shots) await p.locator(".led-trend").screenshot({ path: path.join(shots, `traffic-${engine}-${lang}-${width}.png`) });
            assert.deepEqual(errors, []);
          } finally {
            await ctx.close();
          }
        });
      }
      // a period with no bytes counted says so
      await t.test(lang + " none", async () => {
        const { ctx, p, errors } = await open(browser, lang, 1180, false, { mode: "flowing", asked: 0 });
        try {
          await reader.click(p, p.locator("#ledMetric .opt").nth(4));
          assert.equal((await p.locator("#ledChart .none").textContent()).trim(), w.none);
          assert.deepEqual(errors, []);
        } finally {
          await ctx.close();
        }
      });
    }

    // the gateway goes idle: once the last flow has left the few seconds
    // the rate is read over, the line goes
    await t.test("idle", async () => {
      const state = { mode: "flowing", asked: 0 };
      const { ctx, p, errors } = await open(browser, "en", 1180, true, state);
      try {
        await p.locator("#ledKpi .led-rate:not([hidden])").waitFor();
        state.mode = "idle";
        await p.locator("#ledKpi .led-rate[hidden]").waitFor({ state: "attached", timeout: 10000 });
        assert.equal((await p.locator("#ledKpi .led-rate").textContent()), "");
        // and it comes back when requests flow again
        state.mode = "flowing";
        await p.locator("#ledKpi .led-rate:not([hidden])").waitFor({ timeout: 10000 });

        // leaving the Requests stops the asking
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(0).click();
        await p.waitForFunction(() => !document.querySelector("#ledKpi .led-rate:not([hidden])") || document.querySelector("#ledKpi").offsetParent === null);
        const asked = state.asked;
        await p.waitForTimeout(2500);
        assert(state.asked - asked <= 1, `asked ${state.asked - asked} times after leaving the Requests`);
        assert.deepEqual(errors, []);
      } finally {
        await ctx.close();
      }
    });

    // another magpie serves the gateway: no rate, nothing made up
    await t.test("served elsewhere", async () => {
      const state = { mode: "elsewhere", asked: 0 };
      const { ctx, p, errors } = await open(browser, "en", 1180, true, state);
      try {
        // three answers read, each of them {}
        for (const end = Date.now() + 10000; state.asked < 3; await p.waitForTimeout(100)) assert(Date.now() < end, `asked ${state.asked} times`);
        assert.equal(await p.locator("#ledKpi .led-rate:not([hidden])").count(), 0);
        assert.deepEqual(errors, []);
      } finally {
        await ctx.close();
      }
    });
  });
}
