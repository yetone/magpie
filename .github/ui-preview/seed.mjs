// Gives the UI preview's sandbox (run.sh) data to show: a fresh home has one
// DeepSeek key and three requests, so a change to a chart, a list that pages,
// a subscription's quota or a relay's windows had nothing to draw (#937,
// #977, #1004, #1018, #1019…). Made up, every bit of it, and never real:
//
//   node seed.mjs write   — before magpie starts: providers and a group
//     beside DeepSeek, 30 days of requests (usage.jsonl), Claude Code and
//     Codex session files (more than a page of them), two ChatGPT accounts
//     signed in to Codex, the quota and balance history their charts and
//     forecasts are drawn from, and skills in the library and the agents;
//   node seed.mjs serve   — while magpie runs: the fake providers' API
//     (OpenAI and Anthropic shaped, each its own speed), a Sub2API relay's
//     /v1/usage, and, as an HTTPS proxy, chatgpt.com's account usage under a
//     certificate of our own (CA_CERT/CA_KEY, made by run.sh). Every other
//     host the proxy is asked for is tunnelled to as it is, so DeepSeek
//     answers for real.
//
// env: HOME, XDG_CONFIG_HOME, XDG_CACHE_HOME (as run.sh sets them); MOCK
// (host:port, default 127.0.0.1:3440); for serve, CA_DIR holding
// chatgpt.com's key.pem and cert.pem.
import fs from "node:fs";
import path from "node:path";
import http from "node:http";
import https from "node:https";
import net from "node:net";
import crypto from "node:crypto";

const HOME = process.env.HOME;
const CONFIG = path.join(process.env.XDG_CONFIG_HOME || path.join(HOME, ".config"), "magpie");
const MOCK = process.env.MOCK || "127.0.0.1:3440";
const NOW = Date.now();
const H = 3600e3, D = 24 * H;

// the same data every run, so two previews of one PR differ only by the PR
let seed = 20261006;
const rnd = () => ((seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648);
const pick = (a) => a[Math.floor(rnd() * a.length)];
const between = (lo, hi) => lo + rnd() * (hi - lo);
const int = (lo, hi) => Math.round(between(lo, hi));
const uuid = () => [8, 4, 4, 4, 12].map((n) => Array.from({ length: n }, () => "0123456789abcdef"[int(0, 15)]).join("")).join("-");
const iso = (ms) => new Date(ms).toISOString();
const write = (file, data) => {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, typeof data === "string" ? data : JSON.stringify(data, null, 2) + "\n", { mode: 0o600 });
};

// the ChatGPT accounts, their windows now: what chatgpt.com's mock answers
// and what their history ends at
const ACCOUNTS = [
  { user: "maya.chen@example.com", id: "acct-seed-maya", plan: "pro", five: 37, week: 64, credits: "1250", fiveLeft: 2.2 * H, weekLeft: 2.3 * D },
  { user: "dev.team@example.com", id: "acct-seed-team", plan: "plus", five: 91, week: 83, credits: null, fiveLeft: 0.8 * H, weekLeft: 4.6 * D },
];
const FIVE = 5 * H, WEEK = 7 * D;

// the relay's windows, in dollars: [limit, used]
const RELAY_LEFT = 287.4;
const RELAY = { "5h": [20, 13.4], "1d": [60, 41.7], "7d": [300, 118.2] };

