// Run with Node's test runner and Playwright on the module path; see README.md.
// Phone navigation and content stay usable in both languages (#391). The
// desktop screenshots match BASE_REF within a small rendering tolerance
// (origin/main by default), but for the Agents page while BASE_REF hasn't its
// 「接入」 rows yet; only API boundaries are faked, never the page's layout or
// scrolling helpers.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { PNG } = require("playwright-core/lib/utilsBundle");

const assets = path.resolve(__dirname, "../assets");
const base = process.env.BASE_REF || "origin/main";
const baseline = new Map();
const at = "2026-09-30T12:00:00.000Z";
const groups = [{ name: "Claude Code", icon: "claudecode-color", calls: 123, errors: 2,
  input: 123456, output: 67890, cache_read: 543210, cache_write: 1234, reasoning: 12345, cost: 12.34, unpriced: 0 }];
const usage = { ...groups[0], path: "/test/usage.jsonl", bucket: "day", agents: groups,
  models: [{ ...groups[0], name: "a-model-with-a-long-name" }],
  series: Array.from({ length: 30 }, (_, i) => ({ ...groups[0], label: "09-" + String(i + 1).padStart(2, "0") })) };
// Populated rows are essential: an empty Agents list cannot expose clipped
// model/effort controls or make the desktop comparison cover those rows.
// Connected, so each opens on its pickers.
const modelOptions = [{ value: "claude-sonnet-4.5", label: "Claude Sonnet 4.5", icon: "claude-color", ref: "claude/claude-sonnet-4.5" },
  { value: "gpt-6.1-sol", label: "GPT-6.1-Sol", icon: "openai", ref: "openai/gpt-6.1-sol" }];
const modelField = (key, label, value = "") => ({ key, label, value, options: modelOptions });
const agentRows = [
  { id: "claude", name: "Claude Code", path: "/test/claude.json", icon: "claudecode-color", wired: true, fields: [
    modelField("model", "model", "claude-sonnet-4.5"),
    { key: "effort", label: "thinking", value: "high", options: [{ value: "high", label: "High" }] },
  ] },
  { id: "codex", name: "Codex", path: "/test/codex.toml", icon: "codex-color", wired: true, fields: [
    modelField("model", "model", "gpt-6.1-sol"),
    { key: "effort", label: "effort", value: "high", options: [{ value: "high", label: "High" }] },
    modelField("subagent", "subagents"),
    { key: "signin", label: "sign-in", value: "magpie", options: [{ value: "magpie", label: "magpie" }, { value: "chatgpt", label: "ChatGPT" }] },
  ] },
  { id: "omp", name: "omp", path: "/test/omp.json", icon: "omp", wired: true, fields: [
    modelField("model", "model", "gpt-6.1-sol"), modelField("subagent", "subagents"),
    modelField("small", "smol"), modelField("slow", "slow"),
  ] },
];
// the labels app.js groups into omp's roles square (OMP_ROLES)
const OMP_ROLES = ["subagents", "smol", "slow", "plan", "vision", "advisor"];
const views = ["agents", "providers", "gateway", "routing", "usage", "library", "plugins", "settings"];

// BASE_REF's assets, all read in one git call the first time one is asked
// for. A git show per file blocked the fake server for each of the ~100
// the original page loads: under load its goto passed the 5s timeout, or
// its icons came after the screenshot (WebKit, the two OpenAI marks on
// Providers, 338 pixels).
function baselineFile(file) {
  if (!baseline.size) {
    const root = path.resolve(assets, "../../.."), dir = "internal/gui/assets/";
    const names = execFileSync("git", ["ls-tree", "-r", "--name-only", base, dir], { cwd: root }).toString().split("\n").filter(Boolean);
    const out = execFileSync("git", ["cat-file", "--batch"], { cwd: root, input: names.map((n) => `${base}:${n}\n`).join(""), maxBuffer: 256 * 1024 * 1024 });
    let at = 0;
    for (const name of names) {
      const nl = out.indexOf(10, at);
      const size = Number(out.subarray(at, nl).toString().split(" ")[2]);
      baseline.set(name.slice(dir.length), out.subarray(nl + 1, nl + 1 + size));
      at = nl + 1 + size + 1;
    }
  }
  if (!baseline.has(file)) throw new Error(`${file} isn't in ${base}`);
  return baseline.get(file);
}

