// Run with Node's test runner and Playwright on the module path; see README.md.
// One list of sessions, not two (#752, hisiling: the top bar's Sessions page
// had resume, delete and the trash but no tokens or cost; Usage → Sessions'
// latest sessions had tokens, cost and models but no delete, and both could
// resume and open the same details). Now each Sessions page row shows what
// the session spent, and its details the models and where magpie routed it;
// its filter finds a model. Usage's list keeps its numbers, under the page's
// range and filters, and a row there opens the session on the Sessions page:
// on its agent, its folder unfolded, the row opened and brought into sight
// by that click; All sessions opens the page. English and Chinese, Chromium
// and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (agent, id, cwd, title, min, n) => ({
  agent, id, cwd, title, start: at(min + 5), last: at(min),
  models: [{ model: agent === "claude" ? "claude-opus-4" : "gpt-5", input: 1000 * n, output: 100 * n, cache_read: 500 * n, cache_write: 0, cost: 0.5 * n, priced: true }],
  input: 1000 * n, output: 100 * n, cache_read: 500 * n, cache_write: 0, cost: 0.5 * n, unpriced: 0,
  resume: `cd ${cwd} && ${agent} --resume ${id}`, path: `~/.${agent}/x/${id}.jsonl`,
  via: id === "a-12" ? [{ provider: "relay", model: "opus-x", effort: "high", calls: 3, tokens: 1300 }] : undefined,
});
const look = { claude: ["Claude Code", "claudecode-color"], codex: ["Codex", "openai"] };

function fixture() {
  const claude = [];
  for (let i = 0; i < 16; i++) claude.push(sess("claude", `a-${i}`, "/work/app", `older task ${i}`, 10 + i * 10, i + 1));
  claude.push(sess("claude", "b-1", "/work/blog", "write the post", 400, 2));
  const codex = [sess("codex", "c-1", "/work/tool", "port the parser", 5, 3)];
  return { claude, codex };
}