// each model's own pace: output tokens a second and time to first token
const MODELS = {
  "deepseek/deepseek-flash": { tps: [55, 85], ttft: [300, 900], ep: "/v1/chat/completions" },
  "deepseek/deepseek-v4-pro": { tps: [25, 40], ttft: [900, 2600], ep: "/v1/chat/completions", reason: true },
  "relay/claude-sonnet-4-6": { tps: [60, 95], ttft: [700, 1800], ep: "/v1/messages", cache: true },
  "relay/claude-opus-4-7": { tps: [28, 45], ttft: [1200, 3200], ep: "/v1/messages", cache: true },
  "relay/gpt-5.5": { tps: [70, 120], ttft: [1500, 4200], ep: "/v1/responses", reason: true },
  "acme/gpt-5.5": { tps: [80, 140], ttft: [1100, 3600], ep: "/v1/responses", reason: true },
  "acme/gpt-5.4-mini": { tps: [150, 230], ttft: [250, 800], ep: "/v1/chat/completions" },
  "acme/gpt-5.3-codex": { tps: [35, 60], ttft: [500, 1500], ep: "/v1/chat/completions" },
};
// which agent asks which models, and in which API
const AGENTS = {
  claude: { models: ["relay/claude-sonnet-4-6", "relay/claude-opus-4-7", "deepseek/deepseek-v4-pro", "acme/gpt-5.3-codex"], ep: "/v1/messages", weight: 5 },
  codex: { models: ["acme/gpt-5.5", "relay/gpt-5.5", "codex/gpt-5.5"], ep: "/v1/responses", weight: 3 },
  opencode: { models: ["deepseek/deepseek-flash", "acme/gpt-5.4-mini", "acme/gpt-5.3-codex"], ep: "/v1/chat/completions", weight: 2 },
  gemini: { models: ["deepseek/deepseek-flash", "acme/gpt-5.4-mini"], ep: "/v1beta/models:streamGenerateContent", weight: 1 },
};
MODELS["codex/gpt-5.5"] = { tps: [65, 110], ttft: [1400, 3800], ep: "/backend-api/codex/responses", reason: true };

const PROMPTS = [
  "Fix the flaky login test", "Add pagination to the sessions list", "Why does the build fail on Windows?",
  "Refactor the config loader", "Write a migration for the orders table", "Explain this stack trace",
  "Make the chart readable in dark mode", "Speed up the search endpoint", "Add a README section on install",
  "Review my diff before I push", "Port the CLI flags to the new parser", "修一下设置页的布局", "给 API 加上重试",
  "把这个函数拆成两个", "解释一下这个报错",
];
const PROJECTS = ["/home/dev/work/shop-api", "/home/dev/work/web-app", "/home/dev/work/infra", "/home/dev/work/mobile", "/home/dev/oss/parser", "/home/dev/notes"];

function writeAll() {
  providers();
  const sessions = usage();
  claudeSessions(sessions.claude);
  codexSessions(sessions.codex);
  chatgpt();
  quotaHistory();
  balanceHistory();
  skills();
}

// providers beside the DeepSeek key run.sh added: two on this mock, a
// relay with Sub2API windows and a key of three, and a group over them
function providers() {
  const file = path.join(CONFIG, "providers.json");
  const cfg = fs.existsSync(file) ? JSON.parse(fs.readFileSync(file, "utf8")) : { providers: [] };
  const base = `http://${MOCK}`;
  cfg.providers = (cfg.providers || []).filter((p) => p.id !== "relay" && p.id !== "acme");
  cfg.providers.push(
    {
      id: "relay", name: "Sub2API Relay", key: "sk-seed-relay-0001", keyName: "main",
      keys: [{ name: "backup", key: "sk-seed-relay-0002" }, { name: "team", key: "sk-seed-relay-0003", off: true }],
      routing: "rotate", anthropic: base, chat: `${base}/v1`, responses: `${base}/v1`,
      balanceURL: `${base}/v1/usage`, models: ["claude-sonnet-4-6", "claude-opus-4-7", "gpt-5.5"], catalog: "anthropic",
    },
    {
      id: "acme", name: "Acme Cloud", key: "sk-seed-acme-0001", chat: `${base}/acme/v1`, responses: `${base}/acme/v1`,
      models: ["gpt-5.5", "gpt-5.4-mini", "gpt-5.3-codex"], catalog: "openai",
    },
  );
  cfg.groups = (cfg.groups || []).filter((g) => g.id !== "coding");
  cfg.groups.push({
    id: "coding", name: "Coding", routing: "order",
    members: ["relay/claude-sonnet-4-6", "acme/gpt-5.5", "deepseek/deepseek-v4-pro"],
    rules: [{ use: "deepseek/deepseek-flash", agents: ["opencode"] }],
  });
  write(file, cfg);
}