// the files being served now: a picture still on its way (an icon's mask is
// fetched by the page itself, apart from any preload) isn't drawn yet
let serving = 0;

function server(lang, original = false, opened = []) {
  return async route => {
    const url = new URL(route.request().url());
    const json = data => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: agentRows, profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, calls: [], groups: [] } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/usage/quotas") {
      // main's usage render can reveal the empty quota heading again if it
      // finishes last. Deliver the empty allowances after the totals paint,
      // so both screenshot pages compare the same completed API state.
      await route.request().frame().page().locator("#usageAgents .row.stat").first().waitFor({ state: "attached", timeout: 30000 });
      return json([]);
    }
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: at, seq: 0, routes: [], totals: { requests: 0, rerouted: 0, errors: 0 } });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/library") return json({ dir: "/test/library", home: "/test", agents: [], instructions: { agents: [], sets: [] }, servers: [], skills: [], projects: [], foundServers: [], foundSkills: [] });
    if (url.pathname === "/api/plugins") return json({ bun: true, plugins: [], movable: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/npm") return json({ npm: {} });
    if (url.pathname === "/api/plugins/market") return json({ listings: [], state: { bun: true, plugins: [] } });
    if (url.pathname === "/api/library/reveal") { opened.push(route.request().postDataJSON()); return json({}); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = url.pathname === "/" ? "index.html" : url.pathname.slice(1);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    serving++;
    try {
      let body;
      if (original) body = baselineFile(file);
      else body = await fs.readFile(path.join(assets, file));
      await route.fulfill({ body, contentType });
    } catch { await route.fulfill({ status: 404 }); } finally { serving--; }
  };
}

async function open(browser, lang, width, { original = false, touch = true, opened = [] } = {}) {
  const page = await browser.newPage({ viewport: { width, height: 900 }, hasTouch: touch, reducedMotion: "reduce", colorScheme: "light" });
  page.setDefaultTimeout(5000);
  await page.addInitScript(at => {
    const RealDate = Date;
    window.Date = class extends RealDate {
      constructor(...args) { super(...(args.length ? args : [at])); }
      static now() { return new RealDate(at).getTime(); }
    };
  }, at);
  await page.route("http://magpie.test/**", server(lang, original, opened));
  await page.goto("http://magpie.test/");
  await page.locator("#nav button").first().waitFor();
  return page;
}

async function go(page, view) {
  // Playwright scrolls each real navigation button into sight before clicking;
  // this exercises the phone's horizontal navigation strip too.
  await page.locator(view === "settings" ? "#prefs" : `#nav [data-view="${view}"]`).click();
  await page.locator(`#view-${view}`).waitFor();
  if (view === "library") await page.locator("#view-library .lib-more:not(:disabled)").waitFor();
  if (view === "plugins") await page.locator("#view-plugins .pm-find").waitFor();
  if (view === "usage") {
    await page.locator("#usageAgents .row.stat").waitFor();
    await page.locator("#quotaHead").waitFor({ state: "hidden" });
  }
  await page.evaluate(() => document.fonts.ready);
  // every icon drawn: a mask's picture paints once it has come, which the
  // 800ms below doesn't promise on a loaded machine (nothing served for a
  // while, then a frame)
  for (let quiet = 0; quiet < 3; quiet = serving ? 0 : quiet + 1) await page.waitForTimeout(50);
  await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  await page.waitForTimeout(800); // let the tab's spring and content entrance settle
}

// a screenshot once two in a row are the same: WebKit fetches a mask's
// picture first when it paints it, which a screenshot can be the first to do
// (an icon still on its way when the page was drawn), so the screenshot
// waits for what it asked for and is taken again
async function still(page) {
  let last;
  for (let i = 0; i < 10; i++) {
    const png = await page.screenshot({ animations: "disabled" });
    if (last && png.equals(last)) break;
    last = png;
    for (let quiet = 0; quiet < 3; quiet = serving ? 0 : quiet + 1) await page.waitForTimeout(50);
  }
  return last;
}

async function save(page, name) {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, name + ".png"), animations: "disabled" });
}