function serve(lang) {
  const store = fixture();
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const managed = (s) => ({ ...s, size: 4096, messages: 6, files: 1, deletable: true });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/sessions") {
      const all = [...store.codex, ...store.claude].map((s) => ({ ...s, name: look[s.agent][0], icon: look[s.agent][1] }));
      return json({ sessions: all, terminal: true, dirs: ["~/.claude/projects", "~/.codex/sessions"] });
    }
    if (url.pathname === "/api/sessions/stats") {
      const today = new Date().toISOString().slice(0, 10);
      const usage = [...store.claude, ...store.codex].map((s) => ({ agent: s.agent, cwd: s.cwd, model: s.models[0].model, input: s.input, output: s.output, cache_read: s.cache_read, cache_write: 0, cost: s.cost, priced: true }));
      return json({ from: today, to: today, days: [{ date: today, usage, active: [] }], agents: { claude: "Claude Code", codex: "Codex" } });
    }
    if (url.pathname === "/api/sessions/overview") return json({ count: 18, median: 5000, p90: 15000, days: [1], top: { tokens: [], cost: [], active: [] } });
    if (url.pathname === "/api/sessions/manage") {
      const agent = url.searchParams.get("agent") === "codex" ? "codex" : "claude";
      return json({
        agents: [
          { agent: "claude", count: store.claude.length, deletable: true, name: "Claude Code", icon: "claudecode-color" },
          { agent: "codex", count: store.codex.length, deletable: true, name: "Codex", icon: "openai" },
        ],
        agent, sessions: store[agent].map(managed), terminal: true, trash: [], trashDir: "~/magpie/trash/sessions",
      });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const words = {
  en: { usage: "Usage", sessions: "Sessions", all: "All sessions", routed: "Routed", open: "Open in Sessions" },
  zh: { usage: "用量", sessions: "会话", all: "全部会话", routed: "路由到", open: "在会话页打开" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: the Sessions page shows what each session spent, and Usage's list opens there`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sessions-usage-open.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("console", (m) => { if (m.type() === "warning" && /refused/.test(m.text())) errors.push(m.text()); });
      await page.route("**/*", serve(lang));
      await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "sessions"); localStorage.setItem("magpie.sessRange", "all"); });
      await page.goto("http://magpie.test/?view=usage");

      // the reader's wheel brings a row into sight (a scroll by code is put back)
      const reach = async (v, l) => {
        const box = await v.boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        for (let k = 0; k < 120; k++) {
          const b = await l.boundingBox();
          if (b.y >= box.y + 40 && b.y + b.height <= box.y + box.height - 40) break;
          await page.mouse.wheel(0, b.y < box.y + 40 ? -60 : 60);
          await page.waitForTimeout(20);
        }
        await page.waitForTimeout(300);
      };

      // Usage's list: the numbers stay, the actions live on the Sessions page
      const list = page.locator("#sessList");
      const usageRow = (title) => list.locator(".row.sess", { hasText: title });
      await usageRow("older task 12").waitFor();
      assert.equal(await list.locator(".sess-resume, .sess-term").count(), 0, "Usage's rows don't resume: the Sessions page does");
      assert.match(await usageRow("older task 12").locator(".num b").innerText(), /14\.3K/);
      assert.equal(await usageRow("older task 12").getAttribute("title"), w.open);
      assert(await page.locator("#sessListHead #sessAll").isVisible(), "All sessions beside the search");
      assert.equal((await page.locator("#sessAll").innerText()).trim(), w.all);

      // a row far down the agent's list opens on the Sessions page, in sight
      await reach(page.locator("#view-usage"), usageRow("older task 12"));
      const shot = async (name) => {
        if (!process.env.ARTIFACT_DIR) return;
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}.png`) });
      };
      await shot("usage-list");
      await usageRow("older task 12").click();
      const view = page.locator("#view-sessions");
      await view.waitFor({ state: "visible" });
      const row = (id) => view.locator(`.row.sm-sess[data-id="${id}"]`);
      await row("a-12").waitFor();
      await view.locator(".sm-item.open .sess-detail").waitFor();
      assert.equal(await view.locator(".sm-item.open .row.sm-sess").getAttribute("data-id"), "a-12", "the session clicked is the one opened");
      await page.waitForFunction(() => {
        const r = document.querySelector('.row.sm-sess[data-id="a-12"]').getBoundingClientRect();
        const v = document.querySelector("#view-sessions").getBoundingClientRect();
        return r.top >= v.top && r.bottom <= v.bottom;
      });
      assert(await view.locator("#view-sessions .sm-agents .opt.on, .sm-agents .opt.on").first().innerText().then((x) => x.includes("Claude Code")));
      assert.equal(await page.locator("#nav button.on").innerText(), w.sessions);

      await shot("sessions-page");
      // the Sessions page's row: tokens, cost and model, as Usage showed them
      const r12 = row("a-12");
      assert.match(await r12.locator(".num b").innerText(), /14\.3K/);
      assert.match(await r12.locator(".cost").innerText(), /6\.50/);
      assert.match(await r12.locator(".sub").innerText(), /claude-opus-4 → opus-x · high/);
      assert(await r12.locator(".sess-resume").isVisible(), "resume stays on the Sessions page");
      assert(await r12.locator(".sm-del").count() === 1, "and delete");
      // its details: what each model spent, and where magpie routed it
      const det = view.locator(".sm-item.open .sess-detail");
      assert.match(await det.locator(".sess-models").innerText(), /claude-opus-4/);
      assert((await det.locator(".sess-line .k").allInnerTexts()).includes(w.routed));
      assert.match(await det.innerText(), /relay\/opus-x · high/);

      // a narrow window: the row's numbers and buttons stay inside it
      await page.setViewportSize({ width: 420, height: 700 });
      await page.waitForTimeout(100);
      assert(await r12.evaluate((r) => r.scrollWidth <= r.clientWidth + 1 && r.getBoundingClientRect().right <= innerWidth), "the row fits a 420px window");
      assert((await r12.locator(".who").boundingBox()).width >= 100, "its title keeps room to be read");
      await shot("sessions-narrow");
      await page.setViewportSize({ width: 900, height: 560 });

      // its filter finds a session by model
      const top = await view.evaluate((v) => v.scrollTop);
      await view.locator(".sm-filter").fill("gpt-5");
      await view.locator(".empty-state").waitFor();
      await view.locator(".sm-filter").fill("claude-opus");
      await row("b-1").waitFor();
      await view.locator(".sm-filter").fill("");
      assert(top > 0);

      // another agent's session switches the page to that agent
      await page.locator("#nav").getByRole("button", { name: w.usage, exact: true }).click();
      await reach(page.locator("#view-usage"), usageRow("port the parser"));
      await usageRow("port the parser").click();
      await row("c-1").waitFor();
      assert((await view.locator(".sm-agents .opt.on").innerText()).includes("Codex"));
      assert.equal(await view.locator(".sm-item.open .row.sm-sess").getAttribute("data-id"), "c-1");

      // All sessions opens the page
      await page.locator("#nav").getByRole("button", { name: w.usage, exact: true }).click();
      await reach(page.locator("#view-usage"), page.locator("#sessAll"));
      await page.locator("#sessAll").click();
      await view.waitFor({ state: "visible" });
      assert.deepEqual(errors, []);
    });
  }
}
