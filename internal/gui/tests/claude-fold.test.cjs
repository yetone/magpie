// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code's picker with magpie's Claude subscription signed in to the
// account Claude Code is (#496: 37 rows to Codex's 18, each Claude model
// twice, as Claude Code's own and through magpie). The subscription's models
// (agent.Option's folded) are one row at the end of Claude Code's own, "3
// more through magpie", though they come after another provider's: a click
// shows them under it, the row and the page staying where they were, and
// one is picked as before; another click hides them. Enter on the row opens
// it too, a search finds them, and the one Claude Code is set to is shown,
// never folded away. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const own = (value, note) => ({ value, note, icon: "claude-color", group: "Claude Code", direct: "Anthropic" });
const sub = (model, label, big) => ({ value: `claude/${model}${big ? "[1m]" : ""}`, label, note: "me@example.com · via magpie", icon: "claudecode-color",
  group: "Claude Code", ref: `claude/${model}`, context: big ? 1e6 : 2e5, folded: true });
// in the order magpie gives them: Claude Code's own, then the catalog, where
// the subscription comes after the providers the user added
const options = [
  own("claude-opus-5-5", "Claude Opus 5.5"),
  own("claude-sonnet-5-5", "Claude Sonnet 5.5"),
  own("claude-haiku-4-5", "Claude Haiku 4.5 (latest)"),
  { value: "deepseek/pro", label: "DeepSeek Pro", note: "DeepSeek · via magpie", icon: "deepseek-color", group: "DeepSeek", ref: "deepseek/pro" },
  sub("claude-opus-5-5", "Claude Opus 5.5", true),
  sub("claude-sonnet-5-5", "Claude Sonnet 5.5", true),
  sub("claude-haiku-4-5", "Claude Haiku 4.5 (latest)", false),
];
// the other agents, so Claude Code sits down a list that scrolls
const filler = [{ key: "model", label: "model", value: "deepseek/pro", options: [options[3]] }];
const fresh = () => ({
  agents: [
    ...Array.from({ length: 5 }, (_, i) => ({ id: "agent-" + i, name: "Agent " + i, path: "/test/" + i, fields: filler })),
    { id: "claude", name: "Claude Code", icon: "claudecode-color", path: "~/.claude/settings.json",
      fields: [{ key: "model", label: "model", value: "claude-opus-5-5", options }] },
    ...Array.from({ length: 12 }, (_, i) => ({ id: "more-" + i, name: "More " + i, path: "/test/m" + i, fields: filler })),
  ],
  profiles: [],
});