// 30 days of requests, in sessions of a few to a few dozen, busier on
// weekdays and in working hours, a few failing; the last of them today
function usage() {
  const out = [];
  const sessions = { claude: [], codex: [] };
  let route = 1790000000000;
  const weights = Object.entries(AGENTS).flatMap(([a, x]) => Array(x.weight).fill(a));
  for (let day = 29; day >= 0; day--) {
    const start = new Date(NOW - day * D);
    start.setUTCHours(0, 0, 0, 0);
    const weekend = [0, 6].includes(start.getUTCDay());
    const count = weekend ? int(1, 4) : int(5, 11);
    for (let s = 0; s < count; s++) {
      const agent = pick(weights);
      const a = AGENTS[agent];
      const model = pick(a.models);
      const id = agent === "codex" ? uuid().replace(/^(.{8})/, "01a0$1".slice(0, 8)) : uuid();
      let t = start.getTime() + between(1, 23) * H;
      if (t > NOW - 5 * 60e3) t = NOW - between(10, 300) * 60e3;
      const n = int(3, 36);
      const prompt = pick(PROMPTS);
      const cwd = pick(PROJECTS);
      const turns = [];
      for (let i = 0; i < n && t < NOW; i++) {
        const r = record(agent, model, id, t, route++);
        out.push(r);
        turns.push(r);
        t += r.ms + between(5, 180) * 1e3;
      }
      if (sessions[agent]) sessions[agent].push({ id, prompt, cwd, turns, model });
    }
  }
  out.sort((x, y) => x.t.localeCompare(y.t));
  write(path.join(CONFIG, "usage.jsonl"), out.map((r) => JSON.stringify(r)).join("\n") + "\n");
  return sessions;
}

function record(agent, req, session, t, route) {
  const m = MODELS[req];
  const [provider, model] = req.split("/");
  const out = int(80, 2400);
  const reasoning = m.reason ? int(0, out * 0.6) : 0;
  const ttft = int(...m.ttft);
  const ms = ttft + Math.round((out / between(...m.tps)) * 1000);
  const cached = m.cache || agent === "claude" ? int(8000, 90000) : agent === "codex" ? int(2000, 40000) : int(0, 3000);
  const r = {
    route_id: route, t: iso(t), agent, provider, model, req: req, served: model,
    in: int(600, 9000), out, cache_read: cached, ms, ttft_ms: ttft, status: 200,
    ep: AGENTS[agent].ep === m.ep ? m.ep : `${AGENTS[agent].ep} → ${m.ep}`,
    session, native_session: session,
  };
  if (m.cache) r.cache_write = int(0, 4000);
  if (reasoning) Object.assign(r, { reasoning, first_text_ms: ttft + int(300, 4000), effort: pick(["low", "medium", "high"]) });
  if (provider === "relay") r.providerKeyName = pick(["main", "main", "backup"]);
  if (provider === "codex") r.providerAccount = pick([ACCOUNTS[0].user, ACCOUNTS[0].user, ACCOUNTS[1].user]);
  const fail = rnd();
  if (fail < 0.025) Object.assign(r, { status: 429, err: "rate limited: retry after 20s", err_type: "rate_limit_error", out: 0, ms: int(200, 900) });
  else if (fail < 0.035) Object.assign(r, { status: 502, err: "upstream connection reset", err_type: "server_error", out: 0, ms: int(2000, 9000) });
  if (r.out === 0) for (const k of ["reasoning", "first_text_ms", "ttft_ms", "cache_read", "cache_write"]) delete r[k];
  return r;
}

// Claude Code's sessions: those the log has, and more besides, run without
// magpie, so there are more than the 200 the list shows at first
function claudeSessions(fromLog) {
  const extra = [];
  for (let i = 0; i < 230; i++) {
    const t = NOW - between(0.2, 29) * D;
    extra.push({ id: uuid(), prompt: pick(PROMPTS), cwd: pick(PROJECTS), model: pick(["claude-sonnet-4-6", "claude-opus-4-7", "claude-haiku-4-5"]), turns: Array.from({ length: int(2, 14) }, (_, k) => ({ t: iso(t + k * 90e3) })) });
  }
  for (const s of [...fromLog, ...extra]) {
    const slug = s.cwd.replace(/[/.]/g, "-");
    const lines = [];
    const model = s.model.split("/").pop().startsWith("claude") ? s.model.split("/").pop() : "claude-sonnet-4-6";
    s.turns.forEach((r, i) => {
      const at = Date.parse(r.t);
      if (i === 0) lines.push({ type: "user", message: { role: "user", content: s.prompt }, timestamp: iso(at - 1000), cwd: s.cwd, sessionId: s.id });
      lines.push({
        parentUuid: null, isSidechain: false,
        message: {
          model, id: `msg_seed_${i}`, type: "message", role: "assistant", content: [{ type: "text", text: "Done." }],
          usage: { input_tokens: r.in ?? int(400, 6000), cache_creation_input_tokens: r.cache_write ?? int(0, 3000), cache_read_input_tokens: r.cache_read ?? int(5000, 80000), output_tokens: r.out ?? int(60, 1800) },
        },
        requestId: `req_seed_${i}`, type: "assistant", timestamp: iso(at), cwd: s.cwd, sessionId: s.id,
      });
    });
    lines.push({ type: "ai-title", aiTitle: s.prompt, sessionId: s.id });
    write(path.join(HOME, ".claude", "projects", slug, `${s.id}.jsonl`), lines.map((l) => JSON.stringify(l)).join("\n") + "\n");
  }
}

