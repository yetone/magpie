// Shared by the guards that walk the whole app (button-text-center,
// control-overlap): a faked API that draws every main page with something
// on it, and a walk through them. Nothing reaches the network.
const fs = require("node:fs/promises");
const path = require("node:path");

const assets = path.resolve(__dirname, process.env.MAGPIE_ASSETS || "../assets");
const now = Date.now();
// the Usage page's Requests tab, with its model speed table by provider
const SPEEDS = [{ model: "glm-fast", name: "GLM Fast", requests: 28, tps: 75.4, tps_n: 27, ttft_ms: 600 }];
const PROV = [{ model: "glm-fast", name: "GLM Fast", provider: "relay9", provider_name: "Relay Nine", requests: 3, tries: 4, failed: 1, tps: 40, tps_n: 3, ttft_ms: 900 }];
const ROW = { route_id: 700, t: new Date(now - 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "zai", providerName: "Z.ai", host: "me@example.com", req: "glm-fast", model: "glm-fast", served: "glm-fast", effort: "high", in: 1000, out: 135, cache_read: 0, cache_write: 0, ms: 3000, ttft_ms: 600, status: 200, rid: "req_0", ep: "/v1/messages", cost: 0.01, priced: true };
const ledger = (period) => ({
  period, rows: [ROW], offset: 0, total: 1, calls: 1, errors: 0, input: 1000, output: 135, cache_read: 0, cache_write: 0, cost: 0.01, unpriced: 0,
  timed: 1, ttft_ms: 600, decode_ms: 1800, decode_out: 135, by: { model: [] }, bucket: "hour", series: [],
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }], speeds: SPEEDS, provider_speeds: PROV, speed_min: 1,
});
const LOGO = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#111"/></svg>';
const RIVER = "@magpie-community/opencode-river-auth";
const KILO = "@magpie-community/agent-kilo";

// Usage's cards: several accounts of one subscription (the head's fold,
// its ⋯), keys with a balance each
const at = (h) => new Date(now + h * 36e5).toISOString();
const win = (name, used, h) => ({ name, used, resetsAt: at(h) });
const QUOTAS = [
  { provider: "codex", name: "Codex", kind: "subscription", icon: "codex-color", plan: "Plus", user: "c1@example.com", windows: [win("5 hours", 20, 2), win("7 days", 40, 90)] },
  { provider: "codex", name: "Codex", kind: "subscription", icon: "codex-color", plan: "Plus", user: "c2@example.com", windows: [win("5 hours", 90, 2), win("7 days", 10, 90)] },
  { provider: "kimi", name: "Kimi", icon: "kimi", user: "k1@example.com", windows: [win("Weekly", 42, 70)] },
  { provider: "openrouter", name: "OpenRouter", icon: "openrouter", user: "key-a", windows: [], balance: "$4.00" },
  { provider: "openrouter", name: "OpenRouter", icon: "openrouter", user: "key-b", windows: [], balance: "$9.50" },
];

// with folds: the Library's tabs that have an Advanced fold, each with
// something in it (adv-fold-layout): billion-context's settings, Memory's
// digest, a subagent and a rule to open in their editors
const FOLD_LIB = {
  hooks: [], hookAgents: [], commands: [], commandAgents: [], subagentModels: [],
  subagents: [{ name: "reviewer", description: "Reviews a diff for bugs.", prompt: "Review the diff.", skills: [], mcp: [], agents: ["claude"] }],
  subagentAgents: [{ id: "claude", dir: "/u/.claude/agents", skills: true, mcp: true }],
  rules: [{ name: "api", paths: ["src/api/**"], body: "Keep handlers small.", agents: ["claude"], files: ["/u/.claude/rules/api.md"] }],
  ruleAgents: [{ id: "claude", dir: "/u/.claude/rules", paths: true }],
};
const BILI = { path: "/u/.local/bin/bili", version: "0.1.213", url: "https://github.com/ranxianglei/billion-context", agents: [{ id: "claude", name: "Claude Code", icon: "", on: true }] };
const BILI_SET = {
  path: "/u/.config/billion-context/billion-context.json", growth: 0, nudge: false, summary: false, targets: [], recipes: ["claude/claude-haiku"],
  philosophy: "", risk: false, markers: true, running: 1, models: [{ id: "gpt-5.4-mini", name: "GPT-5.4 mini", provider: "codex", providerName: "Codex" }],
};
const MEMORY = {
  memories: [{ id: "m1", text: "Use pnpm, never npm, in this repo", kind: "rule", scope: "global", origin: "user", status: "active", created: "2026-11-01T10:00:00Z", updated: "2026-11-02T10:00:00Z" }],
  projects: {}, digest: true, agents: ["claude"],
};
const DISTILL = { on: false, model: "", auto: "", running: false, efforts: [], effort: "", days: 0, ranges: [7, 30], models: [] };
const SEMANTIC = { on: false, installed: false, size: 135137323, downloading: false, done: 0, read: 0, of: 0, reading: false };

