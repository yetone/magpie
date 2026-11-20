// Run with Node's test runner and Playwright on the module path; see README.md.
// @InkCoalW on X: read and manage the hooks an agent already runs, and
// clean them up. The Hooks tab lists them "In your agents", under the
// library's own: each with its event and matcher, its command, its agent
// and the file it is in. Bring in posts hooks/import with its key; one
// the library can't keep as written (two commands, a prompt hook) has
// Bring in greyed, saying why, and can still be removed. Remove asks
// first, says the file is copied to the backups, and posts
// hooks/remove-found. RTK's own hooks are the RTK tab's and aren't listed.
// With no hooks in the library the list is still there, under the empty
// state. In en, zh, zh-TW, ja and de, at 440px wide: no sideways scroll.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/ink";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const HOOK_AGENTS = [
  { id: "claude", file: `${HOME}/.claude/settings.json`, events: ["PreToolUse", "PostToolUse", "Notification", "Stop"] },
  { id: "codex", file: `${HOME}/.codex/hooks.json`, events: ["PreToolUse", "PostToolUse", "Stop"] },
];
const FOUND = () => [
  { key: "k-say", agent: "claude", event: "Stop", commands: ["say done"], file: `${HOME}/.claude/settings.json`, hook: { name: "say", event: "turn-end", command: "say done", agents: ["claude"] } },
  { key: "k-two", agent: "codex", event: "PostToolUse", matcher: "Bash", commands: ["lint-a", "lint-b"], file: `${HOME}/.codex/hooks.json`, why: "several" },
  { key: "k-rtk", agent: "claude", event: "PreToolUse", matcher: "Bash", commands: ["rtk hook claude"], file: `${HOME}/.claude/settings.json`, own: "rtk" },
];
const lib = (state) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
  hooks: state.hooks, hookAgents: HOOK_AGENTS, foundHooks: state.found,
});

let I18N;
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, state, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(state));
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST" && !url.pathname.endsWith("/reveal")) {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "hooks/import") {
        const f = state.found.find((x) => body.names.includes(x.key));
        state.hooks = [...state.hooks, f.hook];
        state.found = state.found.filter((x) => !body.names.includes(x.key));
      }
      if (what === "hooks/remove-found") state.found = state.found.filter((x) => !body.names.includes(x.key));
      return json({ ...lib(state), result: { changed: ["claude"] } });
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

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  const v = document.querySelector("#view-library");
  if (v.scrollWidth > v.clientWidth + 1) over.push("library " + v.scrollWidth + ">" + v.clientWidth);
  for (const e of document.querySelectorAll("#view-library .lib-foundhookhead > *, #view-library .lib-foundhook, #view-library .lib-foundhook *, #modal .lib-editor *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the hooks the agents run that magpie didn't give them", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { hooks: [], found: FOUND() };
        const posts = [], errors = [];
        const ctx = await browser.newContext({ viewport: { width: 440, height: 1000 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "hooks"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const rows = page.locator("#view-library .lib-foundhook");
        await rows.first().waitFor();

        // the library has none: the found ones are listed under the empty state
        assert.equal(await page.locator("#view-library .lib-foundhookhead .label").textContent(), w("In your agents"));
        assert.equal(await rows.count(), 2, "RTK's own hook isn't listed");
        const say = rows.filter({ hasText: "say done" });
        assert.equal((await say.locator(".name").textContent()).trim(), "Stop");
        assert.equal((await say.locator(".lib-path").textContent()).trim(), "~/.claude/settings.json");
        assert.equal(await say.locator(".lib-have [title]").getAttribute("title"), "Claude Code");
        const two = rows.filter({ hasText: "lint-a ; lint-b" });
        assert.equal((await two.locator(".name").textContent()).trim(), "PostToolUse · Bash");
        const twoIn = two.getByRole("button", { name: w("Bring in"), exact: true });
        assert(await twoIn.isDisabled(), "one the library can't keep as written can't be brought in");
        assert.equal(await twoIn.getAttribute("title"), w("It runs several commands: the library keeps a hook as one command"));
        assert.deepEqual(await sideways(page), [], "nothing sideways at 440");

        // removing asks first, says where the backup goes, and posts the key
        await two.locator("button.lib-icon.danger").click();
        const modal = page.locator("#modal");
        await modal.locator(".ehead b").waitFor();
        assert.equal(await modal.locator(".ehead b").textContent(), w("Remove this {event} hook?", { event: "PostToolUse · Bash" }));
        assert(await modal.getByText(w("It is taken out of {file}, and the rest of the file stays as it is. The file is copied to magpie's backups first, where it can be put back.", { file: "~/.codex/hooks.json" })).isVisible());
        assert.deepEqual(await sideways(page), [], "the dialog fits at 440");
        assert.equal(posts.length, 0, "nothing posted before the confirm");
        await modal.getByRole("button", { name: w("Remove"), exact: true }).click();
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-foundhook").length === 1);
        assert.deepEqual(posts.at(-1), { what: "hooks/remove-found", body: { names: ["k-two"] } });

        // bringing one in posts its key, and it is the library's after
        await say.getByRole("button", { name: w("Bring in"), exact: true }).click();
        await page.waitForFunction(() => !document.querySelector("#view-library .lib-foundhook"));
        assert.deepEqual(posts.at(-1), { what: "hooks/import", body: { names: ["k-say"] } });
        assert.equal(await page.locator("#view-library .lib-hook .name").first().textContent(), "say");
        assert.equal(await page.locator("#view-library .lib-foundhookhead").count(), 0, "with none found, no heading");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