function codexSessions(fromLog) {
  const extra = [];
  for (let i = 0; i < 40; i++) {
    const t = NOW - between(0.2, 29) * D;
    extra.push({ id: uuid(), prompt: pick(PROMPTS), cwd: pick(PROJECTS), turns: Array.from({ length: int(2, 10) }, (_, k) => ({ t: iso(t + k * 120e3) })) });
  }
  const titles = [];
  for (const s of [...fromLog, ...extra]) {
    const t0 = new Date(Date.parse(s.turns[0].t) - 2000);
    const stamp = t0.toISOString().slice(0, 19).replace(/:/g, "-");
    const dir = path.join(HOME, ".codex", "sessions", stamp.slice(0, 4), stamp.slice(5, 7), stamp.slice(8, 10));
    const total = { input_tokens: 0, cached_input_tokens: 0, output_tokens: 0, reasoning_output_tokens: 0, total_tokens: 0 };
    const lines = [
      { timestamp: iso(t0), type: "session_meta", payload: { id: s.id, cwd: s.cwd, model_provider: "openai" } },
      { timestamp: iso(t0), type: "response_item", payload: { type: "message", role: "user", content: [{ type: "input_text", text: s.prompt }] } },
    ];
    for (const r of s.turns) {
      const last = { input_tokens: (r.in ?? int(800, 6000)) + (r.cache_read ?? int(2000, 30000)), cached_input_tokens: r.cache_read ?? int(2000, 30000), output_tokens: r.out ?? int(80, 1500), reasoning_output_tokens: r.reasoning ?? int(0, 400) };
      last.total_tokens = last.input_tokens + last.output_tokens;
      for (const k in total) total[k] += last[k];
      lines.push({ timestamp: r.t, type: "turn_context", payload: { cwd: s.cwd, model: (r.model && !r.model.includes("/") && r.model) || "gpt-5.5" } });
      lines.push({ timestamp: r.t, type: "event_msg", payload: { type: "token_count", info: { total_token_usage: { ...total }, last_token_usage: last } } });
    }
    titles.push({ id: s.id, thread_name: s.prompt });
    write(path.join(dir, `rollout-${stamp}-${s.id}.jsonl`), lines.map((l) => JSON.stringify(l)).join("\n") + "\n");
  }
  write(path.join(HOME, ".codex", "session_index.jsonl"), titles.map((l) => JSON.stringify(l)).join("\n") + "\n");
}

// two ChatGPT accounts: the one Codex is signed in to (auth.json), and one
// saved in magpie beside it, both with tokens that are no JWT, so magpie
// never refreshes them, only asks chatgpt.com (this mock) for their usage
function chatgpt() {
  const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
  const auth = (a) => ({
    auth_mode: "chatgpt", OPENAI_API_KEY: null, last_refresh: iso(NOW - H),
    tokens: {
      id_token: `${b64({ alg: "none" })}.${b64({ email: a.user, "https://api.openai.com/auth": { chatgpt_plan_type: a.plan, chatgpt_account_id: a.id, chatgpt_subscription_active_until: iso(NOW + 19 * D) } })}.seed`,
      access_token: `seed-access-${a.id}`, refresh_token: `seed-refresh-${a.id}`, account_id: a.id,
    },
  });
  write(path.join(HOME, ".codex", "auth.json"), auth(ACCOUNTS[0]));
  write(path.join(CONFIG, "logins.json"), ACCOUNTS.map((a, i) => ({ agent: "codex", user: a.user, plan: a.plan, seen: iso(NOW - H), on: i === 1, auth: auth(a) })));
}