function server(lang, sets) {
  let cur = fresh();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...cur, settings: { lang, theme: "light" } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      return json({ ...cur, settings: { lang, theme: "light" } });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { more: (n) => `${n} more through magpie`, whose: "on the account Claude Code is signed in to" },
  zh: { more: (n) => `其余 ${n} 个经过 magpie`, whose: "用的是 Claude Code 自己登录的账号" },
};
const row = '.row.agent[data-id="claude"]';
const via = "me@example.com · via magpie";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Claude Code's models through magpie on its own account are one row, opened with a click`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      // the picker held still as it opens, so where a row is can be read
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 700 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const sets = [];
      await page.route("**/*", server(lang, sets));
      await page.goto("http://magpie.test/");
      const field = page.locator(`${row} .field[data-key="model"]`);
      await field.waitFor();
      // the list as drawn: a heading, the fold, or a row with its words
      const rows = () => page.locator("#list li").evaluateAll((ls) => ls.map((li) => li.classList.contains("group") ? `# ${li.textContent}`
        : li.classList.contains("fold") ? `> ${li.querySelector(".v").textContent} · ${li.querySelector(".n").textContent} · ${li.getAttribute("aria-expanded")}`
          : [li.querySelector(".v").textContent, li.querySelector(".n")?.textContent].filter(Boolean).join(" · ")));
      const open = async () => {
        await field.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      };
      const fold = page.locator("#list li.fold");

      // the page scrolled, so a click that moved it would show
      const view = page.locator("#view-agents");
      await page.mouse.move(400, 300);
      for (let i = 0; i < 2; i++) { await page.mouse.wheel(0, 30); await page.waitForTimeout(20); }
      await page.waitForTimeout(300);
      const top = await view.evaluate((v) => v.scrollTop);
      assert(top > 0, "the list must be scrolled");

      await open();
      const shut = await rows();
      const at = shut.indexOf(`> ${w.more(3)} · ${w.whose} · false`);
      assert(at > 0, `one row for the three, shut: ${JSON.stringify(shut)}`);
      assert.equal(shut[at - 1], "claude-haiku-4-5 · Claude Haiku 4.5 (latest)", "at the end of Claude Code's own");
      assert.equal(shut[at + 1], "# DeepSeek", "before the provider the subscription came after");
      assert(!shut.some((r) => r.includes(via)), "none of them shows");

      // a click shows them under it; the row, the list and the page stay
      const before = await fold.evaluate((li) => ({ y: li.getBoundingClientRect().top, list: li.parentElement.scrollTop }));
      await fold.click();
      await page.waitForFunction(() => document.querySelector("#list li.fold")?.getAttribute("aria-expanded") === "true");
      const opened = await rows();
      const i = opened.findIndex((r) => r.startsWith("> "));
      assert.deepEqual(opened.slice(i + 1, i + 5), [`Claude Opus 5.5 · ${via}`, `Claude Sonnet 5.5 · ${via}`, `Claude Haiku 4.5 (latest) · ${via}`, "# DeepSeek"]);
      const after = await fold.evaluate((li) => ({ y: li.getBoundingClientRect().top, list: li.parentElement.scrollTop }));
      assert.deepEqual(after, before, "the row it was opened from moved");
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "the click moved the page");
      assert.deepEqual(sets, [], "opening it picks nothing");
      assert.equal(await page.evaluate(() => document.activeElement?.id), "q", "the filter keeps the keys");
      // shut again, and open
      await fold.click();
      await page.waitForFunction(() => document.querySelector("#list li.fold")?.getAttribute("aria-expanded") === "false");
      assert.deepEqual(await rows(), shut);
      await fold.click();
      await page.waitForFunction(() => document.querySelector("#list li.fold")?.getAttribute("aria-expanded") === "true");

      // one picked is written as ever, and shown as the model
      await page.locator("#list li").filter({ hasText: via }).filter({ hasText: "Claude Sonnet 5.5" }).click();
      await page.waitForFunction(() => document.querySelector('.row.agent[data-id="claude"] .field[data-key="model"] .v')?.textContent === "Claude Sonnet 5.5");
      assert.deepEqual(sets, [{ agent: "claude", field: "model", value: "claude/claude-sonnet-5-5[1m]" }]);
      assert.equal(await field.locator('.ic[data-icon="claudecode-color"]').count(), 1);
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "the pick moved the page");

      // the one it is set to first, never folded away; the rest are
      await open();
      const now = await rows();
      assert.equal(now[1], `Claude Sonnet 5.5 · ${via}`, JSON.stringify(now));
      assert.equal(await page.locator("#list li.cur .v").textContent(), "Claude Sonnet 5.5");
      assert(now.includes(`> ${w.more(2)} · ${w.whose} · false`), JSON.stringify(now));
      // from the keyboard: down to the row, Enter opens it, the next is the first of them
      for (let n = 0; n < 12 && !(await fold.evaluate((li) => li.classList.contains("sel"))); n++) await page.keyboard.press("ArrowDown");
      assert(await fold.evaluate((li) => li.classList.contains("sel")), "the arrows reach the row");
      await page.keyboard.press("Enter");
      await page.waitForFunction(() => document.querySelector("#list li.fold")?.getAttribute("aria-expanded") === "true");
      assert(await fold.evaluate((li) => li.classList.contains("sel")), "the cursor stays on it");
      assert.equal(sets.length, 1, "Enter on it picks nothing");
      await page.keyboard.press("ArrowDown");
      assert.equal(await page.locator("#list li.sel .v").textContent(), "Claude Opus 5.5");
      await page.keyboard.press("Enter");
      await page.waitForFunction(() => document.querySelector('.row.agent[data-id="claude"] .field[data-key="model"] .v')?.textContent === "Claude Opus 5.5");
      assert.deepEqual(sets[1], { agent: "claude", field: "model", value: "claude/claude-opus-5-5[1m]" });

      // a search finds them, folded or not; cleared, they fold again
      await open();
      await page.locator("#q").fill("haiku");
      await page.waitForFunction(() => !document.querySelector("#list li.fold"));
      const found = await rows();
      assert(found.includes(`Claude Haiku 4.5 (latest) · ${via}`) && found.includes("claude-haiku-4-5 · Claude Haiku 4.5 (latest)"), JSON.stringify(found));
      await page.locator("#q").fill("");
      await fold.waitFor();
      assert.equal(await fold.getAttribute("aria-expanded"), "false");
      // Claude Code's group picked on the rail: its own, then the row
      await page.locator('#pickerRail .rail-item[aria-label="Claude Code"]').click();
      await page.waitForFunction(() => ![...document.querySelectorAll("#list li.group")].some((g) => g.textContent === "DeepSeek"));
      const group = await rows();
      assert.deepEqual(group.slice(group.indexOf("# Claude Code")), ["# Claude Code", "claude-opus-5-5 · Claude Opus 5.5",
        "claude-sonnet-5-5 · Claude Sonnet 5.5", "claude-haiku-4-5 · Claude Haiku 4.5 (latest)", `> ${w.more(2)} · ${w.whose} · false`]);
      await page.keyboard.press("Escape");

      const missing = await page.evaluate(() => ["{n} more through magpie", "on the account {agent} is signed in to",
        "{agent} asks for these itself, on that account; picked here, it goes through magpie to it, for magpie's usage or routing"].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "the page moved");
      assert.deepEqual(errors, []);
    });
  }
}
