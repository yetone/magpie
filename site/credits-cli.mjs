#!/usr/bin/env node
// The people magpie's history credits (credits.mjs), from git and GitHub.
//
//   node site/credits-cli.mjs thanks <from>..<to>
//     the thanks line for a release's notes: English on the first line,
//     Chinese on the second, nothing when no one is credited. yetone/
//     magpie-releases' release run calls it for the commits since the last
//     tag.
//   node site/credits-cli.mjs wall [out]
//     public/contributors.json (or out) from every tag's commits, and the
//     contributors page's table in each language beside it
//     (public/contributors/en.html, zh, ja), which worker.js puts into the
//     page, and the replay's data (public/contributors/replay.json, read by
//     public/contributors-replay.js). wrangler.jsonc's build runs it at each
//     deploy. A commit of magpie-community/plugins counts when a version of
//     a package it changed was published (pluginCommits); the CLI keeps a
//     bare clone of it in .cache/plugins.git; when that can't be read, magpie's
//     own history still goes out. An X credit links to its account only
//     when the account is in contributors-x.txt.
//
// The reporters a commit's body names ("Reported on X by @…") are credited
// beside its subject's (credits.mjs parseBody).
//
// The issue and PR authors a subject's "(#N)" names are asked of GitHub's
// GraphQL API with GH_TOKEN, GITHUB_TOKEN or `gh auth token`. When that
// can't be read, those people are left out (and `wall` keeps the authors
// its last output knew); the rest still goes out. The same API gives each
// credited GitHub user's public X accounts (twitter_username and the X
// links among the profile's social accounts), so an X credit under such a
// handle is that GitHub person; a user it can't read joins nobody (and
// `wall` keeps the accounts its last output knew). contributors-aliases.txt
// names GitHub logins with no credit of their own whose profiles are read
// too, for the one X account each line names. Names in
// contributors-exclude.txt are never given.
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync, renameSync, existsSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { credit, everyone, thanks, wall, refKey, parse, renderWall, logins, handlesOf, checkedX, replay, anchors, PLUGINS, MAGPIE } from "./credits.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const warn = (m) => process.stderr.write(`credits: ${m}\n`);

const SEP = "\x1f", END = "\x1e";
function log(args, gitDir) {
  const out = execFileSync("git", [...(gitDir ? ["--git-dir", gitDir] : []), "log", `--format=%H${SEP}%P${SEP}%an${SEP}%ae${SEP}%s${SEP}%(trailers:key=Co-authored-by,valueonly,separator=%x1d)${SEP}%D${SEP}%ct${SEP}%b${END}`, ...args], { encoding: "utf8", maxBuffer: 1 << 30 });
  return out.split(END).map((r) => r.replace(/^\n/, "")).filter(Boolean).map((r) => {
    const [sha, parents, author, email, subject, co, refs, time, body] = r.split(SEP);
    return {
      sha, author, email, subject, body: body || "", time: Number(time) || 0,
      parents: parents.trim() ? parents.trim().split(" ").length : 0,
      // the address only tells an agent from a person; it is never written out
      coauthors: (co || "").split("\x1d").map((s) => s.trim()).filter(Boolean).map((s) => {
        const m = s.match(/^(.*?)\s*<([^>]*)>$/);
        return m ? { name: m[1].trim(), email: m[2].trim() } : { name: s, email: "" };
      }),
      tags: (refs || "").split(", ").map((s) => s.replace(/^tag: /, "")).filter((t) => /^v\d+\.\d+\.\d+$/.test(t)),
    };
  });
}

// --- the community plugins (magpie-community/plugins) ---
//
// A plugin's commit is released when a version of a package it changed was
// published: on npm, or, for a package npm has never had, on the plugins'
// dist branch, where magpie installs it from (the repository's
// scripts/dist.mjs). It goes out in the first such version made from a
// commit at or after it: npm's gitHead for the version, the main commit a
// dist commit names ("dist: 577aedc"), else the last commit made by the
// version's publish time. A commit that changes several packages goes under
// the one its subject names ("zcode 0.1.74: …"), else under the first
// published.
export const PLUGINS_GIT = `https://github.com/${PLUGINS}.git`;
export const PLUGINS_DIST = `https://raw.githubusercontent.com/${PLUGINS}/dist/index.json`;