// readings every 20 minutes of the working day for 35 days: each window
// used up a little at a time and starting over at its reset, ending at
// what the mock says now
function quotaHistory() {
  const hist = {};
  for (const a of ACCOUNTS) {
    hist[`codex|${a.user}`] = {
      "5 hours": series(FIVE, a.fiveLeft, a.five),
      "7 days": series(WEEK, a.weekLeft, a.week),
    };
  }
  write(path.join(CONFIG, "quota-history.json"), hist);
}

function series(span, leftNow, usedNow) {
  const reset = NOW + leftNow;
  const points = [];
  const used = {}; // how far each earlier window got, by its index back from now
  for (let t = NOW - 35 * D; t <= NOW; t += 20 * 60e3) {
    const hour = new Date(t).getUTCHours();
    if ((hour < 1 || hour > 15) && t < NOW - H) continue; // asleep: no readings
    const k = Math.ceil((reset - t) / span);
    const end = reset - (k - 1) * span, start = end - span;
    const through = (t - start) / span;
    const cycleUse = k === 1 ? usedNow / Math.max(0.05, (NOW - start) / span) : (used[k] ??= between(45, 105));
    const u = Math.min(100, Math.max(0, cycleUse * through + between(-1, 1)));
    points.push({ at: iso(t), left: Math.round((100 - u) * 10) / 10, start: iso(start), resetsAt: iso(end) });
  }
  points[points.length - 1].left = 100 - usedNow;
  return points;
}

// the relay's balance read twice a day, spent unevenly, topped up once,
// down to what its usage query says now (RELAY_LEFT)
function balanceHistory() {
  const pts = [];
  let amount = RELAY_LEFT;
  for (let t = NOW; t >= NOW - 30 * D; t -= 12 * H) {
    pts.unshift({ at: iso(t), amount: Math.round(amount * 100) / 100 });
    amount += between(2, 14);
    if (Math.abs(t - (NOW - 12 * D)) < 6 * H) amount -= 300;
  }
  write(path.join(CONFIG, "balance-history.json"), { "relay|main": pts });
}

// skills: some in the library, installed from two authors' repositories
// on GitHub, and some only in Claude Code's and Codex's own folders, which
// the Skills tab offers to bring in
function skills() {
  const md = (name, about) => `---\nname: ${name}\ndescription: ${about}\n---\n\n# ${name}\n\n${about}.\n`;
  const lib = [
    ["pdf", "anthropics/skills", "Read, fill and merge PDF files"],
    ["docx", "anthropics/skills", "Create and edit Word documents"],
    ["webapp-testing", "anthropics/skills", "Test a local web app with Playwright"],
    ["frontend-design", "vercel-labs/agent-skills", "Build polished front-end interfaces"],
    ["react-best-practices", "vercel-labs/agent-skills", "Review React code for common mistakes"],
    ["skill-creator", "anthropics/claude-plugins", "Write a new skill"],
    ["team-style", null, "Our team's code style"],
  ];
  for (const [name, , about] of lib) write(path.join(CONFIG, "library", "skills", name, "SKILL.md"), md(name, about));
  const file = path.join(CONFIG, "library.json");
  const l = fs.existsSync(file) ? JSON.parse(fs.readFileSync(file, "utf8")) : {};
  // one author with two repositories, and one skill from a folder here
  l.skills = lib.map(([name, repo]) => ({
    name, agents: ["claude", "codex"],
    source: repo ? { kind: "github", repo, ref: "main", path: `skills/${name}` } : { kind: "folder", dir: path.join(HOME, "work", "skills", name) },
  }));
  write(path.join(HOME, "work", "skills", "team-style", "SKILL.md"), md("team-style", "Our team's code style"));
  write(file, l);
  for (const [agent, name, about] of [
    [".claude", "db-migrate", "Write and check database migrations"],
    [".claude", "release-notes", "Draft release notes from merged PRs"],
    [".codex", "changelog", "Keep CHANGELOG.md in step with the code"],
  ]) write(path.join(HOME, agent, "skills", name, "SKILL.md"), md(name, about));
}

// ---- serve ----

