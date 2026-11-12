// Run with Node's test runner and Playwright on the module path; see README.md.
// #1790 (AllenReder): Hooks, the Library's fifth tab. A hook is kept once
// and given to agents with the chips every other tab has. The tab lists
// each hook with what it runs on, in words (After a tool runs · File
// edits), and its command; an agent magpie can't write hooks for has its
// chip greyed, saying why, and a click on it posts nothing. Asides say
// what an agent does with a hook given to it: Codex runs one only once it
// is trusted in its /hooks, Cursor runs Claude Code's too. A row's chip
// posts hooks/agents. Add opens the editor: the event and the tools it is
// for are app menus (never a <select>), an agent's own event (Claude
// Code · Notification) leaves only that agent's chip, and Save posts
// hooks/save with what was picked. Select and the bar's chips post
// hooks/agents-some; Remove all asks first and posts hooks/remove-all.
// A hook's description (#2011, emo172) is its own words under its name:
// the editor's Description field, saved with it, and an Enter that only
// ends an input method's composition saves nothing.
// #2085 (emo172): a long description is shown whole, wrapped, in the list
// at 440 and 1100 wide, and the editor's Description is a box that wraps
// and grows to show all of what is written.
// At 440px wide in en, zh, zh-TW, ja and de: no sideways scroll, and what
// each click pressed stays where it was on the screen.
// #5604 (NEKO-CwC): a session-start hook is narrowed to some starts from
// an app menu too (At which starts); an agent that can't run it at just
// those greys, saying why, and is left out when it is saved.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/allen";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const LONG = "【格式化】编辑文件后自动运行 prettier 格式化改动过的文件，保持代码风格统一；只对 TypeScript、JavaScript 和 CSS 文件生效，失败时不会阻止本轮对话继续进行。";
const hk = (name, event, matcher, command, agents, description) => ({ name, event, matcher, command, agents, ...(description ? { description } : {}) });
// the starts each agent can be narrowed to, as hooksView lists them (in part)
const FIVE = ["startup", "resume", "clear", "compact", "fork"];
const HOOK_AGENTS = [
  { id: "claude", file: `${HOME}/.claude/settings.json`, events: ["PreToolUse", "PostToolUse", "Notification", "Stop", "SessionStart"], startSources: FIVE, starts: ["startup", "resume", "clear", "compact", "fork", "startup|resume|clear|compact"] },
  { id: "codex", file: `${HOME}/.codex/hooks.json`, events: ["PreToolUse", "PostToolUse", "Stop", "SessionStart"], startSources: FIVE, starts: ["startup", "resume", "clear", "compact", "fork", "startup|resume|clear|compact"] },
  { id: "cursor", file: `${HOME}/.cursor/hooks.json`, events: ["afterFileEdit", "stop", "sessionStart"] },
  { id: "gemini", file: `${HOME}/.gemini/settings.json`, events: ["AfterTool", "AfterAgent", "SessionStart"], startSources: ["startup", "resume", "clear"], starts: ["startup", "resume", "clear", "startup|resume|clear", "startup|resume|clear|compact"] },
  { id: "opencode", no: "OpenCode has no hooks magpie can write" },
];
const lib = (hooks) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("cursor", "Cursor"), agent("gemini", "Gemini CLI"), agent("opencode", "OpenCode")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
  hooks, hookAgents: HOOK_AGENTS,
});

let I18N;
async function loadI18N() {
  I18N = strings().I18N; // i18n.js's and the plugins'
}
// the words the page says in lang; a string missing from a language fails
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, state, posts) {
  const can = (h, a) => a !== "opencode" && (!h.event.includes(":") || h.event.startsWith(a + ":"));
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(state.hooks));
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST" && !url.pathname.endsWith("/reveal")) {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      const put = (x, agents, on) => ({ ...x, agents: on ? [...new Set([...x.agents, ...agents.filter((a) => can(x, a))])].sort() : x.agents.filter((a) => !agents.includes(a)) });
      if (what === "hooks/agents") state.hooks = state.hooks.map((x) => (x.name === body.name ? { ...x, agents: body.agents.filter((a) => can(x, a)) } : x));
      if (what === "hooks/agents-some") state.hooks = state.hooks.map((x) => (body.names.includes(x.name) ? put(x, body.agents, body.on) : x));
      if (what === "hooks/save") state.hooks = [...state.hooks.filter((x) => x.name !== body.old), body.hook];
      if (what === "hooks/remove-all") state.hooks = state.hooks.filter((x) => !body.names.includes(x.name));
      return json({ ...lib(state.hooks), result: { changed: ["claude"] } });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// a click where the button is, as the reader's, and what was clicked stays