// The plugins' main branch in a bare clone at dir, cloned the first time
// and fetched after, and the dist branch's commits (no files: it holds
// every tarball); false when there is no clone to read.
export function pluginsClone(dir, src = PLUGINS_GIT) {
  const git = (args) => execFileSync("git", args, { stdio: ["ignore", "ignore", "pipe"], timeout: 300000 });
  const why = (e) => String(e.stderr || e.message).trim().split("\n")[0];
  if (existsSync(join(dir, "HEAD"))) {
    try { git(["--git-dir", dir, "fetch", "--quiet", src, "+refs/heads/main:refs/heads/main"]); }
    catch (e) { warn(`${PLUGINS} not fetched (${why(e)}); its last fetch is read`); }
  } else {
    try {
      mkdirSync(dirname(dir), { recursive: true });
      git(["clone", "--quiet", "--bare", "--single-branch", "--branch", "main", src, dir]);
    } catch (e) {
      warn(`${PLUGINS} not cloned (${why(e)})`);
      return false;
    }
  }
  try { git(["--git-dir", dir, "-c", "remote.origin.promisor=true", "-c", "remote.origin.partialclonefilter=tree:0", "fetch", "--quiet", "--filter=tree:0", src, "+refs/heads/dist:refs/heads/dist"]); }
  catch (e) { warn(`${PLUGINS}'s dist commits not fetched (${why(e)}); a dist version is placed by its time`); }
  return true;
}

// npm's versions of a package: [{ v, t (Unix seconds), head }]; [] when npm
// has no such package; throws when npm can't be read
export async function npmVersions(name, { fetchImpl = globalThis.fetch } = {}) {
  const res = await fetchImpl(`https://registry.npmjs.org/${name.replace("/", "%2f")}`, {
    headers: { Accept: "application/json", "User-Agent": "magpie-credits" },
    signal: AbortSignal.timeout(30000),
  });
  if (res.status === 404) return [];
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const doc = await res.json();
  const out = [];
  for (const [v, m] of Object.entries((doc && doc.versions) || {})) {
    const t = Date.parse((doc.time || {})[v]);
    if (!Number.isFinite(t)) continue;
    out.push({ v, t: Math.round(t / 1000), head: m && typeof m.gitHead === "string" ? m.gitHead : "" });
  }
  return out;
}

// the dist branch's index.json: { packages: { <name>: { versions: { <v>:
// { time } } } } }; throws when it can't be read
export async function distIndex({ fetchImpl = globalThis.fetch } = {}) {
  const res = await fetchImpl(PLUGINS_DIST, { headers: { "User-Agent": "magpie-credits" }, signal: AbortSignal.timeout(60000) });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const doc = await res.json();
  if (!doc || typeof doc.packages !== "object") throw new Error("no packages");
  return doc;
}