function serve() {
  const [host, port] = MOCK.split(":");
  const tls = https.createServer({ key: fs.readFileSync(path.join(process.env.CA_DIR, "key.pem")), cert: fs.readFileSync(path.join(process.env.CA_DIR, "cert.pem")) }, chatgptCom);
  const server = http.createServer(api);
  // an HTTPS proxy: chatgpt.com is ours, everything else goes where it asks
  server.on("connect", (req, socket, head) => {
    const [h, p] = req.url.split(":");
    if (h === "chatgpt.com" || h === "auth.openai.com") {
      socket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
      tls.emit("connection", socket);
      if (head.length) socket.unshift(head);
      return;
    }
    const up = net.connect(Number(p) || 443, h, () => {
      socket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
      if (head.length) up.write(head);
      up.pipe(socket);
      socket.pipe(up);
    });
    up.on("error", () => socket.destroy());
    socket.on("error", () => up.destroy());
  });
  server.listen(Number(port), host, () => console.log(`seed mock on ${MOCK}`));
}

function json(res, status, body) {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify(body));
}

function chatgptCom(req, res) {
  const u = new URL(req.url, "https://chatgpt.com");
  const a = ACCOUNTS.find((x) => x.id === req.headers["chatgpt-account-id"]) || ACCOUNTS[0];
  if (u.pathname === "/backend-api/wham/usage") {
    const win = (used, secs, left) => ({ used_percent: used, limit_window_seconds: secs, reset_after_seconds: Math.round(left / 1000), reset_at: Math.round((Date.now() + left) / 1000) });
    return json(res, 200, {
      plan_type: a.plan,
      credits: a.credits ? { has_credits: true, unlimited: false, balance: a.credits } : { has_credits: false, unlimited: false, balance: "0" },
      rate_limit: { allowed: true, limit_reached: false, primary_window: win(a.five, FIVE / 1000, a.fiveLeft), secondary_window: win(a.week, WEEK / 1000, a.weekLeft) },
    });
  }
  if (u.pathname === "/backend-api/wham/rate-limit-reset-credits") return json(res, 200, { available_count: 0, credits: [] });
  if (u.pathname === "/backend-api/codex/models") {
    const levels = ["low", "medium", "high", "xhigh"].map((effort) => ({ effort }));
    const model = (slug, display_name, priority) => ({ slug, display_name, priority, visibility: "list", input_modalities: ["text", "image"], supported_reasoning_levels: levels, context_window: 272000 });
    return json(res, 200, { models: [model("gpt-5.5", "GPT-5.5", 1), model("gpt-5.4", "GPT-5.4", 2), model("gpt-5.3-codex", "GPT-5.3-Codex", 3), model("gpt-5.4-mini", "GPT-5.4-Mini", 4)] });
  }
  // no Codex request is answered: nothing in the preview sends one
  json(res, 404, { detail: "not in the UI preview's mock" });
}