// where it was on the screen while the click redraws around it
const press = async (page, loc) => {
  await page.waitForFunction(() => !document.querySelector("#view-library").getAnimations({ subtree: true }).some((a) => a.effect?.getComputedTiming().endTime !== Infinity));
  const b = await loc.boundingBox();
  const sel = String(loc);
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
  await page.waitForTimeout(250);
  if (!(await loc.count())) return;
  const a = await loc.boundingBox();
  if (a && a.width) assert(Math.abs(a.y - b.y) <= 1, `${sel} moved on the screen from ${Math.round(b.y)} to ${Math.round(a.y)}`);
};
const settled = (page) => page.waitForFunction(() => !document.querySelector("#modal").getAnimations({ subtree: true }).length);
// nothing wider than the window: the page, the Library, its rows, bar and dialog
const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  const v = document.querySelector("#view-library");
  if (v.scrollWidth > v.clientWidth + 1) over.push("library " + v.scrollWidth + ">" + v.clientWidth);
  for (const e of document.querySelectorAll("#view-library .lib-hookhead > *, #view-library .lib-hook, #view-library .lib-pickbar, #view-library .lib-tabs, #modal .lib-hookedit *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Library's Hooks tab", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    await loadI18N();
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { hooks: [hk("bark", "turn-end", "", "curl -s https://api.day.app/KEY/done", ["codex"]), hk("fmt", "after-tool", "edit", "prettier -w .", ["claude", "cursor"], LONG)] };
        const posts = [], errors = [];
        const ctx = await browser.newContext({ viewport: { width: 440, height: 1000 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "hooks"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await v.locator(".lib-hook").first().waitFor();
        const row = (name) => v.locator(".lib-hook").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        const agentsOf = (name) => state.hooks.find((s) => s.name === name)?.agents;

        // the tab, with its count, between Skills and Subagents
        const tabs = (await v.locator(".lib-tabs button").allTextContents()).map((x) => x.trim());
        assert.deepEqual(tabs.slice(3, 9), [w("Skills"), w("Hooks") + " · 2", w("Environment variables"), w("Subagents"), w("Claude plugins"), "RTK"]);
        // each hook says when it runs, in words, and what it runs
        assert.equal((await row("fmt").locator(".lib-hookev").textContent()).trim(), w("After a tool runs") + " · " + w("File edits"));
        assert.equal((await row("bark").locator(".lib-hookev").textContent()).trim(), w("When a turn ends"));
        assert.equal((await row("bark").locator(".lib-hookcmd").textContent()).trim(), "curl -s https://api.day.app/KEY/done");
        // a description is said under the name; one without has none
        assert.equal((await row("fmt").locator(".lib-hookdesc").textContent()).trim(), LONG);
        assert.equal(await row("bark").locator(".lib-hookdesc").count(), 0);
        // the whole of a long one is on the screen, wrapped, at either width,
        // and the hook's name stays beside its glyph, not pushed under it;
        // 1100 in a window of its own, as resizing this one crosses the
        // page's breakpoint mid-test
        const wrapped = async (pg, width) => {
          const r = pg.locator("#view-library .lib-hook").filter({ has: pg.locator(".name", { hasText: /^fmt$/ }) });
          await r.waitFor();
          const fit = await r.locator(".lib-hookdesc").evaluate((e) => ({ clipped: e.scrollWidth > e.clientWidth + 1 || e.scrollHeight > e.clientHeight + 1, lines: Math.round(e.getBoundingClientRect().height / parseFloat(getComputedStyle(e).lineHeight)) }));
          assert.equal(fit.clipped, false, `the description is cut off at ${width}`);
          assert(fit.lines >= 2, `the description isn't wrapped at ${width}: ${fit.lines} line`);
          const gap = await r.evaluate((x) => Math.abs(x.firstElementChild.getBoundingClientRect().top - x.querySelector(".name").getBoundingClientRect().top));
          assert(gap < 12, `the name is ${Math.round(gap)}px off its glyph at ${width}`);
        };
        await wrapped(page, 440);
        {
          const wctx = await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
          await wctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "hooks"); } catch {} });
          const wide = await wctx.newPage();
          wide.setDefaultTimeout(5000);
          wide.setDefaultNavigationTimeout(60000);
          wide.on("pageerror", (e) => errors.push(e.message));
          await wide.route("**/*", server(lang, { hooks: state.hooks }, []));
          await wide.goto("http://magpie.test/");
          await wide.locator('button[data-view="library"]').click();
          await wrapped(wide, 1100);
          await wctx.close();
        }
        // what an agent does with one given to it
        await v.locator(".lib-hooks-codex").filter({ hasText: w("Codex runs a new or changed hook only once you trust it: review it in Codex's /hooks.") }).waitFor();
        await v.locator(".lib-hooks-twice").filter({ hasText: w("Cursor runs Claude Code's hooks too: a hook given to both runs twice in Cursor.") }).waitFor();
        await v.locator(".lib-hooks-no").filter({ hasText: w("magpie can't give hooks to {agents} yet: their chips are greyed, and say why.", { agents: "OpenCode" }) }).waitFor();
        assert.equal(await v.locator(".lib-hooks-off").count(), 0, "Gemini CLI, given none, said to have hooks off");
        assert.deepEqual(await sideways(page), [], "sideways scroll with the list");

        // OpenCode's chip is greyed, says why, and a click posts nothing
        const oc = row("bark").locator('.lib-ag[data-agent="opencode"]');
        assert.equal(await oc.getAttribute("aria-disabled"), "true");
        assert.equal(await oc.getAttribute("title"), w("{agent} has no hooks magpie can write", { agent: "OpenCode" }));
        await press(page, oc);
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "a greyed chip posted");

        // a row's chip gives the hook to Gemini CLI
        await press(page, row("bark").locator('.lib-ag[data-agent="gemini"]'));
        await page.waitForFunction(() => document.querySelector('#view-library .lib-hook .lib-ag[data-agent="gemini"][aria-pressed="true"]'));
        await page.waitForTimeout(200);
        assert.deepEqual(posts.shift(), { what: "hooks/agents", body: { name: "bark", agents: ["codex", "gemini"] } });
        assert.deepEqual(agentsOf("bark"), ["codex", "gemini"]);

        // Add: the editor, its event and tools from app menus
        await press(page, v.locator(".after-list button"));
        const ed = page.locator("#modal .lib-hookedit");
        await ed.waitFor();
        await settled(page);
        assert.equal(await page.locator("#modal select").count(), 0, "a native <select>");
        assert.equal((await ed.locator(".ehead b").textContent()).trim(), w("New hook"));
        assert.equal((await ed.locator(".lib-hookevent").textContent()).trim(), w("When a turn ends"));
        await ed.locator("input").first().fill("notify");
        const desc = ed.locator(".lib-hookdescin");
        assert.equal(await desc.getAttribute("placeholder"), w("e.g. Tell me when a turn ends"));
        await ed.locator(".hint", { hasText: w("Optional: what it's for, in any language. Only the library keeps it") }).first().waitFor();
        // a box that wraps and grows to show all of a long one
        assert.equal(await desc.evaluate((e) => e.tagName), "TEXTAREA");
        await desc.fill(LONG);
        const box = await desc.evaluate((e) => ({ hidden: e.scrollHeight > e.clientHeight + 1, sideways: e.scrollWidth > e.clientWidth + 1, tall: e.clientHeight }));
        assert.equal(box.hidden, false, "part of a long description is out of the box");
        assert.equal(box.sideways, false, "the box scrolls sideways");
        assert(box.tall > 50, `the box is a line high: ${box.tall}px`);
        await desc.fill("跑完命令就说一声");
        // Enter choosing an input method's word is the IME's: nothing saved
        await desc.evaluate((i) => i.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", isComposing: true, keyCode: 229, bubbles: true })));
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "an IME's Enter saved the hook");
        await ed.waitFor();
        // an agent's own event: only that agent can run it
        await ed.locator(".lib-hookevent").click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w("When it runs"));
        await menu.locator(".pm-item", { hasText: "Claude Code · Notification" }).click();
        await menu.waitFor({ state: "detached" });
        assert.equal((await ed.locator(".lib-hookevent").textContent()).trim(), "Claude Code · Notification");
        const edChip = (id) => ed.locator(`.lib-ag[data-agent="${id}"]`);
        assert.equal(await edChip("claude").getAttribute("aria-pressed"), "true");
        assert.equal(await edChip("codex").getAttribute("aria-disabled"), "true");
        assert.equal(await edChip("codex").getAttribute("title"), w("{event} is {agent}'s own event: only it can run this hook", { event: "Notification", agent: "Claude Code" }));
        await ed.locator(".lib-hookmatcher").waitFor();
        // back to one every agent has, after a tool, for shell commands
        await ed.locator(".lib-hookevent").click();
        await menu.waitFor();
        await menu.locator(".pm-item", { hasText: w("After a tool runs") }).click();
        await menu.waitFor({ state: "detached" });
        await ed.locator(".lib-hooktool").click();
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w("For which tools"));
        await menu.locator(".pm-item", { hasText: w("Shell commands") }).click();
        await menu.waitFor({ state: "detached" });
        assert.equal((await ed.locator(".lib-hooktool").textContent()).trim(), w("Shell commands"));
        assert.equal(await edChip("codex").getAttribute("aria-disabled"), null);
        await edChip("codex").click();
        await ed.locator('input[placeholder="curl -s https://api.day.app/KEY/done"]').fill("say done");
        await ed.locator(".lib-hooktimeout").fill("30");
        assert.deepEqual(await sideways(page), [], "sideways scroll in the editor");
        await ed.locator(".bar button.primary").click();
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "hooks/save", body: { old: "", hook: { name: "notify", description: "跑完命令就说一声", event: "after-tool", matcher: "shell", command: "say done", timeout: 30, agents: ["claude", "codex"] } } });
        await row("notify").waitFor();
        assert.equal((await row("notify").locator(".lib-hookdesc").textContent()).trim(), "跑完命令就说一声");
        assert.equal((await row("notify").locator(".lib-hookev").textContent()).trim(), w("After a tool runs") + " · " + w("Shell commands"));

        // Select: the bar's chips turn the ones picked on for an agent
        const head = v.locator(".row-head.lib-hookhead");
        assert.equal(await head.locator(".lib-select").getAttribute("title"), w("Pick some hooks and turn them on or off together"));
        await press(page, head.locator(".lib-select"));
        await v.locator(".lib-pickbar .lib-pickn.none").filter({ hasText: w("Pick hooks to turn them on or off together") }).waitFor();
        await press(page, row("bark").locator(".lib-pickbox"));
        await press(page, row("fmt").locator(".lib-pickbox"));
        await v.locator(".lib-pickbar .lib-pickn").filter({ hasText: w("{n} selected", { n: 2 }) }).waitFor();
        const chip = (id) => v.locator(`.lib-pickbar .lib-ag[data-agent="${id}"]`);
        assert.equal(await chip("opencode").getAttribute("aria-disabled"), "true");
        assert.equal(await chip("gemini").getAttribute("title"), w("{agent} has {n} of these hooks — click to give it the rest", { agent: "Gemini CLI", n: 1 }));
        await press(page, chip("gemini"));
        await page.locator("#status").filter({ hasText: w("{n} hooks are on for {agents}", { n: 2, agents: "Gemini CLI" }) }).waitFor();
        assert.deepEqual(posts.shift(), { what: "hooks/agents-some", body: { names: ["bark", "fmt"], agents: ["gemini"], on: true } });
        assert.deepEqual(agentsOf("fmt"), ["claude", "cursor", "gemini"]);
        assert.deepEqual(await sideways(page), [], "sideways scroll with the bar");
        await press(page, v.locator(".lib-pickdone"));
        assert.equal(await v.locator(".lib-pickbox").count(), 0);

        // Remove all asks first; Cancel posts nothing
        await press(page, head.locator(".lib-everyrm"));
        const sheet = page.locator("#modal .lib-editor");
        await sheet.waitFor();
        await settled(page);
        assert.equal((await sheet.locator(".ehead b").textContent()).trim(), w("Remove all {n} hooks?", { n: 3 }));
        const said = (await sheet.locator(".lib-confirm").allTextContents()).map((x) => x.trim());
        assert.equal(said[1], w("Hooks your agents have that aren't in the library stay as they are, and so does one you changed in an agent's file."));
        await sheet.locator(".bar button", { hasText: w("Cancel") }).click();
        await sheet.waitFor({ state: "detached" });
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "Cancel posts nothing");
        await press(page, v.locator(".lib-hookhead .lib-everyrm"));
        await sheet.waitFor();
        await settled(page);
        await sheet.locator(".bar button.danger-fill").click();
        await sheet.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "hooks/remove-all", body: { names: ["bark", "fmt", "notify"] } });
        await page.locator("#status").filter({ hasText: w("{n} hooks removed", { n: 3 }) }).waitFor();
        assert.equal(await v.locator(".lib-hook").count(), 0);
        await v.locator(".lib-empty").filter({ hasText: w("No hooks in the library yet") }).waitFor();

        // a session-start hook for compaction alone: Claude Code and Codex
        // can run it, Gemini CLI never starts a session that way, and
        // Cursor's start can't be narrowed at all
        await press(page, v.locator(".lib-empty button"));
        await ed.waitFor();
        await settled(page);
        await ed.locator("input").first().fill("reinject");
        await ed.locator('input[placeholder="curl -s https://api.day.app/KEY/done"]').fill("cat AGENTS.md");
        await ed.locator(".lib-hookevent").click();
        await menu.waitFor();
        await menu.locator(".pm-item", { hasText: w("When a session starts") }).click();
        await menu.waitFor({ state: "detached" });
        assert.equal(await ed.locator(".lib-hooktool").count(), 0);
        assert.equal((await ed.locator(".lib-hookstart").textContent()).trim(), w("Any start"));
        await ed.locator("label", { hasText: w("At which starts") }).first().waitFor();
        for (const id of ["claude", "codex", "cursor", "gemini"]) assert.equal(await edChip(id).getAttribute("aria-disabled"), null, id + " greyed for any start");
        await ed.locator(".lib-hookstart").click();
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w("At which starts"));
        assert.deepEqual((await menu.locator(".pm-item").allTextContents()).map((x) => x.trim()),
          ["Any start", "A fresh session", "A resumed session", "After /clear", "After the context is compacted", "A forked session", "Any start but a fork"].map((x) => w(x)));
        await menu.locator(".pm-item", { hasText: w("After the context is compacted") }).click();
        await menu.waitFor({ state: "detached" });
        assert.equal((await ed.locator(".lib-hookstart").textContent()).trim(), w("After the context is compacted"));
        assert.equal(await edChip("claude").getAttribute("aria-disabled"), null);
        assert.equal(await edChip("codex").getAttribute("aria-disabled"), null);
        assert.equal(await edChip("gemini").getAttribute("aria-disabled"), "true");
        assert.equal(await edChip("gemini").getAttribute("title"), w("{agent} never starts a session that way", { agent: "Gemini CLI" }));
        assert.equal(await edChip("cursor").getAttribute("aria-disabled"), "true");
        assert.equal(await edChip("cursor").getAttribute("title"), w("{agent} runs a session-start hook at every start: it can't be narrowed", { agent: "Cursor" }));
        assert.deepEqual(await sideways(page), [], "sideways scroll in the editor, narrowed to a start");
        await ed.locator(".bar button.primary").click();
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "hooks/save", body: { old: "", hook: { name: "reinject", description: "", event: "session-start", matcher: "compact", command: "cat AGENTS.md", timeout: 0, agents: ["claude", "codex"] } } });
        await row("reinject").waitFor();
        assert.equal((await row("reinject").locator(".lib-hookev").textContent()).trim(), w("When a session starts") + " · " + w("After the context is compacted"));
        assert.equal(await row("reinject").locator('.lib-ag[data-agent="gemini"]').getAttribute("title"), w("{agent} never starts a session that way", { agent: "Gemini CLI" }));

        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