async function fits(page, view) {
  const dimensions = await page.locator(`#view-${view}`).evaluate(v => ({
    view: [v.scrollWidth, v.clientWidth], body: [document.body.scrollWidth, innerWidth],
    document: [document.documentElement.scrollWidth, innerWidth],
  }));
  for (const [name, [actual, available]] of Object.entries(dimensions)) assert(actual <= available + 1, `${view}: ${name} scrolls sideways (${actual} > ${available})`);
}

async function agentsFit(page) {
  for (const agent of agentRows) {
    const row = page.locator(`.row.agent[data-id="${agent.id}"]`);
    await row.waitFor();
    // opened, its pickers in the row
    await row.locator(".ag-link").click();
    await row.locator(".ag-exp").waitFor();
    const bad = await row.evaluate(row => {
      const box = row.getBoundingClientRect(), bad = [];
      for (const field of row.querySelectorAll(".field")) {
        const b = field.getBoundingClientRect();
        if (b.left < box.left - 1 || b.right > box.right + 1 || b.top < box.top - 1 || b.bottom > box.bottom + 1) bad.push("field cropped: " + field.dataset.key);
      }
      const model = row.querySelector('.field[data-key="model"] .v');
      if (!model?.textContent || model.scrollWidth > model.clientWidth + 1) bad.push("model name cropped");
      return bad;
    });
    assert.deepEqual(bad, [], agent.name + ": every control and model name must be visible");
    // omp's subagents, smol and slow roles share one square, as Claude
    // Code's tiers do (#1397, fc5dfa3c): it opens their menu
    const keys = [...new Set(agent.fields.map((f) => agent.id === "omp" && OMP_ROLES.includes(f.label) ? "tiers" : f.key))];
    for (const key of keys) {
      await row.locator(`.field[data-key="${key}"]`).click();
      await page.locator("#pop").waitFor();
      assert.equal(await page.locator("#pop").isVisible(), true, agent.name + ": " + key + " opens");
      await page.locator('#nav [data-view="agents"]').click();
      await page.locator("#pop").waitFor({ state: "hidden" });
    }
  }
}