// the fake providers: the relay at the root (a Sub2API usage query is
// /v1/usage exactly) and Acme under /acme, each model at its own pace
function api(req, res) {
  const u = new URL(req.url, `http://${MOCK}`);
  const acme = u.pathname.startsWith("/acme/");
  const prov = acme ? "acme" : "relay";
  const p = acme ? u.pathname.slice(5) : u.pathname;
  if (prov === "relay" && p === "/v1/usage") {
    const reset = { "5h": 3.1 * H, "1d": 9 * H, "7d": 3.4 * D }, span = { "5h": FIVE, "1d": D, "7d": WEEK };
    const rate_limits = Object.entries(RELAY).map(([window, [limit, used]]) => ({
      window, limit, used, remaining: Math.round((limit - used) * 100) / 100,
      window_start: iso(Date.now() + reset[window] - span[window]), reset_at: iso(Date.now() + reset[window]),
    }));
    return json(res, 200, { remaining: RELAY_LEFT, rate_limits });
  }
  if (req.method === "GET" && p.endsWith("/models")) {
    const ids = prov === "relay" ? ["claude-sonnet-4-6", "claude-opus-4-7", "gpt-5.5"] : ["gpt-5.5", "gpt-5.4-mini", "gpt-5.3-codex"];
    return json(res, 200, { object: "list", data: ids.map((id) => ({ id, object: "model", owned_by: prov })) });
  }
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    let q = {};
    try { q = JSON.parse(body); } catch {}
    const m = MODELS[`${prov}/${q.model}`] || { tps: [60, 90], ttft: [400, 900] };
    const text = "This is the UI preview's made-up provider answering. Nothing here came from a real model.";
    const words = text.split(" ");
    const usage = { in: 120 + body.length / 4 | 0, out: words.length * 2 };
    const delay = between(...m.ttft) / 4, step = 1000 / between(...m.tps);
    if (p === "/v1/messages") return anthropic(res, q, words, usage, delay, step);
    if (p === "/v1/chat/completions") return chat(res, q, words, usage, delay, step);
    if (p === "/v1/responses") return responses(res, q, words, usage, delay, step);
    json(res, 404, { error: { message: `no ${p} here` } });
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function stream(res, delay, step, words, each, head, tail) {
  res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
  await sleep(delay);
  for (const e of head) res.write(e);
  for (const w of words) { res.write(each(w + " ")); await sleep(step); }
  for (const e of tail) res.write(e);
  res.end();
}
const sse = (event, data) => (event ? `event: ${event}\n` : "") + `data: ${JSON.stringify(data)}\n\n`;

function anthropic(res, q, words, u, delay, step) {
  const id = "msg_seed_" + crypto.randomBytes(6).toString("hex");
  const usage = { input_tokens: u.in, output_tokens: u.out, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 };
  if (!q.stream) return json(res, 200, { id, type: "message", role: "assistant", model: q.model, content: [{ type: "text", text: words.join(" ") }], stop_reason: "end_turn", usage });
  stream(res, delay, step, words, (w) => sse("content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: w } }),
    [sse("message_start", { type: "message_start", message: { id, type: "message", role: "assistant", model: q.model, content: [], usage: { ...usage, output_tokens: 1 } } }),
      sse("content_block_start", { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } })],
    [sse("content_block_stop", { type: "content_block_stop", index: 0 }),
      sse("message_delta", { type: "message_delta", delta: { stop_reason: "end_turn" }, usage: { output_tokens: u.out } }),
      sse("message_stop", { type: "message_stop" })]);
}

function chat(res, q, words, u, delay, step) {
  const id = "chatcmpl-seed" + crypto.randomBytes(6).toString("hex"), created = Math.floor(Date.now() / 1000);
  const usage = { prompt_tokens: u.in, completion_tokens: u.out, total_tokens: u.in + u.out };
  if (!q.stream) return json(res, 200, { id, object: "chat.completion", created, model: q.model, choices: [{ index: 0, message: { role: "assistant", content: words.join(" ") }, finish_reason: "stop" }], usage });
  const chunk = (delta, finish = null, extra = {}) => sse("", { id, object: "chat.completion.chunk", created, model: q.model, choices: [{ index: 0, delta, finish_reason: finish }], ...extra });
  stream(res, delay, step, words, (w) => chunk({ content: w }), [chunk({ role: "assistant", content: "" })],
    [chunk({}, "stop"), sse("", { id, object: "chat.completion.chunk", created, model: q.model, choices: [], usage }), "data: [DONE]\n\n"]);
}

function responses(res, q, words, u, delay, step) {
  const id = "resp_seed" + crypto.randomBytes(6).toString("hex");
  const item = { id: "msg_" + id, type: "message", role: "assistant", status: "completed", content: [{ type: "output_text", text: words.join(" "), annotations: [] }] };
  const done = { id, object: "response", status: "completed", model: q.model, output: [item], usage: { input_tokens: u.in, output_tokens: u.out, total_tokens: u.in + u.out } };
  if (!q.stream) return json(res, 200, done);
  stream(res, delay, step, words, (w) => sse("response.output_text.delta", { type: "response.output_text.delta", item_id: item.id, output_index: 0, content_index: 0, delta: w }),
    [sse("response.created", { type: "response.created", response: { ...done, status: "in_progress", output: [] } }),
      sse("response.output_item.added", { type: "response.output_item.added", output_index: 0, item: { ...item, status: "in_progress", content: [] } })],
    [sse("response.output_item.done", { type: "response.output_item.done", output_index: 0, item }),
      sse("response.completed", { type: "response.completed", response: done })]);
}

const cmd = process.argv[2];
if (cmd === "write") writeAll();
else if (cmd === "serve") serve();
else {
  console.error("usage: seed.mjs write|serve");
  process.exit(2);
}
