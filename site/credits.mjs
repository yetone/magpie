// Who magpie's releases came from: the people a commit credits, read from
// what is already public in yetone/magpie's history. Nothing here is made up
// and no email is ever given out:
//
//   - a subject's parenthesised credit: "(hoobnn on Discord)",
//     "(qikamatata on X: …)", "(#2356, detail47)", "(willz, Discord)",
//     "(TJHHHH, luci on Discord)", a lone "(mrwl666)";
//   - the author of an issue or PR such a group names, "(#4532)" (looked up
//     by credits-cli.mjs; a lookup that fails names nobody);
//   - a commit's GitHub author and Co-authored-by names other than the
//     maintainer's and bots';
//   - the reporter a body's sentence names: "Reported on X by
//     @niyezaimenglima: …", "Reported by 教父 on Discord.", "Requested by
//     ssharpcc on X.", "…; reported by FiendSaws.", "Reported by OldSuns,
//     ggggggggggz and track23; …", and a Reported-by: trailer
//     ("Reported-by: gua2100 (Discord)"). Only the names right after the
//     verb count: not who diagnosed or patched it, not an alias in
//     brackets, not a name anywhere else in the body.
//
// An X credit is the GitHub person's when that GitHub user's public profile
// names the X account (its twitter_username or an X link among its social
// accounts, which the user sets themselves; looked up by credits-cli.mjs). It goes under the GitHub
// login, via X and GitHub. Nothing else joins two names: never a similar
// spelling, never an email.
//
// A lone word in brackets is often code ("(nogui)", "(P3)", "(Codex)"), so it
// counts only when it is shaped like a handle with digits ("mrwl666") or the
// same name is credited plainly somewhere else ("axelburks on Discord").
//
// Used by credits-cli.mjs (the release notes' thanks line, and
// public/contributors.json at deploy) and by worker.js (the contributors
// page). It imports nothing, so the worker can bundle it.

export const MAINTAINERS = ["yetone"];
// agents and bots that author or co-author commits
const BOT = /\[bot\]$|^(claude( code| opus.*| sonnet.*| haiku.*)?|amp|codex|copilot|github copilot|cursor( agent)?|devin( ai)?|gemini|opencode|droid|factory-droid|dependabot|github-actions|renovate|jev)$/i;
// agents and machine accounts that co-author commits: "Instinct Agent
// <agent@instinct.com>", "glm-5.3-flash", "magpie-fix <fix@localhost>"
const AGENT_MAIL = /@(anthropic\.com|cursor\.com|openai\.com|instinct\.com|multica\.ai|factory\.ai|ampcode\.com|google\.com|localhost)$|^(noreply|agent|bot)@/i;
const MODEL = /^(claude|gpt|glm|qwen|kimi|deepseek|gemini|grok|minimax|doubao|o\d)\b.*\d|\bagent$|^(admin|root|user|ubuntu|runner|test)$/i;
const PLATFORMS = { discord: "Discord", x: "X", twitter: "X", github: "GitHub", v2ex: "V2EX", wechat: "WeChat", 微信: "WeChat", telegram: "Telegram", reddit: "Reddit", "linux.do": "LINUX DO", 即刻: "Jike", 小红书: "Xiaohongshu" };
// words that sit in brackets beside an issue number and aren't people
const STOP = new Set(("discord x github zh en ja de zh-tw zh-cn high low medium ultra xhigh max min mini pro plus free " +
  "model models folder download review fixes fixed see also and or not windows linux macos mac wsl ios android ci " +
  "main red green beta alpha stable new old test tests api cli gui tui web site docs owner user users the pi spelled " +
  "plugin plugins provider providers agent agents gateway routing usage settings panel tray").split(" "));
const LOGIN = /^[A-Za-z0-9](?:[A-Za-z0-9]|-(?=[A-Za-z0-9])){2,38}$/;
// a Discord-style handle ending in its number: tjhhhh_84378
const NUMBERED = /^_?[A-Za-z][A-Za-z0-9]*(?:_[A-Za-z0-9]+)*_\d{3,}$/;
// a lone bracketed word taken without anything else naming it: mrwl666
const DIGITED = /^[A-Za-z]{3,}\d{2,}$/;

const lower = (s) => s.toLowerCase();

// a word next to an issue number or another credit that may be a person
function handle(w) {
  if (STOP.has(lower(w))) return false;
  if (/^\d+$/.test(w) || /^[0-9a-f]{7,40}$/.test(w) || /^v?\d+(\.\d+)+/.test(w)) return false;
  return LOGIN.test(w) || NUMBERED.test(w);
}