// The plugins' released commits, each { ...commit, repo: PLUGINS, v:
// "<folder>@<version>" }, with each release's time and the npm package of
// each folder that is on npm (its versions link there), read from the bare
// clone at gitDir, npm and the dist index. A package that can't be read is
// left out, and said.
export async function pluginCommits(gitDir, { fetchImpl = globalThis.fetch, versions = npmVersions, dist = distIndex } = {}) {
  const git = (args) => execFileSync("git", ["--git-dir", gitDir, ...args], { encoding: "utf8", maxBuffer: 1 << 30, stdio: ["ignore", "pipe", "ignore"] });
  const commits = log(["--topo-order", "--reverse", "main"], gitDir);
  const pos = new Map(commits.map((c, i) => [c.sha, i]));
  const short = new Map(commits.map((c, i) => [c.sha.slice(0, 7), i]));
  // the folders under packages/ each commit changed
  const dirs = new Map();
  for (const r of git(["log", "--format=%x1e%H", "--name-only", "main", "--", "packages/"]).split("\x1e")) {
    const [sha, ...files] = r.trim().split("\n");
    if (!sha) continue;
    const ds = new Set();
    for (const f of files) { const m = f.match(/^packages\/([^/]+)\//); if (m) ds.add(m[1]); }
    dirs.set(sha, ds);
  }
  // each folder's package name, from its package.json on main
  const names = {};
  for (const d of git(["ls-tree", "--name-only", "main", "packages/"]).split("\n").map((l) => l.replace(/^packages\//, "")).filter(Boolean)) {
    try {
      const n = JSON.parse(git(["show", `main:packages/${d}/package.json`])).name;
      if (typeof n === "string" && n) names[d] = n;
    } catch {}
  }
  // the dist commits, oldest first: when each was made and the main commit
  // it packed
  let packs = [];
  try {
    packs = git(["log", "--reverse", "--format=%ct %s", "dist"]).split("\n").map((l) => l.match(/^(\d+) dist: ([0-9a-f]{7,40})$/)).filter(Boolean).map((m) => ({ t: +m[1], at: short.get(m[2].slice(0, 7)) })).filter((p) => p.at != null);
  } catch {}
  // where a version is: the commit it was made from, else the last commit
  // made by its time
  const byTime = (t) => { let k = -1; commits.forEach((c, i) => { if (c.time <= t) k = i; }); return k; };
  // a dist version's: the first dist commit made after it was packed, within the hour
  const packed = (t) => { const p = packs.find((x) => x.t >= t); return p && p.t - t < 3600 ? p.at : byTime(t); };
  const rel = {};
  const times = {};
  const onNpm = {};
  let index = null;
  const fromDist = async (name) => {
    if (!index) index = dist({ fetchImpl });
    const doc = await index;
    return Object.entries(((doc.packages[name] || {}).versions) || {}).map(([v, m]) => ({ v, t: Math.round(Date.parse(m && m.time) / 1000) })).filter((x) => Number.isFinite(x.t));
  };
  const list = Object.entries(names);
  for (let i = 0; i < list.length; i += 8) {
    await Promise.all(list.slice(i, i + 8).map(async ([d, name]) => {
      let vs;
      try {
        vs = (await versions(name, { fetchImpl })).map((x) => ({ ...x, at: pos.has(x.head) ? pos.get(x.head) : byTime(x.t) }));
        if (vs.length) onNpm[d] = name;
        else vs = (await fromDist(name)).map((x) => ({ ...x, at: packed(x.t) }));
      } catch (e) {
        warn(`${name}: versions not read (${e.message}); its commits are left out`);
        return;
      }
      rel[d] = vs.filter((x) => x.at >= 0).sort((a, b) => a.at - b.at || a.t - b.t);
      for (const x of rel[d]) times[`${d}@${x.v}`] = x.t;
    }));
  }
  const out = [];
  const plugins = {};
  commits.forEach((c, i) => {
    if (c.parents >= 2) return;
    const hits = [];
    for (const d of dirs.get(c.sha) || []) {
      const x = (rel[d] || []).find((r) => r.at >= i);
      if (x) hits.push({ d, x });
    }
    if (!hits.length) return;
    const named = (c.subject.match(/^([\w.-]+)[\s:]/) || [])[1];
    const pick = hits.find((h) => named && h.d.toLowerCase() === named.toLowerCase()) || hits.sort((a, b) => a.x.t - b.x.t || (a.d < b.d ? -1 : 1))[0];
    if (onNpm[pick.d]) plugins[pick.d] = onNpm[pick.d];
    out.push({ ...c, repo: PLUGINS, v: `${pick.d}@${pick.x.v}` });
  });
  return { commits: out, times, plugins, head: commits.length ? commits.at(-1).sha : "" };
}

// contributors-aliases.txt: "x-handle github-login" a line, an X account
// and the GitHub login its owner says is theirs. Only a word shaped like
// each counts; lines after # are comments.
export function aliases(file = join(here, "contributors-aliases.txt")) {
  let text = "";
  try { text = readFileSync(file, "utf8"); } catch { return []; }
  const out = [];
  for (const l of text.split("\n")) {
    const m = l.replace(/#.*/, "").trim().match(/^@?([A-Za-z0-9_]{1,15})\s+([A-Za-z0-9-]{1,39})$/);
    if (m) out.push({ x: m[1], login: m[2] });
    else if (l.replace(/#.*/, "").trim()) warn(`contributors-aliases.txt: "${l.trim()}" is not "x-handle github-login"; left out`);
  }
  return out;
}

// The logins to ask GitHub for: the credited ones, and each alias's.
export function toLookUp(credited, list) {
  const seen = new Map(credited.map((l) => [l.toLowerCase(), l]));
  for (const a of list) if (!seen.has(a.login.toLowerCase())) seen.set(a.login.toLowerCase(), a.login);
  return [...seen.values()];
}

// x (login → the X accounts its profile names) as credit() may use it: a
// login no commit credits counts only for the X accounts an alias line
// pairs with it, and only those its profile names. A line whose login's
// profile doesn't name that X account changes nothing.
export function honoured(x, credited, list) {
  const own = new Set(credited.map((l) => l.toLowerCase()));
  const out = {};
  for (const [login, v] of Object.entries(x || {})) {
    if (own.has(login.toLowerCase())) { out[login] = v; continue; }
    const want = new Set(list.filter((a) => a.login.toLowerCase() === login.toLowerCase()).map((a) => a.x.toLowerCase()));
    const kept = handlesOf(v).filter((h) => want.has(h.replace(/^@/, "").toLowerCase()));
    if (kept.length) out[login] = kept.length === 1 ? kept[0] : kept;
  }
  return out;
}

// the X handle a social account's link names: "https://x.com/dou_dev"
const X_LINK = /^https?:\/\/(?:www\.|mobile\.)?(?:x|twitter)\.com\/(?:#!\/)?@?([A-Za-z0-9_]{1,15})\/?(?:[?#].*)?$/i;
const X_PATHS = new Set(["home", "i", "intent", "share", "search", "hashtag", "explore", "settings", "login", "signup", "messages", "notifications"]);
export function xOfLink(url) {
  const m = String(url || "").trim().match(X_LINK);
  return m && !X_PATHS.has(m[1].toLowerCase()) ? m[1] : "";
}

export function excluded(file = join(here, "contributors-exclude.txt")) {
  try {
    return readFileSync(file, "utf8").split("\n").map((l) => l.replace(/#.*/, "").trim()).filter(Boolean);
  } catch { return []; }
}

function token() {
  if (process.env.GH_TOKEN) return process.env.GH_TOKEN;
  if (process.env.GITHUB_TOKEN) return process.env.GITHUB_TOKEN;
  try { return execFileSync("gh", ["auth", "token"], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim(); } catch { return ""; }
}

// "repo#n" → login (a bot's with "[bot]"), "" for no author; refs it
// couldn't read are left out of the map
export async function authors(refs, { known = {}, fetchImpl = globalThis.fetch, tok = token() } = {}) {
  const got = { ...known };
  const want = new Map();
  for (const r of refs) if (!(refKey(r) in got)) want.set(refKey(r), r);
  if (!want.size) return got;
  if (!tok) { warn(`no GitHub token: ${want.size} issue authors left out`); return got; }
  const byRepo = new Map();
  for (const r of want.values()) (byRepo.get(r.repo) || byRepo.set(r.repo, []).get(r.repo)).push(r.n);
  for (const [repo, ns] of byRepo) {
    const [owner, name] = repo.split("/");
    for (let i = 0; i < ns.length; i += 100) {
      const chunk = ns.slice(i, i + 100);
      const who = "author { login __typename }";
      const q = `query { repository(owner: ${JSON.stringify(owner)}, name: ${JSON.stringify(name)}) { ${chunk.map((n) => `i${n}: issueOrPullRequest(number: ${n}) { ... on Issue { ${who} } ... on PullRequest { ${who} } }`).join(" ")} } }`;
      try {
        const res = await fetchImpl("https://api.github.com/graphql", {
          method: "POST",
          headers: { Authorization: `bearer ${tok}`, "Content-Type": "application/json", "User-Agent": "magpie-credits" },
          body: JSON.stringify({ query: q }),
          signal: AbortSignal.timeout(30000),
        });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const body = await res.json();
        const repoData = body && body.data && body.data.repository;
        if (!repoData) throw new Error(JSON.stringify(body.errors || body).slice(0, 300));
        for (const n of chunk) {
          const item = repoData[`i${n}`];
          if (item === undefined) continue; // not answered: unknown, asked again next time
          const a = item && item.author;
          got[`${repo}#${n}`] = a ? (a.__typename === "Bot" ? `${a.login}[bot]` : a.login) : "";
        }
      } catch (e) {
        warn(`${repo}: issue authors not read (${e.message}); those people are left out`);
      }
    }
  }
  return got;
}

// GitHub login → the X handle its public profile names (twitter_username,
// and any x.com or twitter.com link among its social accounts; a list when
// they name more than one), asked fresh each time. Only logins with one are
// in the map. A login GitHub doesn't answer keeps what known had for it, or
// is left out: unknown joins nobody.
export async function xAccounts(users, { known = {}, fetchImpl = globalThis.fetch, tok = token() } = {}) {
  const got = {};
  const kept = (l) => { if (known[l]) got[l] = known[l]; };
  const list = [...new Set(users.filter((l) => /^[A-Za-z0-9-]{1,39}$/.test(l)))];
  if (!list.length) return got;
  if (!tok) { warn(`no GitHub token: X accounts of ${list.length} people not read`); list.forEach(kept); return got; }
  for (let i = 0; i < list.length; i += 100) {
    const chunk = list.slice(i, i + 100);
    const q = `query { ${chunk.map((l, j) => `u${j}: user(login: ${JSON.stringify(l)}) { login twitterUsername socialAccounts(first: 20) { nodes { url } } }`).join(" ")} }`;
    try {
      const res = await fetchImpl("https://api.github.com/graphql", {
        method: "POST",
        headers: { Authorization: `bearer ${tok}`, "Content-Type": "application/json", "User-Agent": "magpie-credits" },
        body: JSON.stringify({ query: q }),
        signal: AbortSignal.timeout(30000),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const body = await res.json();
      const data = body && body.data;
      if (!data) throw new Error(JSON.stringify((body && body.errors) || body).slice(0, 300));
      chunk.forEach((l, j) => {
        const u = data[`u${j}`];
        if (u === undefined) return kept(l); // not answered
        // null: no such user any more, so no X account
        if (!u || String(u.login).toLowerCase() !== l.toLowerCase()) return;
        const hs = [];
        const nodes = (u.socialAccounts && u.socialAccounts.nodes) || [];
        for (const h of [u.twitterUsername, ...nodes.map((n) => xOfLink(n && n.url))]) {
          if (h && !hs.some((k) => k.toLowerCase() === String(h).toLowerCase())) hs.push(String(h));
        }
        if (hs.length) got[l] = hs.length === 1 ? hs[0] : hs;
      });
    } catch (e) {
      warn(`X accounts not read (${e.message}); those people keep their last or none`);
      chunk.forEach(kept);
    }
  }
  return got;
}

async function cmdThanks(range) {
  if (!range) throw new Error("usage: credits-cli.mjs thanks <from>..<to>");
  const commits = log(["--no-merges", range]);
  const refs = commits.flatMap((c) => parse(c.subject).refs);
  const issueAuthors = await authors(refs);
  const exclude = excluded();
  const credited = logins(credit(commits, { issueAuthors, exclude }));
  const al = aliases();
  const x = honoured(await xAccounts(toLookUp(credited, al)), credited, al);
  const people = everyone(credit(commits, { issueAuthors, exclude, xAccounts: x }));
  const en = thanks(people, "en");
  if (en) process.stdout.write(en + "\n" + thanks(people, "zh") + "\n");
}

async function cmdWall(out = join(here, "public", "contributors.json")) {
  let before = {};
  try { before = JSON.parse(readFileSync(out, "utf8")); } catch {}
  let commits;
  try {
    commits = log(["--topo-order", "--reverse", "--decorate-refs=refs/tags/v*", "HEAD"]);
  } catch (e) {
    warn(`git log failed (${e.message.split("\n")[0]}); ${existsSync(out) ? "the last list is kept" : "no list is written"}`);
    return;
  }
  // a commit goes out in the first tag at or after it
  let pending = [];
  for (const c of commits) {
    pending.push(c);
    if (c.tags.length) {
      const v = c.tags.map((t) => t.slice(1)).sort((a, b) => a.localeCompare(b, "en", { numeric: true }))[0];
      for (const p of pending) p.v = v;
      pending = [];
    }
  }
  const own = commits.filter((c) => c.v && c.parents < 2);
  // the plugins' released commits beside magpie's; when they can't be read,
  // magpie's own still go out
  let plug = { commits: [], times: {}, plugins: {}, head: "" };
  try {
    const dir = join(here, ".cache", "plugins.git");
    if (pluginsClone(dir)) plug = await pluginCommits(dir);
  } catch (e) {
    warn(`${PLUGINS} not read (${String(e.message).split("\n")[0]}); only magpie's history is listed`);
  }
  const released = [...own, ...plug.commits];
  const refs = released.flatMap((c) => parse(c.subject, c.repo || MAGPIE).refs);
  const issueAuthors = await authors(refs, { known: before.issueAuthors || {} });
  const exclude = excluded();
  const credited = logins(credit(released, { issueAuthors, exclude }));
  const al = aliases();
  const x = honoured(await xAccounts(toLookUp(credited, al), { known: before.xAccounts || {} }), credited, al);
  let checked = new Map();
  try { checked = checkedX(readFileSync(join(here, "contributors-x.txt"), "utf8")); } catch (e) { warn(`contributors-x.txt not read (${e.message}); no X links`); }
  // each release's time: its tagged commit's, a plugin version's npm publish
  const tags = commits.flatMap((c) => c.tags.map((t) => ({ v: t.slice(1), t: c.time })));
  const times = { ...plug.times };
  for (const g of tags) if (!(g.v in times)) times[g.v] = g.t;
  const people = wall(released, { issueAuthors, exclude, xAccounts: x, checkedX: checked, times });
  const data = { generated: new Date().toISOString(), head: commits.length ? commits.at(-1).sha : "", pluginsHead: plug.head, plugins: plug.plugins, issueAuthors, xAccounts: x, people };
  const put = (file, text) => { writeFileSync(file + ".tmp", text); renameSync(file + ".tmp", file); };
  put(out, JSON.stringify(data) + "\n");
  const dir = join(dirname(out), "contributors");
  mkdirSync(dir, { recursive: true });
  for (const lang of ["en", "zh", "ja"]) put(join(dir, `${lang}.html`), renderWall(data, lang) + "\n");
  put(join(dir, "replay.json"), JSON.stringify(replay(people, tags, times)) + "\n");
  warn(`${people.length} people over ${own.length} released commits and ${plug.commits.length} of ${PLUGINS} → ${out}`);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const [cmd, arg] = process.argv.slice(2);
  const run = cmd === "thanks" ? cmdThanks(arg) : cmd === "wall" ? cmdWall(arg) : Promise.reject(new Error("usage: credits-cli.mjs thanks <range> | wall [out]"));
  run.catch((e) => { warn(e.message); process.exit(cmd === "wall" ? 0 : 1); });
}