async function usageFits(page) {
  const bad = await page.evaluate(() => {
    const bad = [];
    for (const r of document.querySelectorAll(".row.stat")) {
      const box = r.getBoundingClientRect();
      for (const e of r.querySelectorAll(".num, .cost")) {
        const b = e.getBoundingClientRect();
        if (b.left < box.left || b.right > box.right + 1 || e.scrollWidth > e.clientWidth + 1) bad.push("tokens or cost cropped: " + e.textContent);
      }
    }
    for (const e of document.querySelectorAll("#stats small")) if (e.scrollWidth > e.clientWidth + 1) bad.push("stat explanation cropped: " + e.textContent);
    let last;
    const dates = [];
    for (const e of document.querySelectorAll("#chart .labels span")) {
      if (!e.textContent || getComputedStyle(e).visibility === "hidden") continue;
      dates.push(e.textContent);
      const text = document.createRange(); text.selectNodeContents(e);
      const b = text.getBoundingClientRect();
      if (last && b.left < last.right + 2) bad.push("date labels overlap");
      const box = e.parentElement.getBoundingClientRect();
      if (b.left < box.left - 1 || b.right > box.right + 1) bad.push("date label outside chart");
      last = b;
    }
    if (dates.length < 3 || dates[0] !== "09-01" || dates[dates.length - 1] !== "09-30") bad.push("chart must retain its endpoints and intermediate dates");
    return bad;
  });
  assert.deepEqual(bad, []);
  for (const selector of ["#usageAgents .cost", "#usageModels .cost"]) assert.match(await page.locator(selector).first().textContent(), /≈\$12\.34/);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": magpie web on phones", async t => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      for (const width of [360, 390, 430, 820]) {
        await t.test(`${lang} ${width}: every tab and page fits`, async () => {
          const opened = [], errors = [], failures = [], page = await open(browser, lang, width, { opened });
          page.on("pageerror", e => errors.push(e.message));
          try {
            for (const view of views) {
              try {
              await go(page, view);
              await save(page, `${engine}-${lang}-${width}-${view}`);
              await fits(page, view);
              assert.equal(await page.locator(`#view-${view}`).isVisible(), true);
              if (view === "agents") await agentsFit(page);
              if (view === "usage") await usageFits(page);
              if (view === "library") {
                await Promise.all([
                  page.waitForResponse(r => new URL(r.url()).pathname === "/api/library/reveal"),
                  page.getByRole("button", { name: lang === "en" ? "Library folder" : "资源库文件夹", exact: true }).click(),
                ]);
                assert.deepEqual(opened, [{ path: "/test/library" }], "Library folder must be reachable");
              }
              } catch (e) { failures.push(view + ": " + e.message); }
            }
            assert.deepEqual(errors, []);
            assert.deepEqual(failures, []);
          } finally { await page.close(); }
        });
      }
      await t.test(lang + ": idle scripts cannot scroll, touch momentum can, then protection returns", async () => {
        const page = await open(browser, lang, 390);
        try {
          // Settings now has separate parts. Its Usage part in a short phone
          // viewport has real content to scroll; General fits without scrolling.
          await page.setViewportSize({ width: 390, height: 600 });
          await page.goto("http://magpie.test/?view=settings&tab=usage");
          await go(page, "settings");
          await page.waitForTimeout(500);
          const view = page.locator("#view-settings");
          assert(await view.evaluate(v => v.scrollHeight > v.clientHeight + 400));
          await view.evaluate(v => { v.scrollTop = 300; });
          await page.waitForTimeout(100);
          assert.equal(await view.evaluate(v => v.scrollTop), 0, "idle code cannot move a phone page");
          await view.evaluate(async v => {
            for (const name of ["touchstart", "touchmove", "touchend"]) v.dispatchEvent(new Event(name, { bubbles: true }));
            // OS momentum outlasts the original 250ms input window; the scroll
            // events arrive 60ms apart, as they do while a finger's fling slows.
            for (let i = 1; i <= 10; i++) {
              await new Promise(r => setTimeout(r, 60));
              v.scrollTop = i * 30;
            }
          });
          await page.waitForTimeout(100);
          assert.equal(await view.evaluate(v => v.scrollTop), 300, "continuous momentum must not be pulled back");
          await page.waitForTimeout(350);
          await view.evaluate(v => { v.scrollTop = 450; });
          await page.waitForTimeout(100);
          assert.equal(await view.evaluate(v => v.scrollTop), 300, "protection returns after momentum stops");
        } finally { await page.close(); }
      });
      for (const width of [900, 1280]) {
        await t.test(`${lang} ${width}: desktop matches ${base}`, async () => {
          const current = await open(browser, lang, width, { touch: false });
          const original = await open(browser, lang, width, { touch: false, original: true });
          try {
            for (const view of views) {
              if (view === "agents" && !baselineFile("app.js").includes("function connectPanel")) continue;
              await go(current, view); await go(original, view);
              await current.mouse.move(0, 0); await original.mouse.move(0, 0);
              // Removing hover can repaint the tab's shadow in a later frame.
              await current.waitForTimeout(350); await original.waitForTimeout(350);
              const before = PNG.sync.read(await still(original));
              const after = PNG.sync.read(await still(current));
              const sameSize = before.width === after.width && before.height === after.height;
              let changedPixels = 0;
              // Chromium can repaint antialiased edges differently even when
              // comparing HEAD with itself. Count a pixel once only when one
              // of its channels changes by more than 48; size changes always fail.
              if (sameSize) for (let i = 0; i < before.data.length; i += 4) {
                for (let channel = 0; channel < 4; channel++) {
                  if (Math.abs(before.data[i + channel] - after.data[i + channel]) > 48) {
                    changedPixels++;
                    break;
                  }
                }
              }
              const same = sameSize && changedPixels <= 300;
              if (!same) {
                await save(original, `${engine}-${lang}-${width}-${view}-desktop-before`);
                await save(current, `${engine}-${lang}-${width}-${view}-desktop-after`);
              }
              assert(same, `${view}: desktop screenshot changed (${sameSize ? changedPixels + " pixels differ by more than 48; limit 300" : "dimensions differ"})`);
            }
          } finally { await current.close(); await original.close(); }
        });
      }
    }
  });
}