// someone named in front of a platform, "Benjie Zhou on Discord"
// a bracket written in quotes is the text quoted, not who it credits: a
// subject about the credits' own forms, "(@A, @B on X)"
const QUOTES = { '"': '"', "“": "”", "'": "'", "`": "`", "「": "」" };
function quoted(text, m) {
  const open = text[m.index - 1], close = text[m.index + m[0].length];
  return Object.hasOwn(QUOTES, open) && QUOTES[open] === close;
}

function personName(s) {
  s = s.trim().replace(/^@/, "");
  if (!s || s.length > 40 || /[#()\[\]{}<>"“”]/.test(s) || s.split(/\s+/).length > 3) return "";
  if (/^(the|a|an)\s/i.test(s) || STOP.has(lower(s))) return "";
  // who someone is, not their name: "Docker user", "𝕏 user", "𝕏"
  if (/(^|\s)(user|users|someone|reporter|people)$/i.test(s) || !/[\p{L}\p{N}]/u.test(s.replace(/𝕏/gu, ""))) return "";
  return s;
}

// A sentence in a commit's body that says who reported or asked for it.
// It starts a paragraph or follows a sentence's end or a semicolon:
// "a model asked by its bare id" in the middle of a sentence is no credit.
// "Asked for by jwaitq on Discord.", "Thanks to Milktea on Discord for the
// report.", "Credit: axelburks on Discord." say it too.
const BODY_BY = /(?:^|(?<=[.;!?]\s))(?:[-*]\s+)?(?:(?:reported|requested|suggested|asked)(?:\s+for)?(?:\s+on\s+([^\s,.;:()]+))?\s+by|thanks\s+to|credit:)\s+/gimu;
// a body name with no platform after it: one word, never an English one
const BODY_NAME = /^[\p{L}\p{N}][\p{L}\p{N}_.\-]{0,38}$/u;
const NOT_NAME = new Set("default hand name id itself himself herself themselves design mistake email mail them him her this that it".split(" "));

// A paragraph's opener: who, " on <platform>", a channel or a bracket, then
// a colon, a comma or the verb of what they said or saw.
const OPENER = /^(?:[-*]\s+)?([^:]{1,120}?)\s+on\s+([^\s:,.()#]+)(?:\s+#[\w-]+)?(?:\s+\(([^()]*)\))?(:\s|,\s|\s(?=\p{Ll}))/u;
// "NAME (Discord #feedback, 11-27): …": a bracket whose first part is where
const OPENER_BRACKET = /^(?:[-*]\s+)?@?([^\s:()]{1,40})\s+\(([^()]*)\)(:\s|,\s|\s(?=\p{Ll}))/u;
// what opens a paragraph in a name's place: "Reported on X by @…" (read
// above), "Reports on X under the masking demo:"
const OPENER_VERB = /^(?:reported|reports|requested|suggested|asked|posted|seen|said|replies|feedback|thanks)$/i;

function bodyName(s, placed) {
  s = s.trim().replace(/^@/, "");
  // "4 v5 on Discord": a platform after it lets a name have spaces
  if (placed) return personName(s);
  if (!BODY_NAME.test(s) || STOP.has(lower(s)) || NOT_NAME.has(lower(s))) return "";
  if (/^\d{1,5}$/.test(s) || /^v?\d+(\.\d+)+/.test(s)) return "";
  return s;
}

// "(Discord)", "(on Discord)", "(Discord, issue #4420)", "(Discord
// #feedback, …)": where. Any other bracket ("gua2100 (ggbdpq)", "(#5390)",
// "(GitHub MonkeyLeeT)", "(dawei on Discord)") names no platform.
function bracketPlatform(s) {
  const m = String(s).trim().match(/^(?:on\s+)?([^\s,;#]+)(?:$|,|\s+#)/);
  return m ? PLATFORMS[lower(m[1])] || null : null;
}

// One name in a Reported-by: trailer: "linkaww on Discord", "小李
// (Discord)", "Mmx233 <36563672+Mmx233@users.noreply.github.com>",
// "zane668 (#880)". A bracket is where, an issue or an alias, never a
// second person.
function trailerName(part) {
  const m = part.trim().match(/^(.+?)(?:\s*<([^<>]*)>)?(?:\s*\(([^()]*)\))?(?:\s+on\s+(\S+))?$/);
  if (!m) return null;
  const on = m[4] ? PLATFORMS[lower(m[4])] || null : null;
  if (m[4] && !on) return null;
  if (m[2] && AGENT_MAIL.test(m[2]) && !noreply(m[2])) return null;
  // the trailer holds a name and nothing else, so "辰林鸭~" is one too
  const name = bodyName(m[1], true);
  if (!name || STOP.has(lower(name)) || NOT_NAME.has(lower(name)) || /^\d{1,5}$/.test(name)) return null;
  const github = noreply(m[2]);
  return { name, via: on || (m[3] ? bracketPlatform(m[3]) : null) || (github ? "GitHub" : null), sure: true, github };
}

// The reporters a commit's body names (and an older subject's text after
// " — "), each { name, via, sure: true, github? }.
export function parseBody(text) {
  const out = [];
  // "Reported-by: isjiajia01, sounfury": a comma in a bracket is the bracket's
  for (const m of String(text || "").matchAll(/^[ \t]*(?:reported|requested|suggested)-by:[ \t]*(.+)$/gim)) {
    for (const part of m[1].split(/,\s*(?![^()]*\))/)) {
      const n = trailerName(part);
      if (n) out.push(n);
    }
  }
  // a body's lines are wrapped, so a sentence runs on across them
  const flat = String(text || "").split(/\n\s*\n/).map((p) => p.replace(/\s*\n\s*/g, " ").trim()).join("\n");
  for (const m of flat.matchAll(BODY_BY)) {
    const lead = m[1] ? PLATFORMS[lower(m[1])] || null : null; // "Reported on X by @…"
    const rest = flat.slice(m.index + m[0].length).split("\n")[0];
    // the names end at the sentence's end, a colon or semicolon, a bracket
    // or a dash; "ylorn. on Discord" is a name with its dot
    const end = rest.search(/[.;:!?](?=\s|$)(?!\s+on\s+\S)|\s\(|\s[—–-]\s/);
    let clause = end >= 0 ? rest.slice(0, end) : rest;
    const paren = end >= 0 && rest.slice(end).match(/^\s\(([^()]*)\)/);
    const tail = paren ? bracketPlatform(paren[1]) : null;
    // "EncodeTS in #7958", "yetone on #736", "1939323749 with a custom provider …"
    clause = clause.replace(/\s+(?:in|on)\s+[\w./-]*#\d+.*$/, "").replace(/\s+(?:with|for)\s.*$/, "");
    const got = [];
    // a list ends at the first part that isn't a name: "willywang,
    // confirmed again …", "ChangQiu on Discord and filed as #4853 by …"
    for (let part of clause.split(/,\s*|\s+(?:and|&)\s+/)) {
      part = part.trim();
      let via = null;
      const on = part.match(/^(.+?)\s+on\s+(\S+)$/);
      if (on && PLATFORMS[lower(on[2])]) { part = on[1]; via = PLATFORMS[lower(on[2])]; }
      const name = bodyName(part, !!via);
      if (!name) break;
      // "A and B on Discord": both are on Discord
      if (via) for (const g of got) if (!g.via) g.via = via;
      got.push({ name, via });
    }
    for (const g of got) out.push({ name: g.name, via: g.via || lead || tail || null, sure: true });
  }
  for (const p of flat.split("\n")) {
    // a paragraph that opens with who said it: "ZhouYimingsjtu on X: with
    // an Ultra 20x plan, …", "zzstar101 and m0eaki on X: …"; a channel or
    // a bracket may sit before the colon, "buhaodeshiyehao on X (Codex
    // through Lody's ACP): …", "PaulT on Discord #feedback: …"
    const b = p.match(OPENER_BRACKET);
    const bv = b && b[2].split(",")[0].trim().match(/^(?:(?:on\s+)?([^\s#]+)(?:\s+(?:#[\w-]+|DM))?|#[\w-]+\s+on\s+(\S+))$/i);
    const bvia = bv && PLATFORMS[lower(bv[1] || bv[2])];
    if (bvia) {
      // "BigFish (Discord #feedback, 11-27): …", "Passings_z (X) reached …"
      const name = !OPENER_VERB.test(b[1]) && (b[3][0] === ":" ? personName(b[1]) : bodyName(b[1], false));
      if (name) out.push({ name, via: bvia, sure: true });
      continue;
    }
    const m = p.match(OPENER);
    const via = m && PLATFORMS[lower(m[2])];
    if (!via) continue;
    // "Ported for nylesssssssss on X:", "ZeroClover asked on X:"
    const who = m[1].replace(/^(?:reported|requested|suggested|asked|ported)(?:\s+for)?\s+(?:by|for)\s+/i, "").replace(/\s+asked$/i, "");
    // "Asked on X (@xuanbo1889245): …": the bracket's handle said it
    if (OPENER_VERB.test(who)) {
      const h = m[3] && m[3].trim().match(/^@([\w.-]+)$/);
      if (h) out.push({ name: h[1], via, sure: true });
      continue;
    }
    const got = [];
    for (let part of who.split(/,\s*|\s+(?:and|&)\s+/)) {
      // "elis_pina (LoveEcho1145) on X:": a bracket after a name is an
      // alias or a detail, never a second person; "hatanokokoroyo (#6828)
      // and Nietzsche_Li on X:" names an issue's author, on GitHub
      const issue = /\(#\d+[^()]*\)/.test(part);
      part = part.replace(/\s*\([^()]*\)/g, "");
      // with no colon, "jinpwng on X (v0.1.4599) sets up three machines", a
      // name is one word: "Thanks to Milktea on Discord for …" is no opener
      const name = m[4][0] === ":" ? personName(part) : bodyName(part, false);
      if (!name) { got.length = 0; break; }
      got.push({ name, via: issue ? "GitHub" : via, sure: true });
    }
    // "Reported by gua2100 (ggbdpq) on GitHub, #2197": BODY_BY read the
    // name above, and the opener says where
    for (const g of got) {
      const had = out.find((o) => o.name === g.name && (!o.via || o.via === g.via));
      if (had) had.via = g.via; else out.push(g);
    }
  }
  // a bracket of handles and nothing else: "(@ZhouYimingsjtu, @Lmohn39 on X)"
  for (const m of flat.matchAll(/\((@[\w.-]+(?:(?:,\s*|\s+(?:and|&)\s+)@[\w.-]+)*)\s+on\s+([^\s()]+)\)/gu)) {
    if (quoted(flat, m)) continue;
    const via = PLATFORMS[lower(m[2])];
    if (via) for (const n of m[1].split(/,\s*|\s+(?:and|&)\s+/)) out.push({ name: n.slice(1), via, sure: true });
  }
  return out;
}

// The credits in one subject: names (sure, or to be taken only if seen
// elsewhere) and the issues or PRs its brackets name.
export function parse(subject) {
  // older subjects carry their whole body after " — "; credits come before
  const [head, ...rest] = String(subject).split(" — ");
  const out = { names: [], refs: [] };
  for (const m of head.matchAll(/\(([^()]*)\)/g)) if (!quoted(head, m)) group(m[1], out);
  // but a bracket there that says who reported it is one: "(reported by
  // @duanckham on X)", "(spotted by @hezhizhen in #103)"
  const tail = rest.join(" — ");
  for (const m of tail.matchAll(/\(((?:reported|requested|suggested|asked|spotted)\s+by\s+@[^()]*)\)/gi)) if (!quoted(tail, m)) group(m[1], out);
  return out;
}

function group(text, out) {
  const bare = [];
  let parts = 0;
  for (let piece of text.split(/;\s*/)) {
    // a quote after the name: "NAME on X: 打开页面很卡", "#538: …"
    const colon = piece.search(/:(\s|["“「]|$)/);
    if (colon >= 0) piece = piece.slice(0, colon);
    // a piece names someone only when its own words do: "(#615: …; Neon,
    // Notion and Supabase stay)" doesn't
    let context = false;
    const mine = [];
    for (let p of piece.split(/,\s*/)) {
      p = p.trim();
      if (!p) continue;
      parts++;
      // "spotted by @hezhizhen in #103"; "reported by @duanckham on X" is
      // an X account
      const at = p.match(/(?:^|\s)by @([\w-]{1,39})\b(?:\s+on\s+([^\s,;]+))?/);
      const atVia = at && at[2] ? PLATFORMS[lower(at[2])] || null : null;
      const by = at && (atVia || /^[A-Za-z0-9-]+$/.test(at[1])) ? at : null;
      if (by) { out.names.push({ name: by[1], via: atVia || "GitHub", sure: true }); context = true; }
      // "#12", "plugins#12", "plugins #12", "magpie-community/plugins#12"
      p = p.replace(/(?:\b(plugins)\s+|([\w.-]+\/)?([\w.-]+))?#(\d+)\b/g, (_, spaced, owner, repo, n) => {
        const r = spaced || (!repo ? "yetone/magpie" : `${owner || ""}${repo}`);
        const known = { "yetone/magpie": "yetone/magpie", magpie: "yetone/magpie", plugins: "magpie-community/plugins", "magpie-community/plugins": "magpie-community/plugins" }[r];
        if (known) out.refs.push({ repo: known, n: +n });
        context = true;
        return " ";
      }).trim();
      if (!p || by) continue;
      const plat = PLATFORMS[lower(p)];
      if (plat) { // "willz, Discord"
        if (mine.length) { out.names.push({ name: mine.pop(), via: plat, sure: true }); context = true; }
        continue;
      }
      const on = p.match(/^(.+?)\s+on\s+(\S+)$/);
      if (on && PLATFORMS[lower(on[2])]) {
        for (const n of on[1].split(/\s+(?:and|&)\s+/)) {
          const name = personName(n);
          if (name) out.names.push({ name, via: PLATFORMS[lower(on[2])], sure: true });
        }
        context = true;
        continue;
      }
      if (/^@?\S+$/.test(p)) mine.push(p.replace(/^@/, ""));
    }
    for (const b of mine) bare.push({ b, context });
  }
  // a lone word, or one beside a credit or a number ("July, LCorleone",
  // "memory, Osal on Discord", "#860 flake", "#2606 fatcutegg"), is a person
  // only if named plainly elsewhere or shaped like a handle with digits:
  // credit() decides
  for (const { b, context } of bare) {
    if (context ? handle(b) : parts === 1 && (LOGIN.test(b) || NUMBERED.test(b))) out.names.push({ name: b, via: null, sure: false });
  }
}

// the GitHub login in a noreply address; the address itself is never kept
function noreply(email) {
  const m = String(email || "").match(/^(?:\d+\+)?([A-Za-z0-9-]+)@users\.noreply\.github\.com$/i);
  return m ? m[1] : "";
}

export function refKey(r) { return `${r.repo}#${r.n}`; }

// excluded: maintainer, bots, the people who asked to be left off
function left(name, exclude) {
  const k = lower(name);
  return !k || MAINTAINERS.includes(k) || BOT.test(name) || exclude.has(k) || /^(the |a )?(owner|user|users|maintainer)$/.test(k);
}

// a co-author or commit author who is a person, not an agent or machine
function human(name, email) {
  if (!name || BOT.test(name) || MODEL.test(name) || !/^[\p{L}\p{N}]/u.test(name)) return false;
  return !email || !AGENT_MAIL.test(email) || /@users\.noreply\.github\.com$/i.test(email);
}
const coName = (c) => (typeof c === "string" ? { name: c, email: "" } : c);

// Each commit's people. commits: [{ subject, body, author, email, parents,
// coauthors: [{ name, email }] }]
// issueAuthors: Map or object of "repo#n" → login (a bot's ends in "[bot]").
// xAccounts: Map or object of GitHub login → the X handle its public profile
// names, or a list of them ("" for none).
// Returns an array beside commits, each [{ key, name, via, github }].
export function credit(commits, { issueAuthors = {}, exclude = [], xAccounts = {} } = {}) {
  const authorOf = issueAuthors instanceof Map ? (k) => issueAuthors.get(k) : (k) => issueAuthors[k];
  const ex = new Set([...exclude].map((s) => lower(String(s).trim())).filter(Boolean));
  const onX = xLogins(xAccounts);
  const parsed = commits.map((c) => {
    const p = parse(c.subject);
    p.names.push(...parseBody(String(c.subject).split(" — ").slice(1).join(" — ") + "\n\n" + (c.body || "")));
    return p;
  });
  // names credited plainly anywhere, so a lone bracketed one can be trusted
  const known = new Set();
  commits.forEach((c, i) => {
    for (const n of parsed[i].names) if (n.sure) known.add(lower(n.name));
    for (const r of parsed[i].refs) { const a = authorOf(refKey(r)); if (a) known.add(lower(a)); }
    if (human(c.author, c.email)) known.add(lower(c.author));
    const gh = noreply(c.email);
    if (gh) known.add(lower(gh));
    for (const co of (c.coauthors || []).map(coName)) if (human(co.name, co.email)) known.add(lower(co.name));
  });
  return commits.map((c, i) => {
    const people = new Map();
    const add = (name, via, github) => {
      name = String(name || "").trim();
      if (left(name, ex) || (github && left(github, ex))) return;
      const key = lower(github || name);
      const had = people.get(key);
      if (had) { if (via && !had.via.includes(via)) had.via.push(via); if (github) had.github = github; return; }
      people.set(key, { key, name, via: via ? [via] : [], github: github || "" });
    };
    for (const n of parsed[i].names) {
      if (!(n.sure || known.has(lower(n.name)) || (DIGITED.test(n.name) && !/^[0-9a-f]+$/i.test(n.name)) || NUMBERED.test(n.name))) continue;
      // an X handle a GitHub profile names is that GitHub person
      const gh = n.via === "X" ? onX.get(lower(n.name)) : "";
      if (gh) { if (!left(n.name, ex)) add(gh, "X", gh); continue; }
      add(n.name, n.via, n.github || "");
    }
    for (const r of parsed[i].refs) {
      const a = authorOf(refKey(r));
      if (a && !BOT.test(a)) add(a, "GitHub", a);
    }
    if (c.parents == null || c.parents < 2) {
      if (human(c.author, c.email)) add(c.author, "GitHub", noreply(c.email));
      for (const co of (c.coauthors || []).map(coName)) if (human(co.name, co.email)) add(co.name, "GitHub", noreply(co.email));
    }
    return [...people.values()];
  });
}

// lowercased X handle → the GitHub login whose public profile names it. A
// handle two logins both name is nobody's: either could have typed it.
const X_HANDLE = /^[A-Za-z0-9_]{1,15}$/;
function xLogins(xAccounts) {
  const pairs = xAccounts instanceof Map ? [...xAccounts] : Object.entries(xAccounts || {});
  const by = new Map();
  for (const [login, v] of pairs) for (const handle of handlesOf(v)) {
    const h = handle.trim().replace(/^@/, "");
    if (!login || BOT.test(login) || !X_HANDLE.test(h)) continue;
    const k = lower(h);
    by.set(k, by.has(k) && lower(by.get(k) || "") !== lower(login) ? null : login);
  }
  for (const [k, v] of by) if (!v) by.delete(k);
  return by;
}

// an xAccounts value as a list: "a" or ["a", "b"]
export function handlesOf(v) {
  return (Array.isArray(v) ? v : [v]).map((h) => String(h == null ? "" : h)).filter(Boolean);
}

// the GitHub logins credit() found, to look up their X accounts
export function logins(perCommit) {
  const seen = new Map();
  for (const list of perCommit) for (const p of list) if (p.github && !seen.has(lower(p.github))) seen.set(lower(p.github), p.github);
  return [...seen.values()];
}

// every person across commits, merged by key
export function everyone(perCommit) {
  const all = new Map();
  for (const list of perCommit) for (const p of list) {
    const had = all.get(p.key);
    if (!had) { all.set(p.key, { ...p, via: [...p.via] }); continue; }
    for (const v of p.via) if (!had.via.includes(v)) had.via.push(v);
    if (p.github && !had.github) had.github = p.github;
  }
  return [...all.values()];
}

// markdown that shows a name as written: tjhhhh_84378, not tjhhhh84378 in italics
const md = (s) => s.replace(/([\\`*_{}\[\]<>()#+!|~])/g, "\\$1");

export const WALL = { en: "https://usemagpie.ai/docs/contributors", zh: "https://usemagpie.ai/docs/zh/contributors" };

// the release notes' last line, or "" when nobody is credited
export function thanks(people, lang) {
  const names = people.map((p) => md(p.name));
  if (!names.length) return "";
  if (lang === "zh") {
    const list = names.length > 1 ? names.slice(0, -1).join("、") + " 和 " + names.at(-1) : names[0];
    return `感谢 ${list}：这次更新里有${names.length > 1 ? "你们" : "你"}的反馈与代码。所有贡献者见[贡献墙](${WALL.zh})。`;
  }
  const list = names.length > 1 ? names.slice(0, -1).join(", ") + " and " + names.at(-1) : names[0];
  return `Thanks to ${list}, whose reports and code went into this release. Everyone who helped: [contributors](${WALL.en}).`;
}

// The subject as the page shows it: its first part, without the brackets
// that only credit or number it.
export function what(subject) {
  let s = String(subject).split(" — ")[0];
  s = s.replace(/\s*\(([^()]*)\)/g, (all, inner, at, text) => {
    const m = { index: at + all.indexOf("("), 0: all.slice(all.indexOf("(")) };
    if (quoted(text, m)) return all;
    const one = { names: [], refs: [] };
    group(inner, one);
    return one.names.length || one.refs.length ? "" : all;
  }).trim();
  return s.length > 200 ? s.slice(0, 199) + "…" : s;
}

// --- the contributors page ---

const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const WORDS = {
  en: { name: "Who", led: "What came of it, newest first", more: (n) => `${n} more`, none: "The list isn't available right now.", find: "Find a name", one: "1 person", many: "{n} people" },
  zh: { name: "谁", led: "带来的改动，新的在前", more: (n) => `还有 ${n} 项`, none: "名单暂时无法显示。", find: "查找名字", one: "1 人", many: "{n} 人" },
  ja: { name: "誰が", led: "そこから生まれた変更（新しい順）", more: (n) => `ほか ${n} 件`, none: "一覧は今は表示できません。", find: "名前で探す", one: "1 人", many: "{n} 人" },
};
const SHOWN = 3;
const RELEASE = (v) => `https://github.com/yetone/magpie-releases/releases/tag/v${encodeURIComponent(v)}`;

// contributors-x.txt: lowercased handle → { sn, pic }, the X accounts
// checked to be the people credited under those names (pic: the small
// profile picture's path under pbs.twimg.com/profile_images/, "" for X's
// default one)
export function checkedX(text) {
  const out = new Map();
  for (const l of String(text || "").split("\n")) {
    const [sn, pic = ""] = l.replace(/#.*/, "").trim().split(/\s+/);
    if (!sn || !X_HANDLE.test(sn)) continue;
    out.set(lower(sn), { sn, pic: /^[\w.\-]+\/[\w.\-]+$/.test(pic) ? pic : "" });
  }
  return out;
}

// a person's picture: their GitHub one, their checked X account's, or ""
export function avatar(p, size = 64) {
  if (p.github) return `https://avatars.githubusercontent.com/${encodeURIComponent(p.github)}?s=${size}`;
  // X's small picture is 48px; "_bigger" is the same at 73px
  if (p.xpic) return `https://pbs.twimg.com/profile_images/${p.xpic.split("/").map(encodeURIComponent).join("/").replace(/_normal(?=\.\w+$|$)/, size > 48 ? "_bigger" : "_normal")}`;
  return "";
}

const initial = (name) => (String(name).match(/[\p{L}\p{N}]/u) || ["?"])[0].toUpperCase();

// Each person's row id, so /docs/zh/contributors#lijiho96940561 lands on
// it: their key (GitHub login, else the name as credited), lowercased, any
// run of what isn't a letter, digit, "_", "." or "-" made one "-". Two keys
// that come out the same, or one that is the page's own id ("list"), are
// told apart ("-2"): a key that is its own slug first, then in key order,
// so a row keeps its id while the list grows and reorders. A key with nothing left is "p-" and its hash.
export const PAGE_IDS = ["overview", "list", "how", "leave", "wall", "langs", "toc", "sx-list", "cw-q", "replay"];
export function slug(key) {
  const s = String(key || "").normalize("NFKC").toLowerCase().replace(/[^\p{L}\p{N}_.-]+/gu, "-").replace(/^-+|-+$/g, "");
  if (s) return s;
  let h = 0x811c9dc5;
  for (const c of String(key || "")) h = Math.imul(h ^ c.codePointAt(0), 16777619) >>> 0;
  return "p-" + h.toString(36);
}
export function anchors(people) {
  const keys = people.map((p) => lower(p.github || p.name || ""));
  const taken = new Set(PAGE_IDS);
  const ids = new Map();
  // a key that is its own slug (a login, a handle) keeps it
  const order = [...new Set(keys)].map((k) => [k, slug(k)]).sort(([a, x], [b, y]) => (a === x) !== (b === y) ? (a === x ? -1 : 1) : a < b ? -1 : a > b ? 1 : 0);
  for (const [k, base] of order) {
    let id = base;
    for (let n = 2; taken.has(id); n++) id = `${base}-${n}`;
    taken.add(id);
    ids.set(k, id);
  }
  return keys.map((k) => ids.get(k));
}

// The table the page shows from contributors.json's data, with the finder
// above it (public/wall.js shows it; without script the list is whole and
// the field stays hidden). A row's data-k holds what else finds it besides
// its name: the GitHub login and the X account that login's profile names.
export function renderWall(data, lang) {
  const w = WORDS[lang] || WORDS.en;
  const people = data && Array.isArray(data.people) ? data.people : [];
  if (!people.length) return `<p>${esc(w.none)}</p>`;
  const xOf = new Map(Object.entries((data && data.xAccounts) || {}).map(([l, h]) => [lower(l), handlesOf(h)]));
  const ids = anchors(people);
  const line = (r) => r.what.map((t) => `<li><a href="${RELEASE(r.v)}"><code>v${esc(r.v)}</code></a> ${esc(t)}</li>`).join("");
  const rows = people.map((p, i) => {
    const href = p.github ? `https://github.com/${encodeURIComponent(p.github)}` : p.x ? `https://x.com/${encodeURIComponent(p.x)}` : "";
    const name = href ? `<a href="${href}">${esc(p.name)}</a>` : esc(p.name);
    const via = (p.via || []).length ? `<br><span class="cw-via">${p.via.map(esc).join(" · ")}</span>` : "";
    const also = [];
    for (const k of [p.github, ...((p.github && xOf.get(lower(p.github))) || [])]) if (k && lower(k) !== lower(p.name) && !also.includes(lower(k))) also.push(lower(k));
    const src = avatar(p, 48);
    const pic = src
      ? `<img class="cw-av" src="${esc(src)}" alt="" width="24" height="24" loading="lazy" decoding="async" referrerpolicy="no-referrer" data-i="${esc(initial(p.name))}">`
      : `<span class="cw-av" aria-hidden="true">${esc(initial(p.name))}</span>`;
    const items = [];
    for (const r of p.releases || []) for (const t of r.what || []) items.push({ v: r.v, what: [t] });
    const head = items.slice(0, SHOWN).map(line).join("");
    const rest = items.slice(SHOWN);
    const more = rest.length ? `<details><summary>${esc(w.more(rest.length))}</summary><ul>${rest.map(line).join("")}</ul></details>` : "";
    return `<tr id="${esc(ids[i])}"${also.length ? ` data-k="${esc(also.join(" "))}"` : ""}><td><div class="cw-who">${pic}<div><b>${name}</b>${via}</div></div></td><td><ul>${head}</ul>${more}</td></tr>`;
  }).join("");
  const find = `<div class="cw-find" hidden data-one="${esc(w.one)}" data-many="${esc(w.many)}"><input id="cw-q" type="search" placeholder="${esc(w.find)}" aria-label="${esc(w.find)}" autocomplete="off" autocapitalize="off" spellcheck="false"><span class="cw-n" aria-live="polite"></span></div>`;
  return `${find}<div class="table-wrap"><table class="cw"><thead><tr><th>${esc(w.name)}</th><th>${esc(w.led)}</th></tr></thead><tbody>${rows}</tbody></table></div>`;
}

const vnum = (v) => String(v).split(".").map((x) => parseInt(x, 10) || 0);
export function newer(a, b) {
  const x = vnum(a), y = vnum(b);
  for (let i = 0; i < Math.max(x.length, y.length); i++) if ((x[i] || 0) !== (y[i] || 0)) return (x[i] || 0) > (y[i] || 0);
  return false;
}

// contributors.json's people from released commits ({ ...commit, v }):
// each person with the versions their reports led to, newest first.
// opts.checkedX (checkedX()): an X credit whose name is a checked handle
// gets that account (x, and xpic for its picture); any other X name stays
// a name.
export function wall(commits, opts = {}) {
  const per = credit(commits, opts);
  const all = new Map();
  commits.forEach((c, i) => {
    if (!c.v) return;
    for (const p of per[i]) {
      let e = all.get(p.key);
      if (!e) all.set(p.key, (e = { name: p.name, via: [], github: "", byV: new Map() }));
      for (const v of p.via) if (!e.via.includes(v)) e.via.push(v);
      if (p.github) e.github = p.github;
      if (!e.byV.has(c.v)) e.byV.set(c.v, []);
      const t = what(c.subject);
      if (t && !e.byV.get(c.v).includes(t)) e.byV.get(c.v).push(t);
    }
  });
  const people = [...all.values()].map((e) => {
    const releases = [...e.byV.entries()].map(([v, w]) => ({ v, what: w })).sort((a, b) => (newer(a.v, b.v) ? -1 : newer(b.v, a.v) ? 1 : 0));
    const out = { name: e.name, via: e.via, releases };
    if (e.github) out.github = e.github;
    const x = !e.github && e.via.includes("X") && opts.checkedX && opts.checkedX.get(lower(e.name.replace(/^@/, "")));
    if (x) { out.x = x.sn; if (x.pic) out.xpic = x.pic; }
    return out;
  });
  people.sort((a, b) => (newer(a.releases[0].v, b.releases[0].v) ? -1 : newer(b.releases[0].v, a.releases[0].v) ? 1 : lower(a.name) < lower(b.name) ? -1 : 1));
  return people;
}

// The replay above the table: when each person was credited, by release.
// people: contributors.json's; tags: [{ v, t }] with t in Unix seconds.
// Gives { t0, v: versions oldest first, t: each one's minutes after t0,
// p: [[name, picture URL, row id]] in people's order, e: [[version index, person
// index, …]] for the versions that credit someone }.
export function replay(people, tags) {
  const seen = new Set();
  const list = [...tags].filter((g) => g && g.v && Number.isFinite(g.t) && !seen.has(g.v) && seen.add(g.v))
    .sort((a, b) => a.t - b.t || (newer(a.v, b.v) ? 1 : newer(b.v, a.v) ? -1 : 0));
  const t0 = list.length ? list[0].t : 0;
  const at = new Map(list.map((g, i) => [g.v, i]));
  const ids = anchors(people);
  const by = new Map();
  people.forEach((p, i) => {
    for (const r of p.releases || []) {
      const vi = at.get(r.v);
      if (vi == null) continue;
      if (!by.has(vi)) by.set(vi, []);
      by.get(vi).push(i);
    }
  });
  return {
    t0,
    v: list.map((g) => g.v),
    t: list.map((g) => Math.round((g.t - t0) / 60)),
    p: people.map((p, i) => [p.name, avatar(p, 96), ids[i]]),
    e: [...by.entries()].sort((a, b) => a[0] - b[0]).map(([vi, ps]) => [vi, ...ps]),
  };
}