function server(lang, { folds } = {}) {
  const listings = [
    { package: RIVER, name: "River", providers: ["river"], community: true, github: { stars: 7, owner: "a" }, summary: { en: "River pay-as-you-go inference." } },
    { package: KILO, name: "Kilo Code", kind: "agent", community: true, github: { stars: 0, owner: "a" }, summary: { en: "Kilo Code on magpie's Agents page." } },
  ];
  const npm = { [RIVER]: { version: "0.1.3", weekly: 40 }, [KILO]: { version: "2.5.2", weekly: 3 } };
  const pstate = { bun: true, bunVersion: "1.3.0", plugins: [] };
  const settings = { lang, theme: "light", tray: "panel", currency: "usd" };
  const base = { input: 23100, output: 859, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.004, errors: 0, calls: 7 };
  const agents = [{ id: "alma", name: "Alma", icon: "file:logo.svg", ...base }];
  const models = [{ id: "m1", name: "claude-haiku-4.5", sub: "Kiro", icon: "file:logo.svg", ...base }];
  const accounts = [{ id: "a1", name: "yetone", sub: "WorkBuddy", icon: "file:logo.svg", ...base }];
  const lib = {
    dir: "/u/.magpie/library", backups: "/u/.magpie/backups", home: "/u", mcpHub: false, hubPath: "/mcp/magpie/hub/",
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: "/u/.claude/skills", mcp: "/u/.claude.json" }, { id: "qwen", name: "Qwen Code", skills: "/u/.qwen/skills" }],
    instructions: { agents: [], sets: [] }, foundSkills: [], problems: [], foundServers: [], projects: [],
    skills: [{ name: "pdf", description: "Read and write PDF files.", agents: ["claude"] }],
    servers: [{ name: "notes", transport: "stdio", command: "uvx", args: ["notes-mcp"], agents: ["claude"] }],
    ...(folds ? FOLD_LIB : {}),
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") {
      return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    }
    if (url.pathname.startsWith("/api/icons/")) return route.fulfill({ contentType: "image/svg+xml", body: LOGO });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", fields: [] }], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999", calls: [] }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/usage/quotas") return json(QUOTAS);
    if (url.pathname === "/api/usage/requests") return json(ledger(url.searchParams.get("period")));
    if (url.pathname === "/api/usage") return json({ calls: 7, errors: 0, input: 23100, output: 859, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.004, unpriced: 0, accounts, callerKeys: [], agents, models, series: [], bucket: "day", path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/library") return json(lib);
    if (folds && url.pathname === "/api/library/bili") return json(BILI);
    if (folds && url.pathname === "/api/library/bili/settings") return json(BILI_SET);
    if (folds && url.pathname === "/api/memory") return json(MEMORY);
    if (folds && url.pathname.startsWith("/api/memory/distill")) return json(DISTILL);
    if (folds && url.pathname === "/api/memory/semantic") return json(SEMANTIC);
    if (url.pathname === "/api/library/mcp/check") return json({ servers: {} });
    if (url.pathname === "/api/plugins") return json(pstate);
    if (url.pathname === "/api/plugins/listings") return json({ listings });
    if (url.pathname === "/api/plugins/market") return json({ listings, state: pstate });
    if (url.pathname === "/api/plugins/counts") return json({ plugins: { [RIVER]: { users: 3, likes: 2 }, [KILO]: { users: 2500, likes: 6 } }, liked: [] });
    if (url.pathname === "/api/plugins/npm") {
      const out = {};
      for (const n of (url.searchParams.get("names") || "").split(",")) if (npm[n]) out[n] = npm[n];
      return json({ npm: out });
    }
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname === "/api/plugins/github") return json({ repos: [] });
    if (url.pathname === "/api/plugins/page") return json({ readme: "# River", updated: "2026-10-30T00:00:00Z" });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.hostname !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}


// each page, and what shows it is drawn; a step after it opens, if any
const PAGES = [
  ["plugins", `#view-plugins .pm-card[data-pkg="${KILO}"] .pm-ver`],
  ["library", "#view-library .lib-body .row-head"],
  ["usage", "#subscriptionUsage .subscription-card .subscription-head .quota-more"],
  // the card's fold the other way: every account in full
  ["usage:open", "#subscriptionUsage .subscription-card .subscription-head .quota-more[aria-expanded=true]", async (page) => {
    await page.locator('#subscriptionUsage .subscription-card[data-provider="codex"] .subscription-head .quota-more').click();
  }],
  ["usage:requests", "#ledSpeed .led-spd-check", async (page) => {
    await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
    await page.locator("#ledSpeed .led-spd tbody tr").first().waitFor();
    await page.locator("#ledSpeed .led-spd-by .opt").nth(1).click();
  }],
  ["providers", "#view-providers"],
  ["settings", "#view-settings .row"],
];

// visitPages: each page in turn, drawn, settled, and then each(view)
async function visitPages(page, each) {
  await page.goto("http://magpie.test/");
  for (const [view, ready, then] of PAGES) {
    // a step on the page open already (usage:requests is Usage's second
    // tab): a second click on its sidebar item folds its tabs away
    if (!view.includes(":")) await page.locator(view === "settings" ? "#prefs" : `#nav button[data-view="${view}"]`).first().click();
    if (then) await then(page);
    await page.locator(ready).first().waitFor().catch((e) => { throw new Error(view + ": " + ready + " never drew: " + e.message); });
    await page.evaluate(() => document.fonts.ready);
    await page.mouse.move(0, 0);
    await page.waitForTimeout(50);
    await each(view);
  }
}
module.exports = { server, PAGES, visitPages };
